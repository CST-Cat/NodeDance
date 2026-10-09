package agent

import (
	"context"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type composeWriterAdapter struct{ writer *socketEnvelopeWriter }

func (adapter composeWriterAdapter) Send(ctx context.Context, envelope protocol.Envelope) error {
	return adapter.writer.send(ctx, envelope)
}

func newSDKComposeBridge(shared *agentdocker.SDKEngine) (*agentcompose.Bridge, bool) {
	if shared == nil {
		return nil, false
	}
	manager, err := agentcompose.NewManager(shared)
	if err != nil {
		return nil, false
	}
	bridge, err := agentcompose.NewBridge(manager)
	return bridge, err == nil
}
