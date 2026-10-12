package update

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
)

type hostStatusFixture struct {
	output string
	err    error
	calls  int
}

func (f *hostStatusFixture) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.calls++
	if name != "tailscale" || len(args) != 2 || args[0] != "status" || args[1] != "--json" {
		panic("host discovery must only read status")
	}
	if _, ok := ctx.Deadline(); !ok {
		panic("host read must be bounded")
	}
	return []byte(f.output), f.err
}
func TestHostDiscoveryIsScopedReadAndReturnsOnlyPeers(t *testing.T) {
	f := &hostStatusFixture{output: `{"BackendState":"Running","User":{"secret":"must not cross socket"},"Peer":{"nodekey:x":{"PublicKey":"nodekey:x","HostName":"host","OS":"linux","Online":true,"TailscaleIPs":["100.64.0.2"]}}}`}
	m := &Manager{Config: Config{Targets: map[string]Target{"core": {Mode: "docker"}}}, tailscaleRunner: f}
	rpc := RPC{Action: "tailscale-peers", Request: Request{Component: "core"}}
	result := m.Handle(context.Background(), rpc)
	var peers []tailscale.Peer
	if result.Status != 200 || json.Unmarshal(result.Data, &peers) != nil || len(peers) != 1 || peers[0].Identity != "nodekey:x" || peers[0].Class != "linux" {
		t.Fatalf("unexpected host view: %+v", result)
	}
	var raw []map[string]any
	_ = json.Unmarshal(result.Data, &raw)
	if _, ok := raw[0]["User"]; ok {
		t.Fatal("host user profile was disclosed")
	}
	rejected := []RPC{
		{Action: rpc.Action, Request: Request{Component: "agent"}},
		{Action: rpc.Action, Request: Request{Component: "core", NodeID: "remote"}},
		{Action: rpc.Action, Request: Request{Component: "core", Confirm: true}},
		{Action: rpc.Action, Request: Request{Component: "core", Version: "core-v0.1.12"}},
		{Action: rpc.Action, Request: Request{Component: "core", JobID: "arbitrary"}},
		{Action: rpc.Action, Request: rpc.Request, IdempotencyKey: "not-allowed"},
	}
	for _, r := range rejected {
		if got := m.Handle(context.Background(), r); got.Status != 400 {
			t.Fatalf("accepted extra options: %+v", got)
		}
	}
	delete(m.Config.Targets, "core")
	if got := m.Handle(context.Background(), rpc); got.Status != 403 {
		t.Fatalf("unregistered Core accepted: %+v", got)
	}
	if f.calls != 1 {
		t.Fatalf("rejected reads executed status: %d", f.calls)
	}
}
func TestHostDiscoveryReportsUnavailableAndLoginState(t *testing.T) {
	for _, tc := range []struct {
		output string
		err    error
		status int
	}{
		{err: exec.ErrNotFound, status: 404},
		{output: `{"BackendState":"NeedsLogin"}`, status: 412},
		{output: `{"BackendState":"Running","Peer":{}}`, status: 200},
		{err: errors.New("private host error"), status: 503},
		{output: `malformed`, status: 503},
	} {
		m := &Manager{Config: Config{Targets: map[string]Target{"core": {}}}, tailscaleRunner: &hostStatusFixture{output: tc.output, err: tc.err}}
		if got := m.Handle(context.Background(), RPC{Action: "tailscale-peers", Request: Request{Component: "core"}}); got.Status != tc.status {
			t.Fatalf("got %+v, want status %d", got, tc.status)
		}
	}
}
