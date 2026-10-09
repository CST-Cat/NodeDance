package server

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coreupdates "github.com/CST-Cat/NodeDance/internal/core/updates"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAgentUpdateCampaignFiltersNodeArchitecture(t *testing.T) {
	nodes := []agents.Node{
		{Identity: agents.Identity{NodeID: "amd64", Status: "online"}, Capabilities: []string{protocol.CapabilityAgentUpdatesPreparedAck}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
		{Identity: agents.Identity{NodeID: "arm64", Status: "online"}, Capabilities: []string{protocol.CapabilityAgentUpdatesPreparedAck}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "arm64"}},
		{Identity: agents.Identity{NodeID: "offline", Status: "offline"}, Capabilities: []string{protocol.CapabilityAgentUpdatesPreparedAck}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
		{Identity: agents.Identity{NodeID: "legacy", Status: "online"}, Capabilities: []string{protocol.CapabilityAgentUpdates}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
	}
	release := coreupdates.Release{OS: "linux", Architecture: "arm64"}
	if got := compatibleAgentUpdateTargets(nodes, release); !reflect.DeepEqual(got, []string{"arm64"}) {
		t.Fatalf("compatible rollout targets = %v, want only the online arm64 updater", got)
	}
}

func TestLegacyUpdateCapabilityDoesNotSchedulePreparedAckProtocol(t *testing.T) {
	legacy := []string{protocol.CapabilityAgentUpdates}
	if got := negotiateCapabilities(legacy); hasCapability(got, protocol.CapabilityAgentUpdatesPreparedAck) || hasCapability(got, protocol.CapabilityAgentUpdates) {
		t.Fatalf("legacy Core capability negotiated as prepared-ACK updates: %v", got)
	}
	nodes := []agents.Node{{
		Identity:     agents.Identity{NodeID: "old-core-agent", Status: "online"},
		Capabilities: legacy, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"},
	}}
	if got := compatibleAgentUpdateTargets(nodes, coreupdates.Release{OS: "linux", Architecture: "amd64"}); len(got) != 0 {
		t.Fatalf("legacy updater was scheduled without ACK support: %v", got)
	}
}

func TestStagedTaskIsNotRedispatchedDuringCoreReconciliation(t *testing.T) {
	const taskID = "5f29b72e-2613-4f44-8c88-3754fca3a124"
	connection := &agentConnection{
		nodeID: "node", stagedUpdateTaskID: taskID, updateEnabled: true,
		commands: make(chan protocol.Envelope, 1),
	}
	server := &Server{agentConnections: map[string]*agentConnection{"agent": connection}}
	server.dispatchAgentUpdate(context.Background(), coreupdates.Task{ID: taskID, NodeID: "node"})
	select {
	case command := <-connection.commands:
		t.Fatalf("staged task was dispatched again: %#v", command)
	default:
	}
}

func TestAgentUpdateSchedulerDefersForActiveFileAndTerminalWork(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *Server, connection *agentConnection)
	}{
		{
			name: "file transfer",
			setup: func(t *testing.T, _ *Server, connection *agentConnection) {
				connection.fileMu.Lock()
				connection.fileTransfers["transfer"] = &coreFileTransfer{requestID: "transfer", done: make(chan struct{})}
				connection.fileMu.Unlock()
			},
		},
		{
			name: "pending terminal authorization",
			setup: func(t *testing.T, s *Server, connection *agentConnection) {
				_, _, err := s.terminals.create(&session{ID: "browser-session"}, connection.nodeID, connection.agentID, connection.generation,
					protocol.TerminalTargetHost, "", connection, s.now())
				if err != nil {
					t.Fatalf("create pending terminal authorization: %v", err)
				}
			},
		},
		{
			name: "active terminal session",
			setup: func(t *testing.T, s *Server, connection *agentConnection) {
				current := &session{ID: "browser-session"}
				_, ticket, err := s.terminals.create(current, connection.nodeID, connection.agentID, connection.generation,
					protocol.TerminalTargetHost, "", connection, s.now())
				if err != nil {
					t.Fatalf("create terminal authorization: %v", err)
				}
				if _, ok := s.terminals.consume(ticket, current.ID, s.now(), nil, nil); !ok {
					t.Fatal("consume terminal authorization")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, connection, task := newAgentUpdateSchedulerFixture(t)
			test.setup(t, s, connection)
			s.dispatchAgentUpdate(context.Background(), task)

			updated := readAgentUpdateTask(t, s, task.ID)
			if updated.Status != "deferred" || !strings.Contains(updated.Reason, "terminal session") && !strings.Contains(updated.Reason, "file transfer") {
				t.Fatalf("busy Agent update state = %q (%q), want deferred with a busy-work reason", updated.Status, updated.Reason)
			}
			select {
			case command := <-connection.commands:
				t.Fatalf("update dispatched during active work: %#v", command)
			default:
			}
		})
	}
}

func TestAgentUpdateSchedulerDispatchesWhenConnectionIsIdle(t *testing.T) {
	s, connection, task := newAgentUpdateSchedulerFixture(t)
	s.dispatchAgentUpdate(context.Background(), task)

	updated := readAgentUpdateTask(t, s, task.ID)
	if updated.Status != "dispatched" {
		t.Fatalf("idle Agent update status = %q (%q), want dispatched", updated.Status, updated.Reason)
	}
	select {
	case command := <-connection.commands:
		if command.Type != protocol.TypeAgentUpdate || command.RequestID != task.ID {
			t.Fatalf("idle Agent update command = %#v", command)
		}
	case <-time.After(time.Second):
		t.Fatal("idle Agent update was not dispatched")
	}
}

func newAgentUpdateSchedulerFixture(t *testing.T) (*Server, *agentConnection, coreupdates.Task) {
	t.Helper()
	s := newTestServer(t)
	ctx := context.Background()
	enrollment, err := s.agents.CreateEnrollment(ctx, "update scheduler test node", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatalf("create update scheduler node: %v", err)
	}
	release, err := s.updates.CreateRelease(ctx, "33333333-3333-4333-8333-333333333333", agentupdate.Manifest{
		FormatVersion: 1, Version: "1.0.1", OS: "linux", Architecture: "amd64", SHA256: strings.Repeat("a", 64),
		Size: 1, MinProtocol: 1, MaxProtocol: 1,
	}, "/tmp/nodedance-test-agent")
	if err != nil {
		t.Fatalf("create update release: %v", err)
	}
	task, err := s.updates.CreateTask(ctx, enrollment.NodeID, release.ID, "manual", "", 0)
	if err != nil {
		t.Fatalf("create update task: %v", err)
	}
	connection := &agentConnection{
		agentID: "agent", nodeID: enrollment.NodeID, generation: 1, updateEnabled: true, updateBaseURL: "https://panel.test",
		commands: make(chan protocol.Envelope, 1), fileTransfers: make(map[string]*coreFileTransfer),
	}
	s.agentConnectionsMu.Lock()
	s.agentConnections[connection.agentID] = connection
	s.agentConnectionsMu.Unlock()
	return s, connection, task
}

func readAgentUpdateTask(t *testing.T, s *Server, id string) coreupdates.Task {
	t.Helper()
	tasks, err := s.updates.ListTasks(context.Background(), 10)
	if err != nil {
		t.Fatalf("list Agent update tasks: %v", err)
	}
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("Agent update task %s disappeared", id)
	return coreupdates.Task{}
}
