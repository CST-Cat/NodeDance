package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type fakeEngine struct {
	mu    sync.Mutex
	items map[string]agentdocker.Container
}

func (e *fakeEngine) ListAll(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.items))
	for id := range e.items {
		ids = append(ids, id)
	}
	return ids, nil
}

func (e *fakeEngine) Inspect(_ context.Context, id string) (agentdocker.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.items[id]
	if !ok {
		return agentdocker.Container{}, errors.New("not found")
	}
	return item, nil
}

type fakeRunner struct {
	mu        sync.Mutex
	args      [][]string
	output    []byte
	err       error
	actionErr error
	onAction  func([]string)
}

func (r *fakeRunner) Run(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, append([]string(nil), args...))
	if r.err != nil {
		return nil, r.err
	}
	if r.actionErr != nil && isComposeMutation(args) {
		return nil, r.actionErr
	}
	if r.onAction != nil && len(args) > 1 && args[len(args)-1] != "--quiet" && args[len(args)-1] != "json" && args[len(args)-1] != "--services" {
		r.onAction(args)
	}
	if strings.Contains(strings.Join(args, " "), "--format json") {
		return r.output, nil
	}
	return []byte("validated\n"), nil
}

func isComposeMutation(args []string) bool {
	for _, action := range []string{"up", "start", "stop", "restart", "down"} {
		if len(args) >= 2 && args[len(args)-1] == action {
			return true
		}
		if len(args) >= 2 && args[len(args)-2] == action && args[len(args)-1] == "--detach" {
			return true
		}
	}
	return false
}

func TestListGroupsProjectsBySourceContextAndKeepsInstances(t *testing.T) {
	first := testProject(t, "same-project", "first", []string{"compose.yaml"})
	second := testProject(t, "same-project", "second", []string{"compose.yaml"})
	engine := &fakeEngine{items: map[string]agentdocker.Container{
		"id-a": testContainer("id-a", "web", first, true, agentdocker.HealthNone),
		"id-b": testContainer("id-b", "web", first, true, agentdocker.HealthNone),
		"id-c": testContainer("id-c", "web", second, true, agentdocker.HealthNone),
	}}
	manager := testManager(t, engine, &fakeRunner{}, Options{})
	projects, err := manager.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("projects = %d, want two isolated source contexts", len(projects))
	}
	if projects[0].Ref.Key == projects[1].Ref.Key || projects[0].Ref.WorkingDirectory == projects[1].Ref.WorkingDirectory {
		t.Fatalf("same project/service names in different directories were merged: %+v", projects)
	}
	var firstView protocol.ComposeProject
	for _, project := range projects {
		if project.Ref.Key == first.Key {
			firstView = project
		}
	}
	if len(firstView.Services) != 1 || len(firstView.Services[0].Instances) != 2 {
		t.Fatalf("Compose service replicas were merged or lost: %+v", firstView)
	}
}

func TestListShowsMissingSourceAndKeepsContainerInventory(t *testing.T) {
	ref := testProject(t, "missing-source", "source", []string{"compose.yaml"})
	if err := os.Remove(ref.ConfigFiles[0]); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{items: map[string]agentdocker.Container{"id-a": testContainer("id-a", "web", ref, true, agentdocker.HealthNone)}}
	projects, err := testManager(t, engine, &fakeRunner{}, Options{}).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].ConfigAvailable || projects[0].ConfigReason != "config_missing" || len(projects[0].Services[0].Instances) != 1 {
		t.Fatalf("missing source must keep Docker-backed service inventory visible: %+v", projects)
	}
}

func TestExecuteUsesOriginalContextOrderedFilesAndVerifiesAllReplicas(t *testing.T) {
	ref := testProject(t, "special-project", "work dir; ' quoted", []string{"compose.yaml", "compose.override.yaml"})
	envFile := filepath.Join(ref.WorkingDirectory, "environment;file.env")
	if err := os.WriteFile(envFile, []byte("VALUE=hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{items: map[string]agentdocker.Container{
		"id-a": testContainer("id-a", "web", ref, true, agentdocker.HealthNone),
		"id-b": testContainer("id-b", "web", ref, true, agentdocker.HealthNone),
	}}
	runner := &fakeRunner{output: []byte(`{"services":{"web":{"deploy":{"replicas":2}}}}`)}
	manager := testManager(t, engine, runner, Options{VerifyTimeout: time.Second, PollInterval: 100 * time.Millisecond})
	request := protocol.ComposeRequest{OperationID: "compose-operation-1", Action: protocol.ComposeUp, Project: ref,
		EnvFiles: []string{envFile}, Profiles: []string{"with.dots", "worker"}}
	response, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("up with two healthy instances failed: %v", err)
	}
	if response.Status != "succeeded" || !response.Verified {
		t.Fatalf("operation did not return verified result: %+v", response)
	}
	runner.mu.Lock()
	got := runner.args
	runner.mu.Unlock()
	var upArgs []string
	for _, args := range got {
		if len(args) > 0 && args[len(args)-1] == "--detach" {
			upArgs = args
		}
	}
	want := []string{"compose", "--project-name", ref.Name, "--project-directory", ref.WorkingDirectory,
		"-f", ref.ConfigFiles[0], "-f", ref.ConfigFiles[1], "--env-file", envFile,
		"--profile", "with.dots", "--profile", "worker", "up", "--detach"}
	if !reflect.DeepEqual(upArgs, want) {
		t.Fatalf("up argv differs; want %#v, got %#v", want, upArgs)
	}
	// All Engine instances must satisfy the postcondition; one healthy replica
	// cannot mask an unhealthy sibling.
	engine.mu.Lock()
	item := engine.items["id-b"]
	item.HealthcheckConfigured, item.Health = true, agentdocker.HealthUnhealthy
	engine.items["id-b"] = item
	engine.mu.Unlock()
	if _, err := manager.Execute(context.Background(), request); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("unhealthy sibling replica returned %v, want a result that remains unconfirmed", err)
	}
}

func TestMutationCommandErrorRemainsUnknown(t *testing.T) {
	ref := testProject(t, "ambiguous-project", "ambiguous", []string{"compose.yaml"})
	engine := &fakeEngine{items: map[string]agentdocker.Container{"id-a": testContainer("id-a", "web", ref, false, agentdocker.HealthNone)}}
	runner := &fakeRunner{actionErr: context.DeadlineExceeded}
	manager := testManager(t, engine, runner, Options{})
	_, err := manager.Execute(context.Background(), protocol.ComposeRequest{OperationID: "uncertain-1", Action: protocol.ComposeUp, Project: ref})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("mutation command error = %v, want ErrOutcomeUnknown", err)
	}
	if errorCode(err) != "result_pending" {
		t.Fatalf("uncertain mutation error code = %q, want result_pending", errorCode(err))
	}
}

func TestUpPreservesExplicitlyScaledExistingInstancesWhenConfigHasNoReplicaCount(t *testing.T) {
	ref := testProject(t, "scaled-project", "scaled", []string{"compose.yaml"})
	engine := &fakeEngine{items: map[string]agentdocker.Container{
		"id-a": testContainer("id-a", "web", ref, true, agentdocker.HealthNone),
		"id-b": testContainer("id-b", "web", ref, true, agentdocker.HealthNone),
	}}
	runner := &fakeRunner{output: []byte(`{"services":{"web":{}}}`)}
	manager := testManager(t, engine, runner, Options{VerifyTimeout: time.Second, PollInterval: 100 * time.Millisecond})
	response, err := manager.Execute(context.Background(), protocol.ComposeRequest{OperationID: "scaled-up-1", Action: protocol.ComposeUp, Project: ref})
	if err != nil || !response.Verified || response.Status != "succeeded" {
		t.Fatalf("up discarded the known existing replica count: response=%+v err=%v", response, err)
	}
}

func TestDownDoesNotRemoveVolumesAndRequiresEngineRemoval(t *testing.T) {
	ref := testProject(t, "down-project", "down", []string{"compose.yaml"})
	engine := &fakeEngine{items: map[string]agentdocker.Container{"id-a": testContainer("id-a", "web", ref, true, agentdocker.HealthNone)}}
	runner := &fakeRunner{onAction: func(args []string) {
		if args[len(args)-1] == "down" {
			engine.mu.Lock()
			delete(engine.items, "id-a")
			engine.mu.Unlock()
		}
	}}
	manager := testManager(t, engine, runner, Options{VerifyTimeout: time.Second, PollInterval: 100 * time.Millisecond})
	response, err := manager.Execute(context.Background(), protocol.ComposeRequest{OperationID: "down-1", Action: protocol.ComposeDown, Project: ref})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "succeeded" || !response.Verified {
		t.Fatalf("down result not verified: %+v", response)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, args := range runner.args {
		for _, arg := range args {
			if arg == "--volumes" || arg == "-v" || arg == "--rmi" || arg == "--remove-orphans" {
				t.Fatalf("default down unexpectedly removes data or unrelated resources: %#v", args)
			}
		}
	}
}

func TestExecuteRejectsStaleSourceAndConfigFailureBeforeMutation(t *testing.T) {
	ref := testProject(t, "safe-project", "safe", []string{"compose.yaml"})
	engine := &fakeEngine{items: map[string]agentdocker.Container{"id-a": testContainer("id-a", "web", ref, true, agentdocker.HealthNone)}}
	runner := &fakeRunner{err: errors.New("invalid compose file contains secret output")}
	manager := testManager(t, engine, runner, Options{VerifyTimeout: time.Second, PollInterval: 100 * time.Millisecond})
	stale := ref
	stale.WorkingDirectory = filepath.Join(ref.WorkingDirectory, "other")
	stale.Key = protocol.ComposeProjectKey(stale.Name, stale.WorkingDirectory, stale.ConfigFiles)
	if _, err := manager.Execute(context.Background(), protocol.ComposeRequest{OperationID: "stale-1", Action: protocol.ComposeUp, Project: stale}); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("stale target returned %v", err)
	}
	if _, err := manager.Execute(context.Background(), protocol.ComposeRequest{OperationID: "invalid-1", Action: protocol.ComposeUp, Project: ref}); !errors.Is(err, ErrComposeFailed) {
		t.Fatalf("invalid configuration returned %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, args := range runner.args {
		if args[len(args)-1] == "--detach" {
			t.Fatal("Compose mutation ran after config validation failed")
		}
	}
}

func testManager(t *testing.T, engine Engine, runner CommandRunner, options Options) *Manager {
	t.Helper()
	manager, err := NewManager(engine, runner, options)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func testProject(t *testing.T, name, directoryName string, configNames []string) protocol.ComposeProjectRef {
	t.Helper()
	directory := filepath.Join(t.TempDir(), directoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(configNames))
	for _, configName := range configNames {
		file := filepath.Join(directory, configName)
		if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	ref := protocol.ComposeProjectRef{Name: name, WorkingDirectory: directory, ConfigFiles: files}
	ref.Key = protocol.ComposeProjectKey(name, directory, files)
	return ref
}

func testContainer(id, service string, ref protocol.ComposeProjectRef, running bool, health agentdocker.HealthState) agentdocker.Container {
	state := "exited"
	if running {
		state = "running"
	}
	return agentdocker.Container{ID: id, Name: "/" + service + "-" + id,
		State: state, Running: running, Health: health,
		Compose: &agentdocker.ComposeIdentity{Project: ref.Name, Service: service,
			WorkingDir: ref.WorkingDirectory, ConfigFiles: strings.Join(ref.ConfigFiles, ",")}}
}
