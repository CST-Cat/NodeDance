package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type fileDataWriter struct{ conn *websocket.Conn }

func (w *fileDataWriter) send(ctx context.Context, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	return w.conn.Write(ctx, websocket.MessageText, data)
}

func (b *agentFileBridge) dialCoreFileStream(ctx context.Context, route string, grant protocol.FileStreamGrant) (*websocket.Conn, func(), error) {
	config, err := LoadConfig(b.configPath)
	if err != nil {
		return nil, nil, err
	}
	server, err := url.Parse(config.Server)
	if err != nil {
		return nil, nil, err
	}
	if server.Scheme == "https" {
		server.Scheme = "wss"
	} else if config.Development && isLiteralLoopback(server.Hostname()) {
		server.Scheme = "ws"
	} else {
		return nil, nil, errors.New("file data connections require WSS")
	}
	server.Path = route
	client, err := newHTTPClient(config.CAFile)
	if err != nil {
		return nil, nil, err
	}
	dialCtx, cancel := context.WithDeadline(ctx, grant.ExpiresAt)
	defer cancel()
	conn, _, err := websocket.Dial(dialCtx, server.String(), &websocket.DialOptions{HTTPClient: client, HTTPHeader: grant.Headers(), CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		client.CloseIdleConnections()
		return nil, nil, err
	}
	cleanup := func() { _ = conn.CloseNow(); client.CloseIdleConnections() }
	return conn, cleanup, nil
}

func (b *agentFileBridge) openFileData(parent context.Context, id string, request protocol.FileDataOpen) {
	ctx, cancel := context.WithCancel(parent)
	b.mu.Lock()
	if b.closed || b.dataSessions[id] != nil || len(b.dataSessions) >= maxAgentFileTransfers {
		b.mu.Unlock()
		cancel()
		_ = b.respond(parent, id, protocol.FileResponse{Operation: request.Request.Operation}, errors.New("file data capacity reached"))
		return
	}
	b.dataSessions[id] = cancel
	b.mu.Unlock()
	defer func() { cancel(); b.mu.Lock(); delete(b.dataSessions, id); b.mu.Unlock() }()
	conn, cleanup, err := b.dialCoreFileStream(ctx, protocol.FileChunkDataRoute, request.Grant)
	if err != nil {
		_ = b.respond(parent, id, protocol.FileResponse{Operation: request.Request.Operation}, err)
		return
	}
	defer cleanup()
	stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
	defer stop()
	conn.SetReadLimit(protocol.MaxTextControlBytes + (64 << 10))
	bridge := newAgentFileBridge(b.service, b.generation, &fileDataWriter{conn: conn}, b.tasks)
	initial := request.Request.FileRequest(id)
	bridge.dataID, bridge.dataBinding = id, &initial
	messages := make(chan protocol.Envelope, 4)
	done := make(chan error, 1)
	go func() { done <- bridge.run(ctx, messages); cancel() }()
	defer func() { cancel(); <-done }()
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if kind != websocket.MessageText {
			return
		}
		var envelope protocol.Envelope
		if json.Unmarshal(data, &envelope) != nil {
			return
		}
		select {
		case messages <- envelope:
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			return
		}
	}
}
