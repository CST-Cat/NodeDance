package hostcontrol

import (
	"context"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryCapacityCompactsWithoutReplayingAndPreservesUnknown(t *testing.T) {
	m := rootManager(t)
	req := protocol.HostRequest{Action: "commands.submit", IdempotencyKey: "capacity-original", Command: &protocol.CommandSpec{Script: "true", TimeoutSeconds: 3, Confirm: true}}
	first := m.Handle(req)
	if first.Job == nil {
		t.Fatal(first.Error)
	}
	completed := awaitJob(t, m, first.Job.ID)
	m.mu.Lock()
	// Capacity is the number of full-output files, not the number of retry IDs.
	for i := 1; i < maxCommandHistoryFiles; i++ {
		j := completed
		j.ID = fmt.Sprintf("%064x", i)
		j.StartedAt = completed.StartedAt.Add(time.Duration(i) * time.Nanosecond)
		m.history[j.ID] = j
	}
	result, err := m.cleanupLocked(time.Now())
	m.mu.Unlock()
	if err != nil || result.Archived < 128 {
		t.Fatal(result, err)
	}
	retry := m.Handle(req)
	if retry.Job == nil || retry.Job.ID != completed.ID || !retry.Job.Archived {
		t.Fatal("compacted retry identity lost", retry)
	}
	unknown := completed
	unknown.ID = strings.Repeat("f", 64)
	unknown.Status = "unknown"
	if err := localstate.Write(m.jobPath(unknown.ID), unknown); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.history[unknown.ID] = unknown
	result, err = m.cleanupLocked(time.Now().Add(366 * 24 * time.Hour))
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.jobPath(unknown.ID)); err != nil {
		t.Fatal("uncertain work expired", err)
	}
}

func TestResultPersistenceFailureReportsUnknownWithoutReplay(t *testing.T) {
	m := rootManager(t)
	started := filepath.Join(m.dir, "started")
	release := filepath.Join(m.dir, "release")
	marker := filepath.Join(m.dir, "marker")
	script := fmt.Sprintf("touch '%s'; while [ ! -e '%s' ]; do sleep 0.01; done; printf x >> '%s'", started, release, marker)
	req := protocol.HostRequest{Action: "commands.submit", IdempotencyKey: "failed-result-write", Command: &protocol.CommandSpec{Script: script, TimeoutSeconds: 3, Confirm: true}}
	r := m.Handle(req)
	if r.Job == nil {
		t.Fatal(r.Error)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, e := os.Stat(started); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("script did not start")
		}
		time.Sleep(time.Millisecond)
	}
	commands := filepath.Join(m.dir, "commands")
	saved := filepath.Join(m.dir, "saved")
	if err := os.Rename(commands, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, commands); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	j := awaitJob(t, m, r.Job.ID)
	if j.Status != "unknown" {
		t.Fatal("lost result misreported", j.Status)
	}
	os.Remove(commands)
	os.Rename(saved, commands)
	if retry := m.Handle(req); retry.Job == nil || retry.Job.Status != "unknown" {
		t.Fatal("uncertain retry accepted", retry)
	}
	m.Close()
	restarted, err := Open(context.Background(), m.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if retry := restarted.Handle(req); retry.Job == nil || retry.Job.Status != "interrupted" {
		t.Fatal("restart lost uncertain identity", retry)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "x" {
		t.Fatal("script replayed", string(data), err)
	}
}
