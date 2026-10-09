package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSupervisorRestartsAndConfirmsPreparedCandidate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Agent updates target Linux")
	}
	state, old, candidate := prepareSupervisorVersions(t)
	marker := filepath.Join(state, "candidate-started")
	prepared := Journal{
		State: "prepared", OldTarget: filepath.Join("versions", "old", "nodedance-agent"),
		CandidateTarget: filepath.Join("versions", "candidate", "nodedance-agent"), Version: "candidate",
	}
	oldScript := "#!/bin/sh\nprintf '%s' '" + marshalJournalForShell(t, prepared) + "' > " + shellQuote(JournalPath(state)) + "\nexit 0\n"
	writeProgram(t, old, oldScript)
	writeProgram(t, candidate, "#!/bin/sh\ntouch "+shellQuote(marker)+"\nexec /bin/sleep 30\n")
	setCurrent(t, state, prepared.OldTarget)

	cancel := startTestSupervisor(t, state)
	waitForFile(t, marker)
	j, err := ReadJournal(state)
	if err != nil || j.State != "awaiting_confirmation" {
		t.Fatalf("candidate journal = %#v, %v", j, err)
	}
	if err := ConfirmStartup(state, "candidate"); err != nil {
		t.Fatal(err)
	}
	waitForJournalRemoval(t, state)
	cancel()
	assertCurrent(t, state, prepared.CandidateTarget)
}

func TestSupervisorRollsBackWhenCandidateCannotExec(t *testing.T) {
	state, old, candidate := prepareSupervisorVersions(t)
	oldTarget := filepath.Join("versions", "old", "nodedance-agent")
	candidateTarget := filepath.Join("versions", "candidate", "nodedance-agent")
	marker := filepath.Join(state, "old-started")
	writeProgram(t, old, "#!/bin/sh\ntouch "+shellQuote(marker)+"\nexec /bin/sleep 30\n")
	if err := os.WriteFile(candidate, []byte("this is not an executable format\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setCurrent(t, state, oldTarget)
	if err := WriteJournal(state, Journal{State: "prepared", OldTarget: oldTarget, CandidateTarget: candidateTarget, Version: "candidate"}); err != nil {
		t.Fatal(err)
	}

	cancel := startTestSupervisor(t, state)
	waitForFile(t, marker)
	assertCurrent(t, state, oldTarget)
	if _, err := os.Stat(JournalPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed candidate left an update journal: %v", err)
	}
	cancel()
}

func TestSupervisorRollsBackCandidateThatExitsBeforeConfirmation(t *testing.T) {
	state, old, candidate := prepareSupervisorVersions(t)
	oldTarget := filepath.Join("versions", "old", "nodedance-agent")
	candidateTarget := filepath.Join("versions", "candidate", "nodedance-agent")
	oldMarker := filepath.Join(state, "old-started")
	candidateMarker := filepath.Join(state, "candidate-started")
	writeProgram(t, old, "#!/bin/sh\ntouch "+shellQuote(oldMarker)+"\nexec /bin/sleep 30\n")
	writeProgram(t, candidate, "#!/bin/sh\ntouch "+shellQuote(candidateMarker)+"\nexit 17\n")
	setCurrent(t, state, oldTarget)
	if err := WriteJournal(state, Journal{State: "prepared", OldTarget: oldTarget, CandidateTarget: candidateTarget, Version: "candidate"}); err != nil {
		t.Fatal(err)
	}

	cancel := startTestSupervisor(t, state)
	waitForFile(t, candidateMarker)
	waitForFile(t, oldMarker)
	assertCurrent(t, state, oldTarget)
	if _, err := os.Stat(JournalPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("early-exiting candidate left an update journal: %v", err)
	}
	cancel()
}

func TestSupervisorStartupConfirmedKeepsCandidate(t *testing.T) {
	state, _, candidate := prepareSupervisorVersions(t)
	candidateTarget := filepath.Join("versions", "candidate", "nodedance-agent")
	marker := filepath.Join(state, "candidate-started")
	writeProgram(t, candidate, "#!/bin/sh\ntouch "+shellQuote(marker)+"\nexec /bin/sleep 30\n")
	setCurrent(t, state, candidateTarget)
	if err := WriteJournal(state, Journal{State: "confirmed", OldTarget: filepath.Join("versions", "old", "nodedance-agent"), CandidateTarget: candidateTarget, Version: "candidate"}); err != nil {
		t.Fatal(err)
	}

	cancel := startTestSupervisor(t, state)
	waitForFile(t, marker)
	assertCurrent(t, state, candidateTarget)
	if _, err := os.Stat(JournalPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed journal was not cleared: %v", err)
	}
	cancel()
}

func prepareSupervisorVersions(t *testing.T) (state, old, candidate string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Agent updates target Linux")
	}
	state = t.TempDir()
	old = filepath.Join(state, "bin", "versions", "old", "nodedance-agent")
	candidate = filepath.Join(state, "bin", "versions", "candidate", "nodedance-agent")
	for _, path := range []string{old, candidate} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return state, old, candidate
}

func writeProgram(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
}

func setCurrent(t *testing.T, state, target string) {
	t.Helper()
	current := filepath.Join(state, "bin", "current")
	if err := os.Symlink(target, current); err != nil {
		t.Fatal(err)
	}
}

func startTestSupervisor(t *testing.T, state string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Supervisor(ctx, state, "/unused/test-config.json") }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Supervisor returned error: %v", err)
			}
		case <-time.After(8 * time.Second):
			t.Error("Supervisor did not stop after cancellation")
		}
	})
	return cancel
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForJournalRemoval(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(JournalPath(state)); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("confirmed update journal was not removed")
}

func assertCurrent(t *testing.T, state, expected string) {
	t.Helper()
	target, err := os.Readlink(filepath.Join(state, "bin", "current"))
	if err != nil || target != expected {
		t.Fatalf("current target = %q, want %q (err %v)", target, expected, err)
	}
}

func marshalJournalForShell(t *testing.T, journal Journal) string {
	t.Helper()
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestSupervisorHelperEnvironmentIsSingular(t *testing.T) {
	got := withHelperEnvironment([]string{"PATH=/bin", HelperEnvironment + "=0", HelperEnvironment + "=old", "LANG=C"})
	count := 0
	for _, variable := range got {
		if variable == HelperEnvironment+"=1" {
			count++
		}
		if variable == HelperEnvironment+"=0" || variable == HelperEnvironment+"=old" {
			t.Fatalf("stale helper marker retained: %q", variable)
		}
	}
	if count != 1 {
		t.Fatalf("helper marker count = %d, want 1", count)
	}
}

func TestSupervisorRecoveryKeepsConfirmedCurrent(t *testing.T) {
	state, _, _ := prepareSupervisorVersions(t)
	candidateTarget := filepath.Join("versions", "candidate", "nodedance-agent")
	setCurrent(t, state, candidateTarget)
	if err := WriteJournal(state, Journal{State: "confirmed", OldTarget: filepath.Join("versions", "old", "nodedance-agent"), CandidateTarget: candidateTarget, Version: "candidate"}); err != nil {
		t.Fatal(err)
	}
	if err := recoverJournalAtBoot(state); err != nil {
		t.Fatal(err)
	}
	assertCurrent(t, state, candidateTarget)
	if _, err := os.Stat(JournalPath(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed startup record remains: %v", err)
	}
}
