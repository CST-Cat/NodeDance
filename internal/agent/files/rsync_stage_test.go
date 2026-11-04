package files

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func transferExecutorFixture(t *testing.T) (*TaskExecutor, *taskjournal.Store, string, string, protocol.FileTransferDescriptor) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("private transfer state requires a root Agent")
	}
	root, state := t.TempDir(), t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	node := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	journal, err := taskjournal.Open(context.Background(), filepath.Join(state, "tasks.sqlite"), node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	executor, err := NewTaskExecutor(service, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.ConfigureTransfers(state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { executor.Close() })
	sum := sha256.Sum256([]byte("verified payload"))
	descriptor := protocol.FileTransferDescriptor{TaskID: "ndt_" + string(bytes.Repeat([]byte("a"), 32)), TargetNodeID: node,
		File: protocol.FileTaskSpec{Path: "/target", Size: int64(len("verified payload")), SHA256: hex.EncodeToString(sum[:]),
			Transfer: &protocol.FileTransferSpec{Mode: protocol.FileTransferModeRelay, SourceNodeID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", SourcePath: "/source", SourceVersion: "source-version"}}}
	return executor, journal, root, state, descriptor
}

func executeTransferFixture(t *testing.T, executor *TaskExecutor, descriptor protocol.FileTransferDescriptor) <-chan taskjournal.Snapshot {
	t.Helper()
	ctx := context.Background()
	done := make(chan taskjournal.Snapshot, 1)
	dispatch := protocol.TaskDispatch{TaskID: descriptor.TaskID, NodeID: descriptor.TargetNodeID, IdempotencyKey: descriptor.TaskID,
		Intent: protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(descriptor.File.Path), File: &descriptor.File}}
	go func() {
		snapshot, _ := executor.ExecuteFile(ctx, dispatch, nil)
		done <- snapshot
	}()
	t.Cleanup(func() { executor.CancelFileTransfer(descriptor.TaskID) })
	return done
}

func awaitTransferFixture(t *testing.T, done <-chan taskjournal.Snapshot, status taskstate.Status) taskjournal.Snapshot {
	t.Helper()
	select {
	case snapshot := <-done:
		if snapshot.Status != status {
			t.Fatalf("transfer ended as %s: %+v", snapshot.Status, snapshot)
		}
		return snapshot
	case <-time.After(5 * time.Second):
		t.Fatal("transfer worker did not finish")
		return taskjournal.Snapshot{}
	}
}

func TestRsyncCommitChecksDigestAndTargetVersion(t *testing.T) {
	for _, scenario := range []string{"valid", "digest", "version"} {
		t.Run(scenario, func(t *testing.T) {
			executor, _, root, _, descriptor := transferExecutorFixture(t)
			if scenario == "version" {
				if err := os.WriteFile(filepath.Join(root, "target"), []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				entry, err := executor.service.Stat("/target")
				if err != nil {
					t.Fatal(err)
				}
				descriptor.File.ExpectedVersion = entry.Version
			}
			done := executeTransferFixture(t, executor, descriptor)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, stage, release, err := executor.AcquireRsync(ctx, descriptor)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("verified payload")
			if scenario == "digest" {
				data[0] ^= 1
			}
			if _, err := stage.Payload.WriteAt(data, 0); err != nil {
				t.Fatal(err)
			}
			release()
			if scenario == "version" {
				if err := os.WriteFile(filepath.Join(root, "target"), []byte("external replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			commitErr := executor.CommitRsync(ctx, descriptor, "direct")
			status := taskstate.Succeeded
			if scenario != "valid" {
				status = taskstate.Failed
			}
			snapshot := awaitTransferFixture(t, done, status)
			actual, readErr := os.ReadFile(filepath.Join(root, "target"))
			switch scenario {
			case "valid":
				if commitErr != nil || readErr != nil || !bytes.Equal(actual, data) || snapshot.Result.ObservedState != "rsync_direct" {
					t.Fatal("verified commit", commitErr, readErr, snapshot)
				}
			case "digest":
				if !errors.Is(commitErr, ErrTransferDigest) || !errors.Is(readErr, os.ErrNotExist) || snapshot.Result.ResourceRevision != "digest_mismatch" {
					t.Fatal("bad digest reached destination", commitErr, readErr, snapshot)
				}
			case "version":
				if readErr != nil || string(actual) != "external replacement" || snapshot.Result.ResourceRevision != "version_conflict" {
					t.Fatal("external replacement lost", readErr, snapshot)
				}
			}
		})
	}
}

func TestCanceledPartialIsBoundToTheRetryDescriptor(t *testing.T) {
	executor, _, _, state, descriptor := transferExecutorFixture(t)
	done := executeTransferFixture(t, executor, descriptor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, stage, release, err := executor.AcquireRsync(ctx, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	partial := []byte("verified")
	if _, err := stage.Payload.WriteAt(partial, 0); err != nil {
		t.Fatal(err)
	}
	if err := stage.Payload.Sync(); err != nil {
		t.Fatal(err)
	}
	executor.CancelFileTransfer(descriptor.TaskID)
	release()
	awaitTransferFixture(t, done, taskstate.Canceled)
	retained := filepath.Join(state, "file-transfers", descriptor.TaskID, "payload")
	if actual, err := os.ReadFile(retained); err != nil || !bytes.Equal(actual, partial) {
		t.Fatal("partial was discarded", err)
	}
	retry := descriptor
	retry.TaskID = "ndt_" + string(bytes.Repeat([]byte("c"), 32))
	transfer := *descriptor.File.Transfer
	transfer.RetryOfTaskID = descriptor.TaskID
	retry.File.Transfer = &transfer
	next, err := executor.openRsyncStage(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
	if actual, err := os.ReadFile(filepath.Join(state, "file-transfers", retry.TaskID, "payload")); err != nil || !bytes.Equal(actual, partial) {
		t.Fatal("partial was not adopted", err)
	}
	retry.TaskID = "ndt_" + string(bytes.Repeat([]byte("d"), 32))
	retry.File.ExpectedVersion = "externally-changed-version"
	if stage, err := executor.openRsyncStage(ctx, retry); err == nil {
		stage.Close()
		t.Fatal("partial adopted after changing the original target version")
	}
	retry.TaskID = "ndt_" + string(bytes.Repeat([]byte("e"), 32))
	retry.File.ExpectedVersion = descriptor.File.ExpectedVersion
	retry.File.Path = "/different-target"
	if stage, err := executor.openRsyncStage(ctx, retry); err == nil {
		stage.Close()
		t.Fatal("partial adopted under a different destination")
	}
}

func TestPrivatePartialRejectsSymlinksAndHardlinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			executor, _, root, state, descriptor := transferExecutorFixture(t)
			ctx := context.Background()
			stage, err := executor.openRsyncStage(ctx, descriptor)
			if err != nil {
				t.Fatal(err)
			}
			stage.Close()
			name := filepath.Join(state, "file-transfers", descriptor.TaskID, "payload")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "protected")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(outside, name); err != nil {
				t.Fatal(err)
			}
			if stage, err := executor.openRsyncStage(ctx, descriptor); err == nil {
				stage.Close()
				t.Fatal("unsafe partial was accepted")
			}
			actual, err := os.ReadFile(outside)
			if err != nil || string(actual) != "untouched" {
				t.Fatal("unsafe partial changed an external file", err)
			}
		})
	}
}

func TestInterruptedTransferReconcilesActualFilesWithoutReplay(t *testing.T) {
	for _, scenario := range []string{"unchanged", "committed", "different"} {
		t.Run(scenario, func(t *testing.T) {
			executor, journal, root, _, descriptor := transferExecutorFixture(t)
			intent := protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(descriptor.File.Path), File: &descriptor.File}
			identity, err := protocol.TaskIdentity(descriptor.TaskID, descriptor.TargetNodeID, descriptor.TaskID, intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := journal.EnqueueDelivered(context.Background(), identity); err != nil {
				t.Fatal(err)
			}
			if err := journal.BeginExecution(context.Background(), descriptor.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.RecoverInterrupted(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := taskstate.Failed
			if scenario != "unchanged" {
				data := "verified payload"
				want = taskstate.Succeeded
				if scenario == "different" {
					data, want = "external replacement", taskstate.Unknown
				}
				if err := os.WriteFile(filepath.Join(root, "target"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			snapshot, err := executor.ReconcileFile(context.Background(), descriptor.TaskID, intent)
			if err != nil || snapshot.Status != want {
				t.Fatal(snapshot, err)
			}
			if scenario == "unchanged" {
				if _, err := os.Stat(filepath.Join(root, "target")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("reconciliation replayed a write", err)
				}
			}
		})
	}
}
