package update

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuiltInWorkerCompletesAfterAcceptanceReaderCloses(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only isolated update fixture")
	}
	manager, _, _ := nativeFixture(t)
	config := manager.Config
	manager.Close()
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- runRPC(config, RPC{Action: "apply", Request: Request{Component: "core", Confirm: true}, IdempotencyKey: "builtin-default-update"}, writer, fixtureResolver{})
	}()
	var response Response
	if err = json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if response.Status != 202 || response.Error != "" {
		t.Fatalf("acceptance: %+v", response)
	}
	var accepted Job
	if err = json.Unmarshal(response.Data, &accepted); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not finish independently")
	}
	raw, err := os.ReadFile(filepath.Join(config.StateDir, accepted.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var record journal
	if Decode(raw, &record) != nil || record.job().Status != "succeeded" {
		t.Fatalf("worker failed after response reader closed: %s", raw)
	}
	// The saved identity observes the original job without performing another update.
	r, w := io.Pipe()
	defer r.Close()
	go func() {
		defer w.Close()
		_ = runRPC(config, RPC{Action: "apply", Request: Request{Component: "core", Confirm: true}, IdempotencyKey: "builtin-default-update"}, w, fixtureResolver{})
	}()
	if err = json.NewDecoder(r).Decode(&response); err != nil {
		t.Fatal(err)
	}
	var repeated Job
	json.Unmarshal(response.Data, &repeated)
	if repeated.ID != accepted.ID || repeated.Status != "succeeded" {
		t.Fatalf("submission replay: %+v", repeated)
	}
}
func TestBuiltInObservationDoesNotRequireControllerInstallation(t *testing.T) {
	config := Config{StateDir: filepath.Join(t.TempDir(), "updates"), Targets: map[string]Target{"agent": {Mode: "docker"}}}
	r, w := io.Pipe()
	defer r.Close()
	done := make(chan error, 1)
	go func() {
		defer w.Close()
		done <- runRPC(config, RPC{Action: "status", Request: Request{Component: "agent"}}, w, fixtureResolver{})
	}()
	var response Response
	if err := json.NewDecoder(r).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Status != 200 || string(response.Data) != `{"jobs":[]}` {
		t.Fatalf("fresh built-in observation: %+v", response)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBuiltInCheckRunsCurrentBinaryWhileControllerLockIsHeld(t *testing.T) {
	manager, _, _ := nativeFixture(t)
	defer manager.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		defer writer.Close()
		done <- runRPC(manager.Config, RPC{Action: "check", Request: Request{Component: "core"}}, writer, fixtureResolver{})
	}()
	var response Response
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var check Check
	if response.Status != 200 || json.Unmarshal(response.Data, &check) != nil || check.Current != "core-v0.1.8" || !check.Available || !check.Configured {
		t.Fatalf("built-in default preflight: %+v / %+v", response, check)
	}
}
func TestInternalWorkerRejectsUnscopedRequests(t *testing.T) {
	handled, err := Internal([]string{"__update-rpc"}, io.Discard, io.Discard)
	if !handled || err == nil {
		t.Fatal("worker accepted missing fixed request")
	}
	if handled, _ = Internal([]string{"update"}, io.Discard, io.Discard); handled {
		t.Fatal("public update was intercepted by internal worker")
	}
}
func TestBuiltInStateStaysOutsideBackupAndHasNoGlobalDeploymentRegistry(t *testing.T) {
	data := filepath.Join(t.TempDir(), "private-data")
	state := updateState(data)
	if strings.HasPrefix(state, data+string(os.PathSeparator)) || state == data {
		t.Fatal("update backups would include update state")
	}
	if _, _, _, err := discover(context.Background(), "other", data); err == nil {
		t.Fatal("unknown product accepted")
	}
}
