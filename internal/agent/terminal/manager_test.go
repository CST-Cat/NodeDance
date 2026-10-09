package terminal

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type fakeProvider struct {
	mu        sync.Mutex
	endpoints []*fakeEndpoint
}

func (p *fakeProvider) Open(_ context.Context, _ protocol.TerminalFrame) (Endpoint, error) {
	ep := newFakeEndpoint()
	p.mu.Lock()
	p.endpoints = append(p.endpoints, ep)
	p.mu.Unlock()
	return ep, nil
}

type fakeEndpoint struct {
	mu       sync.Mutex
	input    bytes.Buffer
	rows     uint16
	columns  uint16
	output   chan []byte
	closed   chan struct{}
	closeOne sync.Once
}

func newFakeEndpoint() *fakeEndpoint {
	return &fakeEndpoint{output: make(chan []byte, 4), closed: make(chan struct{})}
}

func (e *fakeEndpoint) Read(buffer []byte) (int, error) {
	select {
	case data := <-e.output:
		return copy(buffer, data), nil
	case <-e.closed:
		return 0, io.EOF
	}
}

func (e *fakeEndpoint) Write(data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.input.Write(data)
}

func (e *fakeEndpoint) Resize(rows, columns uint16) error {
	e.mu.Lock()
	e.rows, e.columns = rows, columns
	e.mu.Unlock()
	return nil
}

func (e *fakeEndpoint) ExitCode() *int { code := 0; return &code }
func (e *fakeEndpoint) Close() error   { e.closeOne.Do(func() { close(e.closed) }); return nil }

func (e *fakeEndpoint) inputString() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.input.String()
}

func (e *fakeEndpoint) size() (uint16, uint16) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rows, e.columns
}

func TestManagerOpenInputResizeOutputAndClose(t *testing.T) {
	provider := &fakeProvider{}
	manager := NewManager(provider)
	events := make(chan protocol.TerminalFrame, 8)
	emit := func(frame protocol.TerminalFrame) error { events <- frame; return nil }
	ctx := context.Background()
	open := protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80}
	if err := manager.Handle(ctx, open, emit); err != nil {
		t.Fatal(err)
	}
	if got := <-events; got.Action != protocol.TerminalActionReady {
		t.Fatalf("first frame action=%q, want ready", got.Action)
	}
	if manager.Count() != 1 {
		t.Fatalf("active sessions=%d, want 1", manager.Count())
	}
	provider.mu.Lock()
	endpoint := provider.endpoints[0]
	provider.mu.Unlock()

	secretInput := "printf 'NodeDance 你好\\n'\r"
	input := protocol.TerminalFrame{StreamID: open.StreamID, Action: protocol.TerminalActionInput, Data: []byte(secretInput)}
	if err := manager.Handle(ctx, input, emit); err != nil {
		t.Fatal(err)
	}
	if got := endpoint.inputString(); got != secretInput {
		t.Fatalf("input=%q, want exact Unicode input", got)
	}
	resize := protocol.TerminalFrame{StreamID: open.StreamID, Action: protocol.TerminalActionResize, Rows: 42, Columns: 111}
	if err := manager.Handle(ctx, resize, emit); err != nil {
		t.Fatal(err)
	}
	if rows, columns := endpoint.size(); rows != 42 || columns != 111 {
		t.Fatalf("terminal size=%dx%d, want 42x111", rows, columns)
	}
	endpoint.output <- []byte("output 你好")
	select {
	case got := <-events:
		if got.Action != protocol.TerminalActionOutput || string(got.Data) != "output 你好" {
			t.Fatalf("output frame=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal output was not forwarded")
	}
	closeFrame := protocol.TerminalFrame{StreamID: open.StreamID, Action: protocol.TerminalActionClose}
	if err := manager.Handle(ctx, closeFrame, emit); err != nil {
		t.Fatal(err)
	}
	if manager.Count() != 0 {
		t.Fatalf("active sessions after close=%d", manager.Count())
	}
	select {
	case <-endpoint.closed:
	default:
		t.Fatal("endpoint was not closed")
	}
}

func TestManagerEnforcesTwoConcurrentSessions(t *testing.T) {
	manager := NewManager(&fakeProvider{})
	emit := func(protocol.TerminalFrame) error { return nil }
	for _, id := range []string{"abcdefghijklmnopqrstuvwxyz0123456789A", "abcdefghijklmnopqrstuvwxyz0123456789B"} {
		frame := protocol.TerminalFrame{StreamID: id, Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80}
		if err := manager.Handle(context.Background(), frame, emit); err != nil {
			t.Fatal(err)
		}
	}
	third := protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789C", Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80}
	if err := manager.Handle(context.Background(), third, emit); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("third session error=%v, want ErrSessionLimit", err)
	}
	manager.CloseAll()
	if manager.Count() != 0 {
		t.Fatal("CloseAll left terminal sessions active")
	}
}

func TestManagerIdleTimeoutClosesEndpoint(t *testing.T) {
	provider := &fakeProvider{}
	manager := NewManagerWithLimits(provider, 2, 35*time.Millisecond)
	events := make(chan protocol.TerminalFrame, 8)
	emit := func(frame protocol.TerminalFrame) error { events <- frame; return nil }
	frame := protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789IDLE", Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80}
	if err := manager.Handle(context.Background(), frame, emit); err != nil {
		t.Fatal(err)
	}
	<-events // ready
	select {
	case got := <-events:
		if got.Action != protocol.TerminalActionError || got.Message == "" {
			t.Fatalf("idle closure frame=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("idle terminal was not closed")
	}
	if manager.Count() != 0 {
		t.Fatalf("idle terminal remains active: %d", manager.Count())
	}
}

func TestManagerOutputQueueFailureClosesEndpoint(t *testing.T) {
	provider := &fakeProvider{}
	manager := NewManager(provider)
	frame := protocol.TerminalFrame{StreamID: "abcdefghijklmnopqrstuvwxyz0123456789FULL", Action: protocol.TerminalActionOpen, TargetKind: protocol.TerminalTargetHost, Rows: 24, Columns: 80}
	var count int
	emit := func(value protocol.TerminalFrame) error {
		if value.Action == protocol.TerminalActionOutput {
			return errors.New("bounded transport queue full")
		}
		count++
		return nil
	}
	if err := manager.Handle(context.Background(), frame, emit); err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	endpoint := provider.endpoints[0]
	provider.mu.Unlock()
	endpoint.output <- []byte("chunk")
	select {
	case <-endpoint.closed:
	case <-time.After(time.Second):
		t.Fatal("endpoint remained open after output queue saturation")
	}
	if manager.Count() != 0 {
		t.Fatal("queue-saturated terminal remained active")
	}
}
