package server

import (
	"reflect"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coreupdates "github.com/CST-Cat/NodeDance/internal/core/updates"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAgentUpdateCampaignFiltersNodeArchitecture(t *testing.T) {
	nodes := []agents.Node{
		{Identity: agents.Identity{NodeID: "amd64", Status: "online"}, Capabilities: []string{protocol.CapabilityAgentUpdates}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
		{Identity: agents.Identity{NodeID: "arm64", Status: "online"}, Capabilities: []string{protocol.CapabilityAgentUpdates}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "arm64"}},
		{Identity: agents.Identity{NodeID: "offline", Status: "offline"}, Capabilities: []string{protocol.CapabilityAgentUpdates}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
		{Identity: agents.Identity{NodeID: "legacy", Status: "online"}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"}},
	}
	release := coreupdates.Release{OS: "linux", Architecture: "arm64"}
	if got := compatibleAgentUpdateTargets(nodes, release); !reflect.DeepEqual(got, []string{"arm64"}) {
		t.Fatalf("compatible rollout targets = %v, want only the online arm64 updater", got)
	}
}
