package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corebackup "github.com/CST-Cat/NodeDance/internal/core/backup"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestCreateAndRestorePreservesCoreDatabaseAndKeys(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	dataDir := filepath.Join(workspace, "source")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatalf("open source Core data: %v", err)
	}
	defer source.Close()
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO admin_user(id,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,?,?,?,?,1)`, []byte("password-hash"), []byte("password-salt"), "test", "Backup Admin"); err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	if _, err := source.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('123e4567-e89b-42d3-a456-426614174000','Backup VPS','online',1,1)`); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	alertKey := []byte("0123456789abcdef0123456789abcdef")
	csrfKey := []byte("abcdef0123456789abcdef0123456789")
	for name, value := range map[string][]byte{
		"alert-channel-encryption.key": alertKey,
		"csrf-signing.key":             csrfKey,
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), value, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	archive := filepath.Join(workspace, "core-backup.tar")
	if err := corebackup.Create(ctx, dataDir, archive); err != nil {
		t.Fatalf("create backup while Core database is open: %v", err)
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if archiveInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode is %04o, want 0600", archiveInfo.Mode().Perm())
	}

	restoredDir := filepath.Join(workspace, "restored")
	if err := corebackup.Restore(ctx, archive, restoredDir); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	restored, err := storage.Open(ctx, restoredDir)
	if err != nil {
		t.Fatalf("open restored Core data: %v", err)
	}
	defer restored.Close()
	var displayName, nodeName string
	if err := restored.DB.QueryRowContext(ctx, `SELECT display_name FROM admin_user WHERE id=1`).Scan(&displayName); err != nil {
		t.Fatalf("read restored administrator: %v", err)
	}
	if err := restored.DB.QueryRowContext(ctx, `SELECT display_name FROM nodes WHERE id='123e4567-e89b-42d3-a456-426614174000'`).Scan(&nodeName); err != nil {
		t.Fatalf("read restored node: %v", err)
	}
	if displayName != "Backup Admin" || nodeName != "Backup VPS" {
		t.Fatalf("restored data mismatch: admin=%q node=%q", displayName, nodeName)
	}
	for name, want := range map[string][]byte{
		"alert-channel-encryption.key": alertKey,
		"csrf-signing.key":             csrfKey,
	} {
		got, err := os.ReadFile(filepath.Join(restoredDir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Fatalf("restored %s does not match source", name)
		}
	}
}
