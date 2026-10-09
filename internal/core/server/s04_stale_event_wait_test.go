package server

import (
	"strings"
	"testing"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
)

func TestS04DockerStaleEventOutcomeIgnoresInterimInventory(t *testing.T) {
	deadline := time.Date(2026, time.October, 9, 12, 0, 15, 0, time.UTC)
	const nodeID = "00000000-0000-4000-8000-000000000020"
	tests := []struct {
		name      string
		view      coredocker.View
		wantMatch bool
		wantError string
	}{
		{
			name: "other node is ignored",
			view: coredocker.View{NodeID: "00000000-0000-4000-8000-000000000021", DataStale: true,
				StaleReason: "docker_health_stale", ServerTime: deadline.Add(time.Second)},
		},
		{
			name: "online stale waiting for Docker Engine is interim",
			view: coredocker.View{NodeID: nodeID, AgentOnline: true, DataStale: true,
				StaleReason: "awaiting_authoritative_snapshot", ServerTime: deadline.Add(-time.Second)},
		},
		{
			name: "Agent reconnect transition is interim",
			view: coredocker.View{NodeID: nodeID, AgentOnline: false, DataStale: true,
				StaleReason: "agent_offline_or_lease_invalid", ServerTime: deadline.Add(-time.Second)},
		},
		{
			name: "final online Docker health expiry after deadline",
			view: coredocker.View{NodeID: nodeID, AgentOnline: true, DataStale: true,
				StaleReason: "docker_health_stale", ServerTime: deadline},
			wantMatch: true,
		},
		{
			name: "final stale event before deadline fails",
			view: coredocker.View{NodeID: nodeID, AgentOnline: true, DataStale: true,
				StaleReason: "docker_health_stale", ServerTime: deadline.Add(-time.Nanosecond)},
			wantError: "before its 15-second receive-time lease expired",
		},
		{
			name: "final stale event while Agent offline fails",
			view: coredocker.View{NodeID: nodeID, AgentOnline: false, DataStale: true,
				StaleReason: "docker_health_stale", ServerTime: deadline.Add(time.Second)},
			wantError: "while the Agent was offline",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matched, failure := s04DockerStaleEventOutcome(test.view, nodeID, deadline)
			if matched != test.wantMatch {
				t.Fatalf("matched=%t, want %t", matched, test.wantMatch)
			}
			if test.wantError == "" {
				if failure != "" {
					t.Fatalf("unexpected failure: %s", failure)
				}
				return
			}
			if !strings.Contains(failure, test.wantError) {
				t.Fatalf("failure=%q, want substring %q", failure, test.wantError)
			}
		})
	}
}
