package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"github.com/CST-Cat/NodeDance/internal/update"
)

func TestDiscoveryReadsDeploymentHostOverExistingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	cases := []update.Response{
		{Status: 200, Data: json.RawMessage(`[{"identity":"host-peer","name":"host","os":"linux","class":"linux","online":true,"ips":["100.64.0.2"],"managed":false}]`)},
		{Status: 404}, {Status: 412}, {Status: 503, Error: "Cannot read deployment host Tailscale status"},
	}
	responses := make(chan update.Response, len(cases))
	for _, result := range cases {
		responses <- result
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc update.RPC
		if r.Method != "POST" || r.URL.Path != "/rpc" || json.NewDecoder(r.Body).Decode(&rpc) != nil || rpc.Action != "tailscale-peers" || rpc.Request != (update.Request{Component: "core"}) || rpc.IdempotencyKey != "" {
			t.Error("discovery must send a fixed host read")
		}
		_ = json.NewEncoder(w).Encode(<-responses)
	})}
	go server.Serve(listener)
	defer server.Close()
	s := &Server{updateSocket: socket}
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
func TestUnsupervisedDiscoveryUsesCoreEnvironment(t *testing.T) {
	d := (&Server{updateSocket: filepath.Join(t.TempDir(), "absent.sock")}).tailscaleDiscovery()
	if d.ReadPeers != nil {
		t.Fatal("unsupervised Core must use its own environment")
	}
}
