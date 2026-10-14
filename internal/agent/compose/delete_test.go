package compose

import (
	"context"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type deletionEngine struct {
	*composeTestEngine
	marker string
}

func (e *deletionEngine) ListAll(ctx context.Context) ([]string, error) {
	if _, err := os.Stat(e.marker); err == nil {
		return []string{}, nil
	}
	return e.composeTestEngine.ListAll(ctx)
}
func TestDeleteProjectPreservesConfigAndNeverRequestsVolumeRemoval(t *testing.T) {
	root := t.TempDir()
	ref := composeTestRef("app", root)
	config := []byte("services: {}\n")
	if err := os.WriteFile(ref.ConfigFiles[0], config, 0600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "docker")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$0.args"
case "$*" in *" down --remove-orphans") touch "$0.deleted" ;; esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	identity := &agentdocker.ComposeIdentity{Project: ref.Name, WorkingDir: root, ConfigFiles: strings.Join(ref.ConfigFiles, ",")}
	engine := &deletionEngine{composeTestEngine: &composeTestEngine{containers: map[string]agentdocker.Container{"container": {ID: "container", Compose: identity}}}, marker: command + ".deleted"}
	manager := &Manager{engine: engine, dockerPath: command}
	state, err := manager.ExecuteProject(context.Background(), ref, "task", protocol.TaskComposeDelete)
	if err != nil || state != "removed" {
		t.Fatal(state, err)
	}
	after, err := os.ReadFile(ref.ConfigFiles[0])
	if err != nil || string(after) != string(config) {
		t.Fatal("config changed", err)
	}
	args, _ := os.ReadFile(command + ".args")
	if !strings.Contains(string(args), "down\n--remove-orphans\n") || strings.Contains(string(args), "--volumes") || strings.Contains(string(args), "--rmi") {
		t.Fatal("unsafe deletion arguments", string(args))
	}
}
