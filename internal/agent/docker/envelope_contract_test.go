package docker

import (
	"encoding/json"
	"strings"
	"testing"
)

// This test-only mirror is copied from the frozen S02
// internal/protocol/version.go Envelope field names and JSON tags. It exists
// only because this isolated S04 worktree cannot import an internal package
// that is currently present in a sibling worktree. Replace it with
// protocol.Envelope after S02 is integrated; this is not final WebSocket
// framing or WSS acceptance evidence.
type s02EnvelopeMirror struct {
	Version    int             `json:"version"`
	Type       string          `json:"type"`
	Generation uint64          `json:"generation,omitempty"`
	Sequence   uint64          `json:"sequence,omitempty"`
	RequestID  string          `json:"requestId,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

const (
	s02EnvelopeCurrentVersion = 1
	s02EnvelopeMaxMessageSize = 1 << 20
	s02EnvelopeHeartbeatType  = "heartbeat"
)

func TestS04BoundedBatchFitsCurrentS02EnvelopeMirror(t *testing.T) {
	sequence := ^uint64(0)
	container := baseContainer("container-id", "", "running")
	low, high := 0, MaxBatchBytes*2
	for low < high {
		middle := low + (high-low+1)/2
		candidate := cloneContainer(container)
		candidate.Name = strings.Repeat("n", middle)
		if containerFitsBatch(candidate) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	container.Name = strings.Repeat("n", low)
	if !containerFitsBatch(container) {
		t.Fatal("test fixture does not fit the production bounded-record check")
	}
	change := snapshotChange(sequence, container)
	batch := snapshotBatch(sequence, ^uint64(0), 1_000_000, true, []Change{change}, Health{Sequence: sequence, Availability: EngineAvailable, EventsConnected: true, SnapshotFresh: true}, true)
	batchBytes, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(batchBytes) > MaxBatchBytes {
		t.Fatalf("production batch exceeds its 48 KiB budget: %d", len(batchBytes))
	}
	if len(batchBytes) < MaxBatchBytes-2048 {
		t.Fatalf("test fixture did not exercise the upper batch budget: %d bytes", len(batchBytes))
	}

	frame, err := marshalS02EnvelopeMirror(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) <= len(batchBytes) {
		t.Fatalf("test frame did not include its outer envelope: frame=%d payload=%d", len(frame), len(batchBytes))
	}
	if len(frame) > s02EnvelopeMaxMessageSize {
		t.Fatalf("48 KiB Docker batch plus the current S02 Envelope exceeds the 1 MiB frame limit: %d", len(frame))
	}
	var decoded s02EnvelopeMirror
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("encoded current S02 envelope is invalid JSON: %v", err)
	}
	if decoded.Version != s02EnvelopeCurrentVersion || decoded.Type != s02EnvelopeHeartbeatType || decoded.Generation != sequence || decoded.Sequence != sequence || len(decoded.Payload) != len(batchBytes) {
		t.Fatalf("outer envelope fields or embedded batch changed unexpectedly: envelope=%+v payload=%d", decoded, len(decoded.Payload))
	}
	t.Logf("component framing contract only: batch=%d bytes envelope=%d bytes max_frame=%d bytes; replace mirror with protocol.Envelope after S02 integration; no WSS acceptance implied", len(batchBytes), len(frame), s02EnvelopeMaxMessageSize)
}

func marshalS02EnvelopeMirror(batch Batch) ([]byte, error) {
	payload, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s02EnvelopeMirror{
		Version: s02EnvelopeCurrentVersion, Type: s02EnvelopeHeartbeatType,
		Generation: ^uint64(0), Sequence: batch.Sequence,
		RequestID: "nodedance-s04-component-test-00000001", Payload: payload,
	})
}
