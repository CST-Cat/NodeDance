package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func TestStreamQueueOverflowKeepsAgentControlWriterAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan protocol.Envelope, 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "test complete")
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var envelope protocol.Envelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				return
			}
			select {
			case received <- envelope:
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial local WebSocket peer: %v", err)
	}
	defer conn.CloseNow()
	writerCtx, writerCancel := context.WithCancel(ctx)
	writer := &socketEnvelopeWriter{
		conn: conn, ctx: writerCtx, cancel: writerCancel,
		high: make(chan envelopeWrite, 8), docker: make(chan protocol.Envelope, 16),
		streams: make(chan protocol.Envelope, 32), metrics: make(chan protocol.Envelope, 1),
		streamFailures: make(chan struct{}, 1), failedStreams: make(map[string]error),
		pendingStreamFailures: make(map[string]error), failures: make(chan error, 1), done: make(chan struct{}),
	}
	for sequence := uint64(1); sequence <= uint64(cap(writer.streams)); sequence++ {
		frame := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerLog,
			Generation: 1, Sequence: sequence, RequestID: "00000000-0000-4000-8000-000000000001",
			Payload: json.RawMessage(`{"stream":"bounded"}`)}
		if err := writer.offerStream(ctx, frame); err != nil {
			t.Fatalf("fill bounded stream queue at %d: %v", sequence, err)
		}
	}
	if err := writer.offerStream(ctx, protocol.Envelope{RequestID: "00000000-0000-4000-8000-000000000001"}); err != errStreamWriterQueueFull {
		t.Fatalf("stream queue overflow error=%v, want %v", err, errStreamWriterQueueFull)
	}
	heartbeatResult := make(chan error, 1)
	writer.high <- envelopeWrite{envelope: protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: 1, Sequence: 1, Payload: json.RawMessage(`{}`)}, result: heartbeatResult}
	go writer.run()
	started := time.Now()
	if err := <-heartbeatResult; err != nil {
		t.Fatalf("heartbeat control write failed after stream queue overflow: %v", err)
	}
	if elapsed := time.Since(started); elapsed > metricWriteTimeout {
		t.Fatalf("heartbeat control was delayed behind stream queue: elapsed=%s limit=%s", elapsed, metricWriteTimeout)
	}
	select {
	case envelope := <-received:
		if envelope.Type != protocol.TypeHeartbeat {
			t.Fatalf("the queued stream was written ahead of ready heartbeat control: first type=%s", envelope.Type)
		}
	case <-time.After(controlWriteTimeout):
		t.Fatal("Core did not receive heartbeat after a stream queue overflow")
	}
	select {
	case err := <-writer.failures:
		t.Fatalf("stream queue overflow was incorrectly promoted to Agent connection failure: %v", err)
	default:
	}
	if !writer.closeAndWait() {
		t.Fatal("Agent writer did not stop cleanly")
	}
}
