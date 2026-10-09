package images

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
)

const imageTestNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"

type fakeEngine struct {
	mu          sync.Mutex
	images      map[string]Image
	refs        map[string][]string
	pulls       int
	removes     int
	pullErr     error
	authSeen    string
	waitPull    bool
	pullStarted chan struct{}
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{images: make(map[string]Image), refs: make(map[string][]string)}
}
func (f *fakeEngine) List(context.Context) ([]Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Image, 0, len(f.images))
	for _, image := range f.images {
		image.Containers = len(f.refs[image.ID])
		out = append(out, image)
	}
	return out, nil
}
func (f *fakeEngine) Inspect(_ context.Context, id string) (Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if image, ok := f.images[id]; ok {
		image.Containers = len(f.refs[image.ID])
		return image, nil
	}
	if normalized, err := reference.ParseNormalizedNamed(id); err == nil {
		if image, ok := f.images[reference.TagNameOnly(normalized).String()]; ok {
			image.Containers = len(f.refs[image.ID])
			return image, nil
		}
	}
	return Image{}, errdefs.ErrNotFound.WithMessage("missing")
}
func (f *fakeEngine) Pull(ctx context.Context, ref, auth string, report func(PullProgress)) error {
	f.mu.Lock()
	f.pulls++
	f.authSeen = auth
	failure := f.pullErr
	wait := f.waitPull
	started := f.pullStarted
	f.mu.Unlock()
	if wait {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	if report != nil {
		report(PullProgress{Completed: 2, Total: 4})
	}
	if failure != nil {
		return failure
	}
	named, _ := reference.ParseNormalizedNamed(ref)
	canonical := reference.TagNameOnly(named).String()
	f.mu.Lock()
	f.images[canonical] = Image{ID: "sha256:" + strings.Repeat("a", 64), Tags: []string{canonical}, Size: 4}
	f.mu.Unlock()
	return nil
}
func (f *fakeEngine) ContainersUsing(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refs[id]...), nil
}
func (f *fakeEngine) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	if len(f.refs[id]) != 0 {
		return ErrImageInUse
	}
	for key, image := range f.images {
		if image.ID == id {
			delete(f.images, key)
		}
	}
	return nil
}

func openImageJournal(t *testing.T) *taskjournal.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	journal, err := taskjournal.Open(context.Background(), filepath.Join(directory, "tasks.sqlite"), imageTestNodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func enqueueImageDispatch(t *testing.T, journal *taskjournal.Store, intent protocol.TaskIntent, key string, auth *protocol.RegistryCredentials) protocol.TaskDispatch {
	t.Helper()
	id := "task-" + key
	identity, err := protocol.TaskIdentity(id, imageTestNodeID, key, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.EnqueueDelivered(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.TaskRequestDigest(id, imageTestNodeID, key, intent)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.TaskDispatch{TaskID: id, NodeID: imageTestNodeID, JournalID: journal.JournalID(), TargetID: intent.ContainerID,
		IdempotencyKey: key, RequestDigest: protocol.DigestString(digest), Intent: intent, RegistryAuth: auth}
}

func TestPullUsesEphemeralCredentialsAndVerifiesEngineState(t *testing.T) {
	journal := openImageJournal(t)
	engine := newFakeEngine()
	executor, err := NewExecutor(engine, journal)
	if err != nil {
		t.Fatal(err)
	}
	ref := "nginx"
	intent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + ref), ImageReference: ref}
	credentials := &protocol.RegistryCredentials{Username: "private-user", Password: "private-password"}
	dispatch := enqueueImageDispatch(t, journal, intent, "pull-ok", credentials)
	progressCalls := 0
	task, err := executor.ExecuteImage(context.Background(), dispatch, func() { progressCalls++ })
	if err != nil || task.Status != taskstate.Succeeded || task.Result.ObservedState != "present" {
		t.Fatalf("pull task = %+v, err=%v", task, err)
	}
	if engine.pulls != 1 || progressCalls == 0 || engine.authSeen == "" {
		t.Fatalf("pull was not executed with progress/auth: pulls=%d progress=%d auth=%q", engine.pulls, progressCalls, engine.authSeen)
	}
	if credentials.Username != "" || credentials.Password != "" {
		t.Fatal("decoded credential fields remained in the dispatch after execution")
	}
	if strings.Contains(engine.authSeen, "private-user") || strings.Contains(engine.authSeen, "private-password") {
		t.Fatal("Docker auth header unexpectedly contains plaintext credentials")
	}
	entry, err := journal.Get(context.Background(), task.TaskID)
	if err != nil || entry.Status != taskstate.Succeeded {
		t.Fatalf("durable pull journal = %+v, err=%v", entry, err)
	}
}

func TestInterruptedPullBecomesUnknownThenReadOnlyReconciles(t *testing.T) {
	journal := openImageJournal(t)
	engine := newFakeEngine()
	engine.pullErr = context.Canceled
	executor, _ := NewExecutor(engine, journal)
	ref := "registry.example.test/team/service:v1"
	intent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + ref), ImageReference: ref}
	dispatch := enqueueImageDispatch(t, journal, intent, "pull-interrupted", nil)
	task, err := executor.ExecuteImage(context.Background(), dispatch, nil)
	if !errors.Is(err, ErrOutcomeUnknown) || task.Status != taskstate.Unknown {
		t.Fatalf("interrupted pull = %+v, err=%v", task, err)
	}
	task, err = executor.ReconcileImage(context.Background(), dispatch.TaskID, intent)
	if err != nil || task.Status != taskstate.Failed || task.Result.ObservedState != "absent_after_pull" {
		t.Fatalf("read-only pull reconcile = %+v, err=%v", task, err)
	}
	if engine.pulls != 1 {
		t.Fatalf("reconciliation repeated the pull %d times", engine.pulls)
	}
}

func TestImageReferenceVerificationUsesDockerDistributionNormalization(t *testing.T) {
	image := Image{ID: "sha256:" + strings.Repeat("a", 64), Tags: []string{"docker.io/library/nginx:latest"}}
	if !imageMatchesReference(image, "nginx") {
		t.Fatal("short Docker Hub reference did not match its canonical Engine tag")
	}
	if imageMatchesReference(image, "example.test/other:latest") {
		t.Fatal("unrelated normalized reference matched the image")
	}
}

func TestExplicitPullCancellationWaitsForStoppedEngineAndConfirmedState(t *testing.T) {
	journal := openImageJournal(t)
	engine := newFakeEngine()
	engine.waitPull = true
	engine.pullStarted = make(chan struct{})
	executor, _ := NewExecutor(engine, journal)
	ref := "registry.example.test/team/service:v1"
	intent := protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: protocol.ImageTargetKey("pull:" + ref), ImageReference: ref}
	dispatch := enqueueImageDispatch(t, journal, intent, "pull-canceled", nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	type result struct {
		task taskjournal.Snapshot
		err  error
	}
	finished := make(chan result, 1)
	go func() {
		task, err := executor.ExecuteImage(ctx, dispatch, nil)
		finished <- result{task: task, err: err}
	}()
	<-engine.pullStarted
	cancel(taskstate.ErrCancellationRequested)
	completed := <-finished
	task, err := completed.task, completed.err
	if err != nil || task.Status != taskstate.Canceled || task.Result.ObservedState != "absent_after_cancel" {
		t.Fatalf("confirmed canceled pull = %+v, err=%v", task, err)
	}
	if !task.Evidence.CancellationConfirmed || !task.Evidence.ProcessTerminated || !task.Evidence.ActualResultConfirmed {
		t.Fatalf("cancellation evidence is incomplete: %+v", task.Evidence)
	}
	if engine.pulls != 1 {
		t.Fatalf("canceled pull executed %d times", engine.pulls)
	}
}

func TestDeleteRefusesReferencedImageAndVerifiesSuccessfulRemoval(t *testing.T) {
	journal := openImageJournal(t)
	engine := newFakeEngine()
	imageID := "sha256:" + strings.Repeat("b", 64)
	engine.images[imageID] = Image{ID: imageID, Tags: []string{"example.test/app:latest"}}
	engine.refs[imageID] = []string{strings.Repeat("c", 64)}
	executor, _ := NewExecutor(engine, journal)
	intent := protocol.TaskIntent{Action: protocol.TaskImageDelete, ContainerID: protocol.ImageTargetKey("delete:" + imageID), ImageID: imageID}
	dispatch := enqueueImageDispatch(t, journal, intent, "delete-in-use", nil)
	task, err := executor.ExecuteImage(context.Background(), dispatch, nil)
	if err != nil || task.Status != taskstate.Failed || task.Result.ObservedState != "in_use" || engine.removes != 0 {
		t.Fatalf("referenced image delete = %+v err=%v removes=%d", task, err, engine.removes)
	}
	engine.refs[imageID] = nil
	dispatch = enqueueImageDispatch(t, journal, intent, "delete-unused", nil)
	task, err = executor.ExecuteImage(context.Background(), dispatch, nil)
	if err != nil || task.Status != taskstate.Succeeded || task.Result.ObservedState != "absent" || engine.removes != 1 {
		t.Fatalf("unused image delete = %+v err=%v removes=%d", task, err, engine.removes)
	}
}
