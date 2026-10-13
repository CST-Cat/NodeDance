package agent

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containerstreams"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type malformedLogBody struct {
	io.Reader
	done chan struct{}
	once sync.Once
}

func (b *malformedLogBody) Close() error { b.once.Do(func() { close(b.done) }); return nil }

type malformedLogEngine struct{ body *malformedLogBody }

func (e malformedLogEngine) Inspect(_ context.Context, id string) (containerstreams.ContainerInfo, error) {
	return containerstreams.ContainerInfo{ID: id}, nil
}
func (e malformedLogEngine) OpenLogs(context.Context, string, containerstreams.LogsOptions) (io.ReadCloser, error) {
	return e.body, nil
}
func (e malformedLogEngine) OpenStats(context.Context, string) (io.ReadCloser, error) {
	return nil, io.EOF
}

type terminalLogWriter struct {
	bodyDone <-chan struct{}
	terminal string
}

func (w *terminalLogWriter) offerStream(context.Context, protocol.Envelope) error {
	<-w.bodyDone
	time.Sleep(time.Millisecond)
	return nil
}
func (w *terminalLogWriter) sendStreamTerminal(_ context.Context, e protocol.Envelope) error {
	w.terminal = e.Type
	return nil
}
func TestMalformedLogStreamCannotBeReportedAsNormalEnd(t *testing.T) {
	for range 50 {
		body := &malformedLogBody{Reader: bytes.NewReader([]byte{1, 0, 0}), done: make(chan struct{})}
		writer := &terminalLogWriter{bodyDone: body.done}
		bridge := &containerStreamBridge{engine: malformedLogEngine{body}}
		stream := &agentContainerStream{id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", kind: protocol.StreamLogs, containerID: strings.Repeat("a", 64)}
		bridge.runLogs(context.Background(), writer, stream, protocol.ContainerStreamOpen{Kind: protocol.StreamLogs, ContainerID: stream.containerID, Tail: "1"}, 1)
		if writer.terminal != protocol.TypeContainerStreamError {
			t.Fatalf("malformed logs emitted %s instead of an error", writer.terminal)
		}
	}
}
