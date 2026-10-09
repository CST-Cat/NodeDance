package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestTerminalTicketsAreSessionBoundOneUseAndCountPendingSlots(t *testing.T) {
	manager := newTerminalStreamManager()
	current := &session{ID: "0123456789abcdef0123456789abcdef", RemoteAddr: "127.0.0.1"}
	agent := &agentConnection{agentID: "11111111-1111-4111-8111-111111111111", generation: 3, terminalEnabled: true, commands: make(chan protocol.Envelope, 8)}
	streams := make([]*terminalStream, 2)
	tickets := make([]string, 2)
	for index := range streams {
		stream, ticket, err := manager.create(current, "22222222-2222-4222-8222-222222222222", agent.agentID, agent.generation, protocol.TerminalTargetHost, "", agent, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		streams[index], tickets[index] = stream, ticket
	}
	if _, _, err := manager.create(current, "22222222-2222-4222-8222-222222222222", agent.agentID, agent.generation, protocol.TerminalTargetHost, "", agent, time.Now()); err != errTerminalLimit {
		t.Fatalf("third terminal error=%v, want concurrency limit", err)
	}
	if stream, ok := manager.consume(tickets[0], "another-session", time.Now(), nil, nil); ok || stream != nil {
		t.Fatal("ticket was accepted for a different browser Session")
	}
	active, ok := manager.consume(tickets[0], current.ID, time.Now(), nil, nil)
	if !ok || active != streams[0] {
		t.Fatal("correct Session could not consume its ticket")
	}
	if _, ok := manager.consume(tickets[0], current.ID, time.Now(), nil, nil); ok {
		t.Fatal("terminal ticket was reusable")
	}
	manager.close(nil, active, "test close", false)
	manager.close(nil, streams[1], "test cleanup", false)
	manager.mu.Lock()
	remaining := manager.byNode["22222222-2222-4222-8222-222222222222"]
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("terminal slots left reserved after close: %d", remaining)
	}
}

func TestTerminalAuthorizationExpirationReleasesPendingSlot(t *testing.T) {
	manager := newTerminalStreamManager()
	current := &session{ID: "0123456789abcdef0123456789abcdef"}
	agent := &agentConnection{agentID: "11111111-1111-4111-8111-111111111111", generation: 1, commands: make(chan protocol.Envelope, 1)}
	stream, ticket, err := manager.create(current, "22222222-2222-4222-8222-222222222222", agent.agentID, agent.generation, protocol.TerminalTargetHost, "", agent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	manager.expireTicket(stream.ticket, stream)
	if _, ok := manager.consume(ticket, current.ID, time.Now(), nil, nil); ok {
		t.Fatal("expired terminal authorization was consumed")
	}
	manager.mu.Lock()
	remaining := manager.byNode[stream.nodeID]
	manager.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired authorization retained %d node slots", remaining)
	}
}

func TestTerminalSessionRevocationCancelsStreamAndNotifiesAgent(t *testing.T) {
	s := newTestServer(t)
	current := &session{ID: "0123456789abcdef0123456789abcdef", RemoteAddr: "127.0.0.1"}
	agent := &agentConnection{agentID: "11111111-1111-4111-8111-111111111111", generation: 3,
		commands: make(chan protocol.Envelope, 2)}
	stream, ticket, err := s.terminals.create(current, "22222222-2222-4222-8222-222222222222", agent.agentID,
		agent.generation, protocol.TerminalTargetHost, "", agent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	browserCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if consumed, ok := s.terminals.consume(ticket, current.ID, time.Now(), nil, cancel); !ok || consumed != stream {
		t.Fatal("could not activate terminal stream")
	}
	s.terminals.mu.Lock()
	stream.started = true
	s.terminals.mu.Unlock()
	s.terminals.closeBrowserSession(s, current.ID, "browser Session revoked")
	select {
	case <-browserCtx.Done():
	default:
		t.Fatal("browser data channel remained open after Session revocation")
	}
	if s.terminals.find(stream.streamID) != nil {
		t.Fatal("revoked terminal remained active")
	}
	select {
	case command := <-agent.commands:
		var frame protocol.TerminalFrame
		if command.Type != protocol.TypeTerminalFrame || decodeAgentPayload(command.Payload, &frame) != nil ||
			frame.StreamID != stream.streamID || frame.Action != protocol.TerminalActionClose {
			t.Fatalf("Agent cleanup command=%+v frame=%+v", command, frame)
		}
	default:
		t.Fatal("Agent was not notified to close the revoked terminal")
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='terminal_end' AND target_kind='terminal_host' AND target_id=?`, stream.nodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("Session revocation terminal end audit rows=%d, want 1", count)
	}
}

func TestTerminalAgentOutputQueueOverflowClosesOnlyBoundedStream(t *testing.T) {
	manager := newTerminalStreamManager()
	current := &session{ID: "0123456789abcdef0123456789abcdef"}
	agent := &agentConnection{agentID: "11111111-1111-4111-8111-111111111111", generation: 2, commands: make(chan protocol.Envelope, 1)}
	stream, ticket, err := manager.create(current, "22222222-2222-4222-8222-222222222222", agent.agentID, agent.generation, protocol.TerminalTargetHost, "", agent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.consume(ticket, current.ID, time.Now(), nil, nil); !ok {
		t.Fatal("consume terminal ticket")
	}
	for range terminalBrowserQueue {
		stream.out <- protocol.TerminalFrame{StreamID: stream.streamID, Action: protocol.TerminalActionOutput, Data: []byte("x")}
	}
	manager.handleAgentFrame(nil, agent, agents.Identity{AgentID: agent.agentID, NodeID: stream.nodeID}, protocol.TerminalFrame{
		StreamID: stream.streamID, Action: protocol.TerminalActionOutput, Data: []byte("overflow"),
	})
	if manager.find(stream.streamID) != nil {
		t.Fatal("overflowed terminal remained active")
	}
	if len(agent.commands) != 1 {
		t.Fatal("closing overflowed browser stream did not enqueue Agent cleanup")
	}
}

func TestTerminalRoutesRequireSessionCSRFAndOrigin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	s, err := New("test", Options{DataDir: dir, Development: true, PublicOrigin: "http://panel.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sessionToken, csrfToken, err := installIntegrationAdmin(s)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := "22222222-2222-4222-8222-222222222222"
	r := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/terminals", strings.NewReader(`{"targetKind":"host"}`))
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	r.Header.Set("Origin", "http://evil.example.test")
	r.Header.Set(csrfHeaderName, csrfToken)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin terminal creation status=%d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/terminals", strings.NewReader(`{"targetKind":"host"}`))
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	r.Header.Set("Origin", "http://panel.example.test")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("terminal creation without CSRF header status=%d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/terminals", strings.NewReader(`{"targetKind":"host"}`))
	r.Header.Set("Origin", "http://panel.example.test")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous terminal creation status=%d, want 401", w.Code)
	}

	var entries int
	if err := s.store.DB.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Fatalf("unexpected audit rows before an authorized terminal starts: %d", entries)
	}
}

func TestTerminalAuditStoresOnlySessionMetadata(t *testing.T) {
	s := newTestServer(t)
	stream := &terminalStream{nodeID: "22222222-2222-4222-8222-222222222222", targetKind: protocol.TerminalTargetHost, remoteAddr: "127.0.0.1"}
	if !s.recordTerminalAudit(stream, "terminal_start", "succeeded") || !s.recordTerminalAudit(stream, "terminal_end", "succeeded") {
		t.Fatal("could not record terminal lifecycle audit")
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action IN ('terminal_start','terminal_end') AND target_kind='terminal_host' AND target_id=?`, stream.nodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("terminal audit event count=%d, want start and end", count)
	}
	var stored string
	if err := s.store.DB.QueryRow(`SELECT group_concat(action || ':' || outcome || ':' || coalesce(target_id,''), '|') FROM audit_entries`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "NEVER_AUDIT_TERMINAL_INPUT") {
		t.Fatal("terminal input appeared in audit storage")
	}

	stream = &terminalStream{nodeID: "22222222-2222-4222-8222-222222222222", targetKind: protocol.TerminalTargetContainer,
		containerID: strings.Repeat("a", 64), remoteAddr: "127.0.0.1"}
	if !s.recordTerminalAudit(stream, "terminal_start", "succeeded") {
		t.Fatal("could not record container terminal target")
	}
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='terminal_start' AND target_kind='terminal_container' AND target_id=?`, stream.containerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("container terminal audit target count=%d, want 1", count)
	}
}
