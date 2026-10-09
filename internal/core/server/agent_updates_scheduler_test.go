package server

import (
	"context"
	"reflect"
	"testing"

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
