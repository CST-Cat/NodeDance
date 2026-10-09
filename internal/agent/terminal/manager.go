// Package terminal owns short-lived, bounded interactive Agent terminals.
// Browser traffic never becomes an arbitrary command: sessions select only a
// fixed host shell or a fixed shell inside an exact Docker container ID.
package terminal

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	DefaultMaxSessions = 2
	DefaultIdleTimeout = 30 * time.Minute
	terminalChunkBytes = 16 << 10
)

var ErrSessionLimit = errors.New("terminal session limit reached")

type Endpoint interface {
	io.ReadWriteCloser
	Resize(rows, columns uint16) error
	ExitCode() *int
}

type Provider interface {
	Open(context.Context, protocol.TerminalFrame) (Endpoint, error)
}

type Manager struct {
	provider Provider
	max      int
	idle     time.Duration

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	id         string
	ctx        context.Context
	cancel     context.CancelFunc
	endpoint   Endpoint
	emit       func(protocol.TerminalFrame) error
	activity   chan struct{}
	closed     chan struct{}
	readerDone chan struct{}
	once       sync.Once
	mu         sync.Mutex
}

func NewManager(provider Provider) *Manager {
	return NewManagerWithLimits(provider, DefaultMaxSessions, DefaultIdleTimeout)
}

func NewManagerWithLimits(provider Provider, maximum int, idle time.Duration) *Manager {
	if maximum < 1 || maximum > 8 {
		maximum = DefaultMaxSessions
	}
	if idle <= 0 || idle > 24*time.Hour {
		idle = DefaultIdleTimeout
	}
	return &Manager{provider: provider, max: maximum, idle: idle, sessions: make(map[string]*session)}
}

func (m *Manager) Handle(ctx context.Context, frame protocol.TerminalFrame, emit func(protocol.TerminalFrame) error) error {
	if m == nil || m.provider == nil || emit == nil {
		return errors.New("terminal provider is unavailable")
	}
	if err := protocol.ValidateTerminalFrame(frame, false); err != nil {
		return err
	}
	switch frame.Action {
	case protocol.TerminalActionOpen:
		return m.open(ctx, frame, emit)
	case protocol.TerminalActionInput:
		current := m.lookup(frame.StreamID)
		if current == nil {
			return errors.New("terminal session is not active")
		}
		current.mu.Lock()
		endpoint := current.endpoint
		if endpoint != nil {
			_, err := endpoint.Write(frame.Data)
			current.mu.Unlock()
			if err != nil {
				m.close(current, true)
				return errors.New("write terminal input failed")
			}
			current.touch()
			return nil
		}
		current.mu.Unlock()
		return errors.New("terminal session is still opening")
	case protocol.TerminalActionResize:
		current := m.lookup(frame.StreamID)
		if current == nil {
			return errors.New("terminal session is not active")
		}
		current.mu.Lock()
		endpoint := current.endpoint
		if endpoint == nil {
			current.mu.Unlock()
			return errors.New("terminal session is still opening")
		}
		err := endpoint.Resize(frame.Rows, frame.Columns)
		current.mu.Unlock()
		if err != nil {
			m.close(current, true)
			return errors.New("resize terminal failed")
		}
		current.touch()
		return nil
	case protocol.TerminalActionClose:
		if current := m.lookup(frame.StreamID); current != nil {
			m.close(current, true)
		}
		return nil
	default:
		return errors.New("unsupported terminal action")
	}
}

func (m *Manager) open(parent context.Context, frame protocol.TerminalFrame, emit func(protocol.TerminalFrame) error) error {
	m.mu.Lock()
	if _, exists := m.sessions[frame.StreamID]; exists {
		m.mu.Unlock()
		return errors.New("terminal stream ID is already active")
	}
	if len(m.sessions) >= m.max {
		m.mu.Unlock()
		return ErrSessionLimit
	}
	ctx, cancel := context.WithCancel(parent)
	current := &session{id: frame.StreamID, ctx: ctx, cancel: cancel, emit: emit, activity: make(chan struct{}, 1), closed: make(chan struct{}), readerDone: make(chan struct{})}
	m.sessions[current.id] = current
	m.mu.Unlock()

	endpoint, err := m.provider.Open(ctx, frame)
	if err != nil {
		m.remove(current)
		cancel()
		return err
	}
	m.mu.Lock()
	stillActive := m.sessions[current.id] == current
	m.mu.Unlock()
	if !stillActive {
		_ = endpoint.Close()
		return context.Canceled
	}
	current.mu.Lock()
	current.endpoint = endpoint
	current.mu.Unlock()
	if err := emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionReady}); err != nil {
		m.close(current, false)
		return errors.New("terminal ready frame could not be queued")
	}
	go func() {
		defer close(current.readerDone)
		m.readOutput(current)
	}()
	go m.expireIdle(current)
	return nil
}

func (m *Manager) readOutput(current *session) {
	buffer := make([]byte, terminalChunkBytes)
	for {
		current.mu.Lock()
		endpoint := current.endpoint
		current.mu.Unlock()
		if endpoint == nil {
			return
		}
		count, err := endpoint.Read(buffer)
		if count > 0 {
			payload := append([]byte(nil), buffer[:count]...)
			if emitErr := current.emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionOutput, Data: payload}); emitErr != nil {
				m.close(current, false)
				return
			}
		}
		if err != nil {
			if current.ctx.Err() == nil {
				code := endpoint.ExitCode()
				if code != nil {
					_ = current.emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionExit, ExitCode: code})
				}
				_ = current.emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionClosed})
			}
			m.close(current, false)
			return
		}
		if count == 0 {
			select {
			case <-current.ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
}

func (m *Manager) expireIdle(current *session) {
	timer := time.NewTimer(m.idle)
	defer timer.Stop()
	for {
		select {
		case <-current.ctx.Done():
			return
		case <-current.activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(m.idle)
		case <-timer.C:
			_ = current.emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionError, Message: "terminal closed after 30 minutes without input"})
			m.close(current, true)
			return
		}
	}
}

func (s *session) touch() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	active := make([]*session, 0, len(m.sessions))
	for _, current := range m.sessions {
		active = append(active, current)
	}
	m.mu.Unlock()
	for _, current := range active {
		m.close(current, true)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for _, current := range active {
		select {
		case <-current.readerDone:
		case <-deadline.C:
			return
		}
	}
}

func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func (m *Manager) lookup(id string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *Manager) close(current *session, notify bool) {
	if current == nil {
		return
	}
	current.once.Do(func() {
		m.remove(current)
		current.cancel()
		current.mu.Lock()
		endpoint := current.endpoint
		current.endpoint = nil
		current.mu.Unlock()
		if endpoint != nil {
			_ = endpoint.Close()
		}
		if notify {
			_ = current.emit(protocol.TerminalFrame{StreamID: current.id, Action: protocol.TerminalActionClosed})
		}
		close(current.closed)
	})
}

func (m *Manager) remove(current *session) {
	m.mu.Lock()
	if m.sessions[current.id] == current {
		delete(m.sessions, current.id)
	}
	m.mu.Unlock()
}
