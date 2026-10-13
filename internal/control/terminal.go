package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

// OpenTerminal combines single-use authorization and opening the selected
// host/container shell. Both human TUI and automation use this sequence.
func (c *Client) OpenTerminal(ctx context.Context, in Input, rows, columns uint16) (*TerminalSession, string, error) {
	catalog, err := c.Catalog(ctx)
	if err != nil {
		return nil, "", err
	}
	create, err := FindIn(catalog, "terminal.create")
	if err != nil {
		return nil, "", err
	}
	result := c.Execute(ctx, create, in, nil, nil)
	if err := result.Err(); err != nil {
		return nil, "", err
	}
	var authorized struct {
		StreamID string `json:"streamId"`
		Ticket   string `json:"ticket"`
	}
	if json.Unmarshal(result.Data, &authorized) != nil || authorized.StreamID == "" || authorized.Ticket == "" {
		return nil, "", errors.New("invalid terminal authorization")
	}
	connect, err := FindIn(catalog, "terminal.connect")
	if err != nil {
		return nil, "", err
	}
	conn, err := c.Connect(ctx, connect, Input{Query: map[string]string{"ticket": authorized.Ticket}})
	if err != nil {
		// A failed dial must not leave a pending Agent terminal consuming a slot.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cancelOp, _ := FindIn(catalog, "terminal.cancel")
		c.Execute(cleanup, cancelOp, Input{Params: map[string]string{"nodeId": in.Params["nodeId"], "streamId": authorized.StreamID}}, nil, nil)
		return nil, "", err
	}
	frame := protocol.TerminalFrame{StreamID: authorized.StreamID, Action: protocol.TerminalActionResize, Rows: rows, Columns: columns}
	if err = SendTerminal(ctx, conn, frame); err != nil {
		conn.CloseNow()
		return nil, "", err
	}
	return &TerminalSession{conn: conn, client: c, nodeID: in.Params["nodeId"], streamID: authorized.StreamID, rows: rows, columns: columns}, authorized.StreamID, nil
}
func SendTerminal(ctx context.Context, conn interface {
	Write(context.Context, websocket.MessageType, []byte) error
}, frame protocol.TerminalFrame) error {
	if err := protocol.ValidateTerminalFrame(frame, false); err != nil {
		return err
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, raw)
}

// ResumeTerminal obtains a new single-use, session-bound ticket without opening
// another shell or replaying any input.
func (c *Client) ResumeTerminal(ctx context.Context, nodeID, streamID string) (*websocket.Conn, error) {
	catalog, err := c.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	resume, err := FindIn(catalog, "terminal.resume")
	if err != nil {
		return nil, err
	}
	body := json.RawMessage(`{}`)
	result := c.Execute(ctx, resume, Input{Params: map[string]string{"nodeId": nodeID, "streamId": streamID}, Body: body}, nil, nil)
	if err := result.Err(); err != nil {
		if result.Status == 401 || result.Status == 403 || result.Status == 410 {
			return nil, fmt.Errorf("%w: %v", errTerminalUnavailable, err)
		}
		return nil, err
	}
	var authorized struct {
		Ticket string `json:"ticket"`
	}
	if json.Unmarshal(result.Data, &authorized) != nil || authorized.Ticket == "" {
		return nil, errors.New("invalid terminal authorization")
	}
	connect, err := FindIn(catalog, "terminal.connect")
	if err != nil {
		return nil, err
	}
	return c.Connect(ctx, connect, Input{Query: map[string]string{"ticket": authorized.Ticket}})
}

var errTerminalUnavailable = errors.New("terminal session is no longer available")

// StreamConnection is shared by generic streams and resumable terminal streams.
type StreamConnection interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	CloseNow() error
}

// TerminalSession reattaches the same tmux session through a fresh transport.
// Input is never queued or replayed: a dropped keystroke is safer than running
// an uncertain command twice after reconnecting.
type TerminalSession struct {
	mu               sync.Mutex
	conn             *websocket.Conn
	client           *Client
	nodeID, streamID string
	closed           bool
	ready            bool
	rows, columns    uint16
	resizeOnReady    bool
	readCancel       context.CancelFunc
}

func (t *TerminalSession) Write(ctx context.Context, kind websocket.MessageType, data []byte) error {
	t.mu.Lock()
	var frame protocol.TerminalFrame
	if json.Unmarshal(data, &frame) == nil && frame.Action == protocol.TerminalActionResize {
		t.rows, t.columns = frame.Rows, frame.Columns
		if !t.ready {
			t.resizeOnReady = true
		}
	}
	conn, closed, ready := t.conn, t.closed, t.ready
	t.mu.Unlock()
	if closed {
		return errors.New("terminal is closed")
	}
	if conn == nil || !ready {
		return nil
	}
	if err := conn.Write(ctx, kind, data); err != nil {
		conn.CloseNow()
		t.mu.Lock()
		if t.conn == conn {
			t.conn = nil
			t.ready = false
		}
		t.mu.Unlock()
	}
	return nil
}
func (t *TerminalSession) CloseNow() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	conn := t.conn
	readCancel := t.readCancel
	t.conn = nil
	t.mu.Unlock()
	if readCancel != nil {
		readCancel()
	}
	if conn != nil {
		conn.CloseNow()
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	catalog, err := t.client.Catalog(ctx)
	if err != nil {
		return err
	}
	operation, err := FindIn(catalog, "terminal.cancel")
	if err != nil {
		return err
	}
	return t.client.Execute(ctx, operation, Input{Params: map[string]string{"nodeId": t.nodeID, "streamId": t.streamID}}, nil, nil).Err()
}
func (t *TerminalSession) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, nil, errors.New("terminal is closed")
	}
	t.readCancel = cancel
	t.mu.Unlock()
	for {
		t.mu.Lock()
		conn, closed := t.conn, t.closed
		t.mu.Unlock()
		if closed {
			return 0, nil, errors.New("terminal is closed")
		}
		if conn != nil {
			kind, data, err := conn.Read(ctx)
			if err == nil {
				var message struct {
					Frame protocol.TerminalFrame `json:"frame"`
				}
				if json.Unmarshal(data, &message) == nil && message.Frame.Action == protocol.TerminalActionReady {
					t.mu.Lock()
					t.ready = true
					rows, columns := t.rows, t.columns
					resize := t.resizeOnReady
					t.resizeOnReady = false
					t.mu.Unlock()
					if resize {
						if err := SendTerminal(ctx, t, protocol.TerminalFrame{StreamID: t.streamID, Action: protocol.TerminalActionResize, Rows: rows, Columns: columns}); err != nil {
							return 0, nil, err
						}
					}
				}
				return kind, data, nil
			}
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
				return 0, nil, err
			}
			conn.CloseNow()
			t.mu.Lock()
			if t.conn == conn {
				t.conn = nil
				t.ready = false
			}
			t.mu.Unlock()
		}
		reconnectCtx, stop := context.WithCancel(ctx)
		delay := 500 * time.Millisecond
		for {
			timer := time.NewTimer(delay)
			select {
			case <-reconnectCtx.Done():
				timer.Stop()
				stop()
				return 0, nil, reconnectCtx.Err()
			case <-timer.C:
			}
			t.mu.Lock()
			closed := t.closed
			t.mu.Unlock()
			if closed {
				stop()
				return 0, nil, errors.New("terminal is closed")
			}
			resumed, err := t.client.ResumeTerminal(reconnectCtx, t.nodeID, t.streamID)
			if err == nil {
				t.mu.Lock()
				closed = t.closed
				if !closed {
					t.conn = resumed
					t.resizeOnReady = true
				}
				t.mu.Unlock()
				if closed {
					resumed.CloseNow()
					stop()
					return 0, nil, errors.New("terminal is closed")
				}
				break
			}
			if errors.Is(err, errTerminalUnavailable) {
				stop()
				return 0, nil, err
			}
			delay = min(5*time.Second, delay*2)
		}
		stop()
	}
}
