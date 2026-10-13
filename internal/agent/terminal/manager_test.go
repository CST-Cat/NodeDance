package terminal

import (
	"context"
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
