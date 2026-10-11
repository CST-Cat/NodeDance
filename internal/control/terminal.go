package control

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

// OpenTerminal combines single-use authorization and opening the selected
// host/container shell. Both human TUI and automation use this sequence.
func (c *Client) OpenTerminal(ctx context.Context, in Input, rows, columns uint16) (*websocket.Conn, string, error) {
	create, _ := Find("terminal.create")
	result := c.Execute(ctx, create, in, nil, nil)
	if err := result.Err(); err != nil {
		return nil, "", err
	}
	var authorized struct {
		StreamID string `json:"streamId"`
		Ticket   string `json:"ticket"`
	}
	var target struct {
		TargetKind  string `json:"targetKind"`
		ContainerID string `json:"containerId"`
	}
	if json.Unmarshal(result.Data, &authorized) != nil || authorized.StreamID == "" || authorized.Ticket == "" || json.Unmarshal(in.Body, &target) != nil {
		return nil, "", errors.New("invalid terminal authorization")
	}
	connect, _ := Find("terminal.connect")
	conn, err := c.Connect(ctx, connect, Input{Query: map[string]string{"ticket": authorized.Ticket}})
	if err != nil {
		// A failed dial must not leave a pending Agent terminal consuming a slot.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cancelOp, _ := Find("terminal.cancel")
		c.Execute(cleanup, cancelOp, Input{Params: map[string]string{"nodeId": in.Params["nodeId"], "streamId": authorized.StreamID}}, nil, nil)
		return nil, "", err
	}
	frame := protocol.TerminalFrame{StreamID: authorized.StreamID, Action: protocol.TerminalActionOpen, TargetKind: target.TargetKind, ContainerID: target.ContainerID, Rows: rows, Columns: columns}
	if err = SendTerminal(ctx, conn, frame); err != nil {
		conn.CloseNow()
		return nil, "", err
	}
	return conn, authorized.StreamID, nil
}
func SendTerminal(ctx context.Context, conn *websocket.Conn, frame protocol.TerminalFrame) error {
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
