// Package docker stores authenticated Agent Docker inventory updates. It does
// not own a database schema or transport handler; Core binds each connection
// generation after authentication and supplies the current Agent lease when
// reading a view.
package docker

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	DefaultSnapshotTTL       = 2 * time.Minute
	DefaultMaxSnapshotItems  = 20_000
	DefaultMaxSnapshotBytes  = 32 << 20
	DefaultMaxPendingChanges = 20_000
	DefaultMaxPendingBytes   = 32 << 20
	DefaultDockerHealthFresh = 15 * time.Second
	maxDockerIdentityBytes   = 128
)

var (
	ErrInvalidIdentity    = errors.New("Docker identity is invalid")
	ErrConnectionUnknown  = errors.New("Docker connection is not bound")
	ErrIdentityMismatch   = errors.New("Docker identity does not match the authenticated node")
	ErrStaleGeneration    = errors.New("Docker connection generation is stale")
	ErrGenerationMismatch = errors.New("Docker generation does not match the bound connection")
	ErrStaleSequence      = errors.New("Docker sequence is not newer than the accepted state")
	ErrInvalidBatch       = errors.New("Docker batch is invalid")
	ErrInvalidSnapshot    = errors.New("Docker full snapshot sequence is invalid")
	ErrSnapshotExpired    = errors.New("Docker full snapshot staging expired")
	ErrSnapshotLimit      = errors.New("Docker full snapshot staging limit exceeded")
)

type Options struct {
	SnapshotTTL       time.Duration
	MaxSnapshotItems  int
	MaxSnapshotBytes  int
	MaxPendingChanges int
	MaxPendingBytes   int
	HealthFreshFor    time.Duration
}

func (o Options) withDefaults() Options {
	if o.SnapshotTTL <= 0 {
		o.SnapshotTTL = DefaultSnapshotTTL
	}
	if o.MaxSnapshotItems <= 0 {
		o.MaxSnapshotItems = DefaultMaxSnapshotItems
	}
	if o.MaxSnapshotBytes <= 0 {
		o.MaxSnapshotBytes = DefaultMaxSnapshotBytes
	}
	if o.MaxPendingChanges <= 0 {
		o.MaxPendingChanges = DefaultMaxPendingChanges
	}
	if o.MaxPendingBytes <= 0 {
		o.MaxPendingBytes = DefaultMaxPendingBytes
	}
	if o.HealthFreshFor <= 0 {
		o.HealthFreshFor = DefaultDockerHealthFresh
	}
	return o
}

// Identity comes from the authenticated Agent connection, never from the DTO.
type Identity struct {
	AgentID string
	NodeID  string
}

type LeaseStatus string

const (
	LeaseOnline  LeaseStatus = "online"
	LeaseOffline LeaseStatus = "offline"
	LeaseRevoked LeaseStatus = "revoked"
)

// Lease is the authoritative Core-side Agent lease. Docker messages cannot
// renew it or assert that their node is online.
type Lease struct {
	Identity
	Generation uint64
	ValidUntil time.Time
	Status     LeaseStatus
}

type ContainerRecord struct {
	Container  protocol.DockerContainer `json:"container"`
	Generation uint64                   `json:"generation"`
	Sequence   uint64                   `json:"sequence"`
	ReceivedAt time.Time                `json:"receivedAt"`
}

// View keeps Agent connection state and Docker Engine state separate. A node
// can therefore be online while Docker is unavailable or its inventory stale.
type View struct {
	AgentID               string                      `json:"agentId"`
	NodeID                string                      `json:"nodeId"`
	AgentOnline           bool                        `json:"agentOnline"`
	ActiveGeneration      uint64                      `json:"activeGeneration"`
	LeaseValidUntil       time.Time                   `json:"leaseValidUntil,omitempty"`
	DockerAvailability    protocol.DockerAvailability `json:"dockerAvailability"`
	DockerEventsConnected bool                        `json:"dockerEventsConnected"`
	DockerSnapshotFresh   bool                        `json:"dockerSnapshotFresh"`
	DataStale             bool                        `json:"dataStale"`
	StaleReason           string                      `json:"staleReason,omitempty"`
	Health                *protocol.DockerHealth      `json:"health,omitempty"`
	Containers            []ContainerRecord           `json:"containers"`
	ServerTime            time.Time                   `json:"serverTime"`
}

type Store struct {
	mu       sync.RWMutex
	options  Options
	bindings map[string]binding
	nodes    map[string]*nodeState
}

type binding struct {
	identity   Identity
	generation uint64
}

type nodeState struct {
	identity   Identity
	generation uint64

	lastEnvelopeSequence uint64
	lastDataSequence     uint64
	snapshotFloor        uint64
	lastSnapshotID       uint64
	lastSnapshotSequence uint64
	hasCompleteSnapshot  bool

	containers       map[string]ContainerRecord
	resourceSequence map[string]uint64
	queued           []queuedChange
	queuedBytes      int
	stage            *snapshotStage
	ignored          *ignoredSnapshot

	health         *protocol.DockerHealth
	healthReceived time.Time
	staleReason    string
}

type queuedChange struct {
	change       protocol.DockerChange
	receivedAt   time.Time
	encodedBytes int
}

type snapshotStage struct {
	generation   uint64
	snapshotID   uint64
	sequence     uint64
	nextIndex    int
	startedAt    time.Time
	byteCount    int
	items        map[string]protocol.DockerContainer
	pending      []queuedChange
	pendingBytes int
}

type ignoredSnapshot struct {
	snapshotID uint64
	sequence   uint64
	nextIndex  int
	startedAt  time.Time
	expired    bool
}

func NewStore() *Store { return NewStoreWithOptions(Options{}) }

func NewStoreWithOptions(options Options) *Store {
	return &Store{
		options:  options.withDefaults(),
		bindings: make(map[string]binding),
		nodes:    make(map[string]*nodeState),
	}
}

// BindConnection binds a Core-assigned generation to an authenticated Agent.
// A new generation retains the old inventory as stale until a trustworthy,
// complete Engine scan for that generation is committed.
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
	state := s.nodes[identity.NodeID]
	if state != nil && state.identity != identity {
		return ErrIdentityMismatch
	}
	if state == nil {
		state = &nodeState{
			identity:         identity,
			containers:       make(map[string]ContainerRecord),
			resourceSequence: make(map[string]uint64),
		}
		s.nodes[identity.NodeID] = state
	}
	if generation > state.generation {
		state.generation = generation
		state.lastEnvelopeSequence = 0
		state.lastDataSequence = 0
		state.snapshotFloor = 0
		state.lastSnapshotID = 0
		state.lastSnapshotSequence = 0
		state.hasCompleteSnapshot = false
		state.resourceSequence = make(map[string]uint64)
		state.queued = nil
		state.queuedBytes = 0
		state.stage = nil
		state.ignored = nil
		state.health = nil
		state.healthReceived = time.Time{}
		state.staleReason = "awaiting_authoritative_snapshot"
		for id, record := range state.containers {
			record.Container.Stale = true
			record.Container.UnavailableReason = state.staleReason
			state.containers[id] = cloneRecord(record)
		}
	}
	s.bindings[identity.NodeID] = binding{identity: identity, generation: generation}
	return nil
}

// UnbindConnection is generation-CAS: a delayed close from an older socket
// cannot unbind the active connection.
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

// Accept applies one already-decoded TypeDocker payload. identity and the
// generation are derived from Core's authenticated socket; envelopeSequence
// is the WSS Envelope sequence and is deliberately separate from batch.Sequence.
// The heartbeat lease is not updated here.
func (s *Store) Accept(identity Identity, connectionGeneration, envelopeSequence uint64,
	batch protocol.DockerBatch, receivedAt time.Time) error {
	if s == nil || !validIdentity(identity) || connectionGeneration == 0 || envelopeSequence == 0 || receivedAt.IsZero() {
		return ErrInvalidBatch
	}
	payload, err := protocol.MarshalDockerBatch(batch)
	if err != nil {
		return errors.Join(ErrInvalidBatch, err)
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
	state := s.nodes[identity.NodeID]
	if state == nil || state.identity != identity || state.generation != connectionGeneration {
		return ErrGenerationMismatch
	}
	if envelopeSequence <= state.lastEnvelopeSequence {
		return ErrStaleSequence
	}
	// A semantically rejected but valid frame still consumes its transport
	// sequence, preventing the same Envelope from being replayed later.
	state.lastEnvelopeSequence = envelopeSequence
	expireStageLocked(state, receivedAt, s.options.SnapshotTTL)

	if batch.FullSnapshot {
		return s.acceptSnapshotLocked(state, connectionGeneration, batch, receivedAt, len(payload))
	}
	return s.acceptIncrementalLocked(state, batch, receivedAt, len(payload))
}

func (s *Store) acceptSnapshotLocked(state *nodeState, generation uint64, batch protocol.DockerBatch, receivedAt time.Time, payloadBytes int) error {
	if expireStageLocked(state, receivedAt, s.options.SnapshotTTL) {
		if batch.SnapshotIndex != 0 {
			return ErrSnapshotExpired
		}
	}
	if ignored := state.ignored; ignored != nil {
		if ignored.expired {
			if batch.SnapshotIndex == 0 {
				state.ignored = nil
			} else if ignored.snapshotID == batch.SnapshotID && ignored.sequence == batch.Sequence {
				return ErrSnapshotExpired
			} else {
				return ErrInvalidSnapshot
			}
		}
	}
	if ignored := state.ignored; ignored != nil {
		if receivedAt.Sub(ignored.startedAt) > s.options.SnapshotTTL {
			state.ignored = nil
			state.staleReason = "snapshot_staging_expired"
			if batch.SnapshotIndex != 0 {
				return ErrSnapshotExpired
			}
		}
	}
	if ignored := state.ignored; ignored != nil {
		if batch.SnapshotIndex == 0 {
			state.ignored = nil
		} else {
			if ignored.snapshotID != batch.SnapshotID || ignored.sequence != batch.Sequence || ignored.nextIndex != batch.SnapshotIndex {
				state.ignored = nil
				state.staleReason = "snapshot_sequence_invalid"
				return ErrInvalidSnapshot
			}
			ignored.nextIndex++
			if batch.SnapshotFinal {
				state.ignored = nil
			}
			return nil
		}
	}

	stage := state.stage
	if batch.SnapshotIndex == 0 {
		if stage != nil {
			state.stage = nil
			state.staleReason = "snapshot_restarted"
			stage = nil
		}
		if batch.Sequence < state.lastDataSequence || batch.Sequence < state.snapshotFloor {
			state.staleReason = "snapshot_sequence_stale"
			return ErrStaleSequence
		}
		if batch.Health == nil || !authoritativeHealth(*batch.Health) {
			s.applyHealthLocked(state, batch.Health, receivedAt)
			state.lastDataSequence = maxUint64(state.lastDataSequence, batch.Sequence)
			state.queued = nil
			state.queuedBytes = 0
			state.staleReason = nonAuthoritativeReason(batch.Health)
			if !batch.SnapshotFinal {
				state.ignored = &ignoredSnapshot{
					snapshotID: batch.SnapshotID,
					sequence:   batch.Sequence,
					nextIndex:  1,
					startedAt:  receivedAt,
				}
			}
			return nil
		}
		if batch.SnapshotID < state.lastSnapshotID ||
			(batch.SnapshotID == state.lastSnapshotID && batch.Sequence < state.lastSnapshotSequence) {
			state.staleReason = "snapshot_sequence_stale"
			return ErrInvalidSnapshot
		}
		s.applyHealthLocked(state, batch.Health, receivedAt)
		state.queued = nil // the full image at Sequence supersedes older deltas.
		state.queuedBytes = 0
		stage = &snapshotStage{
			generation: generation,
			snapshotID: batch.SnapshotID,
			sequence:   batch.Sequence,
			nextIndex:  0,
			startedAt:  receivedAt,
			items:      make(map[string]protocol.DockerContainer),
		}
		state.stage = stage
	} else if stage == nil || stage.generation != generation || stage.snapshotID != batch.SnapshotID || stage.sequence != batch.Sequence || stage.nextIndex != batch.SnapshotIndex {
		state.stage = nil
		state.staleReason = "snapshot_sequence_invalid"
		return ErrInvalidSnapshot
	}
	stage = state.stage
	if stage == nil {
		return ErrInvalidSnapshot
	}
	if stage.byteCount+payloadBytes > s.options.MaxSnapshotBytes {
		state.stage = nil
		state.staleReason = "snapshot_staging_byte_limit"
		return ErrSnapshotLimit
	}
	for _, change := range batch.Changes {
		if _, duplicate := stage.items[change.ContainerID]; duplicate {
			state.stage = nil
			state.staleReason = "snapshot_duplicate_container"
			return ErrInvalidSnapshot
		}
		if len(stage.items) >= s.options.MaxSnapshotItems {
			state.stage = nil
			state.staleReason = "snapshot_staging_item_limit"
			return ErrSnapshotLimit
		}
		stage.items[change.ContainerID] = cloneDockerContainer(*change.Container)
	}
	stage.byteCount += payloadBytes
	stage.nextIndex++
	if !batch.SnapshotFinal {
		return nil
	}
	return s.commitSnapshotLocked(state, stage, receivedAt)
}

func (s *Store) acceptIncrementalLocked(state *nodeState, batch protocol.DockerBatch, receivedAt time.Time, payloadBytes int) error {
	if batch.Sequence <= state.snapshotFloor || batch.Sequence <= state.lastDataSequence {
		return ErrStaleSequence
	}
	if state.stage != nil && batch.Sequence <= state.stage.sequence {
		return ErrStaleSequence
	}
	expireStageLocked(state, receivedAt, s.options.SnapshotTTL)
	if batch.Health != nil {
		s.applyHealthLocked(state, batch.Health, receivedAt)
	}
	state.lastDataSequence = batch.Sequence
	changes := make([]queuedChange, 0, len(batch.Changes))
	for _, change := range batch.Changes {
		changes = append(changes, queuedChange{
			change: cloneDockerChange(change), receivedAt: receivedAt, encodedBytes: payloadBytes,
		})
	}
	if len(changes) == 0 {
		return nil
	}
	if state.stage != nil {
		return s.appendStagePendingLocked(state, changes)
	}
	if !state.hasCompleteSnapshot {
		return s.appendQueuedLocked(state, changes)
	}
	if !stateEngineAvailable(state) {
		state.staleReason = "docker_engine_unavailable"
		return nil
	}
	for _, item := range changes {
		if err := applyChange(state, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) appendStagePendingLocked(state *nodeState, changes []queuedChange) error {
	stage := state.stage
	count := len(stage.pending) + len(changes)
	bytes := stage.pendingBytes
	for _, item := range changes {
		bytes += item.encodedBytes
	}
	if count > s.options.MaxPendingChanges || bytes > s.options.MaxPendingBytes ||
		len(stage.items)+count > s.options.MaxSnapshotItems || stage.byteCount+bytes > s.options.MaxSnapshotBytes {
		state.stage = nil
		state.staleReason = "snapshot_pending_change_limit"
		return ErrSnapshotLimit
	}
	stage.pending = append(stage.pending, changes...)
	stage.pendingBytes = bytes
	return nil
}

func (s *Store) appendQueuedLocked(state *nodeState, changes []queuedChange) error {
	count := len(state.queued) + len(changes)
	bytes := state.queuedBytes
	for _, item := range changes {
		bytes += item.encodedBytes
	}
	if count > s.options.MaxPendingChanges || bytes > s.options.MaxPendingBytes {
		state.queued = nil
		state.queuedBytes = 0
		state.staleReason = "pending_change_limit"
		return ErrSnapshotLimit
	}
	state.queued = append(state.queued, changes...)
	state.queuedBytes = bytes
	return nil
}

func (s *Store) commitSnapshotLocked(state *nodeState, stage *snapshotStage, receivedAt time.Time) error {
	if state.stage != stage || stage.generation != state.generation {
		state.staleReason = "snapshot_generation_changed"
		return ErrInvalidSnapshot
	}
	if receivedAt.Sub(stage.startedAt) > s.options.SnapshotTTL {
		state.stage = nil
		state.staleReason = "snapshot_staging_expired"
		return ErrSnapshotExpired
	}
	old := state.containers
	updated := make(map[string]ContainerRecord, len(stage.items)+len(stage.pending))
	resourceSequence := make(map[string]uint64, len(old)+len(stage.items))
	for id, item := range stage.items {
		if item.Stale {
			if previous, exists := old[id]; exists {
				previous = cloneRecord(previous)
				previous.Container.Stale = true
				if item.UnavailableReason != "" {
					previous.Container.UnavailableReason = item.UnavailableReason
				}
				previous.ReceivedAt = receivedAt
				previous.Sequence = stage.sequence
				updated[id] = previous
			} else {
				updated[id] = ContainerRecord{
					Container: cloneDockerContainer(item), Generation: stage.generation,
					Sequence: stage.sequence, ReceivedAt: receivedAt,
				}
			}
		} else {
			updated[id] = ContainerRecord{
				Container: cloneDockerContainer(item), Generation: stage.generation,
				Sequence: stage.sequence, ReceivedAt: receivedAt,
			}
		}
		resourceSequence[id] = stage.sequence
	}
	for id := range old {
		if _, present := stage.items[id]; !present {
			resourceSequence[id] = stage.sequence // tombstone against late replay.
		}
	}
	state.containers = updated
	state.resourceSequence = resourceSequence
	state.snapshotFloor = stage.sequence
	state.lastSnapshotID = maxUint64(state.lastSnapshotID, stage.snapshotID)
	state.lastSnapshotSequence = maxUint64(state.lastSnapshotSequence, stage.sequence)
	state.hasCompleteSnapshot = true
	state.lastDataSequence = maxUint64(state.lastDataSequence, stage.sequence)
	state.stage = nil
	state.ignored = nil
	state.queued = nil
	state.queuedBytes = 0
	state.staleReason = ""
	for _, pending := range stage.pending {
		if pending.change.Sequence <= stage.sequence {
			continue
		}
		if err := applyChange(state, pending); err != nil && !errors.Is(err, ErrStaleSequence) {
			state.staleReason = "snapshot_pending_change_invalid"
			return err
		}
	}
	if state.health != nil && !state.health.EventsConnected {
		state.staleReason = "docker_events_disconnected"
	}
	return nil
}

// SnapshotAt returns the newest accepted inventory and derives connection
// state from Core's current lease. It intentionally returns a view after
// BindConnection even when no Docker snapshot has yet succeeded.
func (s *Store) SnapshotAt(nodeID string, lease *Lease, now time.Time) (View, bool) {
	if s == nil || strings.TrimSpace(nodeID) != nodeID || nodeID == "" || now.IsZero() {
		return View{}, false
	}
	s.ExpireStaging(now)
	s.mu.RLock()
	state := s.nodes[nodeID]
	if state == nil {
		s.mu.RUnlock()
		return View{}, false
	}
	copy := cloneNodeStateForView(state)
	active, bound := s.bindings[nodeID]
	s.mu.RUnlock()

	view := View{
		AgentID: copy.identity.AgentID, NodeID: copy.identity.NodeID,
		ActiveGeneration: copy.generation, ServerTime: now.UTC(),
		DockerAvailability: protocol.DockerAvailabilityUnknown,
		Containers:         make([]ContainerRecord, 0, len(copy.containers)),
	}
	leaseCurrent := lease != nil && lease.Status == LeaseOnline && lease.Generation != 0 &&
		!lease.ValidUntil.IsZero() && now.Before(lease.ValidUntil)
	view.AgentOnline = leaseCurrent && bound && active.identity == copy.identity &&
		active.generation == copy.generation && lease.AgentID == copy.identity.AgentID &&
		lease.NodeID == copy.identity.NodeID && lease.Generation == copy.generation
	if view.AgentOnline {
		view.LeaseValidUntil = lease.ValidUntil.UTC()
	}
	if copy.health != nil {
		health := cloneDockerHealth(*copy.health)
		view.Health = &health
		view.DockerAvailability = health.Availability
		view.DockerEventsConnected = health.EventsConnected
		view.DockerSnapshotFresh = health.SnapshotFresh
	}
	healthAge := now.Sub(copy.healthReceived)
	if healthAge < 0 {
		healthAge = 0
	}
	view.DataStale = !copy.hasCompleteSnapshot || copy.staleReason != "" || !view.AgentOnline || copy.health == nil ||
		copy.health.Availability != protocol.DockerAvailabilityAvailable || !copy.health.SnapshotFresh ||
		!copy.health.EventsConnected || healthAge > s.options.HealthFreshFor
	view.StaleReason = copy.staleReason
	if !view.AgentOnline {
		view.DataStale = true
		view.StaleReason = "agent_offline_or_lease_invalid"
	} else if copy.health == nil {
		view.StaleReason = firstReason(view.StaleReason, "docker_state_unknown")
	} else if copy.health.Availability != protocol.DockerAvailabilityAvailable {
		view.StaleReason = firstReason(view.StaleReason, nonAuthoritativeReason(copy.health))
	} else if !copy.health.EventsConnected {
		view.StaleReason = firstReason(view.StaleReason, "docker_events_disconnected")
	} else if healthAge > s.options.HealthFreshFor {
		view.StaleReason = firstReason(view.StaleReason, "docker_health_stale")
	} else if !copy.hasCompleteSnapshot {
		view.StaleReason = firstReason(view.StaleReason, "awaiting_authoritative_snapshot")
	}
	for _, record := range copy.containers {
		item := cloneRecord(record)
		if view.DataStale {
			item.Container.Stale = true
			if item.Container.UnavailableReason == "" {
				item.Container.UnavailableReason = view.StaleReason
			}
		}
		view.Containers = append(view.Containers, item)
	}
	sort.Slice(view.Containers, func(i, j int) bool {
		return view.Containers[i].Container.ID < view.Containers[j].Container.ID
	})
	return view, true
}

// ExpireStaging releases incomplete full-snapshot buffers whose total staging
// duration has elapsed. Core should call it from its periodic maintenance loop;
// Accept and SnapshotAt also perform lazy expiry.
func (s *Store) ExpireStaging(now time.Time) int {
	if s == nil || now.IsZero() {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := 0
	for _, state := range s.nodes {
		if expireStageLocked(state, now, s.options.SnapshotTTL) {
			expired++
		}
		if ignored := state.ignored; ignored != nil && now.Sub(ignored.startedAt) > s.options.SnapshotTTL {
			state.ignored = nil
			if !ignored.expired {
				state.staleReason = "snapshot_staging_expired"
			}
			expired++
		}
	}
	return expired
}

func (s *Store) applyHealthLocked(state *nodeState, incoming *protocol.DockerHealth, receivedAt time.Time) {
	if incoming == nil {
		return
	}
	if state.health != nil && incoming.Sequence < state.health.Sequence {
		return
	}
	health := cloneDockerHealth(*incoming)
	state.health = &health
	state.healthReceived = receivedAt
	if health.Availability != protocol.DockerAvailabilityAvailable {
		state.staleReason = nonAuthoritativeReason(&health)
	} else if !health.SnapshotFresh {
		state.staleReason = firstReason(health.Reason, "docker_snapshot_stale")
	} else if !health.EventsConnected {
		state.staleReason = "docker_events_disconnected"
	}
}

func applyChange(state *nodeState, item queuedChange) error {
	change := item.change
	if change.Sequence <= state.snapshotFloor || change.Sequence <= state.resourceSequence[change.ContainerID] {
		return ErrStaleSequence
	}
	previous, exists := state.containers[change.ContainerID]
	switch change.Action {
	case protocol.DockerChangeUpsert:
		incoming := cloneDockerContainer(*change.Container)
		if incoming.Stale {
			if exists {
				previous = cloneRecord(previous)
				previous.Container.Stale = true
				previous.Container.UnavailableReason = firstReason(incoming.UnavailableReason, "container_inspect_unavailable")
				previous.Generation = state.generation
				previous.Sequence = change.Sequence
				previous.ReceivedAt = item.receivedAt
				state.containers[change.ContainerID] = previous
			} else {
				incoming.UnavailableReason = firstReason(incoming.UnavailableReason, "container_inspect_unavailable")
				state.containers[change.ContainerID] = ContainerRecord{
					Container: incoming, Generation: state.generation,
					Sequence: change.Sequence, ReceivedAt: item.receivedAt,
				}
			}
		} else {
			state.containers[change.ContainerID] = ContainerRecord{
				Container: incoming, Generation: state.generation,
				Sequence: change.Sequence, ReceivedAt: item.receivedAt,
			}
		}
	case protocol.DockerChangeDelete:
		delete(state.containers, change.ContainerID)
	case protocol.DockerChangeStale:
		if exists {
			previous = cloneRecord(previous)
			previous.Container.Stale = true
			previous.Container.UnavailableReason = change.Reason
			previous.Generation = state.generation
			previous.Sequence = change.Sequence
			previous.ReceivedAt = item.receivedAt
			state.containers[change.ContainerID] = previous
		}
	default:
		return ErrInvalidBatch
	}
	state.resourceSequence[change.ContainerID] = change.Sequence
	state.lastDataSequence = maxUint64(state.lastDataSequence, change.Sequence)
	return nil
}

func authoritativeHealth(health protocol.DockerHealth) bool {
	return health.Availability == protocol.DockerAvailabilityAvailable && health.SnapshotFresh
}

func stateEngineAvailable(state *nodeState) bool {
	return state.health != nil && state.health.Availability == protocol.DockerAvailabilityAvailable
}

func nonAuthoritativeReason(health *protocol.DockerHealth) string {
	if health == nil {
		return "docker_state_unknown"
	}
	if health.Reason != "" {
		return health.Reason
	}
	if health.ErrorKind != "" {
		return health.ErrorKind
	}
	if health.Availability == protocol.DockerAvailabilityUnavailable {
		return "docker_engine_unavailable"
	}
	if !health.SnapshotFresh {
		return "docker_snapshot_stale"
	}
	return "docker_state_unknown"
}

func firstReason(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func validIdentity(identity Identity) bool {
	return strings.TrimSpace(identity.AgentID) == identity.AgentID && identity.AgentID != "" && len(identity.AgentID) <= maxDockerIdentityBytes &&
		strings.TrimSpace(identity.NodeID) == identity.NodeID && identity.NodeID != "" && len(identity.NodeID) <= maxDockerIdentityBytes
}

func cloneRecord(record ContainerRecord) ContainerRecord {
	record.Container = cloneDockerContainer(record.Container)
	return record
}

func cloneNodeStateForView(state *nodeState) *nodeState {
	copy := &nodeState{
		identity: state.identity, generation: state.generation,
		hasCompleteSnapshot: state.hasCompleteSnapshot,
		healthReceived:      state.healthReceived, staleReason: state.staleReason,
		containers: make(map[string]ContainerRecord, len(state.containers)),
	}
	if state.health != nil {
		health := cloneDockerHealth(*state.health)
		copy.health = &health
	}
	for id, record := range state.containers {
		copy.containers[id] = cloneRecord(record)
	}
	return copy
}

func cloneDockerHealth(health protocol.DockerHealth) protocol.DockerHealth {
	if health.LastSuccessAt != nil {
		value := *health.LastSuccessAt
		health.LastSuccessAt = &value
	}
	if health.LastSnapshotAt != nil {
		value := *health.LastSnapshotAt
		health.LastSnapshotAt = &value
	}
	return health
}

func cloneDockerChange(change protocol.DockerChange) protocol.DockerChange {
	if change.Container != nil {
		container := cloneDockerContainer(*change.Container)
		change.Container = &container
	}
	return change
}

func cloneDockerContainer(container protocol.DockerContainer) protocol.DockerContainer {
	if container.CreatedAt != nil {
		value := *container.CreatedAt
		container.CreatedAt = &value
	}
	if container.StartedAt != nil {
		value := *container.StartedAt
		container.StartedAt = &value
	}
	if container.FinishedAt != nil {
		value := *container.FinishedAt
		container.FinishedAt = &value
	}
	if container.ExitCode != nil {
		value := *container.ExitCode
		container.ExitCode = &value
	}
	if container.Compose != nil {
		value := *container.Compose
		container.Compose = &value
	}
	container.Ports = append([]protocol.DockerPort(nil), container.Ports...)
	for index := range container.Ports {
		container.Ports[index].Configured = append([]protocol.DockerHostPort(nil), container.Ports[index].Configured...)
		container.Ports[index].Published = append([]protocol.DockerHostPort(nil), container.Ports[index].Published...)
	}
	container.Networks = append([]protocol.DockerNetwork(nil), container.Networks...)
	for index := range container.Networks {
		container.Networks[index].Aliases = append([]string(nil), container.Networks[index].Aliases...)
	}
	container.Mounts = append([]protocol.DockerMount(nil), container.Mounts...)
	return container
}

func maxUint64(left, right uint64) uint64 {
	if left > right {
		return left
	}
	return right
}

func expireStageLocked(state *nodeState, now time.Time, ttl time.Duration) bool {
	if state.stage == nil || now.Sub(state.stage.startedAt) <= ttl {
		return false
	}
	stage := state.stage
	state.stage = nil
	state.staleReason = "snapshot_staging_expired"
	state.ignored = &ignoredSnapshot{
		snapshotID: stage.snapshotID,
		sequence:   stage.sequence,
		nextIndex:  stage.nextIndex,
		startedAt:  now,
		expired:    true,
	}
	return true
}
