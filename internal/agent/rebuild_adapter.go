package agent

import (
	"context"

	"github.com/CST-Cat/NodeDance/internal/agent/containeractions"
	"github.com/CST-Cat/NodeDance/internal/agent/containerrebuild"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
)

type agentRebuildExecutor struct{ manager *containerrebuild.Manager }

func (e *agentRebuildExecutor) ExecuteObserved(ctx context.Context, request containeractions.RebuildRequest, onRunning func(taskjournal.Snapshot)) (taskjournal.Snapshot, error) {
	return e.manager.ExecuteObserved(ctx, containerrebuild.Request{
		TaskID: request.TaskID, NodeID: request.NodeID, IdempotencyKey: request.IdempotencyKey,
		Action: request.Action, ContainerID: request.ContainerID, Spec: request.Spec,
		ConfirmationID: request.ConfirmationID,
	}, onRunning)
}

func (e *agentRebuildExecutor) Reconcile(ctx context.Context, taskID string) (taskjournal.Snapshot, error) {
	return e.manager.Reconcile(ctx, taskID)
}
