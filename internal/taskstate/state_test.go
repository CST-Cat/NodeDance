package taskstate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalJSONSortsKeysAndPreservesNumbers(t *testing.T) {
	canonical, err := CanonicalJSON([]byte(`{"b":1,"a":2,"c":[3,1.0]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != `{"a":2,"b":1,"c":[3,1.0]}` {
		t.Fatalf("canonical form changed key order or number spelling: %s", canonical)
	}
}

func TestCanonicalJSONRejectsInvalidBodies(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"a":1,"a":2}`),
		[]byte(`{"a":1} {"b":2}`),
		[]byte(`{"a":1}trailing`),
		[]byte("\xff\xfe"),
		[]byte(strings.Repeat("[", 130) + strings.Repeat("]", 130)),
		[]byte(`1e10001`),
		[]byte(`1e1234567`),
		[]byte(`1.` + strings.Repeat("1", 96)),
		[]byte(strings.Repeat("1", 129)),
		[]byte(strings.Repeat("x", (64<<10)+1)),
		[]byte(""),
	} {
		if _, err := CanonicalJSON(raw); err == nil {
			t.Fatalf("invalid body was accepted: %.40q", raw)
		}
	}
	for _, raw := range []string{`1e10000`, `1e-10000`, `1.` + strings.Repeat("1", 95), strings.Repeat("9", 96), strings.Repeat("[", 129) + strings.Repeat("]", 129)} {
		if _, err := CanonicalJSON([]byte(raw)); err != nil {
			t.Fatalf("bounded value was rejected: %s: %v", raw, err)
		}
	}
}

func TestRequestDigestExcludesIdempotencyKeyButIncludesContent(t *testing.T) {
	base := Identity{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "key-1", TargetID: "target-1", ResourceKey: "resource-1", Action: "file_write", Payload: json.RawMessage(`{"path":"/a","size":1}`)}
	first, err := RequestDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	keyed := base
	keyed.IdempotencyKey = "key-2"
	second, err := RequestDigest(keyed)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("idempotency key changed the deduplication digest")
	}
	spelled := base
	spelled.Payload = json.RawMessage(`{"path":"/a","size":1.0}`)
	third, err := RequestDigest(spelled)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("distinct numeric spellings produced one digest")
	}
	renamed := base
	renamed.ResourceKey = "resource-2"
	fourth, err := RequestDigest(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if first == fourth {
		t.Fatal("resource key did not affect the digest")
	}
}

func TestRequestDigestRejectsInvalidIdentities(t *testing.T) {
	base := Identity{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "key-1", TargetID: "target-1", ResourceKey: "resource-1", Action: "file_write", Payload: json.RawMessage(`{}`)}
	if _, err := RequestDigest(base); err != nil {
		t.Fatalf("baseline identity rejected: %v", err)
	}
	invalid := []func(*Identity){
		func(i *Identity) { i.TaskID = "" },
		func(i *Identity) { i.TaskID = "task\t1" },
		func(i *Identity) { i.NodeID = strings.Repeat("n", 257) },
		func(i *Identity) { i.IdempotencyKey = "key!" },
		func(i *Identity) { i.IdempotencyKey = "" },
		func(i *Identity) { i.Action = "File_Write" },
		func(i *Identity) { i.Action = "-lead" },
		func(i *Identity) { i.Action = "" },
		func(i *Identity) { i.Payload = json.RawMessage(`{"a":1,"a":2}`) },
	}
	for _, mutate := range invalid {
		broken := base
		mutate(&broken)
		if _, err := RequestDigest(broken); err == nil {
			t.Fatalf("invalid identity was accepted: %+v", broken)
		}
	}
}

func TestRequestDigestValidatesContainerCreateIdentity(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := Identity{TaskID: "task-1", NodeID: "node-1", IdempotencyKey: "key-1",
		TargetID: "container-create:" + digest, ResourceKey: "docker-container-create:" + digest,
		Action: "container_create", Payload: json.RawMessage(`{"create_sha256":"` + digest + `"}`)}
	if _, err := RequestDigest(valid); err != nil {
		t.Fatalf("valid container create identity rejected: %v", err)
	}
	mismatched := valid
	mismatched.Payload = json.RawMessage(`{"create_sha256":"` + strings.Repeat("b", 64) + `"}`)
	if _, err := RequestDigest(mismatched); err == nil {
		t.Fatal("container create payload digest mismatch was accepted")
	}
	unprefixed := valid
	unprefixed.TargetID = digest
	if _, err := RequestDigest(unprefixed); err == nil {
		t.Fatal("container create identity without target prefix was accepted")
	}
}

func TestCanTransitionMatrix(t *testing.T) {
	full := Evidence{ExecutionAttempted: true, ExecutionCompleted: true, FailureConfirmed: true,
		PostconditionVerified: true, ProcessTerminated: true, ActualResultConfirmed: true, CancellationConfirmed: true}
	if err := CanTransition(Queued, Running, Evidence{ExecutionAttempted: true}); err != nil {
		t.Fatalf("queued to running: %v", err)
	}
	if err := CanTransition(Queued, Running, Evidence{}); err == nil {
		t.Fatal("running without durable execution intent was accepted")
	}
	if err := CanTransition(Running, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true}); err != nil {
		t.Fatalf("running to succeeded: %v", err)
	}
	if err := CanTransition(Running, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true}); err == nil {
		t.Fatal("succeeded without verified postcondition was accepted")
	}
	if err := CanTransition(Queued, Canceled, Evidence{CancellationConfirmed: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("queued to canceled without execution is valid: %v", err)
	}
	if err := CanTransition(Running, TimedOut, Evidence{ExecutionAttempted: true, ProcessTerminated: true, ActualResultConfirmed: true}); err != nil {
		t.Fatalf("running to timed_out: %v", err)
	}
	if err := CanTransition(Queued, Unknown, Evidence{}); err == nil {
		t.Fatal("queued to unknown without committed delivery was accepted")
	}
	if err := CanTransition(Queued, Unknown, Evidence{DeliveryCommitted: true}); err != nil {
		t.Fatalf("queued to unknown with committed delivery: %v", err)
	}
	if err := CanTransition(Running, Unknown, Evidence{}); err != nil {
		t.Fatalf("running to unknown: %v", err)
	}
	if err := CanTransition(Unknown, Succeeded, Evidence{ExecutionAttempted: true, ExecutionCompleted: true, PostconditionVerified: true}); err != nil {
		t.Fatalf("unknown to succeeded: %v", err)
	}
	for _, invalid := range [][2]Status{
		{Succeeded, Failed}, {Failed, Canceled}, {TimedOut, Succeeded}, {Canceled, Unknown},
		{Queued, Queued}, {Queued, Succeeded}, {Running, Queued}, {Unknown, Queued}, {Unknown, Running},
		{Status("bogus"), Queued}, {Queued, Status("bogus")},
	} {
		if err := CanTransition(invalid[0], invalid[1], full); err == nil {
			t.Fatalf("invalid transition %s -> %s was accepted", invalid[0], invalid[1])
		}
	}
}
