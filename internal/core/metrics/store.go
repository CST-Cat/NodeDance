// Package metrics stores only the newest accepted Agent metrics per node.
// Connection identity and generation are bound by Core after authentication;
// reports cannot choose their own identity, generation, or heartbeat lease.
package metrics

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	fastMetricFreshFor = 3 * 5 * time.Second
	diskMetricFreshFor = 3 * 30 * time.Second
)

var (
	ErrInvalidIdentity    = errors.New("metrics identity is invalid")
	ErrConnectionUnknown  = errors.New("metrics connection is not bound")
	ErrIdentityMismatch   = errors.New("metrics identity does not match the authenticated node")
	ErrStaleGeneration    = errors.New("metrics connection generation is stale")
	ErrGenerationMismatch = errors.New("metrics generation does not match the bound connection")
	ErrStaleSequence      = errors.New("metrics sequence is not newer than the latest report")
	ErrInvalidReport      = errors.New("metrics report is invalid")
)

// Identity is always supplied from Core's authenticated connection context;
// it is not part of MetricsSnapshot and cannot be selected by the Agent.
type Identity struct {
	AgentID string
	NodeID  string
}

type binding struct {
	identity   Identity
	generation uint64
}

type LeaseStatus string

const (
	LeaseOnline  LeaseStatus = "online"
	LeaseOffline LeaseStatus = "offline"
	LeaseRevoked LeaseStatus = "revoked"
)

// Lease must come from Core's current authorized node lease, including its
// persisted status. A historical lastSeen+timeout or a metrics report cannot
// assert that a node is currently online.
type Lease struct {
	Identity
	Generation uint64
	ValidUntil time.Time
	Status     LeaseStatus
}

// Record contains the last accepted report and its independent receive time.
// ClockOffsetMillis is Core receive wall time minus Agent collected wall time;
// freshness never uses this offset or wall-clock sample timestamps.
type Record struct {
	AgentID           string                   `json:"agentId"`
	NodeID            string                   `json:"nodeId"`
	Generation        uint64                   `json:"generation"`
	Sequence          uint64                   `json:"sequence"`
	BootID            string                   `json:"bootId,omitempty"`
	PreviousBootID    string                   `json:"previousBootId,omitempty"`
	BootIDChangedAt   time.Time                `json:"bootIdChangedAt,omitempty"`
	CollectedAt       time.Time                `json:"collectedAt"`
	ReceivedAt        time.Time                `json:"receivedAt"`
	ClockOffsetMillis float64                  `json:"clockOffsetMs"`
	Metrics           protocol.MetricsSnapshot `json:"metrics"`
}

// View is calculated against the trusted Core generation and heartbeat lease
// at read time. It needs no delayed sweeper to report an expired lease as stale.
type View struct {
	Record
	NodeStatus       string    `json:"nodeStatus"`
	ActiveGeneration uint64    `json:"activeGeneration"`
	ServerTime       time.Time `json:"serverTime"`
	LeaseValidUntil  time.Time `json:"leaseValidUntil"`
}

type Store struct {
	mu       sync.RWMutex
	bindings map[string]binding
	latest   map[string]Record
}

func NewStore() *Store {
	return &Store{
		bindings: make(map[string]binding),
		latest:   make(map[string]Record),
	}
}

// BindConnection records the generation Core assigned to an authenticated
// AgentID/NodeID pair. The generation must come from Core's active lease, not
// from a metrics report. A newer binding invalidates writes from older sockets.
func (s *Store) BindConnection(identity Identity, generation uint64) error {
	if s == nil || !validIdentity(identity) || generation == 0 {
		return ErrInvalidIdentity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bindings[identity.NodeID]; ok {
		if current.identity != identity {
			return ErrIdentityMismatch
		}
		if generation < current.generation {
			return ErrStaleGeneration
		}
	}
	s.bindings[identity.NodeID] = binding{identity: identity, generation: generation}
	return nil
}

// UnbindConnection removes an authenticated connection only if it is still
// the current generation. A delayed close from an older socket cannot unbind
// a newer connection.
func (s *Store) UnbindConnection(identity Identity, generation uint64) bool {
	if s == nil || !validIdentity(identity) || generation == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.bindings[identity.NodeID]
	if !ok || current.identity != identity || current.generation != generation {
		return false
	}
	delete(s.bindings, identity.NodeID)
	return true
}

// Accept stores one metrics report if its authenticated identity, connection
// generation, and TypeMetrics-specific sequence match the active Core binding.
// It does not renew or extend the heartbeat lease.
func (s *Store) Accept(identity Identity, connectionGeneration, metricsSequence uint64,
	report protocol.MetricsSnapshot, receivedAt time.Time) error {
	if s == nil || !validIdentity(identity) || connectionGeneration == 0 || metricsSequence == 0 || receivedAt.IsZero() {
		return ErrInvalidReport
	}
	if err := protocol.ValidateAgentMetricsSnapshot(report); err != nil {
		return errors.Join(ErrInvalidReport, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	active, ok := s.bindings[identity.NodeID]
	if !ok {
		return ErrConnectionUnknown
	}
	if active.identity != identity {
		return ErrIdentityMismatch
	}
	if connectionGeneration < active.generation {
		return ErrStaleGeneration
	}
	if connectionGeneration != active.generation {
		return ErrGenerationMismatch
	}

	previous, exists := s.latest[identity.NodeID]
	if exists {
		if previous.AgentID != identity.AgentID {
			return ErrIdentityMismatch
		}
		if connectionGeneration < previous.Generation {
			return ErrStaleGeneration
		}
		if connectionGeneration == previous.Generation && metricsSequence <= previous.Sequence {
			return ErrStaleSequence
		}
	}

	merged := cloneSnapshot(report)
	elapsedSincePrevious := time.Duration(0)
	if exists {
		elapsedSincePrevious = receivedAt.Sub(previous.ReceivedAt)
		if elapsedSincePrevious < 0 {
			elapsedSincePrevious = 0
		}
		merged = mergeSnapshot(previous.Metrics, report, elapsedSincePrevious)
	}

	bootID := previous.BootID
	previousBootID := previous.PreviousBootID
	bootIDChangedAt := previous.BootIDChangedAt
	if report.Uptime.Status == protocol.MetricKnown && report.Uptime.Value != nil {
		reportedBootID := report.Uptime.Value.BootID
		if bootID != "" && reportedBootID != bootID {
			previousBootID = bootID
			bootIDChangedAt = receivedAt.UTC()
		}
		bootID = reportedBootID
	} else if bootID == "" && merged.Uptime.Value != nil {
		bootID = merged.Uptime.Value.BootID
	}

	collectedAt := report.CollectedAt.UTC()
	clockOffset := float64(receivedAt.Sub(report.CollectedAt)) / float64(time.Millisecond)
	record := Record{
		AgentID:           identity.AgentID,
		NodeID:            identity.NodeID,
		Generation:        connectionGeneration,
		Sequence:          metricsSequence,
		BootID:            bootID,
		PreviousBootID:    previousBootID,
		BootIDChangedAt:   bootIDChangedAt,
		CollectedAt:       collectedAt,
		ReceivedAt:        receivedAt,
		ClockOffsetMillis: clockOffset,
		Metrics:           merged,
	}
	s.latest[identity.NodeID] = record
	return nil
}

// SnapshotAt derives online/stale status from Core's current authorized lease.
// lease must be nil when Core has no active lease (offline/revoked); a non-nil
// lease is online only when Core marks it online and its validity has not
// expired. Historical lastSeen values and metrics cannot create a lease.
// now should be Core's current time; its monotonic reading keeps freshness
// stable across Core wall-clock adjustments.
func (s *Store) SnapshotAt(nodeID string, lease *Lease, now time.Time) (View, bool) {
	if s == nil {
		return View{}, false
	}
	s.mu.RLock()
	record, ok := s.latest[nodeID]
	if !ok {
		s.mu.RUnlock()
		return View{}, false
	}
	s.mu.RUnlock()

	record = cloneRecord(record)
	serverTime := now.UTC()
	var validUntil time.Time
	var activeGeneration uint64
	leaseCurrent := lease != nil && lease.Status == LeaseOnline && lease.Generation != 0 &&
		!lease.ValidUntil.IsZero() && now.Before(lease.ValidUntil)
	identityMatches := leaseCurrent && lease.NodeID == nodeID && lease.AgentID == record.AgentID
	online := leaseCurrent && identityMatches
	if online {
		validUntil = lease.ValidUntil.UTC()
		activeGeneration = lease.Generation
	}
	elapsedSinceReceive := now.Sub(record.ReceivedAt)
	if elapsedSinceReceive < 0 {
		elapsedSinceReceive = 0
	}
	advanceSnapshotAge(&record.Metrics, elapsedSinceReceive)
	if leaseCurrent && !identityMatches {
		markSnapshotStale(&record.Metrics, "lease_identity_changed")
	} else if !online {
		markSnapshotStale(&record.Metrics, leaseInactiveReason(lease, now))
	} else if lease.Generation != record.Generation {
		markSnapshotStale(&record.Metrics, "connection_generation_changed")
	}
	markSnapshotAged(&record.Metrics)
	return View{
		Record:           record,
		NodeStatus:       map[bool]string{true: "online", false: "offline"}[online],
		ActiveGeneration: activeGeneration,
		ServerTime:       serverTime,
		LeaseValidUntil:  validUntil,
	}, true
}

func leaseInactiveReason(lease *Lease, now time.Time) string {
	if lease != nil && lease.Status == LeaseOnline && lease.Generation != 0 &&
		!lease.ValidUntil.IsZero() && !now.Before(lease.ValidUntil) {
		return "lease_expired"
	}
	return "lease_inactive"
}

func validIdentity(identity Identity) bool {
	return strings.TrimSpace(identity.AgentID) == identity.AgentID && identity.AgentID != "" && len(identity.AgentID) <= 128 &&
		strings.TrimSpace(identity.NodeID) == identity.NodeID && identity.NodeID != "" && len(identity.NodeID) <= 128
}

func cloneRecord(in Record) Record {
	out := in
	out.Metrics = cloneSnapshot(in.Metrics)
	return out
}

func cloneSnapshot(in protocol.MetricsSnapshot) protocol.MetricsSnapshot {
	out := in
	out.System.Hostname = cloneMetric(in.System.Hostname)
	out.System.OS = cloneMetric(in.System.OS)
	out.System.Architecture = cloneMetric(in.System.Architecture)
	out.System.Platform = cloneMetric(in.System.Platform)
	out.System.PlatformFamily = cloneMetric(in.System.PlatformFamily)
	out.System.PlatformVersion = cloneMetric(in.System.PlatformVersion)
	out.System.KernelVersion = cloneMetric(in.System.KernelVersion)
	out.CPU.UsagePercent = cloneMetric(in.CPU.UsagePercent)
	out.CPU.LogicalCores = cloneMetric(in.CPU.LogicalCores)
	out.Memory = cloneMetric(in.Memory)
	out.Network.Summary = cloneMetric(in.Network.Summary)
	out.Network.Interfaces = make([]protocol.MetricsInterface, 0, len(in.Network.Interfaces))
	for _, iface := range in.Network.Interfaces {
		copyIface := iface
		copyIface.Rate = cloneMetric(iface.Rate)
		if iface.Up != nil {
			up := *iface.Up
			copyIface.Up = &up
		}
		out.Network.Interfaces = append(out.Network.Interfaces, copyIface)
	}
	out.Disk.Mounts = make([]protocol.MetricsMount, 0, len(in.Disk.Mounts))
	for _, mount := range in.Disk.Mounts {
		copyMount := mount
		copyMount.Usage = cloneMetric(mount.Usage)
		out.Disk.Mounts = append(out.Disk.Mounts, copyMount)
	}
	out.Uptime = cloneMetric(in.Uptime)
	if in.Uptime.Value != nil && in.Uptime.Value.BootTimeUnix != nil && out.Uptime.Value != nil {
		bootTime := *in.Uptime.Value.BootTimeUnix
		out.Uptime.Value.BootTimeUnix = &bootTime
	}
	return out
}

func cloneMetric[T any](in protocol.Metric[T]) protocol.Metric[T] {
	out := in
	if in.Value != nil {
		value := *in.Value
		out.Value = &value
	}
	return out
}

func mergeSnapshot(previous, incoming protocol.MetricsSnapshot, elapsed time.Duration) protocol.MetricsSnapshot {
	out := cloneSnapshot(incoming)
	out.System.Hostname = mergeMetric(previous.System.Hostname, incoming.System.Hostname, elapsed)
	out.System.OS = mergeMetric(previous.System.OS, incoming.System.OS, elapsed)
	out.System.Architecture = mergeMetric(previous.System.Architecture, incoming.System.Architecture, elapsed)
	out.System.Platform = mergeMetric(previous.System.Platform, incoming.System.Platform, elapsed)
	out.System.PlatformFamily = mergeMetric(previous.System.PlatformFamily, incoming.System.PlatformFamily, elapsed)
	out.System.PlatformVersion = mergeMetric(previous.System.PlatformVersion, incoming.System.PlatformVersion, elapsed)
	out.System.KernelVersion = mergeMetric(previous.System.KernelVersion, incoming.System.KernelVersion, elapsed)
	out.CPU.UsagePercent = mergeMetric(previous.CPU.UsagePercent, incoming.CPU.UsagePercent, elapsed)
	out.CPU.LogicalCores = mergeMetric(previous.CPU.LogicalCores, incoming.CPU.LogicalCores, elapsed)
	out.Memory = mergeMetric(previous.Memory, incoming.Memory, elapsed)
	out.Network = mergeNetwork(previous.Network, incoming.Network, elapsed)
	out.Disk = mergeDisk(previous.Disk, incoming.Disk, elapsed)
	out.Uptime = mergeMetric(previous.Uptime, incoming.Uptime, elapsed)
	return out
}

func mergeMetric[T any](previous, incoming protocol.Metric[T], elapsed time.Duration) protocol.Metric[T] {
	if incoming.Status == protocol.MetricKnown {
		return cloneMetric(incoming)
	}
	if (incoming.Status == protocol.MetricUnknown || incoming.Status == protocol.MetricError) && previous.Value != nil {
		out := cloneMetric(previous)
		out.Status = protocol.MetricStale
		out.Reason = incoming.Reason
		out.SampleAgeMillis = addSampleAge(out.SampleAgeMillis, elapsed)
		return out
	}
	return cloneMetric(incoming)
}

func mergeNetwork(previous, incoming protocol.MetricsNetwork, elapsed time.Duration) protocol.MetricsNetwork {
	out := incoming
	out.Summary = mergeMetric(previous.Summary, incoming.Summary, elapsed)
	if len(incoming.Interfaces) == 0 && incoming.Summary.Status != protocol.MetricKnown && len(previous.Interfaces) != 0 {
		out.Interfaces = make([]protocol.MetricsInterface, 0, len(previous.Interfaces))
		for _, previousInterface := range previous.Interfaces {
			copyInterface := previousInterface
			copyInterface.Rate = mergeMetric(previousInterface.Rate, protocol.Metric[protocol.NetworkRate]{
				Status: protocol.MetricUnknown, Reason: incoming.Summary.Reason,
				SampledAt: incoming.Summary.SampledAt, SampleAgeMillis: incoming.Summary.SampleAgeMillis,
			}, elapsed)
			out.Interfaces = append(out.Interfaces, copyInterface)
		}
		return out
	}
	previousByName := make(map[string]protocol.MetricsInterface, len(previous.Interfaces))
	for _, iface := range previous.Interfaces {
		previousByName[iface.Name] = iface
	}
	out.Interfaces = make([]protocol.MetricsInterface, 0, len(incoming.Interfaces))
	for _, iface := range incoming.Interfaces {
		copyInterface := iface
		if old, exists := previousByName[iface.Name]; exists {
			copyInterface.Rate = mergeMetric(old.Rate, iface.Rate, elapsed)
		}
		if iface.Up != nil {
			up := *iface.Up
			copyInterface.Up = &up
		}
		out.Interfaces = append(out.Interfaces, copyInterface)
	}
	return out
}

func mergeDisk(previous, incoming protocol.MetricsDisk, elapsed time.Duration) protocol.MetricsDisk {
	if (incoming.Status == protocol.MetricUnknown || incoming.Status == protocol.MetricError) && len(previous.Mounts) != 0 {
		out := previous
		out.Status = protocol.MetricStale
		out.Reason = incoming.Reason
		out.SampleAgeMillis = addSampleAge(out.SampleAgeMillis, elapsed)
		out.Mounts = make([]protocol.MetricsMount, 0, len(previous.Mounts))
		for _, mount := range previous.Mounts {
			copyMount := mount
			if mount.Usage.Value != nil {
				copyMount.Usage.Status = protocol.MetricStale
				copyMount.Usage.Reason = incoming.Reason
				copyMount.Usage.SampleAgeMillis = addSampleAge(copyMount.Usage.SampleAgeMillis, elapsed)
			}
			out.Mounts = append(out.Mounts, copyMount)
		}
		return out
	}
	out := incoming
	previousByKey := make(map[string]protocol.MetricsMount, len(previous.Mounts))
	for _, mount := range previous.Mounts {
		previousByKey[mountKey(mount)] = mount
	}
	out.Mounts = make([]protocol.MetricsMount, 0, len(incoming.Mounts))
	for _, mount := range incoming.Mounts {
		copyMount := mount
		if old, exists := previousByKey[mountKey(mount)]; exists {
			copyMount.Usage = mergeMetric(old.Usage, mount.Usage, elapsed)
		}
		out.Mounts = append(out.Mounts, copyMount)
	}
	return out
}

func mountKey(mount protocol.MetricsMount) string {
	return mount.Device + "\x00" + mount.Mountpoint + "\x00" + mount.Filesystem
}

func markSnapshotStale(snapshot *protocol.MetricsSnapshot, reason string) {
	markString := func(metric *protocol.Metric[string]) { markMetricStale(metric, reason) }
	markString(&snapshot.System.Hostname)
	markString(&snapshot.System.OS)
	markString(&snapshot.System.Architecture)
	markString(&snapshot.System.Platform)
	markString(&snapshot.System.PlatformFamily)
	markString(&snapshot.System.PlatformVersion)
	markString(&snapshot.System.KernelVersion)
	markMetricStale(&snapshot.CPU.UsagePercent, reason)
	markMetricStale(&snapshot.CPU.LogicalCores, reason)
	markMetricStale(&snapshot.Memory, reason)
	markMetricStale(&snapshot.Network.Summary, reason)
	for index := range snapshot.Network.Interfaces {
		markMetricStale(&snapshot.Network.Interfaces[index].Rate, reason)
	}
	if snapshot.Disk.Status == protocol.MetricKnown || snapshot.Disk.Status == protocol.MetricStale {
		snapshot.Disk.Status = protocol.MetricStale
		snapshot.Disk.Reason = appendReason(snapshot.Disk.Reason, reason)
	}
	for index := range snapshot.Disk.Mounts {
		markMetricStale(&snapshot.Disk.Mounts[index].Usage, reason)
	}
	markMetricStale(&snapshot.Uptime, reason)
}

func advanceSnapshotAge(snapshot *protocol.MetricsSnapshot, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	advanceMetricAge(&snapshot.System.Hostname, elapsed)
	advanceMetricAge(&snapshot.System.OS, elapsed)
	advanceMetricAge(&snapshot.System.Architecture, elapsed)
	advanceMetricAge(&snapshot.System.Platform, elapsed)
	advanceMetricAge(&snapshot.System.PlatformFamily, elapsed)
	advanceMetricAge(&snapshot.System.PlatformVersion, elapsed)
	advanceMetricAge(&snapshot.System.KernelVersion, elapsed)
	advanceMetricAge(&snapshot.CPU.UsagePercent, elapsed)
	advanceMetricAge(&snapshot.CPU.LogicalCores, elapsed)
	advanceMetricAge(&snapshot.Memory, elapsed)
	advanceMetricAge(&snapshot.Network.Summary, elapsed)
	advanceMetricAge(&snapshot.Uptime, elapsed)
	if snapshot.Disk.Status == protocol.MetricKnown || snapshot.Disk.Status == protocol.MetricStale {
		snapshot.Disk.SampleAgeMillis = addSampleAge(snapshot.Disk.SampleAgeMillis, elapsed)
	}
	for index := range snapshot.Network.Interfaces {
		advanceMetricAge(&snapshot.Network.Interfaces[index].Rate, elapsed)
	}
	for index := range snapshot.Disk.Mounts {
		advanceMetricAge(&snapshot.Disk.Mounts[index].Usage, elapsed)
	}
}

func advanceMetricAge[T any](metric *protocol.Metric[T], elapsed time.Duration) {
	if (metric.Status == protocol.MetricKnown || metric.Status == protocol.MetricStale) && metric.Value != nil {
		metric.SampleAgeMillis = addSampleAge(metric.SampleAgeMillis, elapsed)
	}
}

func markSnapshotAged(snapshot *protocol.MetricsSnapshot) {
	markMetricAged(&snapshot.System.Hostname, fastMetricFreshFor)
	markMetricAged(&snapshot.System.OS, fastMetricFreshFor)
	markMetricAged(&snapshot.System.Architecture, fastMetricFreshFor)
	markMetricAged(&snapshot.System.Platform, fastMetricFreshFor)
	markMetricAged(&snapshot.System.PlatformFamily, fastMetricFreshFor)
	markMetricAged(&snapshot.System.PlatformVersion, fastMetricFreshFor)
	markMetricAged(&snapshot.System.KernelVersion, fastMetricFreshFor)
	markMetricAged(&snapshot.CPU.UsagePercent, fastMetricFreshFor)
	markMetricAged(&snapshot.CPU.LogicalCores, fastMetricFreshFor)
	markMetricAged(&snapshot.Memory, fastMetricFreshFor)
	markMetricAged(&snapshot.Network.Summary, fastMetricFreshFor)
	markMetricAged(&snapshot.Uptime, fastMetricFreshFor)
	markDiskAged(&snapshot.Disk)
	for index := range snapshot.Network.Interfaces {
		markMetricAged(&snapshot.Network.Interfaces[index].Rate, fastMetricFreshFor)
	}
	for index := range snapshot.Disk.Mounts {
		markMetricAged(&snapshot.Disk.Mounts[index].Usage, diskMetricFreshFor)
	}
}

func markMetricStale[T any](metric *protocol.Metric[T], reason string) {
	if metric.Status == protocol.MetricKnown || metric.Status == protocol.MetricStale {
		metric.Status = protocol.MetricStale
		metric.Reason = appendReason(metric.Reason, reason)
	}
}

func markMetricAged[T any](metric *protocol.Metric[T], maxAge time.Duration) {
	if (metric.Status != protocol.MetricKnown && metric.Status != protocol.MetricStale) || metric.Value == nil {
		return
	}
	if sampleExpired(metric.SampleAgeMillis, maxAge) {
		metric.Status = protocol.MetricStale
		metric.Reason = appendReason(metric.Reason, "sample_expired")
	}
}

func markDiskAged(disk *protocol.MetricsDisk) {
	if (disk.Status == protocol.MetricKnown || disk.Status == protocol.MetricStale) && sampleExpired(disk.SampleAgeMillis, diskMetricFreshFor) {
		disk.Status = protocol.MetricStale
		disk.Reason = appendReason(disk.Reason, "sample_expired")
	}
}

func sampleExpired(sampleAgeMillis int64, maxAge time.Duration) bool {
	if sampleAgeMillis < 0 || sampleAgeMillis > protocol.MaxMetricSampleAgeMS {
		return true
	}
	return time.Duration(sampleAgeMillis)*time.Millisecond > maxAge
}

func appendReason(existing, added string) string {
	if existing == "" {
		return added
	}
	if added == "" || strings.Contains(existing, added) {
		return existing
	}
	return existing + "; " + added
}

func addSampleAge(sampleAgeMillis int64, elapsed time.Duration) int64 {
	if sampleAgeMillis < 0 {
		return 0
	}
	if sampleAgeMillis >= protocol.MaxMetricSampleAgeMS || elapsed <= 0 {
		return min(sampleAgeMillis, protocol.MaxMetricSampleAgeMS)
	}
	additional := elapsed.Milliseconds()
	if additional >= protocol.MaxMetricSampleAgeMS-sampleAgeMillis {
		return protocol.MaxMetricSampleAgeMS
	}
	return sampleAgeMillis + additional
}
