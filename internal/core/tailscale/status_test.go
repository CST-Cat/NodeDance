package tailscale

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFindCLIOutsideServicePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	directory := t.TempDir()
	nonExecutable := filepath.Join(directory, "not-executable")
	binary := filepath.Join(directory, "tailscale")
	for name, mode := range map[string]os.FileMode{nonExecutable: 0600, binary: 0755} {
		if err := os.WriteFile(name, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	found, err := findCLI([]string{directory, nonExecutable, binary})
	if err != nil || found != binary {
		t.Fatalf("found %q, err %v", found, err)
	}
	if _, err := findCLI([]string{nonExecutable}); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalStatusQueriesExistingSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "tailscaled.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/localapi/v0/status" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"BackendState":"Running","Peer":{}}`))
	})}
	go server.Serve(listener)
	defer server.Close()
	output, err := localStatus(context.Background(), socket)
	if err != nil || string(output) != `{"BackendState":"Running","Peer":{}}` {
		t.Fatalf("output %s, err %v", output, err)
	}
	if _, err := localStatus(context.Background(), socket+"-missing"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

type statusRunner struct{ state string }

func (r statusRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return []byte(r.state), nil
}
func TestDiscoveryDistinguishesEmptyTailnetFromLoggedOut(t *testing.T) {
	peers, err := (Discovery{Runner: statusRunner{`{"BackendState":"Running","Peer":{}}`}}).List(context.Background())
	if err != nil || peers == nil || len(peers) != 0 {
		t.Fatalf("peers %v, err %v", peers, err)
	}
	_, err = (Discovery{Runner: statusRunner{`{"BackendState":"NeedsLogin"}`}}).List(context.Background())
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v", err)
	}
}
