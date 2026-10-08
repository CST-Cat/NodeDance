package taskstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func validIdentity() Identity {
	return Identity{
		TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "idem-1",
		TargetID: "container-1", ResourceKey: "node-1/container-1", Action: "restart",
		Payload: json.RawMessage(`{"force":false,"timeout":5}`),
	}
}

func TestRequestDigestCanonicalAndScoped(t *testing.T) {
	first := validIdentity()
	firstDigest, err := RequestDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.TaskID = "different-task"
	second.IdempotencyKey = "different-key"
	second.Payload = json.RawMessage(" {\"timeout\":5, \"force\":false} ")
	secondDigest, err := RequestDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest {
		t.Fatal("JSON key order, whitespace, task ID, or idempotency key changed request digest")
	}
	second.Payload = json.RawMessage(`{"force":false,"timeout":5.0}`)
	decimalDigest, err := RequestDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == decimalDigest {
		t.Fatal("integer and decimal wire spellings must remain distinct")
	}

	second.Action = "stop"
	second.Payload = json.RawMessage(`{"force":false,"timeout":5}`)
	different, err := RequestDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == different {
		t.Fatal("different action has the same request digest")
	}
}

func TestCanonicalJSONRejectsAmbiguousPayloads(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"outer":{"a":1,"a":2}}`,
		`{} {}`,
		``,
		`{"n":1e10001}`,
		`{"n":` + string(bytes.Repeat([]byte{'9'}, MaxNumberBytes+1)) + `}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := CanonicalJSON([]byte(raw)); err == nil {
				t.Fatalf("CanonicalJSON(%q) unexpectedly succeeded", raw)
			}
		})
	}
}

func TestCanonicalJSONPreservesExactNumbers(t *testing.T) {
	first, err := CanonicalJSON([]byte(`{"n":1.2500e+3,"big":123456789012345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalJSON([]byte(` { "big" : 123456789012345678901234567890, "n" : 1.2500e+3 } `))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("integer canonical forms differ: %s != %s", first, second)
	}
	other, err := CanonicalJSON([]byte(`{"n":1250,"big":123456789012345678901234567890}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, other) {
		t.Fatalf("distinct valid numeric spellings unexpectedly merged: %s", first)
	}
}

func TestTaskStateTransitionProofs(t *testing.T) {
	if err := CanTransition(Queued, Running, Evidence{ExecutionAttempted: true}); err != nil {
		t.Fatalf("queued to running: %v", err)
	}
	if err := CanTransition(Queued, Succeeded, Evidence{ExecutionCompleted: true, PostconditionVerified: true}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("queued directly to succeeded = %v, want invalid transition", err)
	}
	if err := CanTransition(Running, Succeeded, Evidence{ExecutionCompleted: true}); err == nil {
		t.Fatal("execution completion without a verified postcondition succeeded")
	}
	if err := CanTransition(Running, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true}); err != nil {
		t.Fatalf("verified success: %v", err)
	}
	if err := CanTransition(Running, TimedOut, Evidence{ExecutionAttempted: true, ActualResultConfirmed: true}); err == nil {
		t.Fatal("timed_out without termination proof succeeded")
	}
	if err := CanTransition(Running, TimedOut, Evidence{ExecutionAttempted: true, ProcessTerminated: true}); err == nil {
		t.Fatal("timed_out without actual-result proof succeeded")
	}
	if err := CanTransition(Running, TimedOut, Evidence{ExecutionAttempted: true, ProcessTerminated: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("verified timeout: %v", err)
	}
	if err := CanTransition(Running, Failed, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, FailureConfirmed: true}); err == nil {
		t.Fatal("failed without confirmed actual result succeeded")
	}
	if err := CanTransition(Running, Failed, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, FailureConfirmed: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("verified failure: %v", err)
	}
	if err := CanTransition(Running, Canceled, Evidence{CancellationConfirmed: true, ActualResultConfirmed: true}); err == nil {
		t.Fatal("canceled running execution without termination proof succeeded")
	}
	if err := CanTransition(Running, Canceled, Evidence{ExecutionAttempted: true, CancellationConfirmed: true, ProcessTerminated: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("verified cancellation: %v", err)
	}
	if err := CanTransition(Running, Unknown, Evidence{}); err != nil {
		t.Fatalf("running to unknown: %v", err)
	}
	if err := CanTransition(Queued, Unknown, Evidence{}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("queued without committed delivery became unknown: %v", err)
	}
	if err := CanTransition(Queued, Unknown, Evidence{DeliveryCommitted: true}); err != nil {
		t.Fatalf("durably delivered queued task did not become unknown: %v", err)
	}
	if err := CanTransition(Unknown, Running, Evidence{DeliveryCommitted: true, ExecutionAttempted: true}); err == nil {
		t.Fatal("unknown task was replayed into running after committed delivery")
	}
	if err := CanTransition(Unknown, Succeeded, Evidence{DeliveryCommitted: true, PostconditionVerified: true}); err == nil {
		t.Fatal("committed delivery was mistaken for execution-completed proof")
	}
	if err := CanTransition(Unknown, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true}); err != nil {
		t.Fatalf("verified outcome after queued delivery: %v", err)
	}
	if err := CanTransition(Queued, Failed, Evidence{FailureConfirmed: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("confirmed failure before executor: %v", err)
	}
	if err := CanTransition(Queued, Canceled, Evidence{CancellationConfirmed: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("confirmed cancellation before executor: %v", err)
	}
	if err := CanTransition(Unknown, Running, Evidence{ExecutionAttempted: true}); err == nil {
		t.Fatal("unknown task was replayed into running")
	}
	if err := CanTransition(Unknown, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true}); err != nil {
		t.Fatalf("confirmed recovery to succeeded: %v", err)
	}
	if HoldsResource(Unknown) != true || HoldsResource(Queued) != true || HoldsResource(Running) != true {
		t.Fatal("queued/running/unknown tasks must hold their resource")
	}
	for _, status := range []Status{Succeeded, Failed, TimedOut, Canceled} {
		if HoldsResource(status) {
			t.Errorf("terminal status %q must release its resource", status)
		}
	}
}
