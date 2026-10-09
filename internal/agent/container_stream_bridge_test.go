package agent

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containerstreams"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const streamBridgeTestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestContainerStreamBridgeDecodesMuxedLogsAndTTYRawUnicode(t *testing.T) {
	for _, test := range []struct {
		name string
		tty  bool
		body []byte
		want map[string]string
	}{
		{name: "stdout-stderr", body: append(muxedStreamFrame(1, []byte("stdout: 你好\n")), muxedStreamFrame(2, []byte("stderr: ошибка\n"))...), want: map[string]string{"stdout": "stdout: 你好\n", "stderr": "stderr: ошибка\n"}},
		{name: "tty-raw", tty: true, body: []byte("tty: raw 日志\n"), want: map[string]string{"stdout": "tty: raw 日志\n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := &bridgeTestEngine{id: streamBridgeTestID, tty: test.tty, logs: test.body}
			bridge, err := newContainerStreamBridge(engine, nil)
			if err != nil {
				t.Fatal(err)
			}
			writer := &bridgeTestWriter{messages: make(chan protocol.Envelope, 32)}
			ctx, cancel := context.WithCancel(context.Background())
			commands := make(chan protocol.Envelope, 2)
			done := make(chan error, 1)
			go func() { done <- bridge.run(ctx, writer, commands, 1) }()
			commands <- streamOpenEnvelope(t, streamBridgeTestID, protocol.StreamLogs, "20", true, "00000000-0000-4000-8000-000000000001")
			seen := map[string]string{}
			ready := false
			deadline := time.After(3 * time.Second)
			for {
				select {
				case envelope := <-writer.messages:
					switch envelope.Type {
					case protocol.TypeContainerStreamReady:
						ready = true
					case protocol.TypeContainerLog:
						var frame protocol.ContainerStreamLog
						if err := json.Unmarshal(envelope.Payload, &frame); err != nil {
							t.Fatal(err)
						}
						seen[frame.Channel] += string(frame.Data)
					case protocol.TypeContainerStreamEnd:
						for channel, want := range test.want {
							if seen[channel] != want {
								t.Fatalf("%s bytes=%q want=%q", channel, seen[channel], want)
							}
						}
						if !ready {
							t.Fatal("stream ended before the browser-ready frame")
						}
						cancel()
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Fatal("Agent bridge did not stop after end of history")
						}
						return
					}
				case <-deadline:
					t.Fatalf("timed out waiting for complete stream; ready=%t frames=%v", ready, seen)
				}
			}
		})
	}
}

func TestContainerStreamBridgeBoundsHighVolumeLogsAndCancelsReader(t *testing.T) {
	largeText := strings.Repeat("大量日志 🚀\n", 12000)
	engine := &bridgeTestEngine{id: streamBridgeTestID, logs: muxedStreamFrame(1, []byte(largeText))}
	bridge, err := newContainerStreamBridge(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := &bridgeTestWriter{messages: make(chan protocol.Envelope, 512)}
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan protocol.Envelope, 2)
	done := make(chan error, 1)
	go func() { done <- bridge.run(ctx, writer, commands, 1) }()
	commands <- streamOpenEnvelope(t, streamBridgeTestID, protocol.StreamLogs, "all", true, "00000000-0000-4000-8000-000000000002")
	var total int
	deadline := time.After(5 * time.Second)
	for total < len(largeText) {
		select {
		case envelope := <-writer.messages:
			if envelope.Type != protocol.TypeContainerLog {
				continue
			}
			var frame protocol.ContainerStreamLog
			if err := json.Unmarshal(envelope.Payload, &frame); err != nil {
				t.Fatal(err)
			}
			if len(frame.Data) > containerstreams.LogChunkBytes {
				t.Fatalf("log chunk has %d bytes; limit is %d", len(frame.Data), containerstreams.LogChunkBytes)
			}
			total += len(frame.Data)
		case <-deadline:
			t.Fatalf("large log stream stalled: received %d of %d bytes", total, len(largeText))
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Agent bridge did not close its reader after connection cancellation")
	}
}

func TestContainerStreamBridgeSharesOneStatsReaderAndUnsubscribesLastClient(t *testing.T) {
	observedAt := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	wire, err := encodeStreamStats(containerstreams.StatsSnapshot{ContainerID: streamBridgeTestID, ObservedAt: &observedAt,
		CPUPercent: containerstreams.Metric[float64]{State: containerstreams.MetricUnknown, Reason: "warming_up"},
		Memory:     containerstreams.Metric[containerstreams.MemoryUsage]{State: containerstreams.MetricUnknown, Reason: "unavailable"},
		Network:    containerstreams.Metric[containerstreams.NetworkRate]{State: containerstreams.MetricUnknown, Reason: "warming_up"},
		BlockIO:    containerstreams.Metric[containerstreams.BlockIORate]{State: containerstreams.MetricUnknown, Reason: "warming_up"}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateContainerStreamEnvelope(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStats,
		Generation: 1, Sequence: 1, RequestID: "00000000-0000-4000-8000-000000000003", Payload: encoded}, 1); err != nil {
		t.Fatalf("valid stats protocol payload rejected: %v; payload=%s", err, encoded)
	}
	engine := &bridgeTestEngine{id: streamBridgeTestID}
	bridge, err := newContainerStreamBridge(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := &bridgeTestWriter{messages: make(chan protocol.Envelope, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan protocol.Envelope, 4)
	done := make(chan error, 1)
	go func() { done <- bridge.run(ctx, writer, commands, 1) }()
	first := "00000000-0000-4000-8000-000000000003"
	second := "00000000-0000-4000-8000-000000000004"
	commands <- streamOpenEnvelope(t, streamBridgeTestID, protocol.StreamStats, "", false, first)
	commands <- streamOpenEnvelope(t, streamBridgeTestID, protocol.StreamStats, "", false, second)
	waitForStreamFrames(t, writer.messages, map[string]string{first: protocol.TypeContainerStreamReady, second: protocol.TypeContainerStreamReady})
	if got := waitAtomicCount(t, &engine.statsOpens, 1); got != 1 {
		t.Fatalf("Engine opened %d stats streams for two clients, want one", got)
	}
	engine.writeStats(t, `{"id":"`+streamBridgeTestID+`","read":"2026-10-08T09:00:00Z"}`+"\n")
	waitForStreamFrames(t, writer.messages, map[string]string{first: protocol.TypeContainerStats, second: protocol.TypeContainerStats})
	commands <- streamCloseEnvelope(first, 1)
	engine.writeStats(t, `{"id":"`+streamBridgeTestID+`","read":"2026-10-08T09:00:01Z"}`+"\n")
	waitForStreamFrames(t, writer.messages, map[string]string{second: protocol.TypeContainerStats})
	if got := engine.statsOpens.Load(); got != 1 {
		t.Fatalf("Engine opened %d stats streams after one subscriber left, want one", got)
	}
	commands <- streamCloseEnvelope(second, 1)
	waitAtomicCount(t, &engine.statsCloses, 1)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent stats bridge did not stop")
	}
}

func TestContainerStreamBridgeKeepsLongLivedStreamAliveWithHeartbeats(t *testing.T) {
	engine := &bridgeTestEngine{id: streamBridgeTestID}
	bridge, err := newContainerStreamBridge(engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer := &bridgeTestWriter{messages: make(chan protocol.Envelope, 32)}
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan protocol.Envelope, 2)
	done := make(chan error, 1)
	go func() { done <- bridge.run(ctx, writer, commands, 1) }()
	requestID := "00000000-0000-4000-8000-000000000005"
	commands <- streamOpenEnvelope(t, streamBridgeTestID, protocol.StreamStats, "", false, requestID)
	select {
	case envelope := <-writer.messages:
		if envelope.Type != protocol.TypeContainerStreamReady || envelope.RequestID != requestID {
			t.Fatalf("unexpected initial frame: type=%s request=%s", envelope.Type, envelope.RequestID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("long-lived stats stream did not become ready")
	}
	heartbeatDeadline := time.NewTimer(12 * time.Second)
	defer heartbeatDeadline.Stop()
	for {
		select {
		case envelope := <-writer.messages:
			if envelope.Type != protocol.TypeContainerStreamHeartbeat {
				t.Fatalf("unexpected frame before first heartbeat: %s", envelope.Type)
			}
			if err := protocol.ValidateContainerStreamEnvelope(envelope, 1); err != nil {
				t.Fatalf("Agent heartbeat frame is invalid: %v", err)
			}
			if !containsStream(bridge, requestID) {
				t.Fatal("Agent removed the active stream before its heartbeat")
			}
			goto heartbeatReceived
		case <-heartbeatDeadline.C:
			t.Fatal("Agent did not send an active-stream heartbeat within 12 seconds")
		}
	}

heartbeatReceived:
	engine.writeStats(t, `{"id":"`+streamBridgeTestID+`","read":"2026-10-08T09:00:00Z"}`+"\n")
	gotStats := false
	statsDeadline := time.NewTimer(3 * time.Second)
	defer statsDeadline.Stop()
	for !gotStats {
		select {
		case envelope := <-writer.messages:
			if envelope.Type == protocol.TypeContainerStats {
				gotStats = true
			}
		case <-statsDeadline.C:
			t.Fatal("active stream stopped delivering Docker stats after its heartbeat")
		}
	}
	commands <- streamCloseEnvelope(requestID, 1)
	waitAtomicCount(t, &engine.statsCloses, 1)
	if containsStream(bridge, requestID) {
		t.Fatal("closed stream remained registered in the Agent bridge")
	}
	select {
	case err := <-done:
		t.Fatalf("closing a stream terminated the Agent connection bridge: %v", err)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent stream bridge did not stop after test cancellation")
	}
}

func containsStream(bridge *containerStreamBridge, requestID string) bool {
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	return bridge.streams[requestID] != nil
}

func streamOpenEnvelope(t *testing.T, id, kind, tail string, follow bool, requestID string) protocol.Envelope {
	t.Helper()
	request := protocol.ContainerStreamOpen{Kind: kind, ContainerID: id, Tail: tail, Follow: follow}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamOpen, Generation: 1,
		RequestID: requestID, Payload: encoded}
}

func streamCloseEnvelope(requestID string, generation uint64) protocol.Envelope {
	return protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerStreamClose, Generation: generation,
		RequestID: requestID, Payload: json.RawMessage(`{}`)}
}

func waitForStreamFrames(t *testing.T, messages <-chan protocol.Envelope, expected map[string]string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	remaining := make(map[string]string, len(expected))
	var observed []string
	for requestID, kind := range expected {
		remaining[requestID] = kind
	}
	for len(remaining) > 0 {
		select {
		case envelope := <-messages:
			observed = append(observed, envelope.RequestID+":"+envelope.Type)
			if envelope.Type == protocol.TypeContainerStreamError {
				var streamError protocol.ContainerStreamError
				_ = json.Unmarshal(envelope.Payload, &streamError)
				t.Fatalf("container stream failed: request=%s code=%s observed=%v", envelope.RequestID, streamError.Code, observed)
			}
			if remaining[envelope.RequestID] == envelope.Type {
				delete(remaining, envelope.RequestID)
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for stream frames: remaining=%v observed=%v", remaining, observed)
		}
	}
}

func waitAtomicCount(t *testing.T, value *atomic.Int64, want int64) int64 {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := value.Load(); got == want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	got := value.Load()
	t.Fatalf("counter=%d want=%d", got, want)
	return got
}

func muxedStreamFrame(channel byte, payload []byte) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = channel
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

type bridgeTestWriter struct {
	messages chan protocol.Envelope
}

func (w *bridgeTestWriter) offerStream(ctx context.Context, envelope protocol.Envelope) error {
	select {
	case w.messages <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errStreamWriterQueueFull
	}
}

func (w *bridgeTestWriter) send(ctx context.Context, envelope protocol.Envelope) error {
	select {
	case w.messages <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *bridgeTestWriter) sendStreamTerminal(ctx context.Context, envelope protocol.Envelope) error {
	return w.send(ctx, envelope)
}

type bridgeTestEngine struct {
	id          string
	tty         bool
	logs        []byte
	statsOpens  atomic.Int64
	statsCloses atomic.Int64
	statsMu     sync.Mutex
	statsWrite  *io.PipeWriter
}

func (e *bridgeTestEngine) Inspect(_ context.Context, id string) (containerstreams.ContainerInfo, error) {
	if id != e.id {
		return containerstreams.ContainerInfo{}, errors.New("wrong container")
	}
	return containerstreams.ContainerInfo{ID: e.id, TTY: e.tty}, nil
}

func (e *bridgeTestEngine) OpenLogs(_ context.Context, id string, _ containerstreams.LogsOptions) (io.ReadCloser, error) {
	if id != e.id {
		return nil, errors.New("wrong container")
	}
	return io.NopCloser(bytes.NewReader(e.logs)), nil
}

func (e *bridgeTestEngine) OpenStats(ctx context.Context, id string) (io.ReadCloser, error) {
	if id != e.id {
		return nil, errors.New("wrong container")
	}
	e.statsOpens.Add(1)
	reader, writer := io.Pipe()
	e.statsMu.Lock()
	e.statsWrite = writer
	e.statsMu.Unlock()
	return &bridgeTestStatsBody{PipeReader: reader, onClose: func() { e.statsCloses.Add(1) }}, nil
}

func (e *bridgeTestEngine) writeStats(t *testing.T, record string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.statsMu.Lock()
		writer := e.statsWrite
		e.statsMu.Unlock()
		if writer != nil {
			if _, err := io.WriteString(writer, record); err != nil {
				t.Fatalf("write stats fixture: %v", err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stats Engine reader did not start")
}

type bridgeTestStatsBody struct {
	*io.PipeReader
	onClose func()
	once    sync.Once
}

func (body *bridgeTestStatsBody) Close() error {
	var err error
	body.once.Do(func() {
		err = body.PipeReader.Close()
		body.onClose()
	})
	return err
}
