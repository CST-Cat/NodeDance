package protocol

import (
	"context"
	"github.com/coder/websocket"
)

// AgentChannel carries the same authenticated, generation-bound protocol over
// WebSocket or acknowledged HTTP polling. No business behavior lives here.
type AgentChannel interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
	SetReadLimit(int64)
}
