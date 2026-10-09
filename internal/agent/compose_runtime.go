package agent

import (
	"context"
	"os"
	"strings"
	"time"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type composeWriterAdapter struct{ writer *socketEnvelopeWriter }

func (adapter composeWriterAdapter) Send(ctx context.Context, envelope protocol.Envelope) error {
	return adapter.writer.send(ctx, envelope)
}

func newSDKComposeBridge() (*agentcompose.Bridge, func(), bool) {
	dockerHost := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	engine, err := agentdocker.NewSDKEngine(dockerHost)
	if err != nil {
		return nil, func() {}, false
	}
	runner := agentcompose.ExecRunner{DockerHost: dockerHost}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	version, commandErr := runner.Run(ctx, ".", []string{"compose", "version", "--short"}, dockerHost)
	cancel()
	if commandErr != nil || len(strings.TrimSpace(string(version))) == 0 {
		_ = engine.Close()
		return nil, func() {}, false
	}
	manager, err := agentcompose.NewManager(engine, runner, agentcompose.Options{DockerHost: dockerHost})
	if err != nil {
		_ = engine.Close()
		return nil, func() {}, false
	}
	bridge, err := agentcompose.NewBridge(manager)
	if err != nil {
		_ = engine.Close()
		return nil, func() {}, false
	}
	return bridge, func() { _ = engine.Close() }, true
}
