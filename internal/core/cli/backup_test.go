package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestBackupAndRestoreCommandsUseCoreBackupWorkflowWithoutPrintingSecrets(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "source")
	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	alertKey := []byte("0123456789abcdef0123456789abcdef")
	csrfKey := []byte("fedcba9876543210fedcba9876543210")
	setup := []byte("one-time-secret-cli-test\n")
	for name, contents := range map[string][]byte{
		"alert-channel-encryption.key": alertKey,
		"csrf-signing.key":             csrfKey,
		"setup-credential.txt":         setup,
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(t.TempDir(), "core.backup")
	var stdout, stderr bytes.Buffer
	lookup := func(string) (string, bool) { return "", false }
	if err := Run(context.Background(), []string{"backup", "--data-dir", dataDir, "--output", archive}, lookup, &stdout, &stderr, "test"); err != nil {
		t.Fatalf("backup CLI failed: %v stderr=%q", err, stderr.String())
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := Run(context.Background(), []string{"restore", "--input", archive, "--data-dir", restored}, lookup, &stdout, &stderr, "test"); err != nil {
		t.Fatalf("restore CLI failed: %v stderr=%q", err, stderr.String())
	}
	for _, secret := range []string{string(alertKey), string(csrfKey), string(setup)} {
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatalf("CLI output exposed backup secret material")
		}
	}
	if _, err := os.Stat(filepath.Join(restored, "nodedance.sqlite")); err != nil {
		t.Fatalf("CLI restore did not install Core database: %v", err)
	}
}
