package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/agent/filejournal"
	agentfiles "github.com/CST-Cat/NodeDance/internal/agent/files"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAgentUploadJournalRecoversBeforeAndAfterTemporaryCreation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "agent-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(stateDir, "file-writes.sqlite")
	service, err := agentfiles.New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := filejournal.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}

	beforeID := "00000000-0000-4000-8000-000000000061"
	beforeTemporary := beginUploadIntent(t, ctx, service, journal, beforeID)
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(beforeTemporary[1:]))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-create crash fixture unexpectedly has a temp file: %v", err)
	}

	afterID := "00000000-0000-4000-8000-000000000062"
	afterTemporary := beginUploadIntent(t, ctx, service, journal, afterID)
	file, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(afterTemporary[1:])), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal("create temp file at persisted reservation:", err)
	}
	if _, err := file.WriteString("partial upload body"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	lookalike := filepath.Join(root, "...draft.bin.nodedance-upload-user-file")
	if err := os.WriteFile(lookalike, []byte("keep user file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = agentfiles.New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal("reopen Agent file service:", err)
	}
	defer service.Close()
	journal, err = filejournal.Open(ctx, databasePath)
	if err != nil {
		t.Fatal("reopen Agent file journal:", err)
	}
	defer journal.Close()
	if err := recoverAgentFileUploads(ctx, service, journal); err != nil {
		t.Fatal("recover interrupted Agent uploads:", err)
	}
	for _, temporary := range []string{beforeTemporary, afterTemporary} {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(temporary[1:]))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recovery left owned upload temp %s: %v", temporary, err)
		}
	}
	if content, err := os.ReadFile(lookalike); err != nil || string(content) != "keep user file" {
		t.Fatalf("recovery removed or changed a non-owned lookalike: content=%q err=%v", content, err)
	}
	for _, taskID := range []string{beforeID, afterID} {
		record, err := journal.Get(ctx, taskID)
		if err != nil || record.Status != filejournal.Canceled || record.ResultCode != "cancel_confirmed" {
			t.Fatalf("recovered upload %s state=%+v err=%v; want confirmed cancellation", taskID, record, err)
		}
	}
}

func beginUploadIntent(t *testing.T, ctx context.Context, service *agentfiles.Service, journal *filejournal.Store, taskID string) string {
	t.Helper()
	const target = "/..draft.bin"
	temporary, err := service.ReserveUploadTemporaryPath(target)
	if err != nil {
		t.Fatal("reserve upload path:", err)
	}
	baseline, err := service.CaptureMutationBaseline(protocol.FileUploadBegin, target, "")
	if err != nil {
		t.Fatal("capture upload baseline:", err)
	}
	digest := sha256.Sum256(nil)
	intent := filejournal.Intent{TaskID: taskID, Operation: "upload", TargetPath: target, ExpectedSize: 0,
		ContentSHA256: hex.EncodeToString(digest[:]), BaselineCaptured: true, TemporaryPath: temporary, TemporaryOwned: true,
		BeforeTargetExists: baseline.TargetExists, BeforeTargetFingerprint: baseline.TargetFingerprint}
	if _, created, err := journal.Begin(ctx, intent); err != nil || !created {
		t.Fatalf("persist upload path reservation before file creation: created=%t err=%v", created, err)
	}
	return temporary
}
