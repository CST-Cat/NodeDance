package server

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	terminalTicketLifetime = time.Minute
	terminalBrowserQueue   = 16
	terminalBrowserReadMax = protocol.MaxTerminalFrameJSON
)

type terminalStreamManager struct {
	mu      sync.Mutex
	tickets map[string]*terminalStream
	streams map[string]*terminalStream
	byNode  map[string]int
}

type terminalStream struct {
	streamID string
	ticket   string
	created  time.Time
	expires  time.Time
	consumed bool
	closed   bool
	started  bool

	browserSessionID string
	remoteAddr       string
	nodeID           string
	agentID          string
	generation       uint64
	targetKind       string
	containerID      string
	agent            *agentConnection
	conn             *websocket.Conn
	out              chan protocol.TerminalFrame
	cancel           context.CancelFunc
}

type createTerminalRequest struct {
	TargetKind  string `json:"targetKind"`
	ContainerID string `json:"containerId,omitempty"`
}

type createTerminalResponse struct {
	StreamID  string    `json:"streamId"`
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func newTerminalStreamManager() *terminalStreamManager {
	return &terminalStreamManager{tickets: make(map[string]*terminalStream), streams: make(map[string]*terminalStream), byNode: make(map[string]int)}
}

func terminalRoute(path string) (string, bool) {
	if !strings.HasPrefix(path, "/api/v1/nodes/") || !strings.HasSuffix(path, "/terminals") {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/nodes/"), "/")
	if len(parts) != 2 || parts[1] != "terminals" || !validUUID(parts[0]) {
		return "", false
	}
	return parts[0], true
}

func (s *Server) createTerminal(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request createTerminalRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.TargetKind != protocol.TerminalTargetHost && request.TargetKind != protocol.TerminalTargetContainer {
		http.Error(w, "invalid terminal target", http.StatusBadRequest)
		return
	}
	if request.TargetKind == protocol.TerminalTargetHost && request.ContainerID != "" ||
		request.TargetKind == protocol.TerminalTargetContainer && !validTerminalContainerID(request.ContainerID) {
		http.Error(w, "invalid terminal target", http.StatusBadRequest)
		return
	}
	nodes, err := s.agents.ListNodes(r.Context())
	if err != nil {
		http.Error(w, "node inventory unavailable", http.StatusInternalServerError)
		return
	}
	var node *agents.Node
	for index := range nodes {
		if nodes[index].NodeID == nodeID {
			node = &nodes[index]
			break
		}
	}
	if node == nil || node.Status != "online" || !node.HasLastSeen || s.now().Before(node.LastSeen) || !s.now().Before(node.LastSeen.Add(s.agentOfflineTimeout)) {
		http.Error(w, "node is offline", http.StatusConflict)
		return
	}
	s.agentConnectionsMu.Lock()
	connection := s.agentConnections[node.AgentID]
	if connection == nil || connection.generation != node.ConnectionGeneration || !connection.terminalEnabled {
		connection = nil
	}
	s.agentConnectionsMu.Unlock()
	if connection == nil {
		http.Error(w, "Agent terminal capability is unavailable", http.StatusConflict)
		return
	}
	if request.TargetKind == protocol.TerminalTargetContainer {
		state, inventory, err := s.dockerViewForNode(r.Context(), nodeID)
		if err != nil || !state.Exists || state.Status != "online" || inventory.DataStale || inventory.DockerAvailability != "available" {
			http.Error(w, "fresh Docker inventory is unavailable", http.StatusConflict)
			return
		}
		found := false
		for _, record := range inventory.Containers {
			if record.Container.ID == request.ContainerID && record.Container.Running && !record.Container.Stale {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "running container was not found", http.StatusConflict)
			return
		}
	}
	stream, ticket, err := s.terminals.create(current, nodeID, node.AgentID, node.ConnectionGeneration, request.TargetKind, request.ContainerID, connection, s.now())
	if err != nil {
		if errors.Is(err, errTerminalLimit) {
			http.Error(w, "node already has two active terminals", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "could not create terminal authorization", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, createTerminalResponse{StreamID: stream.streamID, Ticket: ticket, ExpiresAt: stream.expires.UTC()})
}

var errTerminalLimit = errors.New("terminal concurrency limit reached")

func validTerminalContainerID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func (m *terminalStreamManager) create(current *session, nodeID, agentID string, generation uint64, targetKind, containerID string, connection *agentConnection, now time.Time) (*terminalStream, string, error) {
	streamID, _, err := auth.NewToken()
	if err != nil {
		return nil, "", err
	}
	ticket, digest, err := auth.NewToken()
	if err != nil {
		return nil, "", err
	}
	if connection == nil || current == nil || nodeID == "" || agentID == "" || generation == 0 {
		return nil, "", errors.New("terminal authorization binding is incomplete")
	}
	key := hex.EncodeToString(digest)
	stream := &terminalStream{streamID: streamID, ticket: key, created: now, expires: now.Add(terminalTicketLifetime),
		browserSessionID: current.ID, remoteAddr: current.RemoteAddr, nodeID: nodeID, agentID: agentID,
		generation: generation, targetKind: targetKind, containerID: containerID, agent: connection,
		out: make(chan protocol.TerminalFrame, terminalBrowserQueue)}
	m.mu.Lock()
	if m.byNode[nodeID] >= 2 {
		m.mu.Unlock()
		return nil, "", errTerminalLimit
	}
	m.tickets[key] = stream
	m.byNode[nodeID]++
	m.mu.Unlock()
	time.AfterFunc(terminalTicketLifetime, func() { m.expireTicket(key, stream) })
	return stream, ticket, nil
}

func (m *terminalStreamManager) expireTicket(key string, stream *terminalStream) {
	m.mu.Lock()
	if m.tickets[key] != stream || stream.consumed || stream.closed {
		m.mu.Unlock()
		return
	}
	stream.closed = true
	delete(m.tickets, key)
	if m.byNode[stream.nodeID] > 0 {
		m.byNode[stream.nodeID]--
		if m.byNode[stream.nodeID] == 0 {
			delete(m.byNode, stream.nodeID)
		}
	}
	m.mu.Unlock()
}

func (m *terminalStreamManager) consume(ticket, sessionID string, now time.Time, conn *websocket.Conn, cancel context.CancelFunc) (*terminalStream, bool) {
	digest := auth.DigestToken(ticket)
	key := hex.EncodeToString(digest)
	m.mu.Lock()
	defer m.mu.Unlock()
	stream := m.tickets[key]
	if stream == nil || stream.consumed || stream.closed || stream.browserSessionID != sessionID || !now.Before(stream.expires) {
		return nil, false
	}
	delete(m.tickets, key)
	stream.consumed = true
	stream.conn = conn
	stream.cancel = cancel
	m.streams[stream.streamID] = stream
	return stream, true
}

func (m *terminalStreamManager) find(streamID string) *terminalStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[streamID]
}

func (m *terminalStreamManager) handleAgentFrame(s *Server, connection *agentConnection, identity agents.Identity, frame protocol.TerminalFrame) {
	stream := m.find(frame.StreamID)
	if stream == nil || stream.agent != connection || stream.agentID != identity.AgentID || stream.nodeID != identity.NodeID || stream.generation != connection.generation {
		return
	}
	if frame.Action == protocol.TerminalActionReady {
		if !s.recordTerminalAudit(stream, "terminal_start", "succeeded") {
			m.close(s, stream, "terminal audit could not be saved", true)
			return
		}
		m.mu.Lock()
		if stream.closed {
			m.mu.Unlock()
			return
		}
		stream.started = true
		m.mu.Unlock()
	}
	select {
	case stream.out <- frame:
	default:
		m.close(s, stream, "terminal output queue overflow", true)
		return
	}
	if frame.Action == protocol.TerminalActionError {
		m.mu.Lock()
		started := stream.started
		m.mu.Unlock()
		if !started {
			s.recordTerminalAudit(stream, "terminal_start", "failed")
		}
		m.close(s, stream, "Agent rejected terminal target", false)
	}
	if frame.Action == protocol.TerminalActionClosed {
		m.close(s, stream, "terminal closed", false)
	}
}

func (s *Server) recordTerminalAudit(stream *terminalStream, action, outcome string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	target := audit.Target{Kind: audit.TargetTerminalHost, ID: stream.nodeID}
	if stream.targetKind == protocol.TerminalTargetContainer {
		target = audit.Target{Kind: audit.TargetTerminalContainer, ID: stream.containerID}
	}
	err := audit.Record(ctx, s.store.DB, audit.Event{OccurredAt: s.now(), Action: action, Outcome: outcome,
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: stream.remoteAddr,
		Target: target})
	return err == nil
}

func (m *terminalStreamManager) close(s *Server, stream *terminalStream, reason string, notifyAgent bool) {
	if stream == nil {
		return
	}
	m.mu.Lock()
	if stream.closed {
		m.mu.Unlock()
		return
	}
	stream.closed = true
	delete(m.tickets, stream.ticket)
	delete(m.streams, stream.streamID)
	if m.byNode[stream.nodeID] > 0 {
		m.byNode[stream.nodeID]--
		if m.byNode[stream.nodeID] == 0 {
			delete(m.byNode, stream.nodeID)
		}
	}
	conn, cancel, agent, started := stream.conn, stream.cancel, stream.agent, stream.started
	m.mu.Unlock()

	if notifyAgent && agent != nil {
		payload, _ := json.Marshal(protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionClose})
		command := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: stream.generation, Payload: payload}
		if !agent.tryEnqueue(command) && s != nil {
			s.closeAgentConnection(stream.agentID, stream.generation)
		}
	}
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, boundedCloseReason(reason))
	}
	if s != nil && started {
		outcome := "succeeded"
		if reason != "terminal closed" && reason != "browser closed terminal" {
			outcome = "failed"
		}
		s.recordTerminalAudit(stream, "terminal_end", outcome)
	}
}

func boundedCloseReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 100 {
		return value[:100]
	}
	return value
}

func (m *terminalStreamManager) closeBrowserSession(s *Server, sessionID, reason string) {
	m.closeMatching(s, func(stream *terminalStream) bool { return stream.browserSessionID == sessionID }, reason)
}

func (m *terminalStreamManager) closeAgent(s *Server, agentID string, generation uint64, reason string) {
	m.closeMatching(s, func(stream *terminalStream) bool { return stream.agentID == agentID && stream.generation == generation }, reason)
}

func (m *terminalStreamManager) closeAll(s *Server, reason string) {
	m.closeMatching(s, func(*terminalStream) bool { return true }, reason)
}

func (m *terminalStreamManager) closeMatching(s *Server, predicate func(*terminalStream) bool, reason string) {
	m.mu.Lock()
	type closingStream struct {
		stream *terminalStream
		notify bool
	}
	active := make([]closingStream, 0, len(m.streams)+len(m.tickets))
	seen := make(map[*terminalStream]struct{})
	for _, stream := range m.tickets {
		if predicate(stream) {
			if _, exists := seen[stream]; !exists {
				seen[stream] = struct{}{}
				active = append(active, closingStream{stream: stream, notify: stream.consumed})
			}
		}
	}
	for _, stream := range m.streams {
		if predicate(stream) {
			if _, exists := seen[stream]; !exists {
				seen[stream] = struct{}{}
				active = append(active, closingStream{stream: stream, notify: stream.consumed})
			}
		}
	}
	m.mu.Unlock()
	for _, item := range active {
		m.close(s, item.stream, reason, item.notify)
	}
}

func (s *Server) handleTerminalWebSocket(w http.ResponseWriter, r *http.Request, current *session) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.validOrigin(r) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	ticket := r.URL.Query().Get("ticket")
	if len(ticket) < 32 || len(ticket) > 64 || strings.TrimSpace(ticket) != ticket || r.URL.Query().Has("csrf") {
		http.Error(w, "terminal authorization rejected", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(terminalBrowserReadMax)
	ctx, cancel := context.WithCancel(r.Context())
	stream, ok := s.terminals.consume(ticket, current.ID, s.now(), conn, cancel)
	if !ok {
		cancel()
		_ = conn.Close(websocket.StatusPolicyViolation, "terminal authorization rejected")
		return
	}
	defer s.terminals.close(s, stream, "browser closed terminal", true)
	if !s.terminalAgentCurrent(stream) {
		_ = conn.Close(websocket.StatusPolicyViolation, "Agent connection changed")
		return
	}
	openPayload, _ := json.Marshal(protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionOpen,
		TargetKind: stream.targetKind, ContainerID: stream.containerID, Rows: 24, Columns: 80})
	if !stream.agent.tryEnqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: stream.generation, Payload: openPayload}) {
		_ = conn.Close(websocket.StatusTryAgainLater, "Agent command queue is busy")
		return
	}
	readDone := make(chan error, 1)
	go func() {
		for {
			messageType, raw, err := conn.Read(ctx)
			if err != nil {
				readDone <- err
				return
			}
			if messageType != websocket.MessageText || len(raw) == 0 || len(raw) > terminalBrowserReadMax {
				readDone <- errors.New("invalid terminal browser frame")
				return
			}
			var frame protocol.TerminalFrame
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&frame) != nil || protocol.ValidateTerminalFrame(frame, false) != nil || frame.StreamID != stream.streamID {
				readDone <- errors.New("invalid terminal browser frame")
				return
			}
			payload, err := json.Marshal(frame)
			if err != nil || !stream.agent.tryEnqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: stream.generation, Payload: payload}) {
				readDone <- errors.New("Agent terminal queue is busy")
				return
			}
			if frame.Action == protocol.TerminalActionClose {
				readDone <- nil
				return
			}
		}
	}()
	ticker := time.NewTicker(min(s.websocketCheckInterval, 5*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-readDone:
			return
		case frame := <-stream.out:
			writeCtx, cancelWrite := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, marshalAgentPayload(map[string]any{"type": "terminal", "frame": frame}))
			cancelWrite()
			if err != nil {
				return
			}
		case <-ticker.C:
			if !s.dashboardSessionStillValid(ctx, stream.browserSessionID) {
				return
			}
			if !s.terminalAgentCurrent(stream) {
				return
			}
		}
	}
}

func (s *Server) terminalAgentCurrent(stream *terminalStream) bool {
	if stream == nil || stream.agent == nil {
		return false
	}
	s.agentConnectionsMu.Lock()
	current := s.agentConnections[stream.agentID] == stream.agent && stream.agent.generation == stream.generation
	s.agentConnectionsMu.Unlock()
	return current
}
