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
	var calls []RPC
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	invoke := func(ctx context.Context, component, data string, rpc RPC) (Response, error) {
		calls = append(calls, rpc)
		if component != "agent" || data != "/private-agent" {
			t.Fatal("local target changed")
		}
		var value any
		switch rpc.Action {
		case "check":
			value = Check{Component: "agent", Configured: true, Available: true, Current: "agent-v0.1.13", Latest: "agent-v0.1.14"}
		case "apply":
			if rpc.Request.Version != "agent-v0.1.14" || !rpc.Request.Confirm || len(rpc.IdempotencyKey) != 32 {
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
	if err := local(context.Background(), "agent", "/private-agent", nil, strings.NewReader(""), &stdout, &stderr, invoke); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || !strings.Contains(stdout.String(), `"succeeded"`) || !strings.Contains(stderr.String(), "Idempotency-Key:") {
		t.Fatalf("default workflow: %v %s %s", calls, stdout.String(), stderr.String())
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
