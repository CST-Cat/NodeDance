package protocol

import (
	"context"
	"github.com/coder/websocket"
)

type AgentChannel interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
	Close(websocket.StatusCode, string) error
	CloseNow() error
	SetReadLimit(int64)
}
