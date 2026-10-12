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
	terminalBrowserQueue   = 32
	terminalBrowserReadMax = protocol.MaxTerminalFrameJSON
)

type terminalStreamManager struct {
	mu      sync.Mutex
	tickets map[string]*terminalStream
	pending map[string]*terminalStream
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
	attachment       uint64
	opened           bool
	cancel           context.CancelFunc
}

type terminalMessage struct {
	Type  string                 `json:"type"`
	Frame protocol.TerminalFrame `json:"frame"`
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
	return &terminalStreamManager{tickets: make(map[string]*terminalStream), pending: make(map[string]*terminalStream),
		streams: make(map[string]*terminalStream), byNode: make(map[string]int)}
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

func terminalCancellationRoute(path string) (nodeID, streamID string, ok bool) {
	if !strings.HasPrefix(path, "/api/v1/nodes/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/nodes/"), "/")
	if len(parts) != 3 || !validUUID(parts[0]) || parts[1] != "terminals" || !validTerminalStreamID(parts[2]) {
		return "", "", false
	}
	return parts[0], parts[2], true
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
	if connection == nil || connection.generation != node.ConnectionGeneration || !connection.capabilityEnabled(protocol.CapabilityTerminal) {
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
	// Serialize the final Session check and ticket creation with logout,
	// revocation and password changes. A request authenticated just before a
	// logout must not leave a fresh, usable terminal ticket behind it.
	s.terminalSessionMu.Lock()
	defer s.terminalSessionMu.Unlock()
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		s.clearCookies(w)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
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
var errInvalidTerminalFrame = errors.New("invalid terminal browser frame")

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

func validTerminalStreamID(value string) bool {
	return protocol.ValidateTerminalFrame(protocol.TerminalFrame{StreamID: value, Action: protocol.TerminalActionClose}, false) == nil
}

func (s *Server) cancelTerminalTicket(w http.ResponseWriter, r *http.Request, current *session, nodeID, streamID string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.terminalSessionMu.Lock()
	defer s.terminalSessionMu.Unlock()
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		s.clearCookies(w)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	s.terminals.mu.Lock()
	stream := s.terminals.streams[streamID]
	if stream == nil {
		stream = s.terminals.pending[streamID]
	}
	owned := stream != nil && stream.browserSessionID == current.ID && stream.nodeID == nodeID
	s.terminals.mu.Unlock()
	if owned {
		s.terminals.close(s, stream, "browser closed terminal", true)
	}

	w.WriteHeader(http.StatusNoContent)
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
	m.pending[streamID] = stream
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
	delete(m.tickets, key)
	if stream.opened {
		delete(m.pending, stream.streamID)
		m.mu.Unlock()
		return
	}
	stream.closed = true
	delete(m.pending, stream.streamID)
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
	if stream == nil || stream.consumed || stream.closed || stream.conn != nil || stream.browserSessionID != sessionID || !now.Before(stream.expires) {
		return nil, false
	}
	delete(m.tickets, key)
	delete(m.pending, stream.streamID)
	stream.consumed = true
	stream.conn = conn
	stream.cancel = cancel
	stream.attachment++
	m.streams[stream.streamID] = stream
	return stream, true
}

func (m *terminalStreamManager) find(streamID string) *terminalStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[streamID]
}

// hasLiveConnection reports whether this Agent still owns an unexpired
// terminal authorization or an active browser terminal session. Update
// scheduling uses it as a point-in-time check before dispatching an update.
func (m *terminalStreamManager) hasLiveConnection(connection *agentConnection, now time.Time) bool {
	if m == nil || connection == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, stream := range m.tickets {
		if stream.agent == connection && !stream.closed && !stream.consumed && now.Before(stream.expires) {
			return true
		}
	}
	for _, stream := range m.streams {
		if stream.agent == connection && !stream.closed {
			return true
		}
	}
	return false
}

func (m *terminalStreamManager) handleAgentFrame(s *Server, connection *agentConnection, identity agents.Identity, frame protocol.TerminalFrame) {
	stream := m.find(frame.StreamID)
	if stream == nil || stream.agent != connection || stream.agentID != identity.AgentID || stream.nodeID != identity.NodeID || stream.generation != connection.generation {
		return
	}
	if frame.Action == protocol.TerminalActionReady {
		m.mu.Lock()
		alreadyStarted := stream.started
		m.mu.Unlock()
		if !alreadyStarted && !s.recordTerminalAudit(stream, "terminal_start", "succeeded") {
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

	if frame.Action == protocol.TerminalActionError {
		m.mu.Lock()
		started := stream.started
		m.mu.Unlock()
		if !started {
			s.recordTerminalAudit(stream, "terminal_start", "failed")
		}
	}
	m.mu.Lock()
	if stream.closed {
		m.mu.Unlock()
		return
	}
	// Detached output belongs to tmux, which repaints when a new client attaches.
	if stream.conn == nil {
		m.mu.Unlock()
		if frame.Action == protocol.TerminalActionClosed || frame.Action == protocol.TerminalActionError || frame.Action == protocol.TerminalActionExit {
			m.close(s, stream, "terminal closed", true)
		}
		return
	}
	select {
	case stream.out <- frame:
		m.mu.Unlock()
	default:
		cancel := stream.cancel
		m.mu.Unlock()
		// A slow transport reattaches to tmux rather than losing its shell.
		if cancel != nil {
			cancel()
		}
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
	delete(m.pending, stream.streamID)
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
				active = append(active, closingStream{stream: stream, notify: stream.opened})
			}
		}
	}
	for _, stream := range m.streams {
		if predicate(stream) {
			if _, exists := seen[stream]; !exists {
				seen[stream] = struct{}{}
				active = append(active, closingStream{stream: stream, notify: stream.opened})
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

	attachment := stream.attachment
	explicitClose := false
	closeReason := "browser closed terminal"
	defer func() {
		cancel()
		conn.CloseNow()
		if explicitClose {
			s.terminals.close(s, stream, closeReason, true)
		} else {
			s.terminals.detach(s, stream, attachment)
		}
	}()
	if !s.terminalAgentCurrent(stream) {
		explicitClose = true
		return
	}
	s.terminals.mu.Lock()
	opened := stream.opened
	s.terminals.mu.Unlock()
	action := protocol.TerminalActionAttach
	frame := protocol.TerminalFrame{StreamID: stream.streamID, Action: action}
	if !opened {
		frame.Action = protocol.TerminalActionOpen
		frame.TargetKind = stream.targetKind
		frame.ContainerID = stream.containerID
		frame.Rows = 24
		frame.Columns = 80
	}
	payload, _ := json.Marshal(frame)
	if !stream.agent.tryEnqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: stream.generation, Payload: payload}) {
		explicitClose = true
		return
	}
	s.terminals.mu.Lock()
	stream.opened = true
	s.terminals.mu.Unlock()
	readDone := make(chan error, 1)
	go func() {
		for {
			messageType, raw, err := conn.Read(ctx)
			if err != nil {
				readDone <- err
				return
			}
			if messageType != websocket.MessageText || len(raw) == 0 || len(raw) > terminalBrowserReadMax {
				readDone <- errInvalidTerminalFrame
				return
			}
			var frame protocol.TerminalFrame
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&frame) != nil || protocol.ValidateTerminalFrame(frame, false) != nil || frame.StreamID != stream.streamID || (frame.Action != protocol.TerminalActionInput && frame.Action != protocol.TerminalActionResize && frame.Action != protocol.TerminalActionClose) {
				readDone <- errInvalidTerminalFrame
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
		case err := <-readDone:
			explicitClose = err == nil || errors.Is(err, errInvalidTerminalFrame)
			if errors.Is(err, errInvalidTerminalFrame) {
				closeReason = "invalid terminal browser frame"
			}
			return
		case frame := <-stream.out:
			writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(writeCtx, websocket.MessageText, marshalAgentPayload(terminalMessage{Type: "terminal", Frame: frame}))
			stop()
			if err != nil {
				return
			}
			if frame.Action == protocol.TerminalActionClosed || frame.Action == protocol.TerminalActionError {
				closeReason = "terminal closed"
				if frame.Action == protocol.TerminalActionError {
					closeReason = "Agent rejected terminal target"
				}
				explicitClose = true
				return
			}
		case <-ticker.C:
			if !s.dashboardSessionStillValid(ctx, stream.browserSessionID) || !s.terminalAgentCurrent(stream) {
				closeReason = "terminal authorization expired"
				explicitClose = true
				return
			}
			pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			stop()
			if err != nil {
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

// detach disconnects only the tmux client. tmux owns the shell and screen;
// Agent idle expiry and authentication revocation remain authoritative.
func (m *terminalStreamManager) detach(s *Server, stream *terminalStream, attachment uint64) {
	m.mu.Lock()
	if stream.closed || stream.attachment != attachment {
		m.mu.Unlock()
		return
	}
	payload, _ := json.Marshal(protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionDetach})
	queued := stream.agent.tryEnqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTerminalFrame, Generation: stream.generation, Payload: payload})
	stream.conn = nil
	stream.cancel = nil
	for {
		select {
		case <-stream.out:
		default:
			m.mu.Unlock()
			if !queued {
				m.close(s, stream, "Agent terminal queue is busy", true)
			} else {
				m.checkDetachedAuthorization(s, stream, attachment)
			}
			return
		}
	}
}

func (m *terminalStreamManager) checkDetachedAuthorization(s *Server, stream *terminalStream, attachment uint64) {
	time.AfterFunc(min(s.websocketCheckInterval, 5*time.Second), func() {
		m.mu.Lock()
		detached := !stream.closed && stream.conn == nil && stream.attachment == attachment
		m.mu.Unlock()
		if !detached {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !s.dashboardSessionStillValid(ctx, stream.browserSessionID) || !s.terminalAgentCurrent(stream) {
			m.close(s, stream, "terminal authorization expired", true)
			return
		}
		m.checkDetachedAuthorization(s, stream, attachment)
	})
}

func (s *Server) resumeTerminal(w http.ResponseWriter, r *http.Request, current *session, nodeID, streamID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct{}
	if r.ContentLength != 0 && !decodeJSON(w, r, &request) {
		return
	}
	s.terminalSessionMu.Lock()
	defer s.terminalSessionMu.Unlock()
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	stream := s.terminals.find(streamID)
	if stream == nil || stream.browserSessionID != current.ID || stream.nodeID != nodeID || !s.terminalAgentCurrent(stream) {
		http.Error(w, "terminal session is no longer available", http.StatusGone)
		return
	}
	ticket, digest, err := auth.NewToken()
	if err != nil {
		http.Error(w, "could not create terminal authorization", http.StatusInternalServerError)
		return
	}
	key := hex.EncodeToString(digest)
	s.terminals.mu.Lock()
	if stream.closed || !stream.opened || stream.conn != nil {
		s.terminals.mu.Unlock()
		http.Error(w, "terminal connection is still attached", http.StatusConflict)
		return
	}
	delete(s.terminals.tickets, stream.ticket)
	stream.ticket, stream.expires, stream.consumed = key, s.now().Add(terminalTicketLifetime), false
	s.terminals.tickets[key] = stream
	s.terminals.pending[streamID] = stream
	expires := stream.expires
	s.terminals.mu.Unlock()
	time.AfterFunc(terminalTicketLifetime, func() { s.terminals.expireTicket(key, stream) })
	// A fresh ticket grants attachment only; it cannot create a new shell.
	writeJSON(w, http.StatusCreated, createTerminalResponse{StreamID: streamID, Ticket: ticket, ExpiresAt: expires.UTC()})
}
