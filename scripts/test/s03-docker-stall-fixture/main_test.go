package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
)

func TestPinnedSDKListTimeoutLeavesPingUsable(t *testing.T) {
	stateDir := t.TempDir()
	socketPath := filepath.Join(stateDir, "docker.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: &dockerFixture{stateDir: stateDir}, ReadHeaderTimeout: time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("Docker fixture HTTP server did not stop")
		}
	})

	engine, err := agentdocker.NewSDKEngine("unix://" + socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.Close() })

	listCtx, cancelList := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelList()
	listDone := make(chan error, 1)
	go func() {
		_, err := engine.ListAll(listCtx)
		listDone <- err
	}()
	startDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(startDeadline) {
		if _, err := os.Stat(filepath.Join(stateDir, "list-started")); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "list-started")); err != nil {
		t.Fatal("real Moby SDK did not issue the hanging container-list request")
	}

	pingCtx, cancelPing := context.WithTimeout(context.Background(), time.Second)
	if err := engine.Ping(pingCtx); err != nil {
		cancelPing()
		t.Fatalf("Docker ping blocked with list request: %v", err)
	}
	cancelPing()

	select {
	case err := <-listDone:
		if err == nil {
			t.Fatal("hanging container list unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Moby SDK container-list call did not honor its context timeout")
	}
	cancelDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(cancelDeadline) {
		if _, err := os.Stat(filepath.Join(stateDir, "list-cancelled")); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fixture did not observe client cancellation after the Moby SDK context expired")
}
