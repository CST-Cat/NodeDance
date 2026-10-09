package tailscale

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type runnerResult struct {
	output []byte
	err    error
}
type staticRunner struct {
	result runnerResult
	calls  int
	name   string
	args   []string
}

func (r *staticRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	r.name = name
	r.args = append([]string(nil), args...)
	return r.result.output, r.result.err
}

func TestDiscoveryClassifiesVisiblePeersAndUsesStableIdentityAcrossRenameAndIPChange(t *testing.T) {
	first := []byte(`{"BackendState":"Running","Self":{"HostName":"core"},"Peer":{"nodekey:stable-linux":{"PublicKey":"nodekey:stable-linux","HostName":"before","DNSName":"before.tail.example.","OS":"linux","TailscaleIPs":["100.64.0.2"],"Online":true},"nodekey:mac":{"PublicKey":"nodekey:mac","HostName":"mac","OS":"macOS","TailscaleIPs":["100.64.0.3"],"Online":false},"nodekey:unknown":{"PublicKey":"nodekey:unknown","HostName":"unknown","OS":"","TailscaleIPs":["100.64.0.4"],"Online":true}}}`)
	runner := &staticRunner{result: runnerResult{output: first}}
	peers, err := (Discovery{Runner: runner}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if runner.name != "tailscale" || strings.Join(runner.args, " ") != "status --json" {
		t.Fatalf("command=%s %v", runner.name, runner.args)
	}
	if len(peers) != 3 {
		t.Fatalf("visible peer count=%d, want 3", len(peers))
	}
	byID := map[string]Peer{}
	for _, peer := range peers {
		byID[peer.Identity] = peer
	}
	linux := byID["nodekey:stable-linux"]
	if linux.Class != "linux" || !linux.Online || linux.Name != "before" || len(linux.IPs) != 1 {
		t.Fatalf("Linux peer classification: %+v", linux)
	}
	if byID["nodekey:mac"].Class != "unsupported" || byID["nodekey:mac"].Online {
		t.Fatalf("macOS peer classification: %+v", byID["nodekey:mac"])
	}
	if byID["nodekey:unknown"].Class != "unknown" {
		t.Fatalf("unknown OS classification: %+v", byID["nodekey:unknown"])
	}

	runner.result.output = []byte(`{"BackendState":"Running","Peer":{"nodekey:stable-linux":{"PublicKey":"nodekey:stable-linux","HostName":"after","DNSName":"after.tail.example.","OS":"linux","TailscaleIPs":["100.64.0.22"],"Online":true}}}`)
	renamed, err := (Discovery{Runner: runner}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(renamed) != 1 || renamed[0].Identity != linux.Identity || renamed[0].Name != "after" || renamed[0].IPs[0] != "100.64.0.22" {
		t.Fatalf("rename/IP change altered identity: initial=%+v updated=%+v", linux, renamed)
	}
}

func TestDiscoveryReportsUnavailableAndNotLoggedInSeparately(t *testing.T) {
	missing := &staticRunner{result: runnerResult{err: exec.ErrNotFound}}
	if _, err := (Discovery{Runner: missing}).List(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing CLI error=%v", err)
	}
	loggedOut := &staticRunner{result: runnerResult{output: []byte("Tailscale is stopped"), err: errors.New("exit status 1")}}
	if _, err := (Discovery{Runner: loggedOut}).List(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("logged out error=%v", err)
	}
	empty := &staticRunner{result: runnerResult{output: []byte(`{"BackendState":"NeedsLogin","Peer":{}}`)}}
	if _, err := (Discovery{Runner: empty}).List(context.Background()); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("empty visible scope error=%v", err)
	}
}
