package agent

import agentcompose "github.com/CST-Cat/NodeDance/internal/agent/compose"

func newSDKComposeBridge(manager *agentcompose.Manager) (*agentcompose.Bridge, bool) {
	if manager == nil {
		return nil, false
	}
	bridge, err := agentcompose.NewBridge(manager)
	return bridge, err == nil
}
