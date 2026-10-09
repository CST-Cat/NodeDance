package composeedit_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentcomposeedit "github.com/CST-Cat/NodeDance/internal/agent/composeedit"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type composeResolveEngine struct{}

func (composeResolveEngine) ListAll(context.Context) ([]string, error) { return nil, nil }
func (composeResolveEngine) Inspect(context.Context, string) (agentdocker.Container, error) {
	return agentdocker.Container{}, errors.New("no containers are expected in a config-only preview")
}

// TestProposedComposeConfigResolutionUsesOriginalContext exercises the exact
// file staging and Docker Compose config path used by preview without needing
// a running Engine. The DIND test covers the same path against Engine 28/29.
func TestProposedComposeConfigResolutionUsesOriginalContext(t *testing.T) {
	root, err := composeEditorRepositoryRoot()
	if err != nil {
		t.Skip("repository toolchain fixture is unavailable")
	}
	if _, err := os.Stat(filepath.Join(root, ".tools", "docker", "cli-plugins", "docker-compose")); err != nil {
		t.Skip("locked Docker Compose plugin is unavailable")
	}
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is unavailable")
	}
	setup := exec.Command("bash", filepath.Join(root, "scripts", "docker-test-config.sh"))
	setup.Dir = root
	if output, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("could not prepare repository-local Docker CLI config (output length %d): %v", len(output), err)
	}
	t.Setenv("DOCKER_CONFIG", filepath.Join(root, ".artifacts", "docker-config"))

	projectDir := filepath.Join(t.TempDir(), "project ; with spaces")
	if err := os.MkdirAll(filepath.Join(projectDir, "site"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(projectDir, "compose ;.yaml")
	overridePath := filepath.Join(projectDir, "compose.override.yaml")
	envPath := filepath.Join(projectDir, "production.env")
	base := `services:
  web:
    image: nginx:alpine
    depends_on:
      - database
    ports:
      - target: 80
        published: "18080"
        host_ip: 127.0.0.1
        protocol: tcp
      - "127.0.0.1:5300:53/udp"
    volumes:
      - type: bind
        source: ./site
        target: /usr/share/nginx/html
        read_only: true
    environment:
      SECRET_VALUE: ${S11_SECRET:?required}
  database:
    image: redis:alpine
    environment:
      S11_SECRET: ${S11_SECRET:?required}
    volumes:
      - state:/data
  optional:
    image: busybox:latest
    profiles: [monitoring]
    command: ["sh", "-c", "while true; do sleep 60; done"]
volumes:
  state:
`
	if err := os.WriteFile(configPath, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, []byte("services:\n  web:\n    labels:\n      io.nodedance.editor-fixture: fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "fixture-only-secret-value"
	if err := os.WriteFile(envPath, []byte("S11_SECRET="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := protocol.ComposeProjectRef{Name: "nd-s11-preview-context", WorkingDirectory: projectDir, ConfigFiles: []string{configPath, overridePath}}
	project.Key = protocol.ComposeProjectKey(project.Name, project.WorkingDirectory, project.ConfigFiles)
	manager, err := agentcomposeedit.NewManager(composeResolveEngine{}, agentcompose.ExecRunner{DockerPath: dockerPath}, agentcomposeedit.OSFileStore{}, agentcomposeedit.Options{BackupDir: filepath.Join(t.TempDir(), "transactions")})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.ComposeRequest{
		OperationID: "s11-resolve-fixture",
		Action:      protocol.ComposeEditRead,
		Project:     project,
		EnvFiles:    []string{envPath},
		Profiles:    []string{"monitoring"},
		Editor:      &protocol.ComposeEditorInput{},
	}
	read, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("read original multi-file Compose context: %v", err)
	}
	versions := make(map[string]string, len(read.Editor.Files))
	for _, file := range read.Editor.Files {
		versions[file.Path] = file.Version
	}
	edit := protocol.ComposeEditorInput{
		ExpectedVersions: versions,
		PortEdits: []protocol.ComposePortEdit{{
			File: configPath, Service: "web", Target: 80, Protocol: "tcp",
			OldHostIP: "127.0.0.1", OldPublished: 18080,
			NewHostIP: "127.0.0.1", NewPublished: 18081,
		}},
	}
	request.OperationID = "s11-resolve-proposed"
	request.Action = protocol.ComposeEditPreview
	request.Editor = &edit
	preview, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("preview proposed Compose context: %v", err)
	}
	resolved := preview.Editor.ResolvedConfig
	var model struct {
		Services map[string]struct {
			Ports []struct {
				Target    uint16 `json:"target"`
				Published string `json:"published"`
				HostIP    string `json:"host_ip"`
				Protocol  string `json:"protocol"`
			} `json:"ports"`
			Volumes []struct {
				Source string `json:"source"`
				Target string `json:"target"`
				Type   string `json:"type"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(resolved), &model); err != nil {
		t.Fatalf("resolved Compose model is not valid JSON: %v", err)
	}
	web, ok := model.Services["web"]
	if !ok {
		t.Fatal("resolved Compose model omitted the web service")
	}
	portFound := false
	for _, port := range web.Ports {
		if port.Target == 80 && port.Published == "18081" && port.HostIP == "127.0.0.1" && port.Protocol == "tcp" {
			portFound = true
		}
	}
	if !portFound {
		t.Fatalf("resolved Compose model has no correctly typed edited port: %+v", web.Ports)
	}
	mountFound := false
	for _, mount := range web.Volumes {
		if mount.Type == "bind" && mount.Source == filepath.Join(projectDir, "site") && mount.Target == "/usr/share/nginx/html" {
			mountFound = true
		}
	}
	if !mountFound {
		t.Fatalf("resolved Compose model did not preserve the relative bind mount context: %+v", web.Volumes)
	}
	if strings.Contains(resolved, secret) {
		t.Fatal("resolved Compose model leaked a fixture environment value")
	}
	if len(preview.Editor.AffectedServices) != 1 || preview.Editor.AffectedServices[0] != "web" {
		t.Fatalf("preview did not isolate the changed service: %+v", preview.Editor.AffectedServices)
	}
}

func composeEditorRepositoryRoot() (string, error) {
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
			return "", errors.New("repository root not found")
		}
	}
}
