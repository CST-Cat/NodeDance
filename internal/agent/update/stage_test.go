package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStageVerifiesAndAtomicallySwitchesCandidate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Agent updates target Linux")
	}
	artifact := []byte("candidate-agent-binary")
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Sign(Manifest{FormatVersion: 1, Version: "1.0.1", OS: "linux", Architecture: runtime.GOARCH, SHA256: Digest(artifact), Size: int64(len(artifact)), MinProtocol: 1, MaxProtocol: 1, CoreMin: "1.0.0"}, private)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-device-credential" {
			t.Errorf("unexpected artifact authorization header")
		}
		_, _ = w.Write(artifact)
	}))
	defer server.Close()
	state := t.TempDir()
	oldDir := filepath.Join(state, "bin", "versions", "1.0.0")
	if err := os.MkdirAll(oldDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "nodedance-agent"), []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", "1.0.0", "nodedance-agent"), filepath.Join(state, "bin", "current")); err != nil {
		t.Fatal(err)
	}
	if err := Stage(context.Background(), server.Client(), "test-device-credential", public, Request{Manifest: manifest, ArtifactURL: server.URL, CoreVersion: "1.0.0", CurrentAgentVersion: "1.0.0", Protocol: 1}, state); err != nil {
		t.Fatalf("stage signed candidate: %v", err)
	}
	j, err := ReadJournal(state)
	if err != nil || j.State != "prepared" {
		t.Fatalf("journal after staging = %#v, %v", j, err)
	}
	if err := ApplyPrepared(state); err != nil {
		t.Fatalf("switch to candidate: %v", err)
	}
	target, err := os.Readlink(filepath.Join(state, "bin", "current"))
	if err != nil || target != filepath.Join("versions", "1.0.1", "nodedance-agent") {
		t.Fatalf("current target = %q, %v", target, err)
	}
	if err := ConfirmStartup(state, "1.0.1"); err != nil {
		t.Fatal(err)
	}
	j, err = ReadJournal(state)
	if err != nil || j.State != "confirmed" {
		t.Fatalf("confirmed journal = %#v, %v", j, err)
	}
	if err := Rollback(state); err != nil {
		t.Fatal(err)
	}
	target, err = os.Readlink(filepath.Join(state, "bin", "current"))
	if err != nil || !strings.Contains(target, "1.0.1") {
		t.Fatalf("confirmed update rolled back: %q %v", target, err)
	}
}

func TestHelperRestoresPreviousVersionWithoutCoreAfterRestart(t *testing.T) {
	state := t.TempDir()
	versions := filepath.Join(state, "bin", "versions")
	oldDir := filepath.Join(versions, "old")
	candidateDir := filepath.Join(versions, "candidate")
	for _, dir := range []string{oldDir, candidateDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(oldDir, "nodedance-agent")
	candidate := filepath.Join(candidateDir, "nodedance-agent")
	if err := os.WriteFile(old, []byte("#!/bin/sh\nsleep 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nexit 17\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("versions", "candidate", "nodedance-agent"), filepath.Join(state, "bin", "current")); err != nil {
		t.Fatal(err)
	}
	if err := WriteJournal(state, Journal{State: "awaiting_confirmation", OldTarget: filepath.Join("versions", "old", "nodedance-agent"), CandidateTarget: filepath.Join("versions", "candidate", "nodedance-agent"), Version: "candidate", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Supervisor(ctx, state, "/nonexistent/agent.json"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(state, "bin", "current"))
	if err != nil || target != filepath.Join("versions", "old", "nodedance-agent") {
		t.Fatalf("helper did not restore old version while Core was unavailable: %q %v", target, err)
	}
}
