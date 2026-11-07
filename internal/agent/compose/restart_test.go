package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type bringupEngine struct {
	*composeTestEngine
	marker string
}

func (e *bringupEngine) ListAll(_ context.Context) ([]string, error) {
	if _, err := os.Stat(e.marker); err != nil {
		return []string{}, nil
	}
	return e.composeTestEngine.ListAll(nil)
}

func TestRestartEmptyProjectBringsUpAndReportsRunning(t *testing.T) {
	root := t.TempDir()
	ref := composeTestRef("restart-bringup", root)
	if err := os.WriteFile(ref.ConfigFiles[0], []byte("services:\n  web:\n    image: nginx\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "docker")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$0.args"
case "$*" in
  *" config --format json") printf '%s' '{"services":{"web":{}}}' ;;
  *" up -d") touch "$0.up" ;;
esac
exit 0
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC()
	identity := &agentdocker.ComposeIdentity{Project: ref.Name, Service: "web", WorkingDir: root, ConfigFiles: ref.ConfigFiles[0]}
	engine := &bringupEngine{composeTestEngine: &composeTestEngine{containers: map[string]agentdocker.Container{
		"container": {ID: "container", Compose: identity, Running: true, StartedAt: &startedAt},
	}}, marker: command + ".up"}
	manager := &Manager{engine: engine, dockerPath: command}
	state, err := manager.ExecuteProject(context.Background(), ref, "ndt_restart_bringup", protocol.TaskComposeRestart)
	if err != nil || state != "running" {
		t.Fatalf("restart bring-up must succeed, got state=%q err=%v", state, err)
	}
	args, _ := os.ReadFile(command + ".args")
	if !strings.Contains(string(args), "up\n-d\n") {
		t.Fatal("restart of a container-less project did not bring services up", string(args))
	}
}
