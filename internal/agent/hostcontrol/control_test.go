package hostcontrol

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rootManager(t *testing.T) *Manager {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root-only runtime; also run this test binary as root")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func awaitJob(t *testing.T, m *Manager, id string) protocol.CommandJob {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		r := m.Handle(protocol.HostRequest{Action: "commands.get", JobID: id})
		if r.Job != nil && r.Job.Status != "running" {
			return *r.Job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return protocol.CommandJob{}
}
func TestDurableIdempotentRootCommand(t *testing.T) {
	m := rootManager(t)
	marker := filepath.Join(m.dir, "marker")
	spec := protocol.CommandSpec{Script: "printf x >> '" + marker + "'; printf result", TimeoutSeconds: 3, Confirm: true}
	req := protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: "unique-command-key"}
	r := m.Handle(req)
	if r.Job == nil {
		t.Fatal(r.Error)
	}
	j := awaitJob(t, m, r.Job.ID)
	if j.Status != "succeeded" || j.Output != "result" || j.ExitCode == nil || *j.ExitCode != 0 {
		t.Fatalf("result %+v", j)
	}
	if retry := m.Handle(req); retry.Job == nil || retry.Job.ID != j.ID {
		t.Fatal("retry lost identity")
	}
	m.Close()
	reopened, err := Open(context.Background(), m.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if retry := reopened.Handle(req); retry.Job == nil || retry.Job.Status != "succeeded" {
		t.Fatal("restart lost durable job")
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatalf("script replayed: %s %v", data, err)
	}
	spec.Script = "printf different"
	if reopened.Handle(req).Error == "" {
		t.Fatal("different request reused key")
	}
}
func TestTimeoutCancellationAndOutputLimit(t *testing.T) {
	m := rootManager(t)
	spec := protocol.CommandSpec{Script: "sleep 60 & wait", TimeoutSeconds: 1, Confirm: true}
	r := m.Handle(protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: "timeout-command"})
	if r.Job == nil {
		t.Fatal(r.Error)
	}
	j := awaitJob(t, m, r.Job.ID)
	if j.Status != "timed_out" {
		t.Fatalf("timeout %+v", j)
	}
	spec.TimeoutSeconds = 60
	r = m.Handle(protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: "cancel-command"})
	if r.Job == nil {
		t.Fatal(r.Error)
	}
	m.Handle(protocol.HostRequest{Action: "commands.cancel", JobID: r.Job.ID})
	j = awaitJob(t, m, r.Job.ID)
	if j.Status != "cancelled" {
		t.Fatalf("cancel %+v", j)
	}
	spec.Script = "head -c 200000 /dev/zero"
	spec.TimeoutSeconds = 3
	r = m.Handle(protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: "output-command"})
	if r.Job == nil {
		t.Fatal(r.Error)
	}
	j = awaitJob(t, m, r.Job.ID)
	if !j.Truncated || len(j.Output) > 128<<10 {
		t.Fatal("output not bounded")
	}
}
func TestSettingsPersistAndDiagnosticsRedact(t *testing.T) {
	m := rootManager(t)
	s := m.Settings()
	s.FastIntervalSeconds = 12
	s.IncludeInterfaces = []string{"eth*"}
	if r := m.Handle(protocol.HostRequest{Action: "settings.put", Settings: &s}); r.Error != "" {
		t.Fatal(r.Error)
	}
	reopened, err := Open(context.Background(), m.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Settings().FastIntervalSeconds != 12 {
		t.Fatal("settings not persistent")
	}
	got := reopened.Settings()
	got.IncludeInterfaces[0] = "mutated"
	if reopened.Settings().IncludeInterfaces[0] != "eth*" {
		t.Fatal("settings alias escaped")
	}
	if !strings.Contains(reopened.Handle(protocol.HostRequest{Action: "diagnostics"}).Diagnostics["credential"].(string), "redacted") {
		t.Fatal("diagnostic credential")
	}
}

func TestRemoteControlPolicyRejectsNewScriptsAndCancelsRunning(t *testing.T) {
	m := rootManager(t)
	spec := protocol.CommandSpec{Script: "sleep 60", TimeoutSeconds: 60, Confirm: true}
	req := protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: "policy-command"}
	job := m.Handle(req)
	if job.Job == nil {
		t.Fatal(job.Error)
	}
	s := m.Settings()
	s.DisableRemoteControl = true
	if r := m.Handle(protocol.HostRequest{Action: "settings.put", Settings: &s}); r.Error != "" {
		t.Fatal(r.Error)
	}
	req.IdempotencyKey = "policy-other-command"
	if r := m.Handle(req); r.Error == "" {
		t.Fatal("disabled remote control ran a new script")
	}
	if j := awaitJob(t, m, job.Job.ID); j.Status != "cancelled" {
		t.Fatal(j)
	}
}
