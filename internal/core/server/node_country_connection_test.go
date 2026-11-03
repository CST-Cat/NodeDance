package server

import (
	"testing"
	"time"

	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestConnectionPublicIPFromLiveConnection(t *testing.T) {
	s := &Server{
		now: time.Now,
		agentConnections: map[string]*agentConnection{
			"agent-1": {nodeID: "node-1", remoteAddr: "1.2.3.4"},
			"agent-2": {nodeID: "node-2", remoteAddr: "10.0.0.5"},
			"agent-3": {nodeID: "node-3", remoteAddr: "unknown"},
			"agent-4": {nodeID: "node-4", remoteAddr: "2606:4700:4700::1111"},
		},
	}
	if got := s.connectionPublicIP("node-1"); got != "1.2.3.4" {
		t.Fatalf("public IPv4 connection = %q", got)
	}
	if got := s.connectionPublicIP("node-4"); got != "2606:4700:4700::1111" {
		t.Fatalf("public IPv6 connection = %q", got)
	}
	for _, nodeID := range []string{"node-2", "node-3", "node-absent"} {
		if got := s.connectionPublicIP(nodeID); got != "" {
			t.Fatalf("node %s must have no public connection IP, got %q", nodeID, got)
		}
	}
}

func TestApplyConnectionPublicIP(t *testing.T) {
	s := &Server{
		now: time.Now,
		agentConnections: map[string]*agentConnection{
			"agent-1": {nodeID: "node-1", remoteAddr: "1.2.3.4"},
			"agent-4": {nodeID: "node-4", remoteAddr: "2606:4700:4700::1111"},
		},
	}
	view := &coremetrics.View{}
	view.Metrics.Host = &protocol.MetricsHost{}
	s.applyConnectionPublicIP(view, "node-1")
	if view.Metrics.Host.PublicIPv4.Status != protocol.MetricKnown || view.Metrics.Host.PublicIPv4.Value == nil || *view.Metrics.Host.PublicIPv4.Value != "1.2.3.4" {
		t.Fatalf("IPv4 not applied from connection: %+v", view.Metrics.Host.PublicIPv4)
	}
	s.applyConnectionPublicIP(view, "node-4")
	if view.Metrics.Host.PublicIPv6.Value == nil || *view.Metrics.Host.PublicIPv6.Value != "2606:4700:4700::1111" {
		t.Fatalf("IPv6 not applied from connection: %+v", view.Metrics.Host.PublicIPv6)
	}
	offline := &coremetrics.View{}
	offline.Metrics.Host = &protocol.MetricsHost{}
	s.applyConnectionPublicIP(offline, "node-absent")
	if offline.Metrics.Host.PublicIPv4.Value != nil || offline.Metrics.Host.PublicIPv6.Value != nil {
		t.Fatal("offline node view must stay untouched")
	}
}
