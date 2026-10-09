package composeedit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentcomposeedit "github.com/CST-Cat/NodeDance/internal/agent/composeedit"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// TestDINDComposeEditorRealEngine runs only against a fresh, explicit DIND
// fixture created by scripts/test/dind.sh. It never starts or stops a daemon.
func TestDINDComposeEditorRealEngine(t *testing.T) {
	dindRoot := os.Getenv("NODEDANCE_S11_DIND_ROOT")
	fixtureRoot := os.Getenv("NODEDANCE_S11_FIXTURE_ROOT")
	if strings.TrimSpace(dindRoot) == "" || strings.TrimSpace(fixtureRoot) == "" {
		t.Skip("NOT_READY: set NODEDANCE_S11_DIND_ROOT and NODEDANCE_S11_FIXTURE_ROOT to a job-owned DIND fixture")
	}
	root, err := filepath.Abs(dindRoot)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "socket", "docker.sock")
	markerPath := filepath.Join(root, "owner.json")
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("DIND owner marker is required: %v", err)
	}
	var marker struct {
		Suite         string `json:"suite"`
		Socket        string `json:"socket"`
		ServerVersion string `json:"server_version"`
	}
	if err := json.Unmarshal(markerBytes, &marker); err != nil || marker.Suite != "nodedance-s00-dind" || filepath.Clean(marker.Socket) != socket {
		t.Fatalf("DIND owner marker does not identify this dedicated socket: %+v err=%v", marker, err)
	}
	if !strings.HasPrefix(marker.ServerVersion, "28.") && !strings.HasPrefix(marker.ServerVersion, "29.") {
		t.Fatalf("expected Engine 28 or 29, got %q", marker.ServerVersion)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("dedicated DIND socket is unavailable: %v", err)
	}

	repoRoot, err := findRepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	fixtureBase, err := filepath.Abs(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	allowedBase := filepath.Join(repoRoot, ".artifacts", "fixtures")
	if !isWithin(allowedBase, fixtureBase) || fixtureBase == allowedBase {
		t.Fatalf("test fixture must be a unique child of %s, got %s", allowedBase, fixtureBase)
	}
	if err := os.MkdirAll(fixtureBase, 0o700); err != nil {
		t.Fatal(err)
	}
	runID := strings.TrimSpace(os.Getenv("NODEDANCE_S11_RUN_ID"))
	if runID == "" {
		runID = fmt.Sprintf("local-%d", time.Now().UnixNano())
	}
	suite := "nodedance-s11-compose-editor-" + safeRunSuffix(runID)
	ownerPath := filepath.Join(fixtureBase, ".nodedance-s11-owner.json")
	owner, _ := json.Marshal(map[string]string{"suite": suite, "run_id": runID, "path": fixtureBase})
	if err := os.WriteFile(ownerPath, owner, 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureDir := filepath.Join(fixtureBase, "project ; with spaces")
	if err := os.MkdirAll(filepath.Join(fixtureDir, "site"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDir, "site", "index.html"), []byte("<h1>NodeDance S11 fixture</h1>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dockerHost := "unix://" + socket
	engine, err := agentdocker.NewSDKEngine(dockerHost)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	runner := agentcompose.ExecRunner{DockerHost: dockerHost}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := engine.Ping(ctx); err != nil {
		t.Fatalf("dedicated Docker Engine is not available: %v", err)
	}
	images := readLockedImages(t, repoRoot)
	projectName := "nd-s11-" + safeRunSuffix(runID)
	if len(projectName) > 55 {
		projectName = projectName[:55]
	}
	config := filepath.Join(fixtureDir, "compose ;.yaml")
	override := filepath.Join(fixtureDir, "compose.override.yaml")
	envFile := filepath.Join(fixtureDir, "production.env")
	initialPort := findAvailablePublishedPort(t, ctx, runner, dockerHost, fixtureDir, suite, images["nginx"])
	udpPort := findAvailableUDPPort(t, ctx, runner, dockerHost, fixtureDir, suite, images["busybox"])
	if err := os.WriteFile(envFile, []byte(fmt.Sprintf("WEB_PORT=%d\nUDP_PORT=%d\nS11_SECRET=top-secret-s11-value\n", initialPort, udpPort)), 0o600); err != nil {
		t.Fatal(err)
	}
	baseSource := fmt.Sprintf(`services:
  web:
    image: %s
    depends_on:
      - database
    ports:
      - target: 80
        published: "%d"
        host_ip: 127.0.0.1
        protocol: tcp
      - "127.0.0.1:%d:53/udp"
    volumes:
      - type: bind
        source: ./site
        target: /usr/share/nginx/html
        read_only: true
    environment:
      SECRET_VALUE: ${S11_SECRET:?required}
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %s
  database:
    image: %s
    command: ["redis-server", "--appendonly", "yes"]
    environment:
      S11_SECRET: ${S11_SECRET:?required}
    volumes:
      - state:/data
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %s
  optional:
    image: %s
    profiles: [monitoring]
    command: ["sh", "-c", "while true; do sleep 60; done"]
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %s
volumes:
  state:
    labels:
      io.nodedance.test: "true"
      io.nodedance.suite: %s
`, images["nginx"], initialPort, udpPort, suite, images["redis"], suite, images["busybox"], suite, suite)
	overrideSource := fmt.Sprintf(`services:
  web:
    labels:
      io.nodedance.editor-fixture: %q
  database:
    environment:
      NODEDANCE_S11_KEEP: "yes"
`, suite)
	if err := os.WriteFile(config, []byte(baseSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte(overrideSource), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := protocol.ComposeProjectRef{Name: projectName, WorkingDirectory: fixtureDir, ConfigFiles: []string{config, override}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	composePrefix := []string{"compose", "--project-name", projectName, "--project-directory", fixtureDir, "-f", config, "-f", override, "--env-file", envFile, "--profile", "monitoring"}
	cleanupSafe := false
	blockers := make([]string, 0)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, name := range blockers {
			removeOwnedContainer(t, cleanupCtx, runner, fixtureDir, dockerHost, name, suite)
		}
		if cleanupSafe {
			_, cleanupErr := runner.Run(cleanupCtx, fixtureDir, append(append([]string(nil), composePrefix...), "down", "--volumes", "--remove-orphans"), dockerHost)
			if cleanupErr != nil {
				t.Errorf("owned Compose fixture cleanup failed; fixture preserved: %v", cleanupErr)
				return
			}
		}
		data, readErr := os.ReadFile(ownerPath)
		var check map[string]string
		if readErr == nil && json.Unmarshal(data, &check) == nil && check["suite"] == suite && check["path"] == fixtureBase {
			if err := os.RemoveAll(fixtureBase); err != nil {
				t.Errorf("remove only the marked S11 fixture directory: %v", err)
			}
		} else {
			t.Errorf("S11 fixture owner marker changed; preserving %s", fixtureBase)
		}
	})

	cleanupSafe = true
	if _, err := runner.Run(ctx, fixtureDir, append(append([]string(nil), composePrefix...), "up", "--detach"), dockerHost); err != nil {
		t.Fatalf("start locked real Engine Compose fixture: %v", err)
	}
	seedOutput, err := runner.Run(ctx, fixtureDir, append(append([]string(nil), composePrefix...), "exec", "-T", "database", "redis-cli", "set", "nodedance:s11", "persisted-value"), dockerHost)
	if err != nil || strings.TrimSpace(string(seedOutput)) != "OK" {
		t.Fatalf("seed named-volume fixture data: %v", err)
	}
	webBefore, databaseBefore, monitorBefore := findComposeServiceIDs(t, ctx, engine, ref.Name)
	if webBefore == "" || databaseBefore == "" || monitorBefore == "" {
		t.Fatalf("fixture services were not discovered before editing: web=%q database=%q monitor=%q", webBefore, databaseBefore, monitorBefore)
	}

	operationID := "s11-preview-" + safeRunSuffix(runID)
	readRequest := protocol.ComposeRequest{OperationID: operationID, Action: protocol.ComposeEditRead, Project: ref,
		EnvFiles: []string{envFile}, Profiles: []string{"monitoring"}, Editor: &protocol.ComposeEditorInput{}}
	read, err := newEditorManager(t, engine, runner, fixtureBase, dockerHost).Execute(ctx, readRequest)
	if err != nil {
		t.Fatalf("read multi-file, environment and profile source context: %v", err)
	}
	versions := map[string]string{}
	for _, file := range read.Editor.Files {
		versions[file.Path] = file.Version
	}
	newPort := findAvailablePublishedPort(t, ctx, runner, dockerHost, fixtureDir, suite, images["nginx"])
	input := protocol.ComposeEditorInput{ExpectedVersions: versions, PortEdits: []protocol.ComposePortEdit{{File: config, Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: uint16(initialPort), NewHostIP: "127.0.0.1", NewPublished: uint16(newPort)}}}
	manager := newEditorManager(t, engine, runner, fixtureBase, dockerHost)
	previewReq := protocol.ComposeRequest{OperationID: operationID, Action: protocol.ComposeEditPreview, Project: ref, EnvFiles: []string{envFile}, Profiles: []string{"monitoring"}, Editor: &input}
	preview, err := manager.Execute(ctx, previewReq)
	if err != nil {
		t.Fatalf("preview edit using real Compose context: %v", err)
	}
	if strings.Contains(preview.Editor.ResolvedConfig, "top-secret-s11-value") {
		t.Fatal("resolved configuration leaked an environment secret")
	}
	if len(preview.Editor.AffectedServices) != 1 || preview.Editor.AffectedServices[0] != "web" {
		t.Fatalf("port edit did not isolate the affected service: %+v", preview.Editor.AffectedServices)
	}
	invalid := input
	invalid.Files = []protocol.ComposeSourceFile{{Path: config, Content: "services: [not valid"}}
	invalidRequest := previewReq
	invalidRequest.OperationID = operationID + "-invalid"
	invalidRequest.Editor = &invalid
	if _, err := manager.Execute(ctx, invalidRequest); err == nil {
		t.Fatal("invalid merged Compose configuration was accepted")
	}
	if source, err := os.ReadFile(config); err != nil || !strings.Contains(string(source), fmt.Sprintf("published: \"%d\"", initialPort)) {
		t.Fatalf("invalid preview changed the real source: err=%v", err)
	}

	applyRequest := previewReq
	applyRequest.OperationID = "s11-apply-" + safeRunSuffix(runID)
	applyRequest.Action = protocol.ComposeEditApply
	if result, err := manager.Execute(ctx, applyRequest); err != nil || !result.Verified {
		t.Fatalf("apply and verify real service port change: result=%+v err=%v", result.Editor, err)
	}
	volumeOutput, err := runner.Run(ctx, fixtureDir, append(append([]string(nil), composePrefix...), "exec", "-T", "database", "redis-cli", "get", "nodedance:s11"), dockerHost)
	if err != nil || strings.TrimSpace(string(volumeOutput)) != "persisted-value" {
		t.Fatalf("named-volume service became unavailable: %v", err)
	}
	webAfter, dbAfter, monitorAfter := findComposeServiceIDs(t, ctx, engine, ref.Name)
	if webAfter == "" || dbAfter == "" || monitorAfter == "" || webAfter == webBefore || dbAfter != databaseBefore || monitorAfter != monitorBefore {
		t.Fatalf("affected-only recreate failed: web %s->%s, database %s->%s, optional %s->%s", webBefore, webAfter, databaseBefore, dbAfter, monitorBefore, monitorAfter)
	}
	containers := engineContainers(t, ctx, engine)
	web := containers[webAfter]
	if !hasPublishedPort(web, 80, uint16(newPort), "tcp", "127.0.0.1") || !hasPublishedPort(web, 53, uint16(udpPort), "udp", "127.0.0.1") {
		t.Fatalf("real Engine did not publish expected TCP/UDP multi-mappings: %+v", web.Ports)
	}
	if !hasBindMount(containers[webAfter], filepath.Join(fixtureDir, "site")) {
		t.Fatalf("relative bind mount resolved outside the original project directory: %+v", containers[webAfter].Mounts)
	}

	blockPort := findAvailablePublishedPort(t, ctx, runner, dockerHost, fixtureDir, suite, images["nginx"])
	blocker := "nd-s11-blocker-" + safeRunSuffix(runID)
	blockers = append(blockers, blocker)
	if _, err := runner.Run(ctx, fixtureDir, []string{"run", "--detach", "--name", blocker, "--label", "io.nodedance.test=true", "--label", "io.nodedance.suite=" + suite, "-p", fmt.Sprintf("127.0.0.1:%d:80", blockPort), images["nginx"]}, dockerHost); err != nil {
		t.Fatalf("create uniquely labelled owned port blocker: %v", err)
	}
	currentRead := readRequest
	currentRead.OperationID = "s11-reread-" + safeRunSuffix(runID)
	current, err := manager.Execute(ctx, currentRead)
	if err != nil {
		t.Fatalf("reload source before occupied-port attempt: %v", err)
	}
	currentVersions := map[string]string{}
	for _, file := range current.Editor.Files {
		currentVersions[file.Path] = file.Version
	}
	occupiedInput := protocol.ComposeEditorInput{ExpectedVersions: currentVersions, PortEdits: []protocol.ComposePortEdit{{File: config, Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: uint16(newPort), NewHostIP: "127.0.0.1", NewPublished: uint16(blockPort)}}}
	occupiedRequest := protocol.ComposeRequest{OperationID: "s11-occupied-" + safeRunSuffix(runID), Action: protocol.ComposeEditApply, Project: ref, EnvFiles: []string{envFile}, Profiles: []string{"monitoring"}, Editor: &occupiedInput}
	failedResult, err := manager.Execute(ctx, occupiedRequest)
	if !errors.Is(err, agentcomposeedit.ErrPortOccupied) || failedResult.Editor == nil || !failedResult.Editor.RollbackConfirmed {
		t.Fatalf("occupied port did not produce a confirmed rollback: result=%+v err=%v", failedResult.Editor, err)
	}
	rolledBack, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(rolledBack), fmt.Sprintf("published: %d", newPort)) {
		t.Fatalf("occupied-port transaction did not restore Compose source: err=%v source=%s", err, rolledBack)
	}
	rollbackWeb, rollbackDB, rollbackMonitor := findComposeServiceIDs(t, ctx, engine, ref.Name)
	if rollbackWeb == "" || rollbackDB != databaseBefore || rollbackMonitor != monitorBefore || !hasPublishedPort(containersOrInspect(t, ctx, engine, rollbackWeb), 80, uint16(newPort), "tcp", "127.0.0.1") {
		t.Fatalf("occupied-port rollback did not leave live resources consistent: web=%s db=%s monitor=%s", rollbackWeb, rollbackDB, rollbackMonitor)
	}

	// A stale source version is rejected without overwriting an independent edit.
	conflictRead := readRequest
	conflictRead.OperationID = "s11-conflict-read-" + safeRunSuffix(runID)
	conflictSource, err := manager.Execute(ctx, conflictRead)
	if err != nil {
		t.Fatal(err)
	}
	conflictVersions := map[string]string{}
	for _, file := range conflictSource.Editor.Files {
		conflictVersions[file.Path] = file.Version
	}
	conflictingSource := append(append([]byte(nil), rolledBack...), []byte("# external editor change\n")...)
	if err := os.WriteFile(config, conflictingSource, 0o600); err != nil {
		t.Fatal(err)
	}
	conflictInput := protocol.ComposeEditorInput{ExpectedVersions: conflictVersions, PortEdits: []protocol.ComposePortEdit{{File: config, Service: "web", Target: 80, Protocol: "tcp", OldHostIP: "127.0.0.1", OldPublished: uint16(newPort), NewHostIP: "127.0.0.1", NewPublished: uint16(newPort + 1)}}}
	conflictRequest := protocol.ComposeRequest{OperationID: "s11-conflict-" + safeRunSuffix(runID), Action: protocol.ComposeEditApply, Project: ref, EnvFiles: []string{envFile}, Profiles: []string{"monitoring"}, Editor: &conflictInput}
	if _, err := manager.Execute(ctx, conflictRequest); !errors.Is(err, agentcomposeedit.ErrSourceConflict) {
		t.Fatalf("external edit conflict = %v", err)
	}
	conflictAfter, err := os.ReadFile(config)
	if err != nil || string(conflictAfter) != string(conflictingSource) {
		t.Fatalf("stale editor overwrote the external source change: err=%v source=%s", err, conflictAfter)
	}
}

func newEditorManager(t *testing.T, engine agentcomposeedit.Engine, runner agentcomposeedit.Runner, fixtureBase, dockerHost string) *agentcomposeedit.Manager {
	t.Helper()
	manager, err := agentcomposeedit.NewManager(engine, runner, agentcomposeedit.OSFileStore{}, agentcomposeedit.Options{DockerHost: dockerHost, BackupDir: filepath.Join(fixtureBase, "transactions"), OperationTimeout: 2 * time.Minute, VerificationTimeout: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func findAvailablePublishedPort(t *testing.T, ctx context.Context, runner agentcomposeedit.Runner, dockerHost, directory, suite, image string) int {
	t.Helper()
	for attempt := 0; attempt < 16; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		name := fmt.Sprintf("nd-s11-probe-%s-%d", safeRunSuffix(suite), port)
		output, err := runner.Run(ctx, directory, []string{"run", "--detach", "--name", name, "--label", "io.nodedance.test=true", "--label", "io.nodedance.suite=" + suite, "-p", fmt.Sprintf("127.0.0.1:%d:80", port), image}, dockerHost)
		if err == nil {
			removeOwnedContainer(t, ctx, runner, directory, dockerHost, name, suite)
			return port
		}
		if strings.Contains(strings.ToLower(string(output)+" "+err.Error()), "port is already allocated") || strings.Contains(strings.ToLower(string(output)+" "+err.Error()), "address already in use") {
			removeOwnedContainer(t, ctx, runner, directory, dockerHost, name, suite)
			continue
		}
		t.Fatalf("probe an owner-scoped DIND TCP port: %v (%s)", err, output)
	}
	t.Fatal("could not allocate a free TCP port in the dedicated DIND Engine")
	return 0
}

func findAvailableUDPPort(t *testing.T, ctx context.Context, runner agentcomposeedit.Runner, dockerHost, directory, suite, image string) int {
	for attempt := 0; attempt < 16; attempt++ {
		listener, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.LocalAddr().(*net.UDPAddr).Port
		_ = listener.Close()
		name := fmt.Sprintf("nd-s11-udp-probe-%s-%d", safeRunSuffix(suite), port)
		output, err := runner.Run(ctx, directory, []string{"run", "--detach", "--name", name, "--label", "io.nodedance.test=true", "--label", "io.nodedance.suite=" + suite, "-p", fmt.Sprintf("127.0.0.1:%d:53/udp", port), image, "sleep", "30"}, dockerHost)
		if err == nil {
			removeOwnedContainer(t, ctx, runner, directory, dockerHost, name, suite)
			return port
		}
		if strings.Contains(strings.ToLower(string(output)+" "+err.Error()), "port is already allocated") || strings.Contains(strings.ToLower(string(output)+" "+err.Error()), "address already in use") {
			removeOwnedContainer(t, ctx, runner, directory, dockerHost, name, suite)
			continue
		}
		t.Fatalf("probe an owner-scoped DIND UDP port: %v (%s)", err, output)
	}
	t.Fatal("could not allocate a free UDP port in the dedicated DIND Engine")
	return 0
}

func findComposeServiceIDs(t *testing.T, ctx context.Context, engine agentcomposeedit.Engine, project string) (web, database, optional string) {
	t.Helper()
	for id, item := range engineContainers(t, ctx, engine) {
		if item.Compose == nil || item.Compose.Project != project {
			continue
		}
		switch item.Compose.Service {
		case "web":
			web = id
		case "database":
			database = id
		case "optional":
			optional = id
		}
	}
	return
}

func engineContainers(t *testing.T, ctx context.Context, engine agentcomposeedit.Engine) map[string]agentdocker.Container {
	t.Helper()
	ids, err := engine.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]agentdocker.Container, len(ids))
	for _, id := range ids {
		item, err := engine.Inspect(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		out[id] = item
	}
	return out
}

func containersOrInspect(t *testing.T, ctx context.Context, engine agentcomposeedit.Engine, id string) agentdocker.Container {
	t.Helper()
	item, err := engine.Inspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func hasPublishedPort(item agentdocker.Container, target, port uint16, protocolName, ip string) bool {
	for _, mapping := range item.Ports {
		if mapping.ContainerPort != target || !strings.EqualFold(mapping.Protocol, protocolName) {
			continue
		}
		for _, binding := range mapping.Published {
			if binding.Port == fmt.Sprint(port) && binding.IP == ip {
				return true
			}
		}
	}
	return false
}

func hasBindMount(item agentdocker.Container, source string) bool {
	for _, mount := range item.Mounts {
		if mount.Type == "bind" && filepath.Clean(mount.Source) == filepath.Clean(source) {
			return true
		}
	}
	return false
}

func readLockedImages(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "test-images.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nginx", "redis", "busybox"} {
		if !strings.Contains(lock.Images[name], "@sha256:") {
			t.Fatalf("image %q must be pinned by digest", name)
		}
	}
	return lock.Images
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for current := directory; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "test-images.lock.json")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not locate repository root")
		}
	}
}

func isWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func safeRunSuffix(value string) string {
	value = strings.ToLower(value)
	var out strings.Builder
	for _, item := range value {
		if item >= 'a' && item <= 'z' || item >= '0' && item <= '9' || item == '-' {
			out.WriteRune(item)
		} else {
			out.WriteByte('-')
		}
	}
	value = strings.Trim(out.String(), "-")
	if value == "" {
		return "run"
	}
	return value
}

func removeOwnedContainer(t *testing.T, ctx context.Context, runner agentcomposeedit.Runner, directory, dockerHost, name, suite string) {
	t.Helper()
	output, err := runner.Run(ctx, directory, []string{"inspect", "--format", `{{ index .Config.Labels "io.nodedance.suite" }}`, name}, dockerHost)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(output)) != suite {
		t.Errorf("container %s is not owned by this S11 fixture; preserving it", name)
		return
	}
	if _, err := runner.Run(ctx, directory, []string{"rm", "--force", name}, dockerHost); err != nil {
		t.Errorf("remove only the verified owned blocker %s: %v", name, err)
	}
}
