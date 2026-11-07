package terminal

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type managedTestEndpoint struct {
	closed chan struct{}
	once   sync.Once
}

func (e *managedTestEndpoint) Read([]byte) (int, error)    { <-e.closed; return 0, io.EOF }
func (e *managedTestEndpoint) Write(p []byte) (int, error) { return len(p), nil }
func (e *managedTestEndpoint) Close() error                { e.once.Do(func() { close(e.closed) }); return nil }
func (e *managedTestEndpoint) Resize(uint16, uint16) error { return nil }
func (e *managedTestEndpoint) ExitCode() *int              { return nil }
func (e *managedTestEndpoint) Attach() error               { return nil }
func (e *managedTestEndpoint) Detach() error               { return nil }

type managerTestProvider struct{ endpoint Endpoint }

func (p managerTestProvider) Open(context.Context, protocol.TerminalFrame) (Endpoint, error) {
	return p.endpoint, nil
}

type openingTestProvider struct {
	endpoint         Endpoint
	entered, release chan struct{}
	ctx              context.Context
}

func (p *openingTestProvider) Open(ctx context.Context, _ protocol.TerminalFrame) (Endpoint, error) {
	p.ctx = ctx
	close(p.entered)
	<-p.release
	return p.endpoint, nil
}
func TestCloseAllDuringOpenJoinsAndClosesEndpoint(t *testing.T) {
	e := &managedTestEndpoint{closed: make(chan struct{})}
	p := &openingTestProvider{endpoint: e, entered: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(p)
	opened := make(chan error, 1)
	go func() {
		opened <- m.open(context.Background(), protocol.TerminalFrame{StreamID: "opening"}, func(protocol.TerminalFrame) error { return nil })
	}()
	<-p.entered
	closed := make(chan struct{})
	go func() { m.CloseAll(); close(closed) }()
	select {
	case <-p.ctx.Done():
	case <-time.After(time.Second):
		close(p.release)
		t.Fatal("opening was not cancelled")
	}
	close(p.release)
	if err := <-opened; err == nil {
		t.Error("cancelled open succeeded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("CloseAll waited on a reader that never started")
	}
	select {
	case <-e.closed:
	default:
		t.Error("late endpoint leaked")
	}
}
func TestParentCancellationClosesTerminal(t *testing.T) {
	e := &managedTestEndpoint{closed: make(chan struct{})}
	m := NewManager(managerTestProvider{e})
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.open(ctx, protocol.TerminalFrame{StreamID: "session"}, func(protocol.TerminalFrame) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-e.closed:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation left terminal endpoint open")
	}
	if m.Count() != 0 {
		t.Fatal("cancelled session retained capacity")
	}
}

type blockedInputEndpoint struct {
	managedTestEndpoint
	writing chan struct{}
}

func (e *blockedInputEndpoint) Write([]byte) (int, error) {
	close(e.writing)
	<-e.closed
	return 0, io.ErrClosedPipe
}

type saturatedOutputEndpoint struct {
	closed   chan struct{}
	once     sync.Once
	produced bool
}

func (e *saturatedOutputEndpoint) Read([]byte) (int, error) {
	if !e.produced {
		e.produced = true
		return 3, nil
	}
	<-e.closed
	return 0, io.EOF
}
func (e *saturatedOutputEndpoint) Write([]byte) (int, error) { return 0, nil }
func (e *saturatedOutputEndpoint) Close() error              { e.once.Do(func() { close(e.closed) }); return nil }
func (e *saturatedOutputEndpoint) Resize(uint16, uint16) error {
	return nil
}
func (e *saturatedOutputEndpoint) ExitCode() *int { return nil }
func (e *saturatedOutputEndpoint) Attach() error  { return nil }
func (e *saturatedOutputEndpoint) Detach() error  { return nil }
func TestCloseInterruptsBlockedTerminalInput(t *testing.T) {
	e := &blockedInputEndpoint{managedTestEndpoint: managedTestEndpoint{closed: make(chan struct{})}, writing: make(chan struct{})}
	m := NewManager(managerTestProvider{e})
	const id = "01234567-89ab-4cde-8123-456789abcdef"
	if err := m.open(context.Background(), protocol.TerminalFrame{StreamID: id}, func(protocol.TerminalFrame) error { return nil }); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	go func() {
		written <- m.Handle(context.Background(), protocol.TerminalFrame{StreamID: id, Action: protocol.TerminalActionInput, Data: []byte("input")}, func(protocol.TerminalFrame) error { return nil })
	}()
	<-e.writing
	closed := make(chan struct{})
	go func() { m.CloseAll(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(300 * time.Millisecond):
		e.Close()
		<-closed
		t.Error("CloseAll blocked behind terminal input mutex")
	}
	<-written
}

func TestSaturatedTerminalLaneStillReportsClosed(t *testing.T) {
	e := &saturatedOutputEndpoint{closed: make(chan struct{})}
	m := NewManager(managerTestProvider{e})
	defer m.CloseAll()
	var mu sync.Mutex
	closedAttempts, closedFrames := 0, 0
	emit := func(frame protocol.TerminalFrame) error {
		mu.Lock()
		defer mu.Unlock()
		switch frame.Action {
		case protocol.TerminalActionOutput:
			return fmt.Errorf("Agent %w", protocol.ErrTerminalBackpressure)
		case protocol.TerminalActionClosed:
			closedAttempts++
			if closedAttempts < 2 {
				return fmt.Errorf("Agent %w", protocol.ErrTerminalBackpressure)
			}
			closedFrames++
			return nil
		}
		return nil
	}
	if err := m.open(context.Background(), protocol.TerminalFrame{StreamID: "saturated"}, emit); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		delivered, removed := closedFrames == 1, m.Count() == 0
		mu.Unlock()
		if delivered && removed {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("closed frame delivered=%v session removed=%v attempts=%d", delivered, removed, closedAttempts)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
