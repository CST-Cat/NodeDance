package composeedit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"gopkg.in/yaml.v3"
)

type editEngine struct {
	mu    sync.Mutex
	items map[string]agentdocker.Container
}

func (e *editEngine) ListAll(context.Context) ([]string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.items))
	for id := range e.items {
		ids = append(ids, id)
	}
	return ids, nil
}
func (e *editEngine) Inspect(_ context.Context, id string) (agentdocker.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	item, ok := e.items[id]
	if !ok {
		return agentdocker.Container{}, errors.New("not found")
	}
	return item, nil
}

type editRunner struct {
	mu            sync.Mutex
	root          string
	engine        *editEngine
	failNew       bool
	failConfig    bool
	configFailure string
	makeUnhealthy bool
	args          [][]string
}

func (r *editRunner) Run(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.args = append(r.args, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	if len(args) >= 2 && args[0] == "image" && args[1] == "tag" {
		return nil, nil
	}
	if strings.Contains(joined, " config ") || strings.HasSuffix(joined, "config --format json") || strings.HasSuffix(joined, "config --quiet") {
		if r.failConfig {
			return []byte(r.configFailure), errors.New(r.configFailure)
		}
		port := "18080"
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-f" {
				data, err := os.ReadFile(args[i+1])
				if err == nil && strings.Contains(string(data), "18082") {
					port = "18082"
				}
			}
		}
		model := fmt.Sprintf(`{"services":{"web":{"image":"nginx:locked","ports":[{"target":80,"published":"%s","protocol":"tcp","host_ip":"127.0.0.1"}],"environment":{"API_KEY":"super-secret"}},"database":{"image":"redis:locked","volumes":[{"source":"state","target":"/data","type":"volume"}]}}}`, port)
		return []byte(model), nil
	}
	if strings.Contains(joined, " up ") || strings.Contains(joined, " up--") {
		rollback := strings.Contains(joined, "rollback.override.yaml")
		if r.failNew && !rollback {
			return []byte("port is already allocated"), errors.New("port is already allocated")
		}
		port := "18082"
		if rollback {
			port = "18080"
		}
		r.engine.mu.Lock()
		web := r.engine.items["web-id"]
		web.Ports[0].Published = []agentdocker.HostPort{{IP: "127.0.0.1", Port: port}}
		if r.makeUnhealthy && !rollback {
			web.HealthcheckConfigured, web.Health = true, agentdocker.HealthUnhealthy
		} else {
			web.HealthcheckConfigured, web.Health = false, agentdocker.HealthNone
		}
		r.engine.items["web-id"] = web
		r.engine.mu.Unlock()
	}
	return nil, nil
}

func TestStructuredPortEditsHandleIPv4IPv6UDPAndLongSyntax(t *testing.T) {
	cases := []struct {
		name, source string
		edit         protocol.ComposePortEdit
		want         string
	}{
		{"ipv4-tcp", "services:\n  web:\n    ports:\n      - 127.0.0.1:18080:80/tcp\n", protocol.ComposePortEdit{Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: 18080, NewHostIP: "127.0.0.1", NewPublished: 18082}, "127.0.0.1:18082:80/tcp"},
		{"ipv6-udp", "services:\n  dns:\n    ports:\n      - '[::1]:5300:53/udp'\n", protocol.ComposePortEdit{Service: "dns", Target: 53, Protocol: "udp", OldHostIP: "::1", OldPublished: 5300, NewHostIP: "2001:db8::1", NewPublished: 5301}, "[2001:db8::1]:5301:53/udp"},
		{"long-form", "services:\n  web:\n    ports:\n      - target: 443\n        published: 18443\n        protocol: tcp\n        host_ip: 127.0.0.1\n        mode: ingress\n", protocol.ComposePortEdit{Service: "web", Target: 443, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: 18443, NewHostIP: "::1", NewPublished: 18444}, "published: 18444"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			changed, err := applyPortEdit([]byte(test.source), test.edit)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(changed), test.want) {
				t.Fatalf("updated YAML does not contain %q:\n%s", test.want, changed)
			}
			if test.name == "long-form" && !strings.Contains(string(changed), "mode: ingress") {
				t.Fatalf("long-form port options were discarded: %s", changed)
			}
		})
	}
}

func TestPreviewUsesVersionChecksRedactsEnvironmentAndNeverMutatesSource(t *testing.T) {
	manager, request, mainFile, envFile := fixture(t, false, false)
	result, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.Editor == nil || len(result.Editor.AffectedServices) != 1 || result.Editor.AffectedServices[0] != "web" {
		t.Fatalf("unexpected preview: %+v", result)
	}
	if strings.Contains(result.Editor.ResolvedConfig, "super-secret") {
		t.Fatal("resolved preview leaked an environment value")
	}
	for _, path := range []string{mainFile, envFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preview changed source file %s: %v", path, err)
		}
	}
	if len(result.Editor.Files) != 1 || !strings.Contains(result.Editor.Files[0].Content, "18082") {
		t.Fatalf("preview omitted the edited source: %+v", result.Editor.Files)
	}

	stale := request
	input := *request.Editor
	input.ExpectedVersions = map[string]string{}
	stale.Editor = &input
	if _, err := manager.Execute(context.Background(), stale); err == nil {
		t.Fatal("stale source versions were accepted")
	}
}

func TestPreviewInvalidSourceDiagnosticsAreClassifiedAndSafe(t *testing.T) {
	t.Run("structured port edit", func(t *testing.T) {
		manager, request, _, _ := fixture(t, false, false)
		request.Editor.PortEdits[0].OldPublished++
		_, err := manager.Execute(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "stage: structured-port-edit") {
			t.Fatalf("port edit failure was not safely classified: %v", err)
		}
	})

	t.Run("compose config resolution", func(t *testing.T) {
		manager, request, _, _ := fixture(t, false, false)
		runner := manager.runner.(*editRunner)
		runner.failConfig = true
		runner.configFailure = "fixture-only-sensitive-compose-marker"
		_, err := manager.Execute(context.Background(), request)
		if err == nil || !strings.Contains(err.Error(), "stage: current-compose-config-resolution") {
			t.Fatalf("config resolution failure was not safely classified: %v", err)
		}
		if strings.Contains(err.Error(), runner.configFailure) {
			t.Fatal("Compose diagnostic output escaped into the safe error")
		}
	})
}

func TestResolvedConfigRedactsSensitiveFieldsAcrossComposeTree(t *testing.T) {
	raw := []byte(`{"services":{"web":{"build":{"args":{"API_TOKEN":"fixture-marker-token","DbPassword":"fixture-marker-password","privateKey":"fixture-marker-key","DATABASEPASSWORD":"fixture-marker-uppercase-password","APIKEY":"fixture-marker-uppercase-key","visible":"safe-build-arg"}},"environment":["APP_MODE=production","API_KEY=fixture-marker-env"]},"labels":{"Authorization":"fixture-marker-auth","app.mode":"safe-label"}}}`)
	resolved, err := redactResolvedConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"fixture-marker-token", "fixture-marker-password", "fixture-marker-key", "fixture-marker-uppercase-password", "fixture-marker-uppercase-key", "fixture-marker-env", "fixture-marker-auth"} {
		if strings.Contains(resolved, marker) {
			t.Fatal("resolved Compose config exposed a value belonging to a sensitive field")
		}
	}
	for _, safe := range []string{"safe-build-arg", "safe-label", "[REDACTED]"} {
		if !strings.Contains(resolved, safe) {
			t.Fatalf("resolved Compose config lost safe value or redaction marker %q", safe)
		}
	}
}

func TestApplyRebuildsOnlyAffectedServiceAndRetainsRollbackSourceAndVolume(t *testing.T) {
	manager, request, mainFile, _ := fixture(t, false, false)
	result, err := manager.Execute(context.Background(), requestWithAction(request, protocol.ComposeEditApply))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || !result.Verified {
		t.Fatalf("apply not verified: %+v", result)
	}
	updated, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "18082") {
		t.Fatalf("source did not commit: %s", updated)
	}
	engine := manager.engine.(*editEngine)
	engine.mu.Lock()
	web, db := engine.items["web-id"], engine.items["database-id"]
	engine.mu.Unlock()
	if web.Ports[0].Published[0].Port != "18082" {
		t.Fatalf("actual published port = %s", web.Ports[0].Published[0].Port)
	}
	if db.ID != "database-id" || len(db.Mounts) != 1 || db.Mounts[0].Name != "fixture_state" {
		t.Fatalf("unaffected database or volume changed: %+v", db)
	}
	runner := manager.runner.(*editRunner)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	found := false
	for _, args := range runner.args {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " up ") && strings.HasSuffix(joined, " web") && strings.Contains(joined, "--no-deps") {
			found = true
		}
	}
	if !found {
		t.Fatalf("affected-service-only Compose up was not issued: %#v", runner.args)
	}
}

func TestApplyOccupiedPortRestoresSourceAndConfirmsRollback(t *testing.T) {
	manager, request, mainFile, _ := fixture(t, true, false)
	result, err := manager.Execute(context.Background(), requestWithAction(request, protocol.ComposeEditApply))
	if !errors.Is(err, ErrPortOccupied) || result.Editor == nil || !result.Editor.RollbackConfirmed {
		t.Fatalf("rollback was not confirmed: result=%+v err=%v", result, err)
	}
	contents, readErr := os.ReadFile(mainFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(contents), "18080") || strings.Contains(string(contents), "18082") {
		t.Fatalf("source was not restored after occupied port: %s", contents)
	}
	engine := manager.engine.(*editEngine)
	engine.mu.Lock()
	got := engine.items["web-id"].Ports[0].Published[0].Port
	engine.mu.Unlock()
	if got != "18080" {
		t.Fatalf("old live port was not restored: %s", got)
	}
	runner := manager.runner.(*editRunner)
	runner.mu.Lock()
	rollbackCommands := 0
	rollbackRebuilt := false
	rollbackNoBuild := false
	for _, args := range runner.args {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "rollback.override.yaml") || !contains(args, "up") {
			continue
		}
		rollbackCommands++
		rollbackRebuilt = rollbackRebuilt || contains(args, "--build")
		rollbackNoBuild = rollbackNoBuild || contains(args, "--no-build")
	}
	runner.mu.Unlock()
	if rollbackCommands != 1 || rollbackRebuilt || !rollbackNoBuild {
		t.Fatalf("pinned-image rollback must use --no-build and never --build: commands=%d rebuilt=%v noBuild=%v", rollbackCommands, rollbackRebuilt, rollbackNoBuild)
	}
	statusRequest := protocol.ComposeRequest{OperationID: "result-query-1", Action: protocol.ComposeEditStatus, Project: request.Project,
		Editor: &protocol.ComposeEditorInput{TargetOperationID: request.OperationID}}
	status, statusErr := manager.Execute(context.Background(), statusRequest)
	if statusErr != nil || status.Status != "failed" || status.ErrorCode != "port_occupied" || status.Editor == nil || !status.Editor.RollbackConfirmed {
		t.Fatalf("Agent journal did not confirm the rollback after reconnect: response=%+v err=%v", status, statusErr)
	}
}

func TestApplyHealthFailureRestoresSourceAndConfirmsRollback(t *testing.T) {
	manager, request, mainFile, _ := fixture(t, false, true)
	result, err := manager.Execute(context.Background(), requestWithAction(request, protocol.ComposeEditApply))
	if !errors.Is(err, ErrHealthFailed) || result.Editor == nil || !result.Editor.RollbackConfirmed {
		t.Fatalf("health failure did not confirm rollback: result=%+v err=%v", result, err)
	}
	contents, readErr := os.ReadFile(mainFile)
	if readErr != nil || !strings.Contains(string(contents), "18080") || strings.Contains(string(contents), "18082") {
		t.Fatalf("health failure did not restore source: err=%v source=%s", readErr, contents)
	}
}

func TestAgentRestartRecoveryRestoresAnApplyingTransactionWithoutReplay(t *testing.T) {
	manager, request, mainFile, _ := fixture(t, false, false)
	request.Action = protocol.ComposeEditApply
	if _, err := manager.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(manager.opts.BackupDir, request.OperationID, "transaction.json")
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var state transactionState
	if err := json.Unmarshal(journal, &state); err != nil {
		t.Fatal(err)
	}
	state.Phase = "applying" // emulate a process restart after an uncertain Compose result
	journal, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, journal, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := manager.Recover(context.Background()); err != nil {
		t.Fatalf("recover interrupted applying transaction: %v", err)
	}
	contents, err := os.ReadFile(mainFile)
	if err != nil || !strings.Contains(string(contents), "18080") || strings.Contains(string(contents), "18082") {
		t.Fatalf("recovery did not restore original Compose source: err=%v source=%s", err, contents)
	}
	engine := manager.engine.(*editEngine)
	engine.mu.Lock()
	got := engine.items["web-id"].Ports[0].Published[0].Port
	engine.mu.Unlock()
	if got != "18080" {
		t.Fatalf("recovery did not restore the actual old port: %s", got)
	}
	statusRequest := protocol.ComposeRequest{OperationID: "recovery-query-1", Action: protocol.ComposeEditStatus, Project: request.Project,
		Editor: &protocol.ComposeEditorInput{TargetOperationID: request.OperationID}}
	status, statusErr := manager.Execute(context.Background(), statusRequest)
	if statusErr != nil || status.Status != "failed" || status.ErrorCode != "recovered_after_restart" || status.Editor == nil || !status.Editor.RollbackConfirmed {
		t.Fatalf("Agent status query did not resolve recovered operation: response=%+v err=%v", status, statusErr)
	}
	var recovered transactionState
	journal, err = os.ReadFile(journalPath)
	if err != nil || json.Unmarshal(journal, &recovered) != nil || recovered.Phase != "recovered" {
		t.Fatalf("recovery phase was not durably recorded: state=%+v err=%v", recovered, err)
	}
}

func TestExternalSourceConflictIsNotOverwritten(t *testing.T) {
	manager, request, mainFile, _ := fixture(t, false, false)
	if err := os.WriteFile(mainFile, []byte("services:\n  web:\n    image: nginx\n    ports:\n      - 127.0.0.1:19000:80/tcp\n  database:\n    image: redis\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Execute(context.Background(), requestWithAction(request, protocol.ComposeEditApply))
	if !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("external edit conflict = %v", err)
	}
	contents, _ := os.ReadFile(mainFile)
	if !strings.Contains(string(contents), "19000") {
		t.Fatalf("external source change was overwritten: %s", contents)
	}
}

func TestApplyPreservesSourceAndEnvironmentModesAndSecuresBackups(t *testing.T) {
	manager, request, mainFile, envFile := fixture(t, false, false)
	original, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(context.Background(), requestWithAction(request, protocol.ComposeEditApply)); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{mainFile: 0o640, envFile: 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat source %s: %v", path, err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("source %s mode=%#o err=%v, want %#o", path, info.Mode().Perm(), err, want)
		}
	}
	backupDir := filepath.Join(manager.opts.BackupDir, request.OperationID)
	for _, name := range []string{"transaction.json", "resolved.old.json", "source-00.backup", "source-01.backup"} {
		path := filepath.Join(backupDir, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("transaction file %s mode=%#o err=%v, want 0600", name, info.Mode().Perm(), err)
		}
	}
	backup, err := os.ReadFile(filepath.Join(backupDir, "source-00.backup"))
	if err != nil || string(backup) != string(original) {
		t.Fatalf("source backup did not preserve exact contents: err=%v", err)
	}
	stat, err := os.Stat(mainFile)
	if err != nil {
		t.Fatal(err)
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		t.Fatalf("source owner was not retained: stat=%+v", stat)
	}
}

func fixture(t *testing.T, failNew, unhealthy bool) (*Manager, protocol.ComposeRequest, string, string) {
	t.Helper()
	directory := t.TempDir()
	mainFile, envFile := filepath.Join(directory, "compose.yaml"), filepath.Join(directory, "production.env")
	main := "services:\n  web:\n    image: nginx:locked\n    ports:\n      - 127.0.0.1:18080:80/tcp\n  database:\n    image: redis:locked\n    volumes:\n      - state:/data\nvolumes:\n  state:\n"
	if err := os.WriteFile(mainFile, []byte(main), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envFile, []byte("API_KEY=super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := protocol.ComposeProjectRef{Name: "nodedance-edit", WorkingDirectory: directory, ConfigFiles: []string{mainFile}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	web := agentdocker.Container{ID: "web-id", Name: "/nodedance-edit-web-1", ImageID: "sha256:old-web", Running: true, State: "running", Health: agentdocker.HealthNone,
		Ports: []agentdocker.Port{{ContainerPort: 80, Protocol: "tcp", Published: []agentdocker.HostPort{{IP: "127.0.0.1", Port: "18080"}}}}, Compose: &agentdocker.ComposeIdentity{Project: ref.Name, Service: "web", WorkingDir: directory}}
	database := agentdocker.Container{ID: "database-id", Name: "/nodedance-edit-database-1", ImageID: "sha256:old-db", Running: true, State: "running", Health: agentdocker.HealthNone,
		Mounts: []agentdocker.Mount{{Type: "volume", Name: "fixture_state", Destination: "/data", ReadWrite: true}}, Compose: &agentdocker.ComposeIdentity{Project: ref.Name, Service: "database", WorkingDir: directory}}
	engine := &editEngine{items: map[string]agentdocker.Container{"web-id": web, "database-id": database}}
	runner := &editRunner{root: directory, engine: engine, failNew: failNew, makeUnhealthy: unhealthy}
	manager, err := NewManager(engine, runner, OSFileStore{}, Options{BackupDir: filepath.Join(directory, "backups"), OperationTimeout: 20 * time.Second, VerificationTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	readReq := protocol.ComposeRequest{OperationID: "01HXYZ", Action: protocol.ComposeEditRead, Project: ref, EnvFiles: []string{envFile}, Editor: &protocol.ComposeEditorInput{}}
	read, err := manager.Execute(context.Background(), readReq)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{}
	for _, file := range read.Editor.Files {
		versions[file.Path] = file.Version
	}
	request := protocol.ComposeRequest{OperationID: "nodedance-edit-apply", Action: protocol.ComposeEditPreview, Project: ref, EnvFiles: []string{envFile}, Profiles: []string{"verification"}, Editor: &protocol.ComposeEditorInput{
		ExpectedVersions: versions, PortEdits: []protocol.ComposePortEdit{{File: mainFile, Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: 18080, NewHostIP: "127.0.0.1", NewPublished: 18082}}}}
	return manager, request, mainFile, envFile
}

func requestWithAction(request protocol.ComposeRequest, action protocol.ComposeAction) protocol.ComposeRequest {
	request.Action = action
	return request
}

func TestRollbackOverrideUsesPinnedImageAndNeverIncludesVolumes(t *testing.T) {
	override := string(rollbackOverride(map[string]string{"web": "nodedance-rollback:test-web"}))
	var decoded any
	if err := yamlUnmarshal([]byte(override), &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(override, "nodedance-rollback:test-web") || strings.Contains(strings.ToLower(override), "volumes") {
		t.Fatalf("unsafe rollback override: %s", override)
	}
	encoded, _ := json.Marshal(decoded)
	if !strings.Contains(string(encoded), "web") {
		t.Fatalf("rollback override missing service: %s", encoded)
	}
}

func yamlUnmarshal(data []byte, into any) error { return yaml.Unmarshal(data, into) }
