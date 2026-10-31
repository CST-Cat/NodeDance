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

func TestRollbackJournalFailureLeavesRuntimeUntouched(t *testing.T) {
	m, _, running := nativeFixture(t)
	job := Job{ID: strings.Repeat("c", 32), Component: "core", Status: "queued", Stage: "queued"}
	// A non-regular destination deterministically rejects the stage write,
	// including when this test runs as root.
	if err := os.Mkdir(filepath.Join(m.Config.StateDir, job.ID+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	commands := 0
	m.command = func(context.Context, string, ...string) ([]byte, error) {
		commands++
		return nil, errors.New("runtime must not be touched without durable intent")
	}
	m.runRollback(job, Job{ID: strings.Repeat("d", 32), Component: "core"})
	result, err := m.Get("core", job.ID)
	if err != nil || result.Status != "journal_failed" || result.Finished == nil || commands != 0 || !running.Load() {
		t.Fatalf("rollback continued after journal failure: %+v, commands=%d, err=%v", result, commands, err)
	}
}

type pausedResolver struct {
	fixtureResolver
	started chan struct{}
	release chan struct{}
}

func (r pausedResolver) Resolve(ctx context.Context, component, version string) (Release, error) {
	close(r.started)
	<-r.release
	return r.fixtureResolver.Resolve(ctx, component, version)
}

func TestClosedControllerRejectsNewUpdates(t *testing.T) {
	m, _, _ := nativeFixture(t)
	m.Close()
	if job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "after-close-key"); err == nil {
		t.Fatalf("closed controller accepted new work: %+v", job)
	}
	if len(m.Jobs()) != 0 {
		t.Fatal("closed controller persisted a new update")
	}
}

func TestControllerCloseWaitsForAdmissionAndPreventsLateAcceptance(t *testing.T) {
	m, _, running := nativeFixture(t)
	resolver := pausedResolver{started: make(chan struct{}), release: make(chan struct{})}
	m.Resolver = resolver
	result := make(chan error, 1)
	go func() {
		_, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "closing-controller")
		result <- err
	}()
	<-resolver.started
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-closed:
		t.Error("controller released ownership while update admission was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(resolver.release)
	if err := <-result; err == nil {
		t.Error("update was accepted after shutdown started")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller did not finish shutdown")
	}
	if !running.Load() || len(m.Jobs()) != 0 {
		t.Fatal("shutdown allowed an update to affect the runtime or journal")
	}
}

func TestDockerAgentVersionUsesRunningHostExecutable(t *testing.T) {
	for _, pid := range []string{"4321\n", "0\n", "1", "-1", "not-a-pid"} {
		t.Run(strings.TrimSpace(pid), func(t *testing.T) {
			m := &Manager{Config: Config{Targets: map[string]Target{"agent": {Mode: "docker", Container: "nodedance-agent"}}}}
			calls := 0
			m.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if calls == 1 {
					if name != "docker" || strings.Join(args, " ") != "inspect --format {{.State.Pid}} nodedance-agent" {
						t.Fatalf("unexpected process lookup: %s %v", name, args)
					}
					return []byte(pid), nil
				}
				if name != "/proc/4321/exe" || strings.Join(args, " ") != "--version" {
					t.Fatalf("version must come from the running executable: %s %v", name, args)
				}
				return []byte("NodeDance Agent agent-v0.1.6\n"), nil
			}
			version, err := m.current(context.Background(), "agent")
			if pid == "4321\n" {
				if err != nil || version != "agent-v0.1.6" || calls != 2 {
					t.Fatalf("version=%q calls=%d err=%v", version, calls, err)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("invalid/stopped PID must reject execution: calls=%d err=%v", calls, err)
			}
		})
	}
}

func (f fixtureResolver) Resolve(_ context.Context, c, v string) (Release, error) {
	if v == "" {
		v = "core-v0.1.9"
	}
	return Release{Component: c, Version: v}, nil
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
	deadline := time.Now().Add(15 * time.Second)
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
	command := m.command
	m.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "release-binary" {
			return nil, errors.New("update must not launch a second Agent for preflight")
		}
		return command(ctx, name, args...)
	}
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
func TestDevelopmentUpdateSkipsVerificationAndStaysInstalled(t *testing.T) {
	m, _, running := nativeFixture(t)
	binary := m.Config.Targets["core"].Binary
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho NodeDance dev\n"), 0755); err != nil {
		t.Fatal(err)
	}
	original := m.command
	m.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		raw, err := original(ctx, name, args...)
		if name == "systemctl" && args[0] == "start" {
			version, _ := execute(ctx, binary, "--version")
			if strings.Contains(string(version), "core-v0.1.9") {
				running.Store(false)
			}
		}
		return raw, err
	}
	job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "development-unverified-001")
	if err != nil {
		t.Fatal(err)
	}
	job = waitJob(t, m, job.ID)
	if job.Status != "succeeded" || job.Stage != "complete" || job.Progress != 1 {
		t.Fatalf("%+v", job)
	}
	version, _ := m.current(context.Background(), "core")
	if version != "core-v0.1.9" {
		t.Fatal("development update was rolled back despite verification being skipped")
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

func TestUpdateAvailabilityUsesOnlyComponentTags(t *testing.T) {
	for _, component := range []string{"core", "agent"} {
		for _, entry := range []struct {
			version   string
			available bool
		}{{"0.1.7", false}, {"0.1.8", false}, {"0.1.9", true}, {"2.0.0", true}} {
			t.Run(component+entry.version, func(t *testing.T) {
				current := component + "-v0.1.8"
				manager := &Manager{Config: Config{Targets: map[string]Target{component: {Mode: "systemd", Binary: "fixture"}}}, Resolver: fixtureResolver{}, command: func(context.Context, string, ...string) ([]byte, error) { return []byte("NodeDance " + current), nil }}
				result, err := manager.Check(context.Background(), Request{Component: component, Version: component + "-v" + entry.version})
				if err != nil || result.Available != entry.available || result.Current != current || result.Reason != "" {
					t.Fatalf("tag eligibility: %+v %v", result, err)
				}
			})
		}
	}
}

func TestStageProgressFollowsEachFlow(t *testing.T) {
	flows := []struct {
		job    Job
		stages []string
	}{
		{Job{}, releaseStages},
		{Job{Source: true}, sourceStages},
		{Job{Parent: "parent"}, rollbackStages},
	}
	for _, flow := range flows {
		job := flow.job
		previous := -1.0
		for _, stage := range flow.stages {
			job.Stage = stage
			job.Progress = stageProgress(job)
			if job.Progress < previous || job.Progress > 1 {
				t.Fatalf("stage %s progress %v after %v", stage, job.Progress, previous)
			}
			previous = job.Progress
		}
		if job.Progress != 1 {
			t.Fatalf("final stage progress = %v, want 1", job.Progress)
		}
	}
	frozen := Job{Stage: "switching"}
	frozen.Progress = stageProgress(frozen)
	frozen.Stage = "rolling_back"
	if got := stageProgress(frozen); got != frozen.Progress {
		t.Fatalf("rolling_back moved progress to %v, want frozen %v", got, frozen.Progress)
	}
}
