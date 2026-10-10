package compose

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestLockProjectSerializesWritesByReference(t *testing.T) {
	manager := &Manager{projectLocks: make(map[string]*projectLock)}
	workingDirectory := filepath.Join(t.TempDir(), "project")
	ref := composeTestRef("serial-project", workingDirectory)
	other := composeTestRef("other-project", filepath.Join(t.TempDir(), "other"))

	firstRelease, err := manager.lockProject(context.Background(), ref)
	if err != nil {
		t.Fatalf("acquire first project lock: %v", err)
	}

	secondAcquired := make(chan func(), 1)
	attempting := make(chan struct{})
	go func() {
		close(attempting)
		release, lockErr := manager.lockProject(context.Background(), ref)
		if lockErr != nil {
			secondAcquired <- nil
			return
		}
		secondAcquired <- release
	}()
	<-attempting

	select {
	case release := <-secondAcquired:
		if release != nil {
			release()
		}
		t.Fatal("same-reference project lock was acquired concurrently")
	case <-time.After(25 * time.Millisecond):
	}

	otherRelease, err := manager.lockProject(context.Background(), other)
	if err != nil {
		t.Fatalf("different project reference was blocked: %v", err)
	}
	otherRelease()
	firstRelease()

	select {
	case release := <-secondAcquired:
		if release == nil {
			t.Fatal("second project lock failed after first lock was released")
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("second project lock did not proceed after first lock was released")
	}
}

func composeTestRef(name, workingDirectory string) protocol.ComposeProjectRef {
	configFiles := []string{filepath.Join(workingDirectory, "compose.yaml")}
	return protocol.ComposeProjectRef{
		Name: name, WorkingDirectory: workingDirectory, ConfigFiles: configFiles,
		Key: protocol.ComposeProjectKey(name, workingDirectory, configFiles),
	}
}

func TestRemoveTaskCreatedProjectInstancesFailsClosedForForeignContainers(t *testing.T) {
	for _, test := range []struct {
		name   string
		marker bool
	}{
		{name: "missing task marker"},
		{name: "different task marker", marker: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			workingDirectory := filepath.Join(t.TempDir(), "project")
			ref := composeTestRef("foreign-marker-project", workingDirectory)
			createdAt := time.Now().UTC()
			identity := &agentdocker.ComposeIdentity{
				Project: ref.Name, Service: "web", WorkingDir: ref.WorkingDirectory,
				ConfigFiles: ref.ConfigFiles[0], ContainerNumber: "1",
			}
			if test.marker {
				identity.TaskMarkerSeen = true
				identity.TaskMarkerValid = true
				identity.TaskID = "ndt_foreign_task_marker"
				identity.TaskRefKey = ref.Key
			}
			engine := &composeTestEngine{containers: map[string]agentdocker.Container{
				"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": {
					ID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Compose: identity, CreatedAt: &createdAt,
				},
			}}
			manager := &Manager{engine: engine, dockerPath: "/bin/false", projectLocks: make(map[string]*projectLock)}
			err := manager.removeTaskCreatedProjectInstances(context.Background(), ref, nil, createdAt.Add(-time.Second), "ndt_current_task_marker")
			if !errors.Is(err, ErrOperationUncertain) {
				t.Fatalf("foreign container must fail closed with ErrOperationUncertain, got %v", err)
			}
			if len(engine.containers) != 1 {
				t.Fatalf("foreign container set changed after fail-closed result: %#v", engine.containers)
			}
		})
	}
}

type composeTestEngine struct {
	containers map[string]agentdocker.Container
}

func (e *composeTestEngine) ListAll(context.Context) ([]string, error) {
	ids := make([]string, 0, len(e.containers))
	for id := range e.containers {
		ids = append(ids, id)
	}
	return ids, nil
}

func (e *composeTestEngine) Inspect(_ context.Context, id string) (agentdocker.Container, error) {
	container, ok := e.containers[id]
	if !ok {
		return agentdocker.Container{}, errors.New("test container not found")
	}
	return container, nil
}
