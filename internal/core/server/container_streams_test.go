package server

import (
	"encoding/json"
	"testing"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const coreStreamTestID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCoreContainerStreamConsumesHeartbeatAndPreservesSequence(t *testing.T) {
	stream := &coreBrowserStream{requestID: "00000000-0000-4000-8000-000000000001", nodeID: "node-test",
		containerID: coreStreamTestID, kind: protocol.StreamLogs, updates: make(chan protocol.Envelope, 4),
		done: make(chan struct{}), lastActivity: time.Now().Add(-time.Minute)}
	heartbeat := coreStreamEnvelope(protocol.TypeContainerStreamHeartbeat, 1, json.RawMessage(`{}`))
	terminal, err := stream.enqueue(heartbeat)
	if err != nil || terminal {
		t.Fatalf("valid heartbeat failed: terminal=%t err=%v", terminal, err)
	}
	if got := len(stream.updates); got != 0 {
		t.Fatalf("heartbeat was forwarded to browser: queue length=%d", got)
	}
	stream.mu.Lock()
	lastSeq, activity := stream.lastSeq, stream.lastActivity
	stream.mu.Unlock()
	if lastSeq != 1 || time.Since(activity) > time.Second {
		t.Fatalf("heartbeat did not refresh stream liveness: sequence=%d activity=%s", lastSeq, activity)
	}
	logFrame := coreStreamEnvelope(protocol.TypeContainerLog, 2,
		mustCoreStreamJSON(t, protocol.ContainerStreamLog{ContainerID: coreStreamTestID, Channel: "stdout", Data: []byte("still alive\n")}))
	if _, err := stream.enqueue(logFrame); err != nil {
		t.Fatalf("log after heartbeat rejected: %v", err)
	}
	if got := len(stream.updates); got != 1 {
		t.Fatalf("expected only the log frame in browser queue, got %d", got)
	}
	if _, err := stream.enqueue(heartbeat); err == nil {
		t.Fatal("non-monotonic heartbeat sequence accepted")
	}
}

func TestCoreContainerStreamIgnoresLateFramesAfterTerminal(t *testing.T) {
	stream := &coreBrowserStream{requestID: "00000000-0000-4000-8000-000000000001", nodeID: "node-test",
		containerID: coreStreamTestID, kind: protocol.StreamLogs, updates: make(chan protocol.Envelope, 4), done: make(chan struct{})}
	end := coreStreamEnvelope(protocol.TypeContainerStreamEnd, 1, json.RawMessage(`{}`))
	terminal, err := stream.enqueue(end)
	if err != nil || !terminal {
		t.Fatalf("terminal frame failed: terminal=%t err=%v", terminal, err)
	}
	late := coreStreamEnvelope(protocol.TypeContainerStreamHeartbeat, 2, json.RawMessage(`{}`))
	terminal, err = stream.enqueue(late)
	if err != nil || !terminal {
		t.Fatalf("late frame after terminal was not ignored: terminal=%t err=%v", terminal, err)
	}
	if got := len(stream.updates); got != 1 {
		t.Fatalf("late frame was forwarded after terminal: queue length=%d", got)
	}
}

func TestContainerStreamRequiresFreshDockerSnapshot(t *testing.T) {
	available := coredocker.View{AgentOnline: true, DockerAvailability: protocol.DockerAvailabilityAvailable, DockerSnapshotFresh: true}
	if !containerStreamDockerViewAvailable(available) {
		t.Fatal("fresh online Docker view was rejected")
	}
	for name, mutate := range map[string]func(*coredocker.View){
		"agent-offline":        func(view *coredocker.View) { view.AgentOnline = false },
		"data-stale":           func(view *coredocker.View) { view.DataStale = true },
		"snapshot-stale":       func(view *coredocker.View) { view.DockerSnapshotFresh = false },
		"docker-unavailable":   func(view *coredocker.View) { view.DockerAvailability = protocol.DockerAvailabilityUnavailable },
		"docker-unknown-state": func(view *coredocker.View) { view.DockerAvailability = protocol.DockerAvailabilityUnknown },
	} {
		t.Run(name, func(t *testing.T) {
			view := available
			mutate(&view)
			if containerStreamDockerViewAvailable(view) {
				t.Fatalf("unavailable Docker view was authorized: %+v", view)
			}
		})
	}
}

func coreStreamEnvelope(kind string, sequence uint64, payload json.RawMessage) protocol.Envelope {
	return protocol.Envelope{Version: protocol.CurrentVersion, Type: kind, Generation: 1, Sequence: sequence,
		RequestID: "00000000-0000-4000-8000-000000000001", Payload: payload}
}

func mustCoreStreamJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
