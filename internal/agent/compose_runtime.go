package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentcomposeedit "github.com/CST-Cat/NodeDance/internal/agent/composeedit"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type composeWriterAdapter struct{ writer *socketEnvelopeWriter }

func (adapter composeWriterAdapter) Send(ctx context.Context, envelope protocol.Envelope) error {
	return adapter.writer.send(ctx, envelope)
}

func newSDKComposeBridge(configPath string) (*agentcompose.Bridge, func(), bool, bool) {
	dockerHost := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	engine, err := agentdocker.NewSDKEngine(dockerHost)
	if err != nil {
		return nil, func() {}, false, false
	}
	runner := agentcompose.ExecRunner{DockerHost: dockerHost}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	version, commandErr := runner.Run(ctx, ".", []string{"compose", "version", "--short"}, dockerHost)
	cancel()
	if commandErr != nil || len(strings.TrimSpace(string(version))) == 0 {
		_ = engine.Close()
		return nil, func() {}, false, false
	}
	manager, err := agentcompose.NewManager(engine, runner, agentcompose.Options{DockerHost: dockerHost})
	if err != nil {
		_ = engine.Close()
		return nil, func() {}, false, false
	}
	backupDir := filepath.Join(filepath.Dir(configPath), "compose-transactions")
	editor, editorErr := agentcomposeedit.NewManager(engine, runner, agentcomposeedit.OSFileStore{}, agentcomposeedit.Options{DockerHost: dockerHost, BackupDir: backupDir})
	editorAvailable := editorErr == nil
	if editorAvailable {
		recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 2*time.Minute)
		editorErr = editor.Recover(recoverCtx)
		cancelRecover()
		editorAvailable = editorErr == nil
	}
	var bridge *agentcompose.Bridge
	if editorAvailable {
		bridge, err = agentcompose.NewBridgeWithEditor(manager, editor)
	} else {
		bridge, err = agentcompose.NewBridge(manager)
	}
	if err != nil {
		_ = engine.Close()
		return nil, func() {}, false, false
	}
	return bridge, func() { _ = engine.Close() }, true, editorAvailable
}
