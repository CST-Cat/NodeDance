package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

func TestDockerWriterQueuePreservesSnapshotChunkOrderAndRejectsOverflow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &socketEnvelopeWriter{ctx: ctx, docker: make(chan protocol.Envelope, 2)}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		if err := writer.offerDocker(ctx, protocol.Envelope{Type: protocol.TypeDocker, Sequence: sequence}); err != nil {
			t.Fatalf("enqueue ordered Docker chunk %d: %v", sequence, err)
		}
	}
	if err := writer.offerDocker(ctx, protocol.Envelope{Type: protocol.TypeDocker, Sequence: 3}); !errors.Is(err, errDockerWriterQueueFull) {
		t.Fatalf("overflow error=%v, want full-queue resynchronization signal", err)
	}
	for want := uint64(1); want <= 2; want++ {
		got := <-writer.docker
		if got.Sequence != want {
			t.Fatalf("Docker chunk order changed: got sequence=%d want=%d", got.Sequence, want)
		}
	}
}

func TestSocketWriterMetricsAndControlRemainFairUnderContinuousDockerLoad(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan protocol.Envelope, 1024)
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
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial local WebSocket peer: %v", err)
	}
	writerCtx, writerCancel := context.WithCancel(ctx)
	writer := &socketEnvelopeWriter{
		conn: conn, ctx: writerCtx, cancel: writerCancel,
		high: make(chan envelopeWrite, 8), docker: make(chan protocol.Envelope, 16),
		metrics: make(chan protocol.Envelope, 1), failures: make(chan error, 1), done: make(chan struct{}),
	}
	t.Cleanup(func() {
		writer.closeAndWait()
		_ = conn.CloseNow()
	})

	var acceptedCount atomic.Uint64
	var highestAccepted atomic.Uint64
	for sequence := uint64(1); sequence <= uint64(cap(writer.docker)); sequence++ {
		if err := writer.offerDocker(ctx, testDockerEnvelope(sequence)); err != nil {
			t.Fatalf("prime FIFO Docker backlog: %v", err)
		}
		acceptedCount.Add(1)
		highestAccepted.Store(sequence)
	}
	metric := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics, Generation: 1,
		Sequence: 1, Payload: json.RawMessage(`{"sample":1}`)}
	writer.offerMetrics(metric)
	heartbeatResult := make(chan error, 1)
	writer.high <- envelopeWrite{envelope: protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: 1, Sequence: 1}, result: heartbeatResult}

	producerCtx, stopProducer := context.WithCancel(ctx)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		sequence := uint64(cap(writer.docker))
		for {
			select {
			case <-producerCtx.Done():
				return
			default:
			}
			sequence++
			if err := writer.offerDocker(producerCtx, testDockerEnvelope(sequence)); err == nil {
				acceptedCount.Add(1)
				highestAccepted.Store(sequence)
			} else if errors.Is(err, errDockerWriterQueueFull) {
				runtime.Gosched()
			} else {
				return
			}
		}
	}()
	defer func() {
		stopProducer()
		select {
		case <-producerDone:
		case <-time.After(time.Second):
			t.Error("continuous Docker producer did not stop")
		}
	}()
	go writer.run()

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var previousDockerSequence uint64
	deliveredDocker := 0
	dockerBeforeMetrics := 0
	var lastAccepted uint64
	sawHeartbeat, sawMetrics := false, false
	for !sawMetrics {
		select {
		case envelope := <-received:
			switch envelope.Type {
			case protocol.TypeDocker:
				if envelope.Sequence <= previousDockerSequence {
					t.Fatalf("Docker FIFO sequence regressed under fairness load: previous=%d got=%d", previousDockerSequence, envelope.Sequence)
				}
				previousDockerSequence = envelope.Sequence
				deliveredDocker++
				if !sawMetrics {
					dockerBeforeMetrics++
				}
			case protocol.TypeMetrics:
				sawMetrics = true
			case protocol.TypeHeartbeat:
				sawHeartbeat = true
			default:
				t.Fatalf("unexpected fairness-test frame %q", envelope.Type)
			}
		case <-deadline.C:
			t.Fatalf("writer starved an eligible frame under continuous Docker load: metrics=%t heartbeat=%t", sawMetrics, sawHeartbeat)
		}
	}
	if !sawHeartbeat {
		t.Fatal("heartbeat/control did not retain priority over the queued Docker load")
	}
	if err := <-heartbeatResult; err != nil {
		t.Fatalf("control heartbeat write failed under Docker load: %v", err)
	}
	if dockerBeforeMetrics > maxDockerWriteBurst {
		t.Fatalf("metrics waited behind an unbounded Docker stream: Docker frames before metric=%d burst=%d", dockerBeforeMetrics, maxDockerWriteBurst)
	}
	if deliveredDocker != maxDockerWriteBurst {
		t.Fatalf("metrics did not preempt Docker after the configured bounded burst: delivered=%d burst=%d", deliveredDocker, maxDockerWriteBurst)
	}

	stopProducer()
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("continuous Docker producer did not stop")
	}
	lastAccepted = highestAccepted.Load()
	for previousDockerSequence < lastAccepted {
		select {
		case envelope := <-received:
			if envelope.Type != protocol.TypeDocker {
				t.Fatalf("unexpected frame while draining FIFO Docker queue: %q", envelope.Type)
			}
			if envelope.Sequence <= previousDockerSequence {
				t.Fatalf("Docker FIFO sequence regressed while draining: previous=%d got=%d", previousDockerSequence, envelope.Sequence)
			}
			previousDockerSequence = envelope.Sequence
			deliveredDocker++
		case <-time.After(3 * time.Second):
			t.Fatalf("Docker FIFO queue stopped before accepted sequence %d (last=%d)", lastAccepted, previousDockerSequence)
		}
	}
	if uint64(deliveredDocker) != acceptedCount.Load() {
		t.Fatalf("Docker queue lost or duplicated accepted FIFO frames: accepted=%d delivered=%d", acceptedCount.Load(), deliveredDocker)
	}
}

func testDockerEnvelope(sequence uint64) protocol.Envelope {
	return protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker, Generation: 1,
		Sequence: sequence, Payload: json.RawMessage(`{"sequence":1}`)}
}
