package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestRequestImagePullCancellationUsesLiveMatchingJournal(t *testing.T) {
	const nodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"
	journalID := strings.Repeat("c", 64)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	connection := &agentConnection{nodeID: nodeID, generation: 17, taskEnabled: true, ctx: ctx,
		commands: make(chan protocol.Envelope, 1)}
	connection.taskJournalID = journalID
	connection.taskSynced = true
	s := &Server{agentConnections: map[string]*agentConnection{"agent": connection}}
	task := coretasks.Task{TaskID: "image-pull-17", NodeID: nodeID, DispatchJournalID: journalID,
		Intent: protocol.TaskIntent{Action: protocol.TaskImagePull}, Status: taskstate.Running}
	if err := s.requestImagePullCancellation(task); err != nil {
		t.Fatalf("live pull cancellation was rejected: %v", err)
	}
	command := <-connection.commands
	var request protocol.TaskCancelRequest
	if err := json.Unmarshal(command.Payload, &request); err != nil {
		t.Fatal("decode queued cancellation:", err)
	}
	if err := protocol.ValidateTaskCancelRequest(command, request, nodeID, journalID, connection.generation); err != nil {
		t.Fatalf("queued cancellation is not bound to the current task bridge: %v", err)
	}
	connection.taskJournalID = strings.Repeat("d", 64)
	if err := s.requestImagePullCancellation(task); err == nil {
		t.Fatal("cancellation was sent over a different Agent journal")
	}
	if err := s.requestImagePullCancellation(coretasks.Task{TaskID: task.TaskID, NodeID: nodeID,
		DispatchJournalID: journalID, Intent: protocol.TaskIntent{Action: protocol.TaskImageDelete}, Status: taskstate.Running}); err == nil {
		t.Fatal("image deletion was exposed to pull cancellation")
	}
}

func TestImageRegistryCredentialsAreOneUseAndExpireInMemory(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := &Server{imageAuth: make(map[string]pendingImageCredential), now: func() time.Time { return now }}
	s.storeImageCredentials("task-one", "node-one", protocol.RegistryCredentials{Username: "user", Password: "secret"})
	stored := s.imageAuth["task-one"]
	first := s.takeImageCredentials("task-one", "node-one")
	if first == nil || first.Username != "user" || first.Password != "secret" {
		t.Fatal("one-time credentials were not available for the matching task")
	}
	if !allBytesZero(stored.username) || !allBytesZero(stored.password) {
		t.Fatal("one-use credentials were removed from the map without clearing their Core-owned byte buffers")
	}
	first.Username, first.Password = "", ""
	if second := s.takeImageCredentials("task-one", "node-one"); second != nil {
		t.Fatal("one-time credentials were returned more than once")
	}

	s.storeImageCredentials("task-wrong-node", "node-one", protocol.RegistryCredentials{Username: "user", Password: "secret"})
	wrongNode := s.imageAuth["task-wrong-node"]
	if credentials := s.takeImageCredentials("task-wrong-node", "node-two"); credentials != nil {
		t.Fatal("credentials were returned to a different node")
	}
	if !allBytesZero(wrongNode.username) || !allBytesZero(wrongNode.password) {
		t.Fatal("wrong-node take did not clear the rejected credential buffers")
	}

	s.storeImageCredentials("task-expire", "node-one", protocol.RegistryCredentials{Username: "user", Password: "secret"})
	expiredBuffers := s.imageAuth["task-expire"]
	now = now.Add(imageCredentialLifetime)
	s.expireImageCredentials(now)
	if expired := s.takeImageCredentials("task-expire", "node-one"); expired != nil {
		t.Fatal("expired Registry credentials remained available")
	}
	if !allBytesZero(expiredBuffers.username) || !allBytesZero(expiredBuffers.password) {
		t.Fatal("expired credentials were removed without clearing their Core-owned byte buffers")
	}
}

func allBytesZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}
