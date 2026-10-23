package update

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentRuntimeCanCheckUpdateAndRollBack(t *testing.T) {
	m, _, _ := nativeFixture(t)
	binary := m.Config.Targets["core"].Binary
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho NodeDance dev\n"), 0755); err != nil {
		t.Fatal(err)
	}
	check, err := m.Check(context.Background(), Request{Component: "core"})
	if err != nil || check.Current != "dev" || !check.Available || !check.Configured {
		t.Fatalf("development check: %+v %v", check, err)
	}
	job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "development-apply")
	if err != nil {
		t.Fatal(err)
	}
	m.workers.Wait()
	result, _ := m.Get("core", job.ID)
	if result.Status != "succeeded" {
		t.Fatalf("development update: %+v", result)
	}
	rollback, err := m.Apply(context.Background(), Request{Component: "core", JobID: job.ID, Confirm: true}, "development-rollback")
	if err != nil {
		t.Fatal(err)
	}
	m.workers.Wait()
	result, _ = m.Get("core", rollback.ID)
	current, err := m.current(context.Background(), "core")
	if result.Status != "succeeded" || current != "dev" || err != nil {
		t.Fatalf("development rollback: %+v %s %v", result, current, err)
	}
}

func TestBackupDeletionKeepsHistoryAndProtectsRuntime(t *testing.T) {
	m, _, running := nativeFixture(t)
	binary := m.Config.Targets["core"].Binary
	job, err := m.Apply(context.Background(), Request{Component: "core", Confirm: true}, "backup-delete-fixture")
	if err != nil {
		t.Fatal(err)
	}
	m.workers.Wait()
	original, _ := m.Get("core", job.ID)
	if original.Status != "succeeded" || original.Backup == "" {
		t.Fatalf("missing backup: %+v", original)
	}
	workspace := filepath.Join(m.Config.StateDir, job.ID)
	request := Request{Component: "core", JobID: job.ID, Confirm: true}
	invalid := request
	invalid.Confirm = false
	if _, err := m.DeleteBackup(context.Background(), invalid); err == nil {
		t.Fatal("deletion lacked confirmation")
	}
	invalid = request
	invalid.JobID = "../data"
	if _, err := m.DeleteBackup(context.Background(), invalid); err == nil {
		t.Fatal("traversal accepted")
	}
	m.active = true
	if _, err := m.DeleteBackup(context.Background(), request); err == nil {
		t.Fatal("deleted during an active update")
	}
	m.active = false
	saved := workspace + ".saved"
	if err := os.Rename(workspace, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(m.Config.Targets["core"].DataDir, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DeleteBackup(context.Background(), request); err == nil {
		t.Fatal("symlink workspace accepted")
	}
	os.Remove(workspace)
	os.Rename(saved, workspace)
	before, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := m.DeleteBackup(context.Background(), request)
	if err != nil || !deleted.BackupDeleted || deleted.BackupDeleting || deleted.Backup != "" {
		t.Fatalf("cleanup: %+v %v", deleted, err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("backup survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.Config.StateDir, job.ID+".json")); err != nil {
		t.Fatal("history was deleted", err)
	}
	after, err := os.ReadFile(binary)
	if err != nil || string(after) != string(before) || !running.Load() {
		t.Fatal("runtime changed")
	}
	if _, err := m.DeleteBackup(context.Background(), request); err != nil {
		t.Fatal("retry failed", err)
	}
	if _, err := m.Apply(context.Background(), Request{Component: "core", JobID: job.ID, Confirm: true}, "deleted-backup-rollback"); err == nil {
		t.Fatal("rollback accepted a deleted backup")
	}
	config, resolver := m.Config, m.Resolver
	m.Close()
	restored, err := New(config, resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	persisted, err := restored.Get("core", job.ID)
	if err != nil || !persisted.BackupDeleted {
		t.Fatalf("deletion state was lost: %+v %v", persisted, err)
	}
}

func TestBackupDeletionRefusesUnfinishedAndRecoveryJobs(t *testing.T) {
	m, _, _ := nativeFixture(t)
	for _, status := range []string{"queued", "running", "rollback_failed", "journal_failed"} {
		now := time.Now()
		id := strings.Repeat("b", 32)
		m.jobs[id] = Job{ID: id, Component: "core", Status: status, Finished: &now}
		if _, err := m.DeleteBackup(context.Background(), Request{Component: "core", JobID: id, Confirm: true}); err == nil {
			t.Fatalf("deleted %s recovery backup", status)
		}
	}
}

func TestBackupContainerDeletionCannotForceOrRemoveRunningAgent(t *testing.T) {
	for _, scenario := range []string{"active-target", "running-backup", "changed-name", "stopped-backup"} {
		t.Run(scenario, func(t *testing.T) {
			m, _, _ := nativeFixture(t)
			id := strings.Repeat("c", 32)
			now := time.Now()
			workspace := filepath.Join(m.Config.StateDir, id)
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			m.Config.Targets["agent"] = Target{Mode: "docker", Container: "fixture-agent"}
			m.jobs[id] = Job{ID: id, Component: "agent", Status: "succeeded", Stage: "complete", Finished: &now, OldContainer: "old-container"}
			deletes := 0
			m.Docker = &Docker{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"ApiVersion":"1.44"}`
				switch {
				case r.Method == "DELETE":
					deletes++
					if r.URL.Query().Get("force") != "false" {
						t.Fatal("cleanup forced container removal")
					}
					body = `{}`
				case strings.Contains(r.URL.Path, "fixture-agent/json"):
					body = `{"Id":"current-container"}`
					if scenario == "active-target" {
						body = `{"Id":"old-container"}`
					}
				case strings.Contains(r.URL.Path, "old-container/json"):
					name := "/fixture-agent.rollback-" + id
					running := false
					if scenario == "running-backup" {
						running = true
					}
					if scenario == "changed-name" {
						name = "/unrelated-container"
					}
					raw, _ := json.Marshal(map[string]any{"Name": name, "State": map[string]any{"Running": running}})
					body = string(raw)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			result, err := m.DeleteBackup(context.Background(), Request{Component: "agent", JobID: id, Confirm: true})
			if scenario == "stopped-backup" {
				if err != nil || !result.BackupDeleted || deletes != 1 {
					t.Fatalf("stopped backup cleanup: %+v %d %v", result, deletes, err)
				}
			} else {
				if err == nil || deletes != 0 {
					t.Fatalf("unsafe container deletion: %s %d %v", scenario, deletes, err)
				}
				if _, err := os.Stat(workspace); err != nil {
					t.Fatal("guard removed backup files", err)
				}
			}
		})
	}
}
