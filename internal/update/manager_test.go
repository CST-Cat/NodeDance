package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureResolver struct{ fail bool }

func (f fixtureResolver) Resolve(_ context.Context, c, v string) (Release, error) {
	if v == "" {
		v = "core-v0.1.9"
	}
	return Release{Manifest: Describe(c, v)}, nil
}
func (f fixtureResolver) Download(_ context.Context, r Release, path string) error {
	if f.fail {
		return errors.New("checksum verification failed")
	}
	return os.WriteFile(path, []byte("#!/bin/sh\necho NodeDance "+r.Version+"\n"), 0700)
}
func nativeFixture(t *testing.T) (*Manager, string, *atomic.Bool) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root transaction test")
	}
	root := t.TempDir()
	state := filepath.Join(root, "state")
	data := filepath.Join(root, "data")
	for _, path := range []string{state, data} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "nodedance")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho NodeDance core-v0.1.8\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "credentials"), []byte("preserve-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	running := new(atomic.Bool)
	running.Store(true)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !running.Load() {
			w.WriteHeader(503)
			return
		}
		raw, _ := execute(r.Context(), binary, "--version")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": strings.TrimSpace(strings.TrimPrefix(string(raw), "NodeDance "))})
	}))
	t.Cleanup(health.Close)
	m, err := New(Config{StateDir: state, Targets: map[string]Target{"core": {Mode: "systemd", Binary: binary, Unit: "nodedance.service", DataDir: data, HealthURL: health.URL + "/api/v1/health"}}}, fixtureResolver{})
	if err != nil {
		t.Fatal(err)
	}
	m.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			if args[0] == "stop" {
				running.Store(false)
			} else {
				running.Store(true)
			}
			return nil, nil
		}
		return execute(ctx, name, args...)
	}
	m.healthTimeout = 150 * time.Millisecond
	t.Cleanup(m.Close)
	return m, data, running
}
func waitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.Get("core", id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Finished != nil {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}
func TestNativeUpdatePreservesStateAndRollbackRestoresBackup(t *testing.T) {
	m, data, _ := nativeFixture(t)
	request := Request{Component: "core", Confirm: true}
	job, err := m.Apply(context.Background(), request, "update-key-001")
	if err != nil {
		t.Fatal(err)
	}
	job = waitJob(t, m, job.ID)
	if job.Status != "succeeded" {
		t.Fatalf("%+v", job)
	}
	repeat, err := m.Apply(context.Background(), request, "update-key-001")
	if err != nil || repeat.ID != job.ID {
		t.Fatalf("idempotent submission: %+v %v", repeat, err)
	}
	if err := os.WriteFile(filepath.Join(data, "credentials"), []byte("after-update"), 0600); err != nil {
		t.Fatal(err)
	}
	back, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true, JobID: job.ID}, "rollback-key-001")
	if err != nil {
		t.Fatal(err)
	}
	back = waitJob(t, m, back.ID)
	if back.Status != "succeeded" {
		t.Fatalf("%+v", back)
	}
	raw, _ := os.ReadFile(filepath.Join(data, "credentials"))
	if string(raw) != "preserve-secret" {
		t.Fatal("pre-update state not restored")
	}
	info, _ := os.Stat(data)
	if info.Mode().Perm() != 0700 {
		t.Fatal("state permissions changed")
	}
	current, err := m.current(context.Background(), "core")
	if err != nil || current != "core-v0.1.8" {
		t.Fatalf("rollback runtime: %s %v", current, err)
	}
}
func TestFailedDownloadLeavesRuntimeUntouched(t *testing.T) {
	m, _, running := nativeFixture(t)
	m.Resolver = fixtureResolver{fail: true}
	job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "failed-download")
	if err != nil {
		t.Fatal(err)
	}
	job = waitJob(t, m, job.ID)
	if job.Status != "failed" || !running.Load() {
		t.Fatalf("%+v", job)
	}
	current, _ := m.current(context.Background(), "core")
	if current != "core-v0.1.8" {
		t.Fatal("failed download replaced runtime")
	}
}
func TestFailedHealthAutomaticallyRollsBack(t *testing.T) {
	m, _, running := nativeFixture(t)
	original := m.command
	m.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		raw, err := original(ctx, name, args...)
		if name == "systemctl" && args[0] == "start" {
			version, _ := execute(ctx, m.Config.Targets["core"].Binary, "--version")
			if strings.Contains(string(version), "core-v0.1.9") {
				running.Store(false)
			}
		}
		return raw, err
	}
	job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "failed-health-001")
	if err != nil {
		t.Fatal(err)
	}
	job = waitJob(t, m, job.ID)
	if job.Status != "rolled_back" || !running.Load() {
		t.Fatalf("%+v", job)
	}
	version, _ := m.current(context.Background(), "core")
	if version != "core-v0.1.8" {
		t.Fatal("rollback version mismatch")
	}
}
func TestInterruptedDownloadRecoveryDoesNotStopRuntime(t *testing.T) {
	m, _, running := nativeFixture(t)
	job := Job{ID: strings.Repeat("a", 32), Component: "core", Current: "core-v0.1.8", Target: "core-v0.1.9", Status: "running", Stage: "downloading"}
	m.mu.Lock()
	if err := m.persist(job); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	config := m.Config
	m.Close()
	recovered, err := New(config, fixtureResolver{})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	job = waitJob(t, recovered, job.ID)
	if job.Status != "rolled_back" || !running.Load() {
		t.Fatalf("%+v", job)
	}
}
func TestConfirmationAndTagNamespace(t *testing.T) {
	m, _, _ := nativeFixture(t)
	if _, err := m.Apply(context.Background(), Request{Component: "core"}, "unconfirmed-key"); err == nil {
		t.Fatal("unconfirmed update accepted")
	}
	for _, tag := range []string{"agent-v0.1.9", "core-v01.1.9", "core-v18446744073709551616.0.0", "core-v0.1.9-beta"} {
		if ValidTag("core", tag) {
			t.Fatalf("invalid tag accepted: %s", tag)
		}
	}
}
func TestBackupRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	os.Mkdir(source, 0700)
	os.Symlink("/etc/passwd", filepath.Join(source, "unsafe"))
	if err := copyTree(source, filepath.Join(root, "backup")); err == nil {
		t.Fatal("symlink accepted")
	}
}
