package docker

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
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
	s02EnvelopeDockerType     = protocol.TypeDocker
)

func TestS04BoundedBatchFitsCurrentS02EnvelopeMirror(t *testing.T) {
	sequence := uint64(8)
	now := time.Now().UTC()
	health := Health{Sequence: sequence, Availability: EngineAvailable, EventsConnected: true, SnapshotFresh: true, ObservedAt: now}
	changes := make([]Change, 0, 7)
	for index := 0; index < 7; index++ {
		container := baseContainer(fmt.Sprintf("container-%d", index), fmt.Sprintf("node-%d", index), "running")
		container.Image = strings.Repeat("i", 1600)
		container.ImageID = strings.Repeat("d", 1600)
		container.Compose = &ComposeIdentity{
			Project: "fixture", Service: "web",
			WorkingDir: strings.Repeat("w", 1600), ConfigFiles: strings.Repeat("c", 1600),
		}
		container.Mounts = []Mount{{Type: "bind", Source: strings.Repeat("s", 1600), Destination: "/data"}}
		copy := cloneContainer(container)
		changes = append(changes, Change{
			Sequence: sequence, Action: ChangeUpsert, ContainerID: copy.ID,
			Container: &copy, ObservedAt: copy.ObservedAt,
		})
	}
	batch := snapshotBatch(sequence, 1, 0, true, changes, health, true)
	dto, err := ToProtocolBatch(batch)
	if err != nil {
		t.Fatal(err)
	}
	batchBytes, err := protocol.MarshalDockerBatch(dto)
	if err != nil {
		t.Fatal(err)
	}
	if len(batchBytes) > protocol.MaxDockerPayloadBytes {
		t.Fatalf("Docker DTO exceeds its 64 KiB payload budget: %d", len(batchBytes))
	}
	if len(batchBytes) < 50<<10 {
		t.Fatalf("test payload did not exercise the upper DTO bound: %d bytes", len(batchBytes))
	}

	frame, err := marshalS02EnvelopeMirror(sequence, json.RawMessage(batchBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(frame) <= len(batchBytes) {
		t.Fatalf("test frame did not include its outer envelope: frame=%d payload=%d", len(frame), len(batchBytes))
	}
	if len(frame) > s02EnvelopeMaxMessageSize {
		t.Fatalf("Docker payload plus the current S02 Envelope exceeds the 1 MiB frame limit: %d", len(frame))
	}
	var decoded s02EnvelopeMirror
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatalf("encoded current S02 envelope is invalid JSON: %v", err)
	}
	if decoded.Version != s02EnvelopeCurrentVersion || decoded.Type != s02EnvelopeDockerType || decoded.Generation != 2 || decoded.Sequence != sequence || len(decoded.Payload) != len(batchBytes) {
		t.Fatalf("outer envelope fields or embedded batch changed unexpectedly: envelope=%+v payload=%d", decoded, len(decoded.Payload))
	}
	t.Logf("component framing contract only: actual Docker DTO payload=%d bytes envelope=%d bytes max_frame=%d bytes; replace mirror with protocol.Envelope after S02 integration; no WSS acceptance implied", len(batchBytes), len(frame), s02EnvelopeMaxMessageSize)
}

func marshalS02EnvelopeMirror(sequence uint64, payload json.RawMessage) ([]byte, error) {
	return json.Marshal(s02EnvelopeMirror{
		Version: s02EnvelopeCurrentVersion, Type: s02EnvelopeDockerType,
		Generation: 2, Sequence: sequence,
		RequestID: "nodedance-s04-component-test-00000001", Payload: payload,
	})
}
