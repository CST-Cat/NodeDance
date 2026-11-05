package files

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestDirectoryTransferPublishesAndVerifiesTree(t *testing.T) {
	executor, _, root, state, descriptor := transferExecutorFixture(t)
	source := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "中文.txt"), []byte("directory payload\x00"), 0640); err != nil {
		t.Fatal(err)
	}
	first, entry, err := executor.OpenTransfer(context.Background(), "/tree", true)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(first)
	first.Close()
	if err != nil {
		t.Fatal(err)
	}
	second, again, err := executor.OpenTransfer(context.Background(), "/tree", true)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	if again.Version != entry.Version || again.Size != entry.Size {
		t.Fatal("archive changes between attempts")
	}
	private, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range private {
		if bytes.HasPrefix([]byte(item.Name()), []byte(".directory-transfer-")) {
			t.Fatal("source archive was left on disk")
		}
	}
	if err := os.Mkdir(filepath.Join(root, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	descriptor.File.Path, descriptor.File.Size, descriptor.File.SHA256 = "/out/tree", entry.Size, entry.Version
	descriptor.File.Transfer.Directory = true
	descriptor.File.Transfer.SourcePath, descriptor.File.Transfer.SourceVersion = "/tree", entry.Version
	done := executeTransferFixture(t, executor, descriptor)
	ctx := context.Background()
	_, stage, release, err := executor.AcquireRsync(ctx, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Payload.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	release()
	if err := executor.CommitRsync(ctx, descriptor, "relay"); err != nil {
		t.Fatal(err)
	}
	awaitTransferFixture(t, done, taskstate.Succeeded)
	data, err := os.ReadFile(filepath.Join(root, "out/tree/中文.txt"))
	if err != nil || string(data) != "directory payload\x00" {
		t.Fatal("tree differs", err)
	}
	info, err := os.Stat(filepath.Join(root, "out/tree/empty"))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0750 {
		t.Fatal("empty directory/mode lost", info, err)
	}
	verified, err := executor.verifyDirectoryTransfer(ctx, descriptor.File)
	if err != nil || !verified {
		t.Fatal("published tree does not match original archive", err)
	}
	if err := os.WriteFile(filepath.Join(root, "out/tree/中文.txt"), []byte("external change"), 0640); err != nil {
		t.Fatal(err)
	}
	verified, err = executor.verifyDirectoryTransfer(ctx, descriptor.File)
	if err != nil || verified {
		t.Fatal("changed tree incorrectly verified", err)
	}
	if _, err := executor.service.BeginUpload(descriptor.File.Path, "", entry.Size, entry.Version); err == nil {
		t.Fatal("existing directory can be overwritten")
	}
}

func TestDirectoryTransferRejectsUnexpectedRootsAndUnsafeMembers(t *testing.T) {
	for _, scenario := range []string{"other-root", "traversal", "symlink", "digest", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			executor, _, root, _, _ := transferExecutorFixture(t)
			var data bytes.Buffer
			archive := tar.NewWriter(&data)
			if err := archive.WriteHeader(&tar.Header{Name: "target", Mode: 0755, Typeflag: tar.TypeDir}); err != nil {
				t.Fatal(err)
			}
			name, kind := "other/file", byte(tar.TypeReg)
			if scenario == "traversal" {
				name = "target/../escape"
			}
			if scenario == "symlink" {
				name, kind = "target/link", tar.TypeSymlink
			}
			if scenario == "digest" || scenario == "canceled" {
				name = "target/file"
			}
			if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0644, Typeflag: kind, Linkname: map[bool]string{true: "/outside"}[kind == tar.TypeSymlink]}); err != nil {
				t.Fatal(err)
			}
			archive.Close()
			digest := sha256.Sum256(data.Bytes())
			upload, err := executor.service.BeginUpload("/target", "", int64(data.Len()), hex.EncodeToString(digest[:]))
			if err != nil {
				t.Fatal(err)
			}
			content := append([]byte(nil), data.Bytes()...)
			if scenario == "digest" {
				content[0] ^= 1
			}
			for len(content) > 0 {
				n := min(len(content), protocol.MaxFileChunkBytes)
				if err := upload.WriteChunk(content[:n]); err != nil {
					t.Fatal(err)
				}
				content = content[n:]
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			if err := upload.commitDirectory(ctx, "ndt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
				t.Fatal("unsafe directory published")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed transfer left visible or staged data", entries, err)
			}
		})
	}
	executor, _, root, _, _ := transferExecutorFixture(t)
	if err := os.Mkdir(filepath.Join(root, "source"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside", filepath.Join(root, "source/link")); err != nil {
		t.Fatal(err)
	}
	if file, _, err := executor.OpenTransfer(context.Background(), "/source", true); err == nil {
		file.Close()
		t.Fatal("source symlink packed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := executor.OpenTransfer(ctx, "/source", true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCanceledDirectoryTransferRetainsPartialAndRetries(t *testing.T) {
	executor, _, root, state, descriptor := transferExecutorFixture(t)
	if err := os.MkdirAll(filepath.Join(root, "source/empty"), 0755); err != nil {
		t.Fatal(err)
	}
	source, entry, err := executor.OpenTransfer(context.Background(), "/source", true)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(source)
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "out"), 0755); err != nil {
		t.Fatal(err)
	}
	descriptor.File.Path, descriptor.File.Size, descriptor.File.SHA256 = "/out/source", entry.Size, entry.Version
	descriptor.File.Transfer.Directory, descriptor.File.Transfer.SourceVersion = true, entry.Version
	done := executeTransferFixture(t, executor, descriptor)
	ctx := context.Background()
	_, stage, release, err := executor.AcquireRsync(ctx, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	partial := payload[:len(payload)/2]
	if _, err := stage.Payload.WriteAt(partial, 0); err != nil {
		t.Fatal(err)
	}
	executor.CancelFileTransfer(descriptor.TaskID)
	release()
	awaitTransferFixture(t, done, taskstate.Canceled)
	if _, err := os.Stat(filepath.Join(root, "out/source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("canceled directory published", err)
	}
	actual, err := os.ReadFile(filepath.Join(state, "file-transfers", descriptor.TaskID, "payload"))
	if err != nil || !bytes.Equal(actual, partial) {
		t.Fatal("directory partial lost", err)
	}
	retry := descriptor
	retry.TaskID = "ndt_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transfer := *descriptor.File.Transfer
	transfer.RetryOfTaskID = descriptor.TaskID
	retry.File.Transfer = &transfer
	next := executeTransferFixture(t, executor, retry)
	_, stage, release, err = executor.AcquireRsync(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	actual = make([]byte, len(partial))
	if _, err := stage.Payload.ReadAt(actual, 0); err != nil || !bytes.Equal(actual, partial) {
		t.Fatal("directory partial not adopted", err)
	}
	if _, err := stage.Payload.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	release()
	if err := executor.CommitRsync(ctx, retry, "direct"); err != nil {
		t.Fatal(err)
	}
	awaitTransferFixture(t, next, taskstate.Succeeded)
	if info, err := os.Stat(filepath.Join(root, "out/source/empty")); err != nil || !info.IsDir() {
		t.Fatal("retry did not unpack", err)
	}
}

func TestInterruptedDirectoryTransferReconcilesWithoutReplay(t *testing.T) {
	for _, scenario := range []string{"absent", "committed", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			executor, journal, root, _, descriptor := transferExecutorFixture(t)
			if err := os.MkdirAll(filepath.Join(root, "source/tree"), 0755); err != nil {
				t.Fatal(err)
			}
			source, entry, err := executor.OpenTransfer(context.Background(), "/source/tree", true)
			if err != nil {
				t.Fatal(err)
			}
			source.Close()
			descriptor.File.Path, descriptor.File.Size, descriptor.File.SHA256 = "/tree", entry.Size, entry.Version
			descriptor.File.Transfer.Directory = true
			descriptor.File.Transfer.SourcePath, descriptor.File.Transfer.SourceVersion = "/source/tree", entry.Version
			intent := protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey("/tree"), File: &descriptor.File}
			identity, err := protocol.TaskIdentity(descriptor.TaskID, descriptor.TargetNodeID, descriptor.TaskID, intent)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := journal.EnqueueDelivered(ctx, identity); err != nil {
				t.Fatal(err)
			}
			if err := journal.BeginExecution(ctx, descriptor.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.RecoverInterrupted(ctx); err != nil {
				t.Fatal(err)
			}
			want := taskstate.Failed
			if scenario != "absent" {
				if err := os.Mkdir(filepath.Join(root, "tree"), 0755); err != nil {
					t.Fatal(err)
				}
				want = taskstate.Succeeded
				if scenario == "changed" {
					if err := os.WriteFile(filepath.Join(root, "tree/external"), []byte("external"), 0600); err != nil {
						t.Fatal(err)
					}
					want = taskstate.Unknown
				}
			}
			snapshot, err := executor.ReconcileFile(ctx, descriptor.TaskID, intent)
			if err != nil || snapshot.Status != want {
				t.Fatal(snapshot, err)
			}
			if scenario == "absent" {
				if _, err := os.Stat(filepath.Join(root, "tree")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("replayed directory mutation", err)
				}
			}
		})
	}
}
