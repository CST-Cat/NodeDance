package agent

import (
	"context"

	agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type composeWriterAdapter struct{ writer *socketEnvelopeWriter }

func (adapter composeWriterAdapter) Send(ctx context.Context, envelope protocol.Envelope) error {
	return adapter.writer.send(ctx, envelope)
}

func newSDKComposeBridge(manager *agentcompose.Manager) (*agentcompose.Bridge, bool) {
	if manager == nil {
		return nil, false
	}
	bridge, err := agentcompose.NewBridge(manager)
	return bridge, err == nil
}
