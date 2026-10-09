package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/containeractions"
	"github.com/CST-Cat/NodeDance/internal/agent/containerrebuild"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/agent/taskrunner"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var errTaskSnapshotRequired = errors.New("complete task snapshot is required")

// taskBridgeRuntime is created once for the Agent process. WebSocket
// reconnects attach a generation to its Runner but never own or cancel its
// journal, executor, or accepted work.
type taskBridgeRuntime struct {
	nodeID  string
	journal *taskjournal.Store
	engine  *containeractions.SDKEngine
	runner  *taskrunner.Runner
	rebuild *containerrebuild.Manager
	store   *containerrebuild.Store
}

func openTaskBridge(ctx context.Context, configPath, nodeID string) (*taskBridgeRuntime, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("Agent identity is not available for the task journal")
	}
	journalPath := filepath.Join(filepath.Dir(configPath), "tasks.sqlite")
	journal, err := taskjournal.Open(ctx, journalPath, nodeID)
	if err != nil {
		return nil, fmt.Errorf("open durable Agent task journal: %w", err)
	}
	closeOnError := func(err error, engine *containeractions.SDKEngine) (*taskBridgeRuntime, error) {
		if engine != nil {
			_ = engine.Close()
		}
		_ = journal.Close()
		return nil, err
	}
	engine, err := containeractions.NewSDKEngine(os.Getenv("DOCKER_HOST"))
	if err != nil {
		// Still recover interrupted journal state before declining the bridge.
		recoveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, recoverErr := journal.RecoverInterrupted(recoveryCtx)
		cancel()
		if recoverErr != nil {
			return closeOnError(fmt.Errorf("recover Agent task journal: %w", recoverErr), nil)
		}
		return closeOnError(fmt.Errorf("create task Docker Engine client: %w", err), nil)
	}
	rebuildStore, err := containerrebuild.OpenStore(ctx, filepath.Join(filepath.Dir(configPath), "rebuilds.sqlite"))
	if err != nil {
		return closeOnError(fmt.Errorf("open durable Agent container rebuild store: %w", err), engine)
	}
	closeOnError = func(err error, engine *containeractions.SDKEngine) (*taskBridgeRuntime, error) {
		if engine != nil {
			_ = engine.Close()
		}
		_ = rebuildStore.Close()
		_ = journal.Close()
		return nil, err
	}
	dockerEngine, err := containerrebuild.NewDockerEngine(engine.DockerClient())
	if err != nil {
		return closeOnError(fmt.Errorf("create container rebuild Docker adapter: %w", err), engine)
	}
	rebuildManager, err := containerrebuild.NewManager(dockerEngine, journal, rebuildStore, containerrebuild.Options{})
	if err != nil {
		return closeOnError(fmt.Errorf("create durable container rebuild manager: %w", err), engine)
	}
	executor, err := containeractions.New(engine, journal, containeractions.Options{Rebuilder: &agentRebuildExecutor{manager: rebuildManager}})
	if err != nil {
		return closeOnError(fmt.Errorf("create durable container action executor: %w", err), engine)
	}
	runner, err := taskrunner.New(nodeID, journal, executor, taskrunner.Options{})
	if err != nil {
		return closeOnError(fmt.Errorf("create Agent task runner: %w", err), engine)
	}
	if err := runner.Start(ctx); err != nil {
		return closeOnError(fmt.Errorf("start Agent task runner: %w", err), engine)
	}
	return &taskBridgeRuntime{nodeID: nodeID, journal: journal, engine: engine, runner: runner, rebuild: rebuildManager, store: rebuildStore}, nil
}

func (b *taskBridgeRuntime) close() error {
	if b == nil {
		return nil
	}
	var first error
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := b.runner.Stop(stopCtx); err != nil {
		first = fmt.Errorf("stop Agent task runner: %w", err)
	}
	cancel()
	if err := b.engine.Close(); err != nil && first == nil {
		first = fmt.Errorf("close task Docker Engine client: %w", err)
	}
	if err := b.journal.Close(); err != nil && first == nil {
		first = fmt.Errorf("close Agent task journal: %w", err)
	}
	if err := b.store.Close(); err != nil && first == nil {
		first = fmt.Errorf("close Agent container rebuild store: %w", err)
	}
	return first
}

func runTaskBridgeSession(ctx context.Context, writer *socketEnvelopeWriter, runner *taskrunner.Runner, rebuild *containerrebuild.Manager, generation uint64, nodeID, journalID string, incoming <-chan protocol.Envelope) error {
	capabilities := []string{protocol.CapabilityTaskBridge}
	if err := runner.Connect(generation, journalID, capabilities); err != nil {
		return errors.New("Agent task bridge could not attach this connection")
	}
	defer runner.Disconnect(generation)
	hello := protocol.TaskJournalHello{NodeID: nodeID, JournalID: journalID, Capacity: runner.AvailableCapacity(), CapacityLimit: runner.CapacityLimit()}
	if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskJournalHello, "", 0, hello); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-runner.ReportsChanged():
			if err := flushAgentTaskReports(ctx, writer, runner, generation); err != nil {
				if errors.Is(err, errTaskSnapshotRequired) {
					if sendErr := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskJournalHello, "", 0, hello); sendErr != nil {
						return sendErr
					}
					continue
				}
				return err
			}
		case envelope := <-incoming:
			switch envelope.Type {
			case protocol.TypeContainerRebuildPlanRequest:
				var request protocol.ContainerRebuildPlanRequest
				if err := decodeSocketPayload(envelope.Payload, &request); err != nil || protocol.ValidateContainerRebuildPlanRequest(envelope, request, generation) != nil || rebuild == nil {
					return errors.New("Core container rebuild plan request is invalid")
				}
				planCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				plan, planErr := rebuild.Plan(planCtx, request.ContainerID, request.Spec)
				cancel()
				response := protocol.ContainerRebuildPlanResponse{Plan: &plan}
				if planErr != nil {
					response.Plan = nil
					response.ErrorCode = rebuildPlanErrorCode(planErr)
				}
				if err := protocol.ValidateContainerRebuildPlanResponse(protocol.Envelope{Version: protocol.CurrentVersion,
					Type: protocol.TypeContainerRebuildPlanResponse, Generation: generation, RequestID: envelope.RequestID}, response, envelope.RequestID, generation); err != nil {
					return errors.New("Agent container rebuild plan response is invalid")
				}
				if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeContainerRebuildPlanResponse, envelope.RequestID, 0, response); err != nil {
					return err
				}
			case protocol.TypeTaskJournalStatus:
				var status protocol.TaskJournalStatus
				if err := decodeSocketPayload(envelope.Payload, &status); err != nil || protocol.ValidateTaskJournalStatus(envelope, status, generation) != nil || status.JournalID != journalID {
					return errors.New("Core task journal status is invalid")
				}
				if !status.Accepted || status.ReviewRequired || !status.SnapshotAccepted {
					continue
				}
				if err := runner.AcknowledgeSnapshot(generation, status.SnapshotID); err != nil {
					return errors.New("Core task snapshot acknowledgement is invalid")
				}
				if err := runner.MarkSynchronized(generation, journalID); errors.Is(err, taskrunner.ErrNotSynchronized) {
					if sendErr := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskJournalHello, "", 0, hello); sendErr != nil {
						return sendErr
					}
					continue
				} else if err != nil {
					return errors.New("Core task journal synchronization failed")
				}
				if err := flushAgentTaskReports(ctx, writer, runner, generation); err != nil {
					if errors.Is(err, errTaskSnapshotRequired) {
						if sendErr := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskJournalHello, "", 0, hello); sendErr != nil {
							return sendErr
						}
						continue
					}
					return err
				}
			case protocol.TypeTaskSnapshotRequest:
				var request protocol.TaskSnapshotRequest
				if err := decodeSocketPayload(envelope.Payload, &request); err != nil || protocol.ValidateTaskSnapshotRequest(envelope, request, nodeID, journalID, generation) != nil {
					return errors.New("Core task snapshot request is invalid")
				}
				pages, err := runner.SnapshotPages(generation, request.SnapshotID)
				if err != nil {
					return errors.New("Agent task snapshot could not be read")
				}
				for _, page := range pages {
					if err := protocol.ValidateTaskSnapshotPage(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskSnapshotPage, Generation: generation,
						Sequence: uint64(page.Page) + 1, RequestID: page.SnapshotID}, page, nodeID, journalID, generation); err != nil {
						return errors.New("Agent task snapshot is invalid")
					}
					if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskSnapshotPage, page.SnapshotID, uint64(page.Page)+1, page); err != nil {
						return err
					}
				}
			case protocol.TypeTaskDispatch:
				var dispatch protocol.TaskDispatch
				if err := decodeSocketPayload(envelope.Payload, &dispatch); err != nil {
					return errors.New("Core task dispatch is invalid")
				}
				report, err := runner.AcceptDispatch(ctx, generation, envelope, dispatch)
				if err != nil {
					return errors.New("Agent could not durably accept Core task")
				}
				if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskReport, report.TaskID, 0, report); err != nil {
					return err
				}
			case protocol.TypeTaskReconcile:
				var request protocol.TaskReconcileRequest
				if err := decodeSocketPayload(envelope.Payload, &request); err != nil {
					return errors.New("Core task reconciliation request is invalid")
				}
				report, err := runner.AcceptReconcile(ctx, generation, envelope, request)
				if err != nil {
					return errors.New("Agent rejected task reconciliation request")
				}
				if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskReport, report.TaskID, 0, report); err != nil {
					return err
				}
			case protocol.TypeTaskReportAck:
				var ack protocol.TaskReportAck
				if err := decodeSocketPayload(envelope.Payload, &ack); err != nil || protocol.ValidateTaskReportAck(envelope, ack, generation) != nil {
					return errors.New("Core task report acknowledgement is invalid")
				}
				if !ack.Accepted {
					if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskJournalHello, "", 0, hello); err != nil {
						return err
					}
					continue
				}
				if err := runner.AcknowledgeReport(generation, ack.TaskID, ack.ReportRevision); err != nil {
					if errors.Is(err, taskrunner.ErrStaleReportAck) {
						// A newer durable state may have replaced this report before
						// its ACK arrived. Keep the newer report pending and let its
						// ACK or a complete snapshot clear it; an old ACK is not a
						// reason to tear down the synchronized connection.
						continue
					}
					return errors.New("Core task report acknowledgement is stale")
				}
			default:
				return errors.New("Core sent an unsupported task bridge message")
			}
		}
	}
}

func flushAgentTaskReports(ctx context.Context, writer *socketEnvelopeWriter, runner *taskrunner.Runner, generation uint64) error {
	for {
		batch, err := runner.DrainReports(generation, protocol.TaskSnapshotPageSize)
		if errors.Is(err, taskrunner.ErrNotSynchronized) {
			return nil
		}
		if err != nil {
			return errors.New("Agent task reports could not be read")
		}
		if batch.SnapshotRequired {
			return errTaskSnapshotRequired
		}
		for _, report := range batch.Reports {
			if err := sendAgentTaskEnvelope(ctx, writer, generation, protocol.TypeTaskReport, report.TaskID, 0, report); err != nil {
				return err
			}
		}
		if len(batch.Reports) < protocol.TaskSnapshotPageSize {
			return nil
		}
	}
}

func sendAgentTaskEnvelope(ctx context.Context, writer *socketEnvelopeWriter, generation uint64, typeID, requestID string, sequence uint64, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > protocol.MaxTaskPayloadBytes {
		return errors.New("Agent task bridge payload exceeds its bound")
	}
	if err := writer.send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: typeID, Generation: generation,
		RequestID: requestID, Sequence: sequence, Payload: data}); err != nil {
		return errors.New("Agent task bridge write failed")
	}
	return nil
}

func rebuildPlanErrorCode(err error) string {
	switch {
	case errors.Is(err, containerrebuild.ErrUnsupportedConfiguration):
		return "unsupported_configuration"
	case errors.Is(err, containerrebuild.ErrRecordConflict):
		return "resource_conflict"
	default:
		return "inspect_unavailable"
	}
}
