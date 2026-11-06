package audit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func newAuditDatabase(t *testing.T) *storage.Store {
	t.Helper()
	database, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("open Core database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestRecordPersistsAllowlistedEvents(t *testing.T) {
	database := newAuditDatabase(t)
	event := audit.Event{
		Action: "login", Outcome: "succeeded", RemoteAddr: "203.0.113.9",
		Target: audit.Target{Kind: audit.TargetAccount, ID: "42"},
	}
	if err := audit.Record(context.Background(), database.DB, event); err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	var action, outcome, remote string
	if err := database.DB.QueryRow(`SELECT action, outcome, remote_addr FROM audit_entries WHERE target_id='42'`).
		Scan(&action, &outcome, &remote); err != nil {
		t.Fatal(err)
	}
	if action != "login" || outcome != "succeeded" || remote != "203.0.113.9" {
		t.Fatalf("persisted event: action=%q outcome=%q remote=%q", action, outcome, remote)
	}
}

func TestRecordRejectsUnallowlistedFields(t *testing.T) {
	for name, event := range map[string]audit.Event{
		"unknown action":  {Action: "self_promote", Outcome: "succeeded"},
		"unknown outcome": {Action: "login", Outcome: "perfect"},
		"unknown target":  {Action: "login", Outcome: "succeeded", Target: audit.Target{Kind: "kernel", ID: "x"}},
		"non-uuid target": {Action: "login", Outcome: "succeeded", Target: audit.Target{Kind: audit.TargetNode, ID: "not-a-uuid"}},
		"empty target id": {Action: "login", Outcome: "succeeded", Target: audit.Target{Kind: audit.TargetNode, ID: ""}},
	} {
		if err := audit.Record(context.Background(), newAuditDatabase(t).DB, event); err == nil {
			t.Fatalf("unallowlisted event was accepted: %s", name)
		}
	}
}

func TestRecordSanitizesRemoteAddress(t *testing.T) {
	database := newAuditDatabase(t)
	event := audit.Event{Action: "logout", Outcome: "succeeded", RemoteAddr: "not-an-address\r\ninjected"}
	if err := audit.Record(context.Background(), database.DB, event); err != nil {
		t.Fatal(err)
	}
	var remote string
	if err := database.DB.QueryRow(`SELECT remote_addr FROM audit_entries WHERE action='logout'`).Scan(&remote); err != nil {
		t.Fatal(err)
	}
	if remote != "unknown" || strings.Contains(remote, "\n") {
		t.Fatalf("unparseable remote address must collapse to unknown, got %q", remote)
	}
}

func TestFileTargetEscapesForgedPaths(t *testing.T) {
	forged := audit.FileTarget("123e4567-e89b-42d3-a456-426614174000", "123e4567-e89b-42d3-a456-426614174001", "/data/evil:record:more")
	if !strings.Contains(forged, "%3A") {
		t.Fatalf("path separators inside the target must be percent-escaped: %q", forged)
	}
	parts := strings.SplitN(forged, ":", 3)
	if len(parts) != 3 {
		t.Fatalf("escaped path must not introduce new segments: %q", forged)
	}
	if err := audit.Record(context.Background(), newAuditDatabase(t).DB, audit.Event{
		Action: "login", Outcome: "succeeded", RemoteAddr: "unknown",
		Target: audit.Target{Kind: audit.TargetFile, ID: forged},
	}); err != nil {
		t.Fatalf("escaped file target was rejected: %v", err)
	}
	// A decoded NUL byte inside the path is refused outright.
	if err := audit.Record(context.Background(), newAuditDatabase(t).DB, audit.Event{
		Action: "login", Outcome: "succeeded", RemoteAddr: "unknown",
		Target: audit.Target{Kind: audit.TargetFile, ID: audit.FileTarget("123e4567-e89b-42d3-a456-426614174000", "123e4567-e89b-42d3-a456-426614174001", "/data/\x00evil")},
	}); err == nil {
		t.Fatal("a file target with a decoded NUL byte was accepted")
	}
}
