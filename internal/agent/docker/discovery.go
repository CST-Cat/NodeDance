package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
)

const (
	DefaultFullScanInterval = 60 * time.Second
	DefaultHealthInterval   = 5 * time.Second
	DefaultRequestTimeout   = 8 * time.Second
	MaxBatchChanges         = 64
	MaxBatchBytes           = 48 * 1024
	MaxContainerRecordBytes = MaxBatchBytes - 1024
)

type Options struct {
	FullScanInterval   time.Duration
	HealthInterval     time.Duration
	RequestTimeout     time.Duration
	MaxInspectParallel int
	MaxPendingEvents   int
	ObserverQueueSize  int
	ReconnectMin       time.Duration
	ReconnectMax       time.Duration
	Now                func() time.Time
}

func (o Options) withDefaults() Options {
	if o.FullScanInterval <= 0 {
		o.FullScanInterval = DefaultFullScanInterval
	}
	if o.HealthInterval <= 0 {
		o.HealthInterval = DefaultHealthInterval
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.MaxInspectParallel <= 0 {
		o.MaxInspectParallel = 8
	}
	if o.MaxPendingEvents <= 0 {
		o.MaxPendingEvents = 2048
	}
	if o.ObserverQueueSize <= 0 {
		o.ObserverQueueSize = 128
	}
	if o.ReconnectMin <= 0 {
		o.ReconnectMin = time.Second
	}
	if o.ReconnectMax < o.ReconnectMin {
		o.ReconnectMax = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
	return o
}

type Discoverer struct {
	engine   Engine
	observer Observer
	opts     Options
	cache    *stateCache
	bus      *observerBus
}

func NewDiscoverer(engine Engine, observer Observer, options Options) (*Discoverer, error) {
	if engine == nil {
		return nil, errors.New("Docker Engine is required")
	}
	opts := options.withDefaults()
	state := newStateCache(opts.Now)
	d := &Discoverer{engine: engine, observer: observer, opts: opts, cache: state, bus: nil}
	d.bus = newObserverBus(observer, opts.ObserverQueueSize, d.cache.currentBatch)
	return d, nil
}

// Current returns a copy of the last known inventory and Docker health. If
// Docker is unavailable the inventory is retained and health says so.
func (d *Discoverer) Current() ([]Container, Health) {
	return d.cache.view()
}

// Run starts event following, bounded reconciliation, periodic complete
// snapshots, health checks, and asynchronous observer delivery. It returns
// only after ctx is canceled and never takes part in Agent heartbeats.
func (d *Discoverer) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		d.bus.close()
		workers.Wait()
		_ = d.engine.Close()
	}()
	startWorker := func(work func()) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			work()
		}()
	}
	startWorker(func() { d.bus.run(runCtx) })

	scanRequests := make(chan struct{}, 1)
	scanResults := make(chan scanResult, 1)
	scanAcks := make(chan struct{}, 1)
	eventInput := make(chan Event, 256)
	eventStatuses := make(chan eventStatus, 4)
	pingResults := make(chan error, 1)
	queue := newReconcileQueue(d.opts.MaxPendingEvents)

	startWorker(func() { d.scanWorker(runCtx, scanRequests, scanResults, scanAcks) })
	startWorker(func() { d.eventSupervisor(runCtx, eventInput, eventStatuses) })
	startWorker(func() { d.reconcileWorker(runCtx, queue, scanRequests) })
	startWorker(func() { d.healthWorker(runCtx, pingResults) })

	requestScan(scanRequests)
	scanTicker := time.NewTicker(d.opts.FullScanInterval)
	defer scanTicker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return nil
		case <-scanTicker.C:
			requestScan(scanRequests)
		case event := <-eventInput:
			if !relevantContainerEvent(event.Action) {
				continue
			}
			revision := d.cache.observeEvent(event)
			if revision == 0 || !queue.add(event.ContainerID, revision) {
				requestScan(scanRequests)
			}
		case status := <-eventStatuses:
			d.cache.setEventStatus(status.connected, status.err)
			d.bus.publishHealth(d.cache.healthSnapshot())
			// Subscribe before reconciling so changes during the snapshot are
			// either in the event stream or represented in its full inventory.
			requestScan(scanRequests)
		case err := <-pingResults:
			if err != nil {
				for _, change := range d.cache.markInventoryStale(safeError(err), d.opts.Now()) {
					d.bus.publishChange(change)
				}
			}
			d.cache.setPingResult(err)
			d.bus.publishHealth(d.cache.healthSnapshot())
		case result := <-scanResults:
			if result.complete {
				d.cache.applySnapshot(result.startedSequence, result.containers)
				d.bus.requestSnapshot()
			} else {
				for _, change := range d.cache.markInventoryStale(safeError(result.err), d.opts.Now()) {
					d.bus.publishChange(change)
				}
				d.cache.setSnapshotFailure(result.err)
				d.bus.publishHealth(d.cache.healthSnapshot())
			}
			select {
			case scanAcks <- struct{}{}:
			default:
			}
		}
	}
}

func (d *Discoverer) scanWorker(ctx context.Context, requests <-chan struct{}, results chan<- scanResult, acks <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-requests:
			result := d.scan(ctx)
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}
			select {
			case <-acks:
			case <-ctx.Done():
				return
			}
		}
	}
}

type scanResult struct {
	startedSequence uint64
	containers      []Container
	complete        bool
	err             error
}

func (d *Discoverer) scan(parent context.Context) scanResult {
	result := scanResult{startedSequence: d.cache.currentSequence()}
	listCtx, cancel := context.WithTimeout(parent, d.opts.RequestTimeout)
	ids, err := d.engine.ListAll(listCtx)
	cancel()
	if err != nil {
		result.err = fmt.Errorf("list all Docker containers: %w", err)
		return result
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			result.err = fmt.Errorf("Docker Engine returned duplicate container ID %q", ids[i])
			return result
		}
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			result.err = errors.New("Docker Engine returned an empty container ID")
			return result
		}
	}
	if len(ids) == 0 {
		result.complete = true
		result.containers = []Container{}
		return result
	}
	type inspectResult struct {
		index     int
		container Container
		err       error
	}
	jobs := make(chan int)
	results := make(chan inspectResult, len(ids))
	workers := d.opts.MaxInspectParallel
	if workers > len(ids) {
		workers = len(ids)
	}
	var group sync.WaitGroup
	group.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer group.Done()
			for index := range jobs {
				requestCtx, requestCancel := context.WithTimeout(parent, d.opts.RequestTimeout)
				container, inspectErr := d.engine.Inspect(requestCtx, ids[index])
				requestCancel()
				if inspectErr == nil && container.ID != ids[index] {
					inspectErr = fmt.Errorf("Inspect returned ID %q for requested ID %q", container.ID, ids[index])
				}
				results <- inspectResult{index: index, container: container, err: inspectErr}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range ids {
			select {
			case jobs <- index:
			case <-parent.Done():
				return
			}
		}
	}()
	containers := make([]Container, len(ids))
	var firstErr error
	for range ids {
		select {
		case item := <-results:
			if item.err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("inspect Docker container %q: %w", ids[item.index], item.err)
				}
				continue
			}
			containers[item.index] = item.container
		case <-parent.Done():
			if firstErr == nil {
				firstErr = parent.Err()
			}
		}
	}
	group.Wait()
	if firstErr != nil {
		result.err = firstErr
		return result
	}
	result.containers = containers
	result.complete = true
	return result
}

func (d *Discoverer) eventSupervisor(ctx context.Context, output chan<- Event, statuses chan eventStatus) {
	backoff := d.opts.ReconnectMin
	for {
		if ctx.Err() != nil {
			return
		}
		// OpenEvents receives the run context because the SDK request context
		// also owns the long-lived stream after its response headers arrive.
		// SDKEngine bounds only that header wait with ResponseHeaderTimeout.
		stream, err := d.engine.OpenEvents(ctx)
		if err != nil {
			postEventStatus(ctx, statuses, false, err)
			if !waitContext(ctx, backoff) {
				return
			}
			backoff = growBackoff(backoff, d.opts.ReconnectMax)
			continue
		}
		connectedAt := d.opts.Now()
		postEventStatus(ctx, statuses, true, nil)
		var streamErr error
		messages := stream.Events
		streamErrors := stream.Errors
		for messages != nil || streamErrors != nil {
			select {
			case <-ctx.Done():
				if stream.Close != nil {
					stream.Close()
				}
				return
			case event, ok := <-messages:
				if !ok {
					messages = nil
					continue
				}
				select {
				case output <- event:
				default:
					streamErr = errors.New("Docker event consumer backlog exceeded its bounded queue")
					messages = nil
					streamErrors = nil
				}
			case err, ok := <-streamErrors:
				if !ok {
					streamErrors = nil
					continue
				}
				if err != nil {
					streamErr = err
					messages = nil
					streamErrors = nil
				}
			}
			if streamErr != nil {
				break
			}
		}
		if stream.Close != nil {
			stream.Close()
		}
		if ctx.Err() != nil {
			return
		}
		if streamErr == nil {
			streamErr = errors.New("Docker event stream ended")
		}
		postEventStatus(ctx, statuses, false, streamErr)
		if d.opts.Now().Sub(connectedAt) >= time.Minute {
			backoff = d.opts.ReconnectMin
		}
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = growBackoff(backoff, d.opts.ReconnectMax)
	}
}

type eventStatus struct {
	connected bool
	err       error
}

func postEventStatus(ctx context.Context, statuses chan eventStatus, connected bool, err error) {
	select {
	case statuses <- eventStatus{connected: connected, err: err}:
	default:
		select {
		case <-statuses:
		default:
		}
		select {
		case statuses <- eventStatus{connected: connected, err: err}:
		case <-ctx.Done():
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func growBackoff(current, maximum time.Duration) time.Duration {
	if current >= maximum/2 {
		return maximum
	}
	return current * 2
}

func (d *Discoverer) healthWorker(ctx context.Context, output chan error) {
	ticker := time.NewTicker(d.opts.HealthInterval)
	defer ticker.Stop()
	for {
		requestCtx, cancel := context.WithTimeout(ctx, d.opts.RequestTimeout)
		err := d.engine.Ping(requestCtx)
		cancel()
		select {
		case output <- err:
		default:
			select {
			case <-output:
			default:
			}
			select {
			case output <- err:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Discoverer) reconcileWorker(ctx context.Context, queue *reconcileQueue, requests chan<- struct{}) {
	for {
		id, revision, ok := queue.next(ctx)
		if !ok {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, d.opts.RequestTimeout)
		container, err := d.engine.Inspect(requestCtx, id)
		cancel()
		if err == nil && container.ID != id {
			err = fmt.Errorf("Inspect returned ID %q for requested ID %q", container.ID, id)
		}
		if err != nil && isNotFound(err) {
			if change, changed := d.cache.applyEventDelete(id, revision, d.opts.Now()); changed {
				d.bus.publishChange(change)
			}
			continue
		}
		if err != nil {
			if change, changed := d.cache.markContainerStale(id, safeError(err), d.opts.Now()); changed {
				d.bus.publishChange(change)
			}
			d.cache.setObservationFailure(err)
			d.bus.publishHealth(d.cache.healthSnapshot())
			requestScan(requests)
			continue
		}
		container.ObservedAt = d.opts.Now()
		if change, changed := d.cache.applyEventUpsert(id, revision, container); changed {
			d.bus.publishChange(change)
		}
	}
}

func isNotFound(err error) bool {
	return errdefs.IsNotFound(err)
}

func relevantContainerEvent(action string) bool {
	value := strings.ToLower(strings.TrimSpace(action))
	if strings.HasPrefix(value, "health_status") {
		return true
	}
	switch value {
	case "create", "start", "stop", "kill", "die", "pause", "unpause", "restart", "rename", "destroy", "remove", "oom", "update":
		return true
	default:
		return false
	}
}

func requestScan(requests chan<- struct{}) {
	select {
	case requests <- struct{}{}:
	default:
	}
}

type reconcileQueue struct {
	mu      sync.Mutex
	max     int
	pending map[string]uint64
	wake    chan struct{}
}

func newReconcileQueue(max int) *reconcileQueue {
	return &reconcileQueue{max: max, pending: make(map[string]uint64), wake: make(chan struct{}, 1)}
}

func (q *reconcileQueue) add(id string, revision uint64) bool {
	q.mu.Lock()
	if _, exists := q.pending[id]; !exists && len(q.pending) >= q.max {
		q.mu.Unlock()
		return false
	}
	q.pending[id] = revision
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *reconcileQueue) next(ctx context.Context) (string, uint64, bool) {
	for {
		q.mu.Lock()
		if len(q.pending) > 0 {
			ids := make([]string, 0, len(q.pending))
			for id := range q.pending {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			id := ids[0]
			revision := q.pending[id]
			delete(q.pending, id)
			q.mu.Unlock()
			return id, revision, true
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", 0, false
		case <-q.wake:
		}
	}
}

type stateCache struct {
	mu         sync.RWMutex
	containers map[string]Container
	revisions  map[string]uint64
	tombstones map[string]uint64
	sequence   uint64
	snapshotID uint64
	health     Health
	now        func() time.Time
}

func newStateCache(now func() time.Time) *stateCache {
	return &stateCache{
		containers: make(map[string]Container), revisions: make(map[string]uint64),
		tombstones: make(map[string]uint64), now: now,
		health: Health{Availability: EngineUnavailable, SnapshotFresh: false, Reason: "waiting for Docker Engine", ErrorKind: "not_checked", ObservedAt: now()},
	}
}

func (c *stateCache) currentSequence() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sequence
}

func (c *stateCache) observeEvent(event Event) uint64 {
	if strings.TrimSpace(event.ContainerID) == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sequence++
	c.revisions[event.ContainerID] = c.sequence
	c.health.Sequence = c.sequence
	if strings.EqualFold(event.Action, "destroy") || strings.EqualFold(event.Action, "remove") {
		c.tombstones[event.ContainerID] = c.sequence
	}
	return c.sequence
}

func (c *stateCache) applyEventDelete(id string, revision uint64, now time.Time) (Change, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revisions[id] != revision {
		return Change{}, false
	}
	_, existed := c.containers[id]
	delete(c.containers, id)
	c.tombstones[id] = revision
	if !existed {
		return Change{}, false
	}
	c.sequence++
	c.health.Sequence = c.sequence
	return Change{Sequence: c.sequence, Action: ChangeDelete, ContainerID: id, ObservedAt: now.UTC()}, true
}

func (c *stateCache) markContainerStale(id, reason string, now time.Time) (Change, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	container, exists := c.containers[id]
	if !exists || container.Stale && container.UnavailableReason == reason {
		return Change{}, false
	}
	container.Stale = true
	container.UnavailableReason = reason
	c.sequence++
	c.health.Sequence = c.sequence
	c.containers[id] = container
	return Change{Sequence: c.sequence, Action: ChangeStale, ContainerID: id, Reason: reason, ObservedAt: now.UTC()}, true
}

func (c *stateCache) markInventoryStale(reason string, now time.Time) []Change {
	c.mu.Lock()
	defer c.mu.Unlock()
	ids := make([]string, 0, len(c.containers))
	for id := range c.containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	changes := make([]Change, 0, len(ids))
	for _, id := range ids {
		container := c.containers[id]
		if container.Stale && container.UnavailableReason == reason {
			continue
		}
		container.Stale = true
		container.UnavailableReason = reason
		c.sequence++
		c.health.Sequence = c.sequence
		c.containers[id] = container
		changes = append(changes, Change{
			Sequence: c.sequence, Action: ChangeStale, ContainerID: id,
			Reason: reason, ObservedAt: now.UTC(),
		})
	}
	return changes
}

func (c *stateCache) applyEventUpsert(id string, revision uint64, container Container) (Change, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revisions[id] != revision || container.ID != id {
		return Change{}, false
	}
	delete(c.tombstones, id)
	container = cloneContainer(container)
	previous, exists := c.containers[id]
	if !containerFitsBatch(container) {
		const reason = "container record exceeds the NodeDance Docker update size limit"
		if exists {
			previous.Stale = true
			previous.UnavailableReason = reason
			c.containers[id] = previous
			c.sequence++
			c.health.Sequence = c.sequence
			return Change{Sequence: c.sequence, Action: ChangeStale, ContainerID: id, Reason: reason, ObservedAt: container.ObservedAt}, true
		}
		container = compactUnknown(container, reason)
	}
	if exists && equivalentContainer(previous, container) {
		c.containers[id] = container
		return Change{}, false
	}
	c.containers[id] = container
	c.sequence++
	c.health.Sequence = c.sequence
	copy := cloneContainer(container)
	return Change{Sequence: c.sequence, Action: ChangeUpsert, ContainerID: id, Container: &copy, ObservedAt: container.ObservedAt}, true
}

func (c *stateCache) applySnapshot(startedSequence uint64, scanned []Container) {
	c.mu.Lock()
	defer c.mu.Unlock()
	listed := make(map[string]Container, len(scanned))
	for _, item := range scanned {
		item = cloneContainer(item)
		if !containerFitsBatch(item) {
			const reason = "container record exceeds the NodeDance Docker update size limit"
			if previous, exists := c.containers[item.ID]; exists {
				previous.Stale = true
				previous.UnavailableReason = reason
				item = previous
			} else {
				item = compactUnknown(item, reason)
			}
		}
		listed[item.ID] = item
	}
	merged := make(map[string]Container, len(listed)+len(c.containers))
	for id, item := range listed {
		if c.revisions[id] > startedSequence {
			if c.tombstones[id] > startedSequence {
				continue
			}
			if current, ok := c.containers[id]; ok {
				merged[id] = cloneContainer(current)
			}
			continue
		}
		merged[id] = item
	}
	for id, current := range c.containers {
		if _, seen := listed[id]; seen {
			continue
		}
		if c.revisions[id] > startedSequence {
			if c.tombstones[id] <= startedSequence {
				merged[id] = cloneContainer(current)
			}
		}
	}
	c.containers = merged
	for id, revision := range c.revisions {
		if revision <= startedSequence {
			delete(c.revisions, id)
			delete(c.tombstones, id)
		}
	}
	c.sequence++
	c.health.Sequence = c.sequence
	c.snapshotID++
	when := c.now()
	c.health.Availability = EngineAvailable
	c.health.SnapshotFresh = true
	c.health.LastSnapshotAt = timePointer(when)
	c.health.LastSuccessAt = timePointer(when)
	if c.health.ErrorKind != "events_unavailable" {
		c.health.ErrorKind = ""
		c.health.Reason = ""
	}
	c.health.ObservedAt = when
}

func equivalentContainer(left, right Container) bool {
	left.ObservedAt = time.Time{}
	right.ObservedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func (c *stateCache) setPingResult(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := cloneHealth(c.health)
	when := c.now()
	c.health.ObservedAt = when
	if err == nil {
		c.health.Availability = EngineAvailable
		c.health.LastSuccessAt = timePointer(when)
		if c.health.ErrorKind == "engine_unavailable" {
			c.health.ErrorKind = ""
			c.health.Reason = ""
		}
		c.finishHealthMutation(before)
		return
	}
	c.health.Availability = EngineUnavailable
	c.health.SnapshotFresh = false
	c.health.ErrorKind = classifyError(err)
	c.health.Reason = safeError(err)
	c.finishHealthMutation(before)
}

func (c *stateCache) setEventStatus(connected bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := cloneHealth(c.health)
	c.health.EventsConnected = connected
	c.health.ObservedAt = c.now()
	if connected {
		if c.health.ErrorKind == "events_unavailable" {
			c.health.ErrorKind = ""
			c.health.Reason = ""
		}
		c.finishHealthMutation(before)
		return
	}
	if err != nil && c.health.Availability == EngineAvailable {
		c.health.ErrorKind = "events_unavailable"
		c.health.Reason = safeError(err)
	}
	c.finishHealthMutation(before)
}

func (c *stateCache) setSnapshotFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := cloneHealth(c.health)
	c.health.SnapshotFresh = false
	c.health.ObservedAt = c.now()
	if err != nil {
		c.health.ErrorKind = classifyError(err)
		c.health.Reason = safeError(err)
	}
	c.finishHealthMutation(before)
}

func (c *stateCache) setObservationFailure(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := cloneHealth(c.health)
	c.health.SnapshotFresh = false
	c.health.ErrorKind = classifyError(err)
	c.health.Reason = safeError(err)
	c.health.ObservedAt = c.now()
	c.finishHealthMutation(before)
}

func (c *stateCache) finishHealthMutation(before Health) {
	if !equivalentHealth(before, c.health) {
		c.sequence++
		c.health.Sequence = c.sequence
	}
}

func (c *stateCache) healthSnapshot() Health {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneHealth(c.health)
}

func (c *stateCache) view() ([]Container, Health) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.containers))
	for id := range c.containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]Container, 0, len(ids))
	for _, id := range ids {
		items = append(items, cloneContainer(c.containers[id]))
	}
	return items, cloneHealth(c.health)
}

func (c *stateCache) currentBatch() (uint64, uint64, []Container, Health) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.containers))
	for id := range c.containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]Container, 0, len(ids))
	for _, id := range ids {
		items = append(items, cloneContainer(c.containers[id]))
	}
	return c.snapshotID, c.sequence, items, cloneHealth(c.health)
}

func (c *stateCache) setHealthChange(update func(*Health)) (Health, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	before := cloneHealth(c.health)
	update(&c.health)
	current := cloneHealth(c.health)
	current.ObservedAt = time.Time{}
	before.ObservedAt = time.Time{}
	if reflect.DeepEqual(before, current) {
		return cloneHealth(c.health), false
	}
	c.sequence++
	c.health.ObservedAt = c.now()
	return cloneHealth(c.health), true
}

func cloneHealth(value Health) Health {
	out := value
	out.LastSuccessAt = cloneTime(value.LastSuccessAt)
	out.LastSnapshotAt = cloneTime(value.LastSnapshotAt)
	return out
}

func timePointer(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}

func classifyError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "permission denied"), strings.Contains(message, "operation not permitted"):
		return "permission_denied"
	case strings.Contains(message, "api version") || strings.Contains(message, "client version") || strings.Contains(message, "minimum supported api"):
		return "api_incompatible"
	case strings.Contains(message, "connect:") || strings.Contains(message, "connection refused") || strings.Contains(message, "cannot connect") || strings.Contains(message, "no such file or directory"):
		return "engine_unavailable"
	default:
		return "engine_error"
	}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	const max = 512
	value := strings.TrimSpace(err.Error())
	if len(value) > max {
		value = value[:max] + "…"
	}
	return value
}

func containerFitsBatch(container Container) bool {
	copy := cloneContainer(container)
	change := Change{Sequence: ^uint64(0), Action: ChangeUpsert, ContainerID: copy.ID, Container: &copy, ObservedAt: copy.ObservedAt}
	batch := Batch{
		Sequence: ^uint64(0), SnapshotID: ^uint64(0), FullSnapshot: true,
		SnapshotIndex: 1_000_000, SnapshotFinal: true, Changes: []Change{change},
	}
	encoded, err := json.Marshal(batch)
	return err == nil && len(encoded) <= MaxContainerRecordBytes
}

func compactUnknown(container Container, reason string) Container {
	name := container.Name
	if len(name) > 255 {
		name = ""
	}
	return Container{
		ID: container.ID, Name: name, State: "unknown", Health: HealthUnknown,
		Stale: true, UnavailableReason: reason, ObservedAt: container.ObservedAt,
		Ports: []Port{}, Networks: []Network{}, Mounts: []Mount{},
	}
}
