package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/agent"
)

func TestInstallSystemdDefaultsToRootHostManagement(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root default unit validation requires a root-owned config")
	}
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", "")
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "agent.json")
	config := agent.Config{Schema: agent.ConfigSchema, Server: "https://core.example", Credential: strings.Repeat("a", 64)}
	if err := agent.SaveConfig(configPath, config, true); err != nil {
		t.Fatalf("write private Agent config: %v", err)
	}
	unitDir := filepath.Join(directory, "units")
	if err := os.Mkdir(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := runInstallSystemd(context.Background(), []string{"--config", configPath, "--unit-dir", unitDir, "--reload=false"}, &stdout, &stderr); err != nil {
		t.Fatalf("install with default service identity: %v; stderr=%s", err, stderr.String())
	}
	unit, err := os.ReadFile(filepath.Join(unitDir, agent.AgentUnitName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, expected := range []string{"User=root\n", "Group=root\n", `Environment=NODEDANCE_AGENT_FILE_ROOT="/"`} {
		if !strings.Contains(text, expected) {
			t.Errorf("default root unit missing %q:\n%s", expected, text)
		}
	}
	for _, forbidden := range []string{"NoNewPrivileges=", "ProtectSystem=", "ProtectHome=", "PrivateTmp=", "ReadWritePaths="} {
		if strings.Contains(text, forbidden) {
			t.Errorf("default root unit unexpectedly contains %q:\n%s", forbidden, text)
		}
	}
}

func TestSystemdCommandsRejectNonRootServiceUser(t *testing.T) {
	unitDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := runInstallSystemd(context.Background(), []string{"--user", "nobody", "--config", filepath.Join(t.TempDir(), "missing.json"), "--unit-dir", unitDir, "--reload=false"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "only supports the root account") {
		t.Fatalf("install accepted a non-root service identity: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(unitDir, agent.AgentUnitName)); !os.IsNotExist(err) {
		t.Fatalf("rejected install changed the unit directory: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err = runPrepareSystemdState([]string{"--user", "nobody"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "only supports --user root") {
		t.Fatalf("prepare accepted a non-root service identity: %v", err)
	}
}

func TestAgentCommandsRequireRoot(t *testing.T) {
	for _, operation := range []string{"enrollment", "recovery", "runtime"} {
		if err := requireAgentCommandRoot(operation, 1000); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
			t.Errorf("non-root %s command was accepted: %v", operation, err)
		}
		if err := requireAgentCommandRoot(operation, 0); err != nil {
			t.Errorf("root %s command was rejected: %v", operation, err)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("dispatch checks for non-root commands require a non-root test process")
	}
	for _, command := range []string{"enroll", "recover", "run"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), []string{command}, strings.NewReader(""), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), "requires root privileges") {
				t.Fatalf("non-root CLI command %s was accepted: %v", command, err)
			}
		})
	}
}
