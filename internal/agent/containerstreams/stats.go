package containerstreams

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

const statsSubscriberQueue = 1
const maxStatsSampleInterval = 30 * time.Second

const (
	maxStatsRecordBytes      = 1 << 20
	maxStatsJSONDepth        = 64
	maxStatsJSONTokens       = 16_384
	maxStatsContainerEntries = 4_096
	maxStatsJSONStringBytes  = 64 << 10
)

var (
	ErrStatsManagerClosed  = errors.New("Docker stats manager is closed")
	ErrStatsReaderClose    = errors.New("Docker stats reader did not stop after unsubscribe")
	ErrStatsStreamClosing  = errors.New("Docker stats stream is still closing")
	ErrStatsOpen           = errors.New("could not open Docker stats stream")
	ErrStatsRead           = errors.New("could not read Docker stats stream")
	ErrStatsMalformed      = errors.New("Docker returned a malformed stats record")
	ErrStatsRecordTooLarge = errors.New("Docker stats record exceeds the size limit")
	ErrStatsStructureLimit = errors.New("Docker stats record exceeds structural limits")
	ErrStatsContainerID    = errors.New("Docker stats record belongs to a different container")
	ErrStatsStreamEnded    = errors.New("Docker stats stream ended unexpectedly")
)

type MetricState string

const (
	MetricOK      MetricState = "ok"
	MetricUnknown MetricState = "unknown"
)

type Metric[T any] struct {
	State     MetricState `json:"state"`
	Value     *T          `json:"value,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	SampledAt *time.Time  `json:"sampled_at,omitempty"`
}

type MemoryUsage struct {
	UsedBytes   uint64  `json:"used_bytes"`
	CacheBytes  uint64  `json:"cache_bytes"`
	LimitBytes  uint64  `json:"limit_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type NetworkRate struct {
	ReceivedBytesPerSecond float64 `json:"received_bytes_per_second"`
	SentBytesPerSecond     float64 `json:"sent_bytes_per_second"`
}

type BlockIORate struct {
	ReadBytesPerSecond  float64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond float64 `json:"write_bytes_per_second"`
}

type StatsSnapshot struct {
	ContainerID string              `json:"container_id"`
	ObservedAt  *time.Time          `json:"observed_at,omitempty"`
	CPUPercent  Metric[float64]     `json:"cpu_percent"`
	Memory      Metric[MemoryUsage] `json:"memory"`
	Network     Metric[NetworkRate] `json:"network"`
	BlockIO     Metric[BlockIORate] `json:"block_io"`
}

type StatsEngine interface {
	OpenStats(context.Context, string) (io.ReadCloser, error)
}

type StatsManagerOptions struct {
	CloseTimeout time.Duration
}

type StatsManager struct {
	engine       StatsEngine
	closeTimeout time.Duration

	mu      sync.Mutex
	streams map[string]*statsStream
	closed  bool
}

type statsStream struct {
	manager *StatsManager
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	closing bool

	bodyMu sync.Mutex
	body   io.ReadCloser

	subscribers map[*StatsSubscription]struct{}
}

// StatsSubscription has a one-element latest-value queue. Slow consumers lose
// superseded samples rather than causing unbounded memory growth or blocking
// Docker's shared stats reader.
type StatsSubscription struct {
	Updates <-chan StatsSnapshot
	Errors  <-chan error

	manager  *StatsManager
	stream   *statsStream
	updates  chan StatsSnapshot
	errors   chan error
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func NewStatsManager(engine StatsEngine, options StatsManagerOptions) (*StatsManager, error) {
	if engine == nil {
		return nil, errors.New("Docker stats engine is required")
	}
	if options.CloseTimeout == 0 {
		options.CloseTimeout = 5 * time.Second
	}
	if options.CloseTimeout < 100*time.Millisecond || options.CloseTimeout > time.Minute {
		return nil, errors.New("Docker stats close timeout is outside the supported bounds")
	}
	return &StatsManager{engine: engine, closeTimeout: options.CloseTimeout, streams: make(map[string]*statsStream)}, nil
}

// Subscribe starts one Engine stats request for a container and shares its
// newest sample with every subscriber. With no subscribers there is no stats
// request or background polling for that container.
func (m *StatsManager) Subscribe(ctx context.Context, id string) (*StatsSubscription, error) {
	if ctx == nil {
		return nil, errors.New("Docker stats subscription context is required")
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrStatsManagerClosed
	}
	stream := m.streams[id]
	if stream != nil && stream.closing {
		m.mu.Unlock()
		return nil, ErrStatsStreamClosing
	}
	start := false
	if stream == nil {
		streamCtx, cancel := context.WithCancel(context.Background())
		stream = &statsStream{manager: m, id: id, ctx: streamCtx, cancel: cancel,
			done: make(chan struct{}), subscribers: make(map[*StatsSubscription]struct{})}
		m.streams[id] = stream
		start = true
	}
	updates := make(chan StatsSnapshot, statsSubscriberQueue)
	errorsOut := make(chan error, 1)
	done := make(chan struct{})
	sub := &StatsSubscription{Updates: updates, Errors: errorsOut, manager: m, stream: stream,
		updates: updates, errors: errorsOut, done: done}
	stream.subscribers[sub] = struct{}{}
	if start {
		go m.readStream(stream)
	}
	m.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		case <-sub.done:
		case <-stream.done:
		}
	}()
	return sub, nil
}

func (s *StatsSubscription) Done() <-chan struct{} { return s.done }

func (s *StatsSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() { s.closeErr = s.manager.unsubscribe(s) })
	return s.closeErr
}

func (m *StatsManager) unsubscribe(sub *StatsSubscription) error {
	m.mu.Lock()
	stream := sub.stream
	if _, ok := stream.subscribers[sub]; !ok {
		m.mu.Unlock()
		return nil
	}
	delete(stream.subscribers, sub)
	close(sub.updates)
	close(sub.errors)
	close(sub.done)
	last := len(stream.subscribers) == 0 && m.streams[stream.id] == stream
	if last {
		stream.closing = true
	}
	m.mu.Unlock()
	if !last {
		return nil
	}
	stream.cancel()
	stream.closeBody()
	select {
	case <-stream.done:
		return nil
	case <-time.After(m.closeTimeout):
		return ErrStatsReaderClose
	}
}

func (m *StatsManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	streams := make([]*statsStream, 0, len(m.streams))
	for _, stream := range m.streams {
		streams = append(streams, stream)
	}
	m.mu.Unlock()

	for _, stream := range streams {
		stream.cancel()
		stream.closeBody()
	}
	var closeErr error
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-time.After(m.closeTimeout):
			closeErr = ErrStatsReaderClose
		}
	}
	return closeErr
}

func (m *StatsManager) readStream(stream *statsStream) {
	var streamErr error
	defer func() {
		stream.closeBody()
		m.finish(stream, streamErr)
		m.removeFinished(stream)
	}()

	body, err := m.engine.OpenStats(stream.ctx, stream.id)
	if err != nil {
		if stream.ctx.Err() == nil {
			streamErr = ErrStatsOpen
		}
		return
	}
	if body == nil {
		if stream.ctx.Err() == nil {
			streamErr = ErrStatsOpen
		}
		return
	}
	stream.setBody(body)
	reader := bufio.NewReaderSize(body, 32*1024)
	var previous *rawDockerStats
	for {
		record, err := readStatsRecord(reader)
		if err != nil {
			if stream.ctx.Err() == nil {
				if errors.Is(err, io.EOF) {
					streamErr = ErrStatsStreamEnded
				} else if errors.Is(err, ErrStatsRecordTooLarge) {
					streamErr = ErrStatsRecordTooLarge
				} else {
					streamErr = ErrStatsRead
				}
			}
			return
		}
		if len(bytes.TrimSpace(record)) == 0 {
			continue
		}
		if err := validateStatsJSON(record); err != nil {
			streamErr = err
			return
		}
		var sample rawDockerStats
		if err := json.Unmarshal(record, &sample); err != nil {
			streamErr = ErrStatsMalformed
			return
		}
		if sample.ID != stream.id {
			streamErr = ErrStatsContainerID
			return
		}
		// time.Now carries a process-local monotonic clock reading. Keep it on
		// each in-memory sample to detect stream gaps and wall-clock anomalies;
		// never use arrival spacing as the rate denominator.
		sample.receivedAt = time.Now()
		m.publish(stream, calculateStats(stream.id, sample, previous))
		copy := sample
		previous = &copy
	}
}

func readStatsRecord(reader *bufio.Reader) ([]byte, error) {
	record := make([]byte, 0, 4096)
	for {
		part, err := reader.ReadSlice('\n')
		if len(record)+len(part) > maxStatsRecordBytes {
			return nil, ErrStatsRecordTooLarge
		}
		record = append(record, part...)
		if err == nil {
			return record, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(record) == 0 {
				return nil, io.EOF
			}
			return record, nil
		}
		return nil, err
	}
}

func validateStatsJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	tokenCount := 0
	nextToken := func() (json.Token, error) {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		tokenCount++
		if tokenCount > maxStatsJSONTokens {
			return nil, ErrStatsStructureLimit
		}
		switch value := token.(type) {
		case string:
			if len(value) > maxStatsJSONStringBytes {
				return nil, ErrStatsStructureLimit
			}
		case json.Number:
			if len(value) > 128 {
				return nil, ErrStatsStructureLimit
			}
		}
		return token, nil
	}
	var walk func(int) error
	walk = func(depth int) error {
		token, err := nextToken()
		if err != nil {
			if errors.Is(err, ErrStatsStructureLimit) {
				return ErrStatsStructureLimit
			}
			return ErrStatsMalformed
		}
		delim, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		if depth >= maxStatsJSONDepth {
			return ErrStatsStructureLimit
		}
		switch delim {
		case '{':
			entries := 0
			for decoder.More() {
				key, err := nextToken()
				if err != nil {
					if errors.Is(err, ErrStatsStructureLimit) {
						return ErrStatsStructureLimit
					}
					return ErrStatsMalformed
				}
				if _, ok := key.(string); !ok {
					return ErrStatsMalformed
				}
				entries++
				if entries > maxStatsContainerEntries {
					return ErrStatsStructureLimit
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			closeToken, err := nextToken()
			if err != nil {
				if errors.Is(err, ErrStatsStructureLimit) {
					return ErrStatsStructureLimit
				}
				return ErrStatsMalformed
			}
			if closeToken != json.Delim('}') {
				return ErrStatsMalformed
			}
		case '[':
			entries := 0
			for decoder.More() {
				entries++
				if entries > maxStatsContainerEntries {
					return ErrStatsStructureLimit
				}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			closeToken, err := nextToken()
			if err != nil {
				if errors.Is(err, ErrStatsStructureLimit) {
					return ErrStatsStructureLimit
				}
				return ErrStatsMalformed
			}
			if closeToken != json.Delim(']') {
				return ErrStatsMalformed
			}
		default:
			return ErrStatsMalformed
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrStatsMalformed
	}
	return nil
}

func (m *StatsManager) publish(stream *statsStream, snapshot StatsSnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams[stream.id] != stream || stream.ctx.Err() != nil {
		return
	}
	for sub := range stream.subscribers {
		subscriptionSnapshot := cloneStatsSnapshot(snapshot)
		select {
		case sub.updates <- subscriptionSnapshot:
		default:
			select {
			case <-sub.updates:
			default:
			}
			select {
			case sub.updates <- subscriptionSnapshot:
			default:
			}
		}
	}
}

func cloneStatsSnapshot(snapshot StatsSnapshot) StatsSnapshot {
	if snapshot.ObservedAt != nil {
		observedAt := *snapshot.ObservedAt
		snapshot.ObservedAt = &observedAt
	}
	snapshot.CPUPercent = cloneMetric(snapshot.CPUPercent)
	snapshot.Memory = cloneMetric(snapshot.Memory)
	snapshot.Network = cloneMetric(snapshot.Network)
	snapshot.BlockIO = cloneMetric(snapshot.BlockIO)
	return snapshot
}

func cloneMetric[T any](metric Metric[T]) Metric[T] {
	if metric.Value != nil {
		value := *metric.Value
		metric.Value = &value
	}
	if metric.SampledAt != nil {
		sampledAt := *metric.SampledAt
		metric.SampledAt = &sampledAt
	}
	return metric
}

func (m *StatsManager) finish(stream *statsStream, streamErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stream.closing = true
	for sub := range stream.subscribers {
		if streamErr != nil {
			select {
			case sub.errors <- streamErr:
			default:
			}
		}
		close(sub.updates)
		close(sub.errors)
		close(sub.done)
		delete(stream.subscribers, sub)
	}
}

func (m *StatsManager) removeFinished(stream *statsStream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.streams[stream.id] == stream {
		delete(m.streams, stream.id)
	}
	close(stream.done)
}

func (s *statsStream) setBody(body io.ReadCloser) {
	s.bodyMu.Lock()
	if s.ctx.Err() != nil {
		s.bodyMu.Unlock()
		_ = body.Close()
		return
	}
	s.body = body
	s.bodyMu.Unlock()
}

func (s *statsStream) closeBody() {
	s.bodyMu.Lock()
	body := s.body
	s.body = nil
	s.bodyMu.Unlock()
	if body != nil {
		_ = body.Close()
	}
}

type rawDockerStats struct {
	ID          string            `json:"id"`
	Read        *time.Time        `json:"read"`
	PreRead     *time.Time        `json:"preread"`
	CPUStats    *rawCPUStats      `json:"cpu_stats"`
	PreCPUStats *rawCPUStats      `json:"precpu_stats"`
	MemoryStats *rawMemoryStats   `json:"memory_stats"`
	Networks    map[string]rawNet `json:"networks"`
	BlkioStats  *rawBlockIOStats  `json:"blkio_stats"`
	receivedAt  time.Time
}

type rawCPUStats struct {
	CPUUsage    *rawCPUUsage `json:"cpu_usage"`
	SystemUsage *uint64      `json:"system_cpu_usage"`
	OnlineCPUs  *uint32      `json:"online_cpus"`
}

type rawCPUUsage struct {
	TotalUsage *uint64  `json:"total_usage"`
	PerCPU     []uint64 `json:"percpu_usage"`
}

type rawMemoryStats struct {
	Usage *uint64           `json:"usage"`
	Limit *uint64           `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type rawNet struct {
	RxBytes *uint64 `json:"rx_bytes"`
	TxBytes *uint64 `json:"tx_bytes"`
}

type rawBlockIOStats struct {
	ServiceBytes *[]rawBlockIOEntry `json:"io_service_bytes_recursive"`
}

type rawBlockIOEntry struct {
	Major uint64 `json:"major"`
	Minor uint64 `json:"minor"`
	Op    string `json:"op"`
	Value uint64 `json:"value"`
}

func calculateStats(id string, current rawDockerStats, previous *rawDockerStats) StatsSnapshot {
	at := time.Time{}
	if current.Read != nil {
		at = current.Read.UTC()
	}
	return StatsSnapshot{ContainerID: id, ObservedAt: optionalTime(at),
		CPUPercent: calculateCPU(current, at),
		Memory:     calculateMemory(current, at),
		Network:    calculateNetwork(current, previous, at),
		BlockIO:    calculateBlockIO(current, previous, at)}
}

func calculateCPU(sample rawDockerStats, at time.Time) Metric[float64] {
	unknown := func(reason string) Metric[float64] { return unknownMetric[float64](reason, at) }
	if sample.Read == nil || sample.Read.IsZero() || sample.PreRead == nil || sample.PreRead.IsZero() {
		return unknown("warming_up")
	}
	if !sample.Read.After(*sample.PreRead) {
		return unknown("invalid_interval")
	}
	current, previous := sample.CPUStats, sample.PreCPUStats
	if current == nil || previous == nil || current.CPUUsage == nil || previous.CPUUsage == nil ||
		current.CPUUsage.TotalUsage == nil || previous.CPUUsage.TotalUsage == nil ||
		current.SystemUsage == nil || previous.SystemUsage == nil {
		return unknown("unavailable")
	}
	if *current.CPUUsage.TotalUsage < *previous.CPUUsage.TotalUsage || *current.SystemUsage < *previous.SystemUsage {
		return unknown("counter_reset")
	}
	cpuDelta := *current.CPUUsage.TotalUsage - *previous.CPUUsage.TotalUsage
	systemDelta := *current.SystemUsage - *previous.SystemUsage
	if systemDelta == 0 {
		return unknown("no_counter_progress")
	}
	cores := uint32(0)
	if current.OnlineCPUs != nil {
		cores = *current.OnlineCPUs
	}
	if cores == 0 {
		cores = uint32(len(current.CPUUsage.PerCPU))
	}
	if cores == 0 {
		return unknown("cpu_count_unavailable")
	}
	percent := 100 * float64(cpuDelta) / float64(systemDelta) * float64(cores)
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent < 0 {
		return unknown("invalid_data")
	}
	return knownMetric(percent, at)
}

func calculateMemory(sample rawDockerStats, at time.Time) Metric[MemoryUsage] {
	if sample.Read == nil || sample.Read.IsZero() {
		return unknownMetric[MemoryUsage]("sample_time_unavailable", at)
	}
	if sample.MemoryStats == nil || sample.MemoryStats.Usage == nil || sample.MemoryStats.Limit == nil {
		return unknownMetric[MemoryUsage]("unavailable", at)
	}
	cache, ok := memoryCache(sample.MemoryStats.Stats)
	if !ok {
		return unknownMetric[MemoryUsage]("cache_unavailable", at)
	}
	usage, limit := *sample.MemoryStats.Usage, *sample.MemoryStats.Limit
	if cache > usage {
		return unknownMetric[MemoryUsage]("invalid_data", at)
	}
	if limit == 0 {
		return unknownMetric[MemoryUsage]("limit_unavailable", at)
	}
	used := usage - cache
	percent := 100 * float64(used) / float64(limit)
	if math.IsNaN(percent) || math.IsInf(percent, 0) {
		return unknownMetric[MemoryUsage]("invalid_data", at)
	}
	return knownMetric(MemoryUsage{UsedBytes: used, CacheBytes: cache, LimitBytes: limit, UsedPercent: percent}, at)
}

func memoryCache(stats map[string]uint64) (uint64, bool) {
	if stats == nil {
		return 0, false
	}
	for _, key := range []string{"inactive_file", "total_inactive_file", "cache"} {
		if value, ok := stats[key]; ok {
			return value, true
		}
	}
	return 0, false
}

func calculateNetwork(current rawDockerStats, previous *rawDockerStats, at time.Time) Metric[NetworkRate] {
	if current.Networks == nil {
		return unknownMetric[NetworkRate]("unavailable", at)
	}
	if previous == nil {
		return unknownMetric[NetworkRate]("warming_up", at)
	}
	if previous.Networks == nil {
		return unknownMetric[NetworkRate]("warming_up", at)
	}
	if !sameNetworkSet(current.Networks, previous.Networks) {
		return unknownMetric[NetworkRate]("interface_set_changed", at)
	}
	seconds, reason := trustedStatsSampleInterval(current, *previous)
	if reason != "" {
		return unknownMetric[NetworkRate](reason, at)
	}
	var rxDelta, txDelta uint64
	for name, now := range current.Networks {
		old := previous.Networks[name]
		if now.RxBytes == nil || now.TxBytes == nil || old.RxBytes == nil || old.TxBytes == nil {
			return unknownMetric[NetworkRate]("invalid_data", at)
		}
		if *now.RxBytes < *old.RxBytes || *now.TxBytes < *old.TxBytes {
			return unknownMetric[NetworkRate]("counter_reset", at)
		}
		var ok bool
		rxDelta, ok = addCounter(rxDelta, *now.RxBytes-*old.RxBytes)
		if !ok {
			return unknownMetric[NetworkRate]("invalid_data", at)
		}
		txDelta, ok = addCounter(txDelta, *now.TxBytes-*old.TxBytes)
		if !ok {
			return unknownMetric[NetworkRate]("invalid_data", at)
		}
	}
	return knownMetric(NetworkRate{ReceivedBytesPerSecond: float64(rxDelta) / seconds,
		SentBytesPerSecond: float64(txDelta) / seconds}, at)
}

func calculateBlockIO(current rawDockerStats, previous *rawDockerStats, at time.Time) Metric[BlockIORate] {
	currentIO, reason := blockIOCounters(current.BlkioStats)
	if reason != "" {
		return unknownMetric[BlockIORate](reason, at)
	}
	if previous == nil {
		return unknownMetric[BlockIORate]("warming_up", at)
	}
	previousIO, reason := blockIOCounters(previous.BlkioStats)
	if reason != "" {
		if reason == "unavailable" {
			return unknownMetric[BlockIORate]("warming_up", at)
		}
		return unknownMetric[BlockIORate](reason, at)
	}
	if !sameIOSet(currentIO, previousIO) {
		return unknownMetric[BlockIORate]("device_set_changed", at)
	}
	seconds, reason := trustedStatsSampleInterval(current, *previous)
	if reason != "" {
		return unknownMetric[BlockIORate](reason, at)
	}
	var readDelta, writeDelta uint64
	for device, now := range currentIO {
		old := previousIO[device]
		if now.read < old.read || now.write < old.write {
			return unknownMetric[BlockIORate]("counter_reset", at)
		}
		var ok bool
		readDelta, ok = addCounter(readDelta, now.read-old.read)
		if !ok {
			return unknownMetric[BlockIORate]("invalid_data", at)
		}
		writeDelta, ok = addCounter(writeDelta, now.write-old.write)
		if !ok {
			return unknownMetric[BlockIORate]("invalid_data", at)
		}
	}
	return knownMetric(BlockIORate{ReadBytesPerSecond: float64(readDelta) / seconds,
		WriteBytesPerSecond: float64(writeDelta) / seconds}, at)
}

type deviceIO struct{ read, write uint64 }

func blockIOCounters(stats *rawBlockIOStats) (map[string]deviceIO, string) {
	if stats == nil || stats.ServiceBytes == nil {
		return nil, "unavailable"
	}
	out := make(map[string]deviceIO)
	recognized := false
	for _, entry := range *stats.ServiceBytes {
		key := strings.ToLower(entry.Op)
		device := deviceKey(entry.Major, entry.Minor)
		value := out[device]
		var ok bool
		switch key {
		case "read":
			recognized = true
			value.read, ok = addCounter(value.read, entry.Value)
		case "write":
			recognized = true
			value.write, ok = addCounter(value.write, entry.Value)
		default:
			continue
		}
		if !ok {
			return nil, "invalid_data"
		}
		out[device] = value
	}
	if len(*stats.ServiceBytes) > 0 && !recognized {
		return nil, "unavailable"
	}
	return out, ""
}

func deviceKey(major, minor uint64) string {
	return strings.TrimSpace(strings.Join([]string{uintString(major), uintString(minor)}, ":"))
}

func sameNetworkSet(a, b map[string]rawNet) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

func sameIOSet(a, b map[string]deviceIO) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

func sampleInterval(current, previous *time.Time) (float64, string) {
	if current == nil || previous == nil || current.IsZero() || previous.IsZero() {
		return 0, "warming_up"
	}
	seconds := current.Sub(*previous).Seconds()
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, "invalid_interval"
	}
	return seconds, ""
}

// trustedStatsSampleInterval uses Docker's sample timestamps as the rate
// denominator. Local monotonic receive times only reject long stream gaps and
// anomalous timestamps; close receive spacing is expected when buffered Docker
// samples are drained and must never inflate the computed rate.
func trustedStatsSampleInterval(current, previous rawDockerStats) (float64, string) {
	seconds, reason := sampleInterval(current.Read, previous.Read)
	if reason != "" {
		if current.Read != nil && previous.Read != nil && !current.Read.After(*previous.Read) &&
			!current.receivedAt.IsZero() && !previous.receivedAt.IsZero() {
			return 0, "clock_jump"
		}
		return 0, reason
	}
	if seconds > maxStatsSampleInterval.Seconds() {
		return 0, "clock_jump"
	}
	if !current.receivedAt.IsZero() && !previous.receivedAt.IsZero() {
		receivedInterval := current.receivedAt.Sub(previous.receivedAt)
		if receivedInterval <= 0 {
			return 0, "receive_time_invalid"
		}
		if receivedInterval > maxStatsSampleInterval {
			return 0, "sample_gap"
		}
	}
	return seconds, ""
}

func addCounter(a, b uint64) (uint64, bool) {
	if math.MaxUint64-a < b {
		return 0, false
	}
	return a + b, true
}

func knownMetric[T any](value T, at time.Time) Metric[T] {
	return Metric[T]{State: MetricOK, Value: &value, SampledAt: optionalTime(at)}
}

func unknownMetric[T any](reason string, at time.Time) Metric[T] {
	return Metric[T]{State: MetricUnknown, Reason: reason, SampledAt: optionalTime(at)}
}

func optionalTime(at time.Time) *time.Time {
	if at.IsZero() {
		return nil
	}
	value := at
	return &value
}

func uintString(value uint64) string {
	return strconv.FormatUint(value, 10)
}
