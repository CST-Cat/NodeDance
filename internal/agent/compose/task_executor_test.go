package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type fakeComposeJournal struct {
	status    taskstate.Status
	finished  []taskstate.Status
	results   []taskjournal.Result
	unknowns  int
	baselines [][]byte
}

func (j *fakeComposeJournal) EnqueueDelivered(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error) {
	return taskjournal.EnqueueResult{Task: taskjournal.Snapshot{TaskID: "compose-1", Status: j.status}}, nil
}
func (j *fakeComposeJournal) BeginExecution(context.Context, string) error {
	j.status = taskstate.Running
	return nil
}
func (j *fakeComposeJournal) Get(context.Context, string) (taskjournal.Snapshot, error) {
	snapshot := taskjournal.Snapshot{TaskID: "compose-1", Status: j.status, Evidence: taskstate.Evidence{ExecutionAttempted: true}}
	if len(j.finished) > 0 {
		snapshot.Status = j.status
		snapshot.Result = j.result()
	}
	return snapshot, nil
}
func (j *fakeComposeJournal) Finish(_ context.Context, _ string, status taskstate.Status, _ taskstate.Evidence, result taskjournal.Result) error {
	j.status = status
	j.finished = append(j.finished, status)
	j.results = append(j.results, result)
	return nil
}
func (j *fakeComposeJournal) result() taskjournal.Result {
	if len(j.results) == 0 {
		return taskjournal.Result{}
	}
	return j.results[len(j.results)-1]
}
func (j *fakeComposeJournal) MarkUnknown(context.Context, string) error {
	j.unknowns++
	j.status = taskstate.Unknown
	return nil
}
func (j *fakeComposeJournal) MarkUnknownWithResult(_ context.Context, _ string, result taskjournal.Result) error {
	j.unknowns++
	j.status = taskstate.Unknown
	j.results = append(j.results, result)
	return nil
}
func (j *fakeComposeJournal) StoreComposeConfigBaselineIfAbsent(_ context.Context, _ string, files [][]byte) (bool, error) {
	j.baselines = files
	return true, nil
}
func (j *fakeComposeJournal) LoadComposeConfigBaseline(context.Context, string) ([][]byte, bool, error) {
	return j.baselines, len(j.baselines) > 0, nil
}
func (j *fakeComposeJournal) DeleteComposeConfigBaseline(context.Context, string) error { return nil }

func saveDispatch(ref protocol.ComposeProjectRef, content, base []byte) protocol.TaskDispatch {
	contentSum := sha256.Sum256(content)
	baseSum := sha256.Sum256(base)
	return protocol.TaskDispatch{
		TaskID: "compose-1", NodeID: "node-1", IdempotencyKey: "idem-1",
		Intent: protocol.TaskIntent{Action: protocol.TaskComposeSave, ContainerID: ref.Key, Compose: &protocol.ComposeTaskSpec{
			Project: ref, FileIndex: 0, ContentSHA256: hex.EncodeToString(contentSum[:]), BaseSHA256: hex.EncodeToString(baseSum[:]),
		}},
		ComposeContent: &protocol.ComposeContent{Content: content, SHA256: hex.EncodeToString(contentSum[:])},
	}
}

func TestComposeTaskExecutorRejectsPayloadDigestMismatch(t *testing.T) {
	root := t.TempDir()
	ref := composeTestRef("executor-gate", root)
	original := []byte("services: {}\n")
	if err := os.WriteFile(ref.ConfigFiles[0], original, 0600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{engine: &composeTestEngine{}, dockerPath: filepath.Join(root, "docker"), projectLocks: make(map[string]*projectLock)}
	executor, err := NewTaskExecutor(manager, &fakeComposeJournal{status: taskstate.Queued})
	if err != nil {
		t.Fatal(err)
	}
	dispatch := saveDispatch(ref, []byte("services: {web: {image: nginx}}\n"), original)
	tampered := dispatch.ComposeContent
	tampered.SHA256 = strings.Repeat("0", 64)
	result, err := executor.ExecuteCompose(context.Background(), dispatch, nil)
	if err != nil || result.Status != taskstate.Failed {
		t.Fatalf("digest gate outcome: %+v %v", result.Status, err)
	}
	if result.Result.ObservedState != "unchanged" || result.Result.Code != taskjournal.ResultFailed {
		t.Fatalf("digest gate classification: %+v", result.Result)
	}
	if _, err := os.Stat(ref.ConfigFiles[0]); err != nil {
		t.Fatal("rejected save disturbed the compose file")
	}
}

func TestComposeTaskExecutorSavesWithBaseline(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	ref := composeTestRef("executor-save", root)
	original := []byte("services: {}\n")
	if err := os.WriteFile(ref.ConfigFiles[0], original, 0600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "docker")
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{engine: &composeTestEngine{}, dockerPath: command, projectLocks: make(map[string]*projectLock)}
	journal := &fakeComposeJournal{status: taskstate.Queued}
	executor, err := NewTaskExecutor(manager, journal)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("services: {web: {image: nginx}}\n")
	result, err := executor.ExecuteCompose(ctx, saveDispatch(ref, content, original), nil)
	if err != nil || result.Status != taskstate.Succeeded {
		t.Fatalf("save outcome: %+v %v", result.Status, err)
	}
	if result.Result.ObservedState != "saved" {
		t.Fatalf("save classification: %+v", result.Result)
	}
	if len(journal.baselines) != 1 || string(journal.baselines[0]) != string(original) {
		t.Fatalf("the original file must be captured as the durable baseline: %v", journal.baselines)
	}
	updated, err := os.ReadFile(ref.ConfigFiles[0])
	if err != nil || string(updated) != string(content) {
		t.Fatalf("saved content did not land: %q %v", updated, err)
	}
}
