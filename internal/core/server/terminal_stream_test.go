package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const terminalTestNode = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func terminalFixture(t *testing.T) (*Server, *session, *agentConnection, *terminalStream, string) {
	t.Helper()
	s := featureCore(t)
	current := &session{ID: "browser", RemoteAddr: "127.0.0.1"}
	now := s.now().UnixNano()
	if _, err := s.store.DB.Exec(`INSERT INTO users(id,username,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,'test',x'00',x'00','test','test',?)`, now); err != nil {
		t.Fatal(err)
	}
	_, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id,token_digest,csrf_digest,created_at,last_seen_at,user_agent,remote_addr) VALUES(?,?,?,?,?,?,?)`, current.ID, []byte("token"), []byte("csrf"), now, now, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connection := &agentConnection{agentID: "agent", nodeID: terminalTestNode, generation: 1, commands: make(chan protocol.Envelope, 32)}
	s.agentConnections["agent"] = connection
	stream, ticket, err := s.terminals.create(current, terminalTestNode, "agent", 1, protocol.TerminalTargetHost, "", connection, s.now())
	if err != nil {
		t.Fatal(err)
	}
	return s, current, connection, stream, ticket
}

func waitDetached(t *testing.T, s *Server, stream *terminalStream) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.terminals.mu.Lock()
		detached := stream.conn == nil
		s.terminals.mu.Unlock()
		if detached {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("terminal did not detach")
}

func TestTerminalTransportReattachesTmuxWithoutOutputReplay(t *testing.T) {
	s, current, agent, stream, ticket := terminalFixture(t)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleTerminalWebSocket(w, r, current) }))
	defer host.Close()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	dial := func(ticket string) *websocket.Conn {
		conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http")+"?ticket="+ticket, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{host.URL}}})
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	conn := dial(ticket)
	envelope := <-agent.commands
	var frame protocol.TerminalFrame
	json.Unmarshal(envelope.Payload, &frame)
	if frame.Action != protocol.TerminalActionOpen {
		t.Fatal("missing initial shell open")
	}
	s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionReady})
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ready terminalMessage
	json.Unmarshal(raw, &ready)
	if ready.Frame.Action != protocol.TerminalActionReady {
		t.Fatalf("ready = %s", raw)
	}
	conn.CloseNow()
	waitDetached(t, s, stream)
	detachEnvelope := <-agent.commands
	json.Unmarshal(detachEnvelope.Payload, &frame)
	if frame.Action != protocol.TerminalActionDetach {
		t.Fatal("drop did not detach tmux client")
	}
	if stream.closed || s.terminals.byNode[terminalTestNode] != 1 {
		t.Fatal("transport drop killed the shell or freed its slot")
	}
	s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionOutput, Data: []byte("kept running\r\n")})
	request := httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	s.resumeTerminal(response, request, current, terminalTestNode, stream.streamID)
	if response.Code != 201 {
		t.Fatalf("resume %d: %s", response.Code, response.Body.String())
	}
	var authorization createTerminalResponse
	json.Unmarshal(response.Body.Bytes(), &authorization)
	if authorization.StreamID != stream.streamID {
		t.Fatal("resume created a different shell")
	}
	resumed := dial(authorization.Ticket)
	defer resumed.CloseNow()
	attachEnvelope := <-agent.commands
	json.Unmarshal(attachEnvelope.Payload, &frame)
	if frame.Action != protocol.TerminalActionAttach {
		t.Fatalf("resume did not attach tmux: %s", attachEnvelope.Payload)
	}
	if len(stream.out) != 0 {
		t.Fatal("detached output was cached")
	}
	s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionReady})
	s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionOutput, Data: []byte("tmux redraw")})
	_, raw, err = resumed.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(raw, &ready)
	if ready.Frame.Action != protocol.TerminalActionReady {
		t.Fatal(string(raw))
	}
	_, raw, err = resumed.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var output terminalMessage
	json.Unmarshal(raw, &output)
	if string(output.Frame.Data) != "tmux redraw" {
		t.Fatalf("bad tmux redraw: %s", raw)
	}
	s.cancelTerminalTicket(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/terminal", nil), current, terminalTestNode, stream.streamID)
	if s.terminals.find(stream.streamID) != nil || s.terminals.byNode[terminalTestNode] != 0 {
		t.Fatal("explicit end retained the shell")
	}
	closeEnvelope := <-agent.commands
	json.Unmarshal(closeEnvelope.Payload, &frame)
	if frame.Action != protocol.TerminalActionClose {
		t.Fatal("explicit end did not close the Agent PTY")
	}
}

func TestTerminalResumeOwnershipAndRevocation(t *testing.T) {
	s, current, agent, stream, ticket := terminalFixture(t)
	stream, _ = s.terminals.consume(ticket, current.ID, s.now(), nil, func() {})
	stream.opened = true
	s.terminals.detach(s, stream, stream.attachment)
	<-agent.commands
	response := httptest.NewRecorder()
	// Different valid session IDs are rejected even when the caller knows streamId.
	if _, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id,token_digest,csrf_digest,created_at,last_seen_at,user_agent,remote_addr) VALUES('other',x'01',x'01',?,?,'test','127.0.0.1')`, s.now().UnixNano(), s.now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	s.resumeTerminal(response, httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{}`)), &session{ID: "other"}, terminalTestNode, stream.streamID)
	if response.Code != 410 {
		t.Fatalf("foreign resume: %d", response.Code)
	}
	// Revocation also ends a detached shell awaiting consumption of a resume ticket.
	response = httptest.NewRecorder()
	s.resumeTerminal(response, httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{}`)), current, terminalTestNode, stream.streamID)
	if response.Code != 201 {
		t.Fatalf("resume ticket: %d %s", response.Code, response.Body.String())
	}
	s.terminals.closeBrowserSession(s, current.ID, "session revoked")
	if !stream.closed || len(s.terminals.streams) != 0 || len(s.terminals.tickets) != 0 {
		t.Fatal("revocation retained terminal authorization")
	}
	select {
	case command := <-agent.commands:
		var frame protocol.TerminalFrame
		json.Unmarshal(command.Payload, &frame)
		if frame.Action != protocol.TerminalActionClose {
			t.Fatal(frame.Action)
		}
	default:
		t.Fatal("revocation did not close detached PTY")
	}
}

func TestDetachedTerminalEndsOnAgentIdleExpiry(t *testing.T) {
	s, current, agent, stream, ticket := terminalFixture(t)
	stream, _ = s.terminals.consume(ticket, current.ID, s.now(), nil, func() {})
	stream.opened = true
	s.terminals.detach(s, stream, stream.attachment)
	for i := 0; i < 300; i++ {
		s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionOutput, Data: []byte("discarded")})
	}
	if len(stream.out) != 0 || stream.closed {
		t.Fatal("detached output was retained or ended the shell")
	}
	s.terminals.handleAgentFrame(s, agent, agents.Identity{AgentID: "agent", NodeID: terminalTestNode}, protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionClosed})
	response := httptest.NewRecorder()
	s.resumeTerminal(response, httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{}`)), current, terminalTestNode, stream.streamID)
	if response.Code != 410 || !stream.closed {
		t.Fatalf("expired tmux resumed: %d", response.Code)
	}
}

func TestExpiredReattachTicketDoesNotEndTmux(t *testing.T) {
	s, current, _, stream, ticket := terminalFixture(t)
	stream, _ = s.terminals.consume(ticket, current.ID, s.now(), nil, func() {})
	stream.opened = true
	s.terminals.detach(s, stream, stream.attachment)
	response := httptest.NewRecorder()
	s.resumeTerminal(response, httptest.NewRequest(http.MethodPost, "/resume", nil), current, terminalTestNode, stream.streamID)
	if response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	s.terminals.expireTicket(stream.ticket, stream)
	if stream.closed || len(s.terminals.tickets) != 0 || s.terminals.byNode[terminalTestNode] != 1 {
		t.Fatal("unused reattach ticket killed the tmux session")
	}
	response = httptest.NewRecorder()
	s.resumeTerminal(response, httptest.NewRequest(http.MethodPost, "/resume", strings.NewReader(`{}`)), current, terminalTestNode, stream.streamID)
	if response.Code != 201 {
		t.Fatalf("could not reauthorize attachment: %d", response.Code)
	}
	s.terminals.closeBrowserSession(s, current.ID, "session revoked")
}

func TestTerminalRejectsTrailingJSONBeforeForwardingInput(t *testing.T) {
	s, current, agent, stream, ticket := terminalFixture(t)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.handleTerminalWebSocket(w, r, current) }))
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(host.URL, "http")+"?ticket="+ticket, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {host.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	select {
	case <-agent.commands:
	case <-ctx.Done():
		t.Fatal("terminal did not open")
	}
	raw, err := json.Marshal(protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionInput, Data: []byte("x")})
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(" {}")...)
	if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case envelope := <-agent.commands:
		var frame protocol.TerminalFrame
		if err := json.Unmarshal(envelope.Payload, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Action != protocol.TerminalActionClose {
			t.Fatal("malformed frame reached Agent", frame.Action)
		}
	case <-ctx.Done():
		t.Fatal("invalid frame did not close terminal")
	}
}
