package update

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLocalUpdateNeedsNoArgumentsAndWaitsForLatestRelease(t *testing.T) {
	for _, component := range []string{"core", "agent"} {
		t.Run(component, func(t *testing.T) {
			var calls []RPC
			now := time.Now().UTC()
			id := strings.Repeat("a", 32)
			invoke := func(ctx context.Context, component, data string, rpc RPC) (Response, error) {
				calls = append(calls, rpc)
				if rpc.Request.Component != component || data != "/private-product" {
					t.Fatal("local target changed")
				}
				var value any
				switch rpc.Action {
				case "check":
					t.Fatal("manual update performed a redundant preflight")
				case "apply":
					if rpc.Request.Version != "" || !rpc.Request.Confirm || len(rpc.IdempotencyKey) != 32 {
						t.Fatalf("default submission: %+v", rpc)
					}
					value = Job{ID: id, Status: "queued"}
				case "status":
					if rpc.Request.JobID != id {
						t.Fatal("default command did not observe accepted job")
					}
					value = Job{ID: id, Status: "succeeded", Finished: &now}
				default:
					t.Fatalf("unexpected action %s", rpc.Action)
				}
				raw, _ := json.Marshal(value)
				return Response{Status: 200, Data: raw}, nil
			}
			var stdout, stderr bytes.Buffer
			if err := local(context.Background(), component, "/private-product", nil, strings.NewReader(""), &stdout, &stderr, invoke); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 2 || !strings.Contains(stdout.String(), `"succeeded"`) || !strings.Contains(stderr.String(), "Idempotency-Key:") {
				t.Fatalf("default workflow: %v %s %s", calls, stdout.String(), stderr.String())
			}
		})
	}
}
func TestLocalRollbackCancellationDoesNotLookUpOrApplyRelease(t *testing.T) {
	called := false
	invoke := func(context.Context, string, string, RPC) (Response, error) { called = true; return Response{}, nil }
	err := local(context.Background(), "agent", "/private-agent", []string{"--rollback", strings.Repeat("a", 32)}, strings.NewReader("n\n"), io.Discard, io.Discard, invoke)
	if err == nil || called {
		t.Fatal("cancelled rollback performed a request")
	}
}

func TestLocalUpdateChannelSelectsTheSharedRequest(t *testing.T) {
	for _, entry := range []struct{ component, channel string }{{"core", "beta"}, {"agent", "dev"}} {
		called := false
		invoke := func(_ context.Context, _ string, _ string, rpc RPC) (Response, error) {
			called = true
			if rpc.Action != "check" || rpc.Request.Component != entry.component || rpc.Request.Version != entry.channel {
				t.Fatalf("wrong channel request: %+v", rpc)
			}
			return Response{Status: 200, Data: json.RawMessage(`{}`)}, nil
		}
		if err := local(context.Background(), entry.component, "/unused", []string{"--channel", entry.channel, "--check"}, strings.NewReader(""), io.Discard, io.Discard, invoke); err != nil || !called {
			t.Fatal(entry, err)
		}
		called = false
		if err := local(context.Background(), entry.component, "/unused", []string{"--channel", entry.channel, "--version", "other", "--check"}, strings.NewReader(""), io.Discard, io.Discard, invoke); err == nil || called {
			t.Fatal("conflicting channel/version reached the executor")
		}
	}
}
