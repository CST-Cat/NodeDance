package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	maxAgentBrowserStreams     = 16
	maxBrowserContainerStreams = 64
	browserStreamQueueMessages = 16
	containerStreamWriteLimit  = 5 * time.Second
	containerStreamIdleLimit   = 30 * time.Second
)

type coreBrowserStream struct {
	requestID    string
	nodeID       string
	containerID  string
	kind         string
	updates      chan protocol.Envelope
	done         chan struct{}
	mu           sync.Mutex
	lastSeq      uint64
	lastActivity time.Time
	terminal     bool
	closed       bool
	once         sync.Once
}

func (stream *coreBrowserStream) finish() {
	if stream == nil {
		return
	}
	stream.once.Do(func() {
		stream.mu.Lock()
		stream.closed = true
		close(stream.done)
		stream.mu.Unlock()
	})
}

func (stream *coreBrowserStream) enqueue(envelope protocol.Envelope) (terminal bool, err error) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return false, nil
	}
	if stream.terminal {
		return true, nil
	}
	if envelope.Sequence <= stream.lastSeq {
		return false, errors.New("Agent container stream sequence did not increase")
	}
	if envelope.Type == protocol.TypeContainerStreamHeartbeat {
		stream.lastSeq = envelope.Sequence
		stream.lastActivity = time.Now()
		return false, nil
	}
	if err := stream.matches(envelope); err != nil {
		return false, err
	}
	select {
	case stream.updates <- envelope:
		stream.lastSeq = envelope.Sequence
		stream.lastActivity = time.Now()
	default:
		return false, errBrowserStreamSlowConsumer
	}
	if envelope.Type == protocol.TypeContainerStreamError || envelope.Type == protocol.TypeContainerStreamEnd {
		stream.terminal = true
		return true, nil
	}
	return false, nil
}

func (stream *coreBrowserStream) isClosed() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.closed
}

func (stream *coreBrowserStream) isIdle(now time.Time) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.lastActivity.IsZero() || now.Sub(stream.lastActivity) > containerStreamIdleLimit
}

func (stream *coreBrowserStream) matches(envelope protocol.Envelope) error {
	switch envelope.Type {
	case protocol.TypeContainerStreamReady:
		var ready protocol.ContainerStreamReady
		if err := decodeAgentPayload(envelope.Payload, &ready); err != nil || ready.Kind != stream.kind || ready.ContainerID != stream.containerID {
			return errors.New("Agent stream ready identity mismatch")
		}
	case protocol.TypeContainerLog:
		if stream.kind != protocol.StreamLogs {
			return errors.New("Agent sent logs on a stats stream")
		}
		var frame protocol.ContainerStreamLog
		if err := decodeAgentPayload(envelope.Payload, &frame); err != nil || frame.ContainerID != stream.containerID {
			return errors.New("Agent log stream identity mismatch")
		}
	case protocol.TypeContainerStats:
		if stream.kind != protocol.StreamStats {
			return errors.New("Agent sent stats on a log stream")
		}
		var frame protocol.ContainerStreamStats
		if err := decodeAgentPayload(envelope.Payload, &frame); err != nil || frame.Snapshot.ContainerID != stream.containerID {
			return errors.New("Agent stats stream identity mismatch")
		}
	case protocol.TypeContainerStreamError, protocol.TypeContainerStreamEnd:
	default:
		return errors.New("Agent sent an invalid container stream message")
	}
	return nil
}

var errBrowserStreamSlowConsumer = errors.New("browser container stream consumer is too slow")

func (connection *agentConnection) addBrowserStream(stream *coreBrowserStream) error {
	if connection == nil || stream == nil || connection.streams == nil || !connection.capabilityEnabled(protocol.CapabilityContainerStreams) || connection.ctx == nil || connection.ctx.Err() != nil {
		return errors.New("Agent container stream is unavailable")
	}
	connection.streamMu.Lock()
	defer connection.streamMu.Unlock()
	if connection.ctx.Err() != nil {
		return errors.New("Agent container stream is unavailable")
	}
	if len(connection.streams) >= maxAgentBrowserStreams {
		return errors.New("Agent container stream limit reached")
	}
	if _, exists := connection.streams[stream.requestID]; exists {
		return errors.New("duplicate container stream request ID")
	}
	connection.streams[stream.requestID] = stream
	return nil
}

func (connection *agentConnection) removeBrowserStream(stream *coreBrowserStream) bool {
	if connection == nil || stream == nil {
		return false
	}
	connection.streamMu.Lock()
	defer connection.streamMu.Unlock()
	if connection.streams[stream.requestID] != stream {
		return false
	}
	delete(connection.streams, stream.requestID)
	connection.rememberStreamTombstoneLocked(stream.requestID)
	return true
}

func (connection *agentConnection) closeBrowserStreams() {
	if connection == nil {
		return
	}
	connection.streamMu.Lock()
	streams := make([]*coreBrowserStream, 0, len(connection.streams))
	for requestID, stream := range connection.streams {
		streams = append(streams, stream)
		connection.rememberStreamTombstoneLocked(requestID)
		delete(connection.streams, requestID)
	}
	connection.streamMu.Unlock()
	for _, stream := range streams {
		stream.finish()
	}
}

func (connection *agentConnection) takeAgentStream(envelope protocol.Envelope) (*coreBrowserStream, bool, error) {
	if connection == nil || !connection.capabilityEnabled(protocol.CapabilityContainerStreams) {
		return nil, false, errors.New("Agent container streams were not negotiated")
	}
	if err := protocol.ValidateContainerStreamEnvelope(envelope, connection.generation); err != nil {
		return nil, false, err
	}
	connection.streamMu.Lock()
	stream := connection.streams[envelope.RequestID]
	if stream == nil {
		_, tombstone := connection.streamTombstones[envelope.RequestID]
		connection.streamMu.Unlock()
		if tombstone {
			return nil, true, nil
		}
		return nil, false, errors.New("Agent sent data for an unknown container stream")
	}
	connection.streamMu.Unlock()
	terminal, err := stream.enqueue(envelope)
	if errors.Is(err, errBrowserStreamSlowConsumer) {
		stream.finish()
		connection.removeBrowserStream(stream)
		return stream, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return stream, terminal, nil
}

func (connection *agentConnection) rememberStreamTombstoneLocked(requestID string) {
	if connection.streamTombstones == nil {
		connection.streamTombstones = make(map[string]struct{})
	}
	if _, exists := connection.streamTombstones[requestID]; exists {
		return
	}
	connection.streamTombstones[requestID] = struct{}{}
	connection.streamTombstoneOrder = append(connection.streamTombstoneOrder, requestID)
	if len(connection.streamTombstoneOrder) > 64 {
		oldest := connection.streamTombstoneOrder[0]
		connection.streamTombstoneOrder = connection.streamTombstoneOrder[1:]
		delete(connection.streamTombstones, oldest)
	}
}

func (s *Server) handleAgentContainerStreamMessage(connection *agentConnection, envelope protocol.Envelope) error {
	stream, _, err := connection.takeAgentStream(envelope)
	if err != nil {
		return err
	}
	if stream != nil && stream.isClosed() {
		_ = s.enqueueContainerStreamClose(connection, stream.requestID)
	}
	return nil
}

func (s *Server) handleContainerStreamWebSocket(w http.ResponseWriter, r *http.Request) {
	current, ok := s.authenticateRequest(w, r, false)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validOrigin(r) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	request, err := parseBrowserContainerStreamRequest(r)
	if err != nil {
		http.Error(w, "invalid stream request", http.StatusBadRequest)
		return
	}
	select {
	case s.containerStreamSlots <- struct{}{}:
		defer func() { <-s.containerStreamSlots }()
	default:
		http.Error(w, "container stream limit reached", http.StatusTooManyRequests)
		return
	}
	_, _, connection, err := s.authorizeContainerStream(r.Context(), request)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errContainerStreamNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, errContainerStreamUnavailable) {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, "container stream unavailable", status)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "container stream closed")
	conn.SetReadLimit(1024)
	streamID, err := newContainerStreamRequestID()
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "stream request could not be created")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stream := &coreBrowserStream{requestID: streamID, nodeID: request.nodeID, containerID: request.containerID,
		kind: request.kind, updates: make(chan protocol.Envelope, browserStreamQueueMessages), done: make(chan struct{}), lastActivity: time.Now()}
	if err := connection.addBrowserStream(stream); err != nil {
		_ = conn.Close(websocket.StatusTryAgainLater, "Agent stream limit reached")
		return
	}
	defer func() {
		if connection.removeBrowserStream(stream) {
			_ = s.enqueueContainerStreamClose(connection, stream.requestID)
		}
		stream.finish()
	}()
	open := protocol.ContainerStreamOpen{Kind: request.kind, ContainerID: request.containerID}
	if request.kind == protocol.StreamLogs {
		open.Tail, open.Follow, open.Timestamps, open.ShowStdout, open.ShowStderr = request.tail, true, true, true, true
	}
	if err := protocol.ValidateContainerStreamOpen(open); err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "invalid stream request")
		return
	}
	command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamOpen,
		Generation: connection.generation, RequestID: stream.requestID, Payload: marshalAgentPayload(open)}
	if !s.enqueueContainerStream(connection, command) {
		connection.removeBrowserStream(stream)
		stream.finish()
		_ = conn.Close(websocket.StatusTryAgainLater, "Agent command queue is full")
		return
	}
	readDone := make(chan error, 1)
	go func() {
		_, _, readErr := conn.Read(ctx)
		if readErr == nil {
			readErr = errors.New("browser stream does not accept client data")
		}
		select {
		case readDone <- readErr:
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(s.websocketCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-connection.ctx.Done():
			_ = conn.Close(websocket.StatusTryAgainLater, "Agent connection ended")
			return
		case <-stream.done:
			_ = conn.Close(websocket.StatusTryAgainLater, "container stream ended")
			return
		case err := <-readDone:
			if err != nil {
				return
			}
		case envelope := <-stream.updates:
			if !s.dashboardSessionStillValid(ctx, current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
			writeCtx, writeCancel := context.WithTimeout(ctx, containerStreamWriteLimit)
			writeErr := conn.Write(writeCtx, websocket.MessageText, marshalAgentPayload(envelope))
			writeCancel()
			if writeErr != nil {
				return
			}
			stream.mu.Lock()
			terminal := stream.terminal && len(stream.updates) == 0
			stream.mu.Unlock()
			if terminal {
				return
			}
		case <-ticker.C:
			if !s.dashboardSessionStillValid(ctx, current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
			if stream.isIdle(time.Now()) {
				_ = conn.Close(websocket.StatusTryAgainLater, "Agent stream heartbeat timed out")
				return
			}
		}
	}
}

type browserContainerStreamRequest struct {
	nodeID      string
	containerID string
	kind        string
	tail        string
}

var (
	errContainerStreamNotFound    = errors.New("container stream target does not exist")
	errContainerStreamUnavailable = errors.New("container stream target is unavailable")
)

func parseBrowserContainerStreamRequest(r *http.Request) (browserContainerStreamRequest, error) {
	request := browserContainerStreamRequest{kind: strings.TrimPrefix(r.URL.Path, "/ws/v1/streams/")}
	if request.kind != protocol.StreamLogs && request.kind != protocol.StreamStats {
		return request, errors.New("unsupported stream kind")
	}
	query := r.URL.Query()
	for key, values := range query {
		if key != "nodeId" && key != "containerId" && key != "tail" || len(values) != 1 {
			return request, errors.New("unsupported or repeated stream parameter")
		}
	}
	request.nodeID = query.Get("nodeId")
	request.containerID = query.Get("containerId")
	request.tail = query.Get("tail")
	if !validUUID(request.nodeID) || !protocol.IsFullContainerID(request.containerID) {
		return request, errors.New("stream requires a valid node and full container ID")
	}
	if request.kind == protocol.StreamStats && request.tail != "" {
		return request, errors.New("stats stream does not accept log options")
	}
	if request.kind == protocol.StreamLogs {
		if request.tail == "" {
			request.tail = "200"
		}
		if err := protocol.ValidateContainerStreamOpen(protocol.ContainerStreamOpen{Kind: request.kind, ContainerID: request.containerID, Tail: request.tail, Follow: true}); err != nil {
			return request, err
		}
	}
	return request, nil
}

func (s *Server) authorizeContainerStream(ctx context.Context, request browserContainerStreamRequest) (dashboardNodeState, coredocker.View, *agentConnection, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, view, err := s.dockerViewForNode(queryCtx, request.nodeID)
	if err != nil {
		return state, view, nil, errContainerStreamUnavailable
	}
	if !state.Exists {
		return state, view, nil, errContainerStreamNotFound
	}
	if !containerStreamDockerViewAvailable(view) {
		return state, view, nil, errContainerStreamUnavailable
	}
	var selected *coredocker.ContainerRecord
	for index := range view.Containers {
		if view.Containers[index].Container.ID == request.containerID {
			selected = &view.Containers[index]
			break
		}
	}
	if selected == nil {
		return state, view, nil, errContainerStreamNotFound
	}
	if selected.Container.Stale {
		return state, view, nil, errContainerStreamUnavailable
	}
	if request.kind == protocol.StreamStats && (!selected.Container.Running || selected.Container.Paused || selected.Container.Restarting) {
		return state, view, nil, errContainerStreamUnavailable
	}
	connection := s.activeAgentConnectionForNode(request.nodeID)
	if connection == nil || connection.generation != view.ActiveGeneration || !connection.capabilityEnabled(protocol.CapabilityContainerStreams) || connection.ctx == nil || connection.ctx.Err() != nil {
		return state, view, nil, errContainerStreamUnavailable
	}
	return state, view, connection, nil
}

func containerStreamDockerViewAvailable(view coredocker.View) bool {
	return view.AgentOnline && !view.DataStale && view.DockerSnapshotFresh &&
		view.DockerAvailability == protocol.DockerAvailabilityAvailable
}

func (s *Server) activeAgentConnectionForNode(nodeID string) *agentConnection {
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID {
			return connection
		}
	}
	return nil
}

func (s *Server) enqueueContainerStream(connection *agentConnection, envelope protocol.Envelope) bool {
	if connection == nil || connection.ctx == nil || connection.ctx.Err() != nil {
		return false
	}
	select {
	case connection.commands <- envelope:
		return true
	case <-connection.ctx.Done():
		return false
	default:
		return false
	}
}

func (s *Server) enqueueContainerStreamClose(connection *agentConnection, requestID string) bool {
	if connection == nil || requestID == "" {
		return false
	}
	command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamClose,
		Generation: connection.generation, RequestID: requestID, Payload: marshalAgentPayload(protocol.ContainerStreamClose{})}
	return s.enqueueContainerStream(connection, command)
}

func newContainerStreamRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
