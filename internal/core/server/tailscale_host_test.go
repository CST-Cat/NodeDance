package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"github.com/CST-Cat/NodeDance/internal/update"
)

func TestDiscoveryReadsDeploymentHostThroughBuiltInExecutor(t *testing.T) {
	cases := []update.Response{
		{Status: 200, Data: json.RawMessage(`[{"identity":"host-peer","name":"host","os":"linux","class":"linux","online":true,"ips":["100.64.0.2"],"managed":false}]`)},
		{Status: 404}, {Status: 412}, {Status: 503, Error: "Cannot read deployment host Tailscale status"},
	}
	responses := make(chan update.Response, len(cases))
	for _, result := range cases {
		responses <- result
	}
	s := &Server{updateInvoke: func(ctx context.Context, rpc update.RPC) (update.Response, error) {
		if rpc.Action != "tailscale-peers" || rpc.Request != (update.Request{Component: "core"}) || rpc.IdempotencyKey != "" {
			t.Error("discovery must send a fixed host read")
		}
		return <-responses, nil
	}}
	peers, err := s.tailscaleDiscovery().List(context.Background())
	if err != nil || len(peers) != 1 || peers[0].Identity != "host-peer" {
		t.Fatalf("host discovery failed: %v %v", peers, err)
	}
	for _, want := range []error{tailscale.ErrUnavailable, tailscale.ErrNotLoggedIn, nil} {
		_, err = s.tailscaleDiscovery().List(context.Background())
		if err == nil || want != nil && !errors.Is(err, want) {
			t.Fatalf("host failure must be retained without falling back to Core: %v", err)
		}
	}
}

func TestNativeDiscoveryUsesHostWithoutUpdateSetup(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("requires native Core environment")
	}
	if (&Server{}).tailscaleDiscovery().ReadPeers != nil {
		t.Fatal("native discovery must not require an update worker")
	}
}
