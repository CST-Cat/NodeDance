package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

const backupTestNodeID = "00000000-0000-4000-8000-000000000011"
const backupTestAgentID = "00000000-0000-4000-8000-000000000022"

type backupFixture struct {
	dataDir string
	store   *storage.Store
	alert   []byte
	csrf    []byte
}

func newBackupFixture(t *testing.T) backupFixture {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "source")
	store, err := storage.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO nodes(id, display_name, status, created_at, updated_at)
		VALUES(?, 'backup node', 'offline', 100, 200)`, backupTestNodeID); err != nil {
		t.Fatal(err)
	}
	agentDigest := []byte("0123456789abcdef0123456789abcdef")
	if _, err := store.DB.Exec(`INSERT INTO agent_devices(id, node_id, credential_digest, created_at)
		VALUES(?, ?, ?, 250)`, backupTestAgentID, backupTestNodeID, agentDigest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO agent_credential_verifiers(digest, agent_id, state)
		VALUES(?, ?, 'active')`, agentDigest, backupTestAgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, 'argon2id-v1', 'backup admin', 300)`, []byte("password-hash"), []byte("password-salt")); err != nil {
		t.Fatal(err)
	}
	if err := (dashboard.Repository{DB: store.DB}).Put(context.Background(), dashboard.Preference{
		NodeID: backupTestNodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(backupTestNodeID),
		Alias: "Primary", Icon: "server", Notes: "restored preference", ServiceURL: "https://node.example.test",
		Visible: true, Pinned: true, SortOrder: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO metrics_minute(node_id, metric_key, bucket_at, sample_count, sample_sum, minimum, maximum)
		VALUES(?, 'cpu.usage_percent', 12345, 3, 75, 20, 30)`, backupTestNodeID); err != nil {
		t.Fatal(err)
	}
	alert := []byte("0123456789abcdef0123456789abcdef")
	csrf := []byte("fedcba9876543210fedcba9876543210")
	for name, contents := range map[string][]byte{alertKeyName: alert, csrfKeyName: csrf} {
		if err := os.WriteFile(filepath.Join(dataDir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dataDir, setupName), []byte("one-time-setup-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, tailscaleName), []byte(`{"hostKeyPins":{},"managedPeers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return backupFixture{dataDir: dataDir, store: store, alert: alert, csrf: csrf}
}

func TestBackupRestoreRoundTripPreservesDatabaseHistoryAndIdentityKeys(t *testing.T) {
	fixture := newBackupFixture(t)
	walInfo, err := os.Stat(filepath.Join(fixture.dataDir, databaseName+"-wal"))
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("fixture did not keep committed data in WAL: info=%v err=%v", walInfo, err)
	}

	archive := filepath.Join(t.TempDir(), "core.nodedance-backup")
	if err := Create(context.Background(), fixture.dataDir, archive); err != nil {
		t.Fatalf("create online backup: %v", err)
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil || archiveInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, err=%v; want 0600", archiveInfo, err)
	}
	_ = fixture.store.Close()

	restoredDir := filepath.Join(t.TempDir(), "new-core-data")
	if err := Restore(context.Background(), archive, restoredDir); err != nil {
		t.Fatalf("restore to a new directory: %v", err)
	}
	for name, want := range map[string][]byte{alertKeyName: fixture.alert, csrfKeyName: fixture.csrf} {
		got, err := os.ReadFile(filepath.Join(restoredDir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored %s differs: got=%x err=%v", name, got, err)
		}
		info, err := os.Stat(filepath.Join(restoredDir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("restored %s mode = %v, err=%v; want 0600", name, info, err)
		}
	}
	for _, name := range []string{setupName, tailscaleName} {
		if _, err := os.Stat(filepath.Join(restoredDir, name)); err != nil {
			t.Fatalf("optional Core state %s was not restored: %v", name, err)
		}
	}

	restored, err := storage.Open(context.Background(), restoredDir)
	if err != nil {
		t.Fatalf("reopen restored database: %v", err)
	}
	defer restored.Close()
	var nodeName, adminName string
	var hash, salt []byte
	if err := restored.DB.QueryRow(`SELECT display_name FROM nodes WHERE id=?`, backupTestNodeID).Scan(&nodeName); err != nil {
		t.Fatal(err)
	}
	if err := restored.DB.QueryRow(`SELECT display_name, password_hash, password_salt FROM admin_user WHERE id=1`).Scan(&adminName, &hash, &salt); err != nil {
		t.Fatal(err)
	}
	if nodeName != "backup node" || adminName != "backup admin" || string(hash) != "password-hash" || string(salt) != "password-salt" {
		t.Fatalf("restored node/admin identity differs: node=%q admin=%q hash=%q salt=%q", nodeName, adminName, hash, salt)
	}
	var agentNodeID string
	var agentDigest []byte
	if err := restored.DB.QueryRow(`SELECT node_id, credential_digest FROM agent_devices WHERE id=?`, backupTestAgentID).Scan(&agentNodeID, &agentDigest); err != nil {
		t.Fatal(err)
	}
	if agentNodeID != backupTestNodeID || string(agentDigest) != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("restored Agent identity differs: node=%q credential digest=%x", agentNodeID, agentDigest)
	}
	var verifierState string
	if err := restored.DB.QueryRow(`SELECT state FROM agent_credential_verifiers WHERE agent_id=?`, backupTestAgentID).Scan(&verifierState); err != nil || verifierState != "active" {
		t.Fatalf("restored Agent verifier state=%q err=%v", verifierState, err)
	}
	preferences, err := (dashboard.Repository{DB: restored.DB}).List(context.Background(), backupTestNodeID)
	if err != nil || len(preferences) != 1 || preferences[0].Alias != "Primary" || preferences[0].Icon != "server" ||
		preferences[0].Notes != "restored preference" || preferences[0].ServiceURL != "https://node.example.test" ||
		!preferences[0].Visible || !preferences[0].Pinned || preferences[0].SortOrder != 4 {
		t.Fatalf("restored preferences = %+v, err=%v", preferences, err)
	}
	var samples int
	if err := restored.DB.QueryRow(`SELECT sample_count FROM metrics_minute WHERE node_id=? AND metric_key='cpu.usage_percent'`, backupTestNodeID).Scan(&samples); err != nil || samples != 3 {
		t.Fatalf("restored metric history samples=%d err=%v", samples, err)
	}
	var integrity string
	if err := restored.DB.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("restored database integrity=%q err=%v", integrity, err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	emptyTarget := filepath.Join(t.TempDir(), "existing-empty-core-data")
	if err := os.Mkdir(emptyTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), archive, emptyTarget); err != nil {
		t.Fatalf("restore into an existing empty directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(emptyTarget, databaseName)); err != nil {
		t.Fatalf("existing empty directory was not atomically restored: %v", err)
	}
}

func TestBackupDoesNotOverwriteExistingOutput(t *testing.T) {
	fixture := newBackupFixture(t)
	output := filepath.Join(t.TempDir(), "existing.backup")
	want := []byte("keep this file")
	if err := os.WriteFile(output, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Create(context.Background(), fixture.dataDir, output); err == nil {
		t.Fatal("backup unexpectedly overwrote an existing output")
	}
	got, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("existing output changed: got=%q err=%v", got, err)
	}
}

func TestRestoreRejectsCorruptArchiveWithoutChangingEmptyTarget(t *testing.T) {
	fixture := newBackupFixture(t)
	validArchive := filepath.Join(t.TempDir(), "valid.backup")
	if err := Create(context.Background(), fixture.dataDir, validArchive); err != nil {
		t.Fatal(err)
	}
	tamperedArchive := filepath.Join(t.TempDir(), "tampered.backup")
	corruptEntry(t, validArchive, tamperedArchive, csrfKeyName)
	target := filepath.Join(t.TempDir(), "empty-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), tamperedArchive, target); err == nil {
		t.Fatal("restore accepted a bad file digest")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed restore changed empty target: entries=%v err=%v", entries, err)
	}
}

func TestRestoreRejectsSQLiteThatPassesArchiveDigestButFailsIntegrity(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad-database.backup")
	files := map[string][]byte{
		databaseName: []byte("this is not a SQLite database"),
		alertKeyName: []byte("0123456789abcdef0123456789abcdef"),
		csrfKeyName:  []byte("fedcba9876543210fedcba9876543210"),
	}
	writeTestArchive(t, archive, files, "", "")
	target := filepath.Join(t.TempDir(), "empty-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), archive, target); err == nil {
		t.Fatal("restore accepted a SQLite file that failed integrity_check")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed restore changed empty target: entries=%v err=%v", entries, err)
	}
}

func TestRestoreRejectsPathTraversalAndLeavesTargetUntouched(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "traversal.backup")
	files := map[string][]byte{
		databaseName:    []byte("placeholder"),
		alertKeyName:    []byte("0123456789abcdef0123456789abcdef"),
		csrfKeyName:     []byte("fedcba9876543210fedcba9876543210"),
		"../escape.txt": []byte("outside target"),
	}
	writeTestArchive(t, archive, files, "", "")
	parent := t.TempDir()
	target := filepath.Join(parent, "empty-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), archive, target); err == nil {
		t.Fatal("restore accepted a path-traversal entry")
	}
	if _, err := os.Stat(filepath.Join(parent, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("path-traversal entry escaped restore directory: err=%v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed restore changed empty target: entries=%v err=%v", entries, err)
	}
}

func TestRestoreRejectsLinkEntriesAndDuplicateEntries(t *testing.T) {
	fixture := newBackupFixture(t)
	valid := filepath.Join(t.TempDir(), "valid.backup")
	if err := Create(context.Background(), fixture.dataDir, valid); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink} {
		name := "symlink"
		if kind == tar.TypeLink {
			name = "hardlink"
		}
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), name+".backup")
			rewriteArchive(t, valid, archive, func(writer *tar.Writer, header *tar.Header, content []byte) {
				if header.Name == csrfKeyName {
					header.Typeflag = kind
					header.Linkname = "../../outside"
					header.Size = 0
					if err := writer.WriteHeader(header); err != nil {
						t.Fatal(err)
					}
					return
				}
				if err := writer.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(content); err != nil {
					t.Fatal(err)
				}
			})
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := Restore(context.Background(), archive, target); err == nil {
				t.Fatal("restore accepted a link archive entry")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected link entry changed target: entries=%v err=%v", entries, err)
			}
		})
	}

	duplicate := filepath.Join(t.TempDir(), "duplicate.backup")
	rewriteArchive(t, valid, duplicate, func(writer *tar.Writer, header *tar.Header, content []byte) {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
		if header.Name == alertKeyName {
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	})
	target := filepath.Join(t.TempDir(), "duplicate-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), duplicate, target); err == nil {
		t.Fatal("restore accepted a duplicate archive entry")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected duplicate entry changed target: entries=%v err=%v", entries, err)
	}
}

func TestRestoreInstallRejectsTargetCreatedAfterValidationWithoutReplacingData(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "new-target")
	initial, exists, err := inspectRestoreTarget(destination)
	if err != nil || exists {
		t.Fatalf("initial absent target inspection = (%v, %v, %v)", initial, exists, err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "created-during-restore.txt")
	want := []byte("concurrent data")
	if err := os.WriteFile(marker, want, 0o600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, "restore-stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installRestoreStage(stage, destination, initial, exists); err == nil {
		t.Fatal("restore target created during validation was accepted")
	}
	got, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("target race damaged concurrent data: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("failed target-race install consumed its staging directory: %v", err)
	}
}

func TestRestoreInstallRejectsReplacementEmptyDirectory(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "existing-target")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	initial, exists, err := inspectRestoreTarget(destination)
	if err != nil || !exists {
		t.Fatalf("initial target inspection = (%v, %v, %v)", initial, exists, err)
	}
	if err := os.Rename(destination, filepath.Join(parent, "original-empty-target")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, "restore-stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	stageMarker := filepath.Join(stage, "staged-data")
	if err := os.WriteFile(stageMarker, []byte("restore payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installRestoreStage(stage, destination, initial, exists); err == nil {
		t.Fatal("restore replaced an empty directory created after validation")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement empty target changed: entries=%v err=%v", entries, err)
	}
	if _, err := os.Stat(stageMarker); err != nil {
		t.Fatalf("failed replacement-target install consumed its staging data: %v", err)
	}
}

func TestRestoreInstallRejectsTargetThatBecameNonempty(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "existing-target")
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	initial, exists, err := inspectRestoreTarget(destination)
	if err != nil || !exists {
		t.Fatalf("initial target inspection = (%v, %v, %v)", initial, exists, err)
	}
	marker := filepath.Join(destination, "concurrent-data")
	want := []byte("preserve this target data")
	if err := os.WriteFile(marker, want, 0o600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(parent, "restore-stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := installRestoreStage(stage, destination, initial, exists); err == nil {
		t.Fatal("restore replaced a target that became nonempty")
	}
	got, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("target race damaged existing data: got=%q err=%v", got, err)
	}
}

func rewriteArchive(t *testing.T, input, output string, transform func(*tar.Writer, *tar.Header, []byte)) {
	t.Helper()
	source, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(target)
	reader := tar.NewReader(source)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		transform(writer, &copyHeader, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesNonemptyTargetWithoutChangingIt(t *testing.T) {
	fixture := newBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "valid.backup")
	if err := Create(context.Background(), fixture.dataDir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "existing-target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep.txt")
	want := []byte("existing user data")
	if err := os.WriteFile(marker, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), archive, target); err == nil {
		t.Fatal("restore accepted a nonempty target")
	}
	got, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("failed restore changed existing target data: got=%q err=%v", got, err)
	}
}

func corruptEntry(t *testing.T, input, output, name string) {
	t.Helper()
	source, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(target)
	reader := tar.NewReader(source)
	corrupted := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		if err := writer.WriteHeader(&copyHeader); err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == name {
			contents[0] ^= 0xff
			corrupted = true
		}
		if _, err := writer.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if !corrupted {
		t.Fatalf("archive entry %s not found", name)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestArchive(t *testing.T, output string, files map[string][]byte, pathOverride, digestOverride string) {
	t.Helper()
	entries := make([]manifestFile, 0, len(files))
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		archiveName := name
		if pathOverride != "" && name == databaseName {
			archiveName = pathOverride
		}
		digest := sha256.Sum256(files[name])
		digestText := hex.EncodeToString(digest[:])
		if digestOverride != "" && name == csrfKeyName {
			digestText = digestOverride
		}
		entries = append(entries, manifestFile{Path: archiveName, Size: int64(len(files[name])), SHA256: digestText})
	}
	manifestBytes, err := json.Marshal(manifest{Format: archiveFormat, Version: archiveVersion, Files: entries})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(file)
	write := func(name string, content []byte) {
		t.Helper()
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write(manifestName, manifestBytes)
	for _, name := range names {
		archiveName := name
		if pathOverride != "" && name == databaseName {
			archiveName = pathOverride
		}
		write(archiveName, files[name])
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveManifestChecksumIsValidated(t *testing.T) {
	fixture := newBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "bad-checksum.backup")
	if err := Create(context.Background(), fixture.dataDir, archive); err != nil {
		t.Fatal(err)
	}
	// Keep the archive structure valid while changing a key's payload without
	// changing its manifest hash.
	tampered := filepath.Join(t.TempDir(), "bad-checksum-tampered.backup")
	corruptEntry(t, archive, tampered, alertKeyName)
	target := filepath.Join(t.TempDir(), "restore-target")
	if err := Restore(context.Background(), tampered, target); err == nil {
		t.Fatal("restore accepted payload that did not match its manifest digest")
	}
}

func TestRestoreRejectsInvalidManifestDigest(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "invalid-digest.backup")
	files := map[string][]byte{
		databaseName: []byte("database"),
		alertKeyName: []byte("0123456789abcdef0123456789abcdef"),
		csrfKeyName:  []byte("fedcba9876543210fedcba9876543210"),
	}
	writeTestArchive(t, archive, files, "", strings.Repeat("z", sha256.Size*2))
	target := filepath.Join(t.TempDir(), "restore-target")
	if err := Restore(context.Background(), archive, target); err == nil {
		t.Fatal("restore accepted an invalid manifest digest")
	}
}
