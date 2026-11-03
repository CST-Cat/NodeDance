package update

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUnknownAndCorruptJournalEntriesDoNotBlockUpdates(t *testing.T) {
	m, _, _ := nativeFixture(t)
	config, command := m.Config, m.command
	id := strings.Repeat("a", 32)
	job := Job{ID: id, Component: "core", Mode: "systemd", Status: "succeeded", Stage: "complete", Current: "core-v0.1.7", Target: "core-v0.1.8", Started: time.Now().UTC(), IdempotencyKey: "saved-history-identity"}
	raw, err := json.Marshal(job.record())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["futureMetadata"] = json.RawMessage(`{"format":900,"capabilities":["unrecognized"],"source":{"anything":true}}`)
	raw, _ = json.Marshal(fields)
	if err := os.WriteFile(filepath.Join(config.StateDir, id+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(config.StateDir, strings.Repeat("b", 32)+".json")
	if err := os.WriteFile(broken, []byte(`{"id":`), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep-private"), 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(config.StateDir, strings.Repeat("c", 32)+".json")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runRPC(config, RPC{Action: "status", Request: Request{Component: "core", JobID: id}}, &output, fixtureResolver{}); err != nil {
		t.Fatal(err)
	}
	var response Response
	var observed Job
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Status != 200 || json.Unmarshal(response.Data, &observed) != nil || observed.ID != id || observed.Status != "succeeded" {
		t.Fatalf("unrelated corrupt history blocked observation: %s", output.Bytes())
	}
	m.Close()
	recovered, err := New(config, fixtureResolver{})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	recovered.command = command
	recovered.healthTimeout = time.Second
	retained, err := recovered.Get("core", id)
	if err != nil || retained.IdempotencyKey != job.IdempotencyKey {
		t.Fatalf("recovery metadata was lost: %+v %v", retained, err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep-private" {
		t.Fatal("journal reader followed or changed an external symlink")
	}
	accepted, err := recovered.Apply(context.Background(), Request{Component: "core", Confirm: true}, "update-after-corrupt-history")
	if err != nil {
		t.Fatal(err)
	}
	if result := waitJob(t, recovered, accepted.ID); result.Status != "succeeded" {
		t.Fatalf("usable journals did not permit the actual update: %+v", result)
	}
}

func TestDevRetryObservesOriginalAcceptanceWhileControllerIsLocked(t *testing.T) {
	m, _, _ := nativeFixture(t)
	defer m.Close()
	job := Job{ID: strings.Repeat("d", 32), Component: "core", Status: "queued", Target: "core-v0.1.9", IdempotencyKey: "accepted-before-disconnect"}
	if err := m.persist(job); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runRPC(m.Config, RPC{Action: "apply", Request: Request{Component: "core", Confirm: true}, IdempotencyKey: job.IdempotencyKey}, &output, fixtureResolver{}); err != nil {
		t.Fatal(err)
	}
	var response Response
	var repeated Job
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Status != 202 || json.Unmarshal(response.Data, &repeated) != nil || repeated.ID != job.ID {
		t.Fatalf("locked controller rejected accepted request: %s", output.Bytes())
	}
	dev := Job{ID: strings.Repeat("e", 32), Component: "agent", Status: "queued", Target: "agent-dev-" + strings.Repeat("e", 40), IdempotencyKey: "accepted-dev-before-disconnect"}
	if err := m.persist(dev); err != nil {
		t.Fatal(err)
	}
	config := Config{StateDir: m.Config.StateDir, Targets: map[string]Target{"agent": {Mode: "docker"}}}
	rpc := RPC{Action: "apply", Request: Request{Component: "agent", Version: ChannelDev, Confirm: true}, IdempotencyKey: dev.IdempotencyKey}
	output.Reset()
	if err := runRPC(config, rpc, &output, fixtureResolver{}); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Status != 202 || json.Unmarshal(response.Data, &repeated) != nil || repeated.ID != dev.ID || repeated.Target != dev.Target {
		t.Fatalf("Dev retry did not observe its original artifact: %s", output.Bytes())
	}
	rpc.Request.Version = "agent-v0.1.34"
	output.Reset()
	if err := runRPC(config, rpc, &output, fixtureResolver{}); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(output.Bytes(), &response) != nil || response.Status != 409 {
		t.Fatalf("retry identity changed the selected artifact: %s", output.Bytes())
	}
}
