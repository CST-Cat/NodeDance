package probes

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type probeWriterFunc func(context.Context, protocol.Envelope) error

func (f probeWriterFunc) Send(ctx context.Context, envelope protocol.Envelope) error {
	return f(ctx, envelope)
}

func probeDispatchEnvelope() protocol.Envelope {
	dispatch := protocol.ProbeDispatch{
		ProbeID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		RunID:   "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", NodeID: "node",
		Kind: protocol.ProbeTCP, Target: "tcp://127.0.0.1:12345",
		IntervalSeconds: 60, TimeoutSeconds: 30,
	}
	payload, _ := json.Marshal(dispatch)
	return protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeProbeDispatch,
		Generation: 1, RequestID: dispatch.RunID, Payload: payload}
}

func TestBridgeCancelsWorkersBeforeReturning(t *testing.T) {
	for _, reason := range []string{"closed input", "invalid dispatch", "writer failure", "parent cancellation", "cancellation during write"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{}, MaxConcurrent)
			stopped := make(chan struct{}, MaxConcurrent)
			executor := NewExecutor()
			executor.dialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				started <- struct{}{}
				<-ctx.Done()
				stopped <- struct{}{}
				return nil, ctx.Err()
			}
			bridge := NewBridge(executor)
			incoming := make(chan protocol.Envelope)
			writerErr := errors.New("writer failed")
			writer := probeWriterFunc(func(context.Context, protocol.Envelope) error {
				if reason == "cancellation during write" {
					cancel()
				}
				return writerErr
			})
			done := make(chan error, 1)
			go func() { done <- bridge.Run(ctx, writer, 1, "node", incoming) }()
			for range MaxConcurrent {
				incoming <- probeDispatchEnvelope()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("probe did not start")
				}
			}
			switch reason {
			case "closed input":
				close(incoming)
			case "invalid dispatch":
				incoming <- protocol.Envelope{}
			case "writer failure", "cancellation during write":
				// A saturated bridge reports capacity through the same writer.
				incoming <- probeDispatchEnvelope()
			case "parent cancellation":
				cancel()
			}
			select {
			case err := <-done:
				canceled := reason == "parent cancellation" || reason == "cancellation during write"
				if canceled && err != nil || !canceled && err == nil {
					t.Fatalf("unexpected bridge result: %v", err)
				}
				if reason == "writer failure" && !errors.Is(err, writerErr) {
					t.Fatalf("writer error lost: %v", err)
				}
			case <-time.After(time.Second):
				cancel()
				<-done
				t.Fatal("bridge waited for probe deadlines instead of canceling its workers")
			}
			if len(stopped) != MaxConcurrent || len(bridge.Slots) != 0 {
				t.Fatal("bridge returned before all workers released their resources")
			}
		})
	}
}
