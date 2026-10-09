package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	coreTaskSnapshotTimeout = 10 * time.Second
	coreTaskDispatchBurst   = 32
)

type agentTaskSnapshot struct {
	id        string
	journalID string
	page      uint32
	bytes     int
	startedAt time.Time
	reports   []coretasks.AgentTask
	seen      map[string]struct{}
}

func (c *agentConnection) setTaskJournal(journalID string, synced bool) {
	c.taskMu.Lock()
	c.taskJournalID = journalID
	c.taskSynced = synced
	c.taskMu.Unlock()
}

func (c *agentConnection) taskBridgeState() (string, bool) {
	c.taskMu.RLock()
	defer c.taskMu.RUnlock()
	return c.taskJournalID, c.taskSynced
}

func (s *Server) handleAgentTaskMessage(ctx context.Context, connection *agentConnection, identity agents.Identity, envelope protocol.Envelope) error {
	switch envelope.Type {
	case protocol.TypeTaskJournalHello:
		var hello protocol.TaskJournalHello
		if err := decodeAgentPayload(envelope.Payload, &hello); err != nil || protocol.ValidateTaskJournalHello(envelope, hello, identity.NodeID, connection.generation) != nil {
			return protocol.ErrInvalidTaskMessage
		}
		journal := coretasks.AgentConnection{NodeID: identity.NodeID, ConnectionGeneration: connection.generation, JournalID: hello.JournalID}
		observation, err := s.tasks.ObserveAgentConnection(ctx, journal)
		if err != nil {
			return err
		}
		connection.setTaskJournal(hello.JournalID, false)
		connection.taskCapacity = hello.CapacityLimit
		connection.taskSlots = 0
		connection.taskSnapshot = nil
		status := protocol.TaskJournalStatus{Accepted: !observation.ReviewRequired, JournalID: hello.JournalID,
			ReviewRequired: observation.ReviewRequired, Capacity: hello.Capacity}
		if err := s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion,
			Type: protocol.TypeTaskJournalStatus, Generation: connection.generation, Payload: marshalAgentPayload(status)}); err != nil {
			return err
		}
		if observation.ReviewRequired {
			return nil
		}
		snapshotID, err := newAgentTaskSnapshotID()
		if err != nil {
			return err
		}
		connection.taskSnapshot = &agentTaskSnapshot{id: snapshotID, journalID: hello.JournalID, startedAt: time.Now(),
			reports: make([]coretasks.AgentTask, 0), seen: make(map[string]struct{})}
		request := protocol.TaskSnapshotRequest{SnapshotID: snapshotID, JournalID: hello.JournalID}
		return s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion,
			Type: protocol.TypeTaskSnapshotRequest, Generation: connection.generation, RequestID: snapshotID, Payload: marshalAgentPayload(request)})
	case protocol.TypeTaskSnapshotPage:
		return s.acceptAgentTaskSnapshotPage(ctx, connection, identity, envelope)
	case protocol.TypeTaskReport:
		return s.acceptAgentTaskReport(ctx, connection, identity, envelope)
	default:
		return protocol.ErrInvalidTaskMessage
	}
}

func (s *Server) acceptAgentTaskSnapshotPage(ctx context.Context, connection *agentConnection, identity agents.Identity, envelope protocol.Envelope) error {
	snapshot := connection.taskSnapshot
	if snapshot == nil || time.Since(snapshot.startedAt) > coreTaskSnapshotTimeout {
		return protocol.ErrInvalidTaskMessage
	}
	var page protocol.TaskSnapshotPage
	if err := decodeAgentPayload(envelope.Payload, &page); err != nil || protocol.ValidateTaskSnapshotPage(envelope, page, identity.NodeID, snapshot.journalID, connection.generation) != nil {
		return protocol.ErrInvalidTaskMessage
	}
	if page.SnapshotID != snapshot.id || page.Page != snapshot.page || page.JournalID != snapshot.journalID || len(envelope.Payload) > protocol.MaxTaskPayloadBytes {
		return protocol.ErrInvalidTaskMessage
	}
	snapshot.bytes += len(envelope.Payload)
	if snapshot.bytes > protocol.MaxTaskSnapshotBytes || len(snapshot.reports)+len(page.Reports) > protocol.MaxTaskSnapshotTasks {
		return protocol.ErrInvalidTaskMessage
	}
	for _, report := range page.Reports {
		reportEnvelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskReport, Generation: connection.generation, RequestID: report.TaskID}
		if err := protocol.ValidateTaskReport(reportEnvelope, report, identity.NodeID, snapshot.journalID, connection.generation); err != nil {
			return err
		}
		if _, duplicate := snapshot.seen[report.TaskID]; duplicate {
			return protocol.ErrInvalidTaskMessage
		}
		converted, err := coretasks.AgentTaskFromProtocol(report)
		if err != nil {
			return err
		}
		snapshot.seen[report.TaskID] = struct{}{}
		snapshot.reports = append(snapshot.reports, converted)
	}
	if !page.Final {
		snapshot.page++
		return nil
	}
	journal := coretasks.AgentConnection{NodeID: identity.NodeID, ConnectionGeneration: connection.generation, JournalID: snapshot.journalID}
	if _, err := s.tasks.ReconcileAgentJournal(ctx, journal, snapshot.reports, true); err != nil {
		return err
	}
	for _, report := range snapshot.reports {
		if report.Status != taskstate.Succeeded {
			continue
		}
		after, err := s.tasks.Get(ctx, identity.NodeID, report.TaskID)
		if err != nil {
			return err
		}
		if err := s.migrateRebuiltContainerPreferences(ctx, after); err != nil {
			return err
		}
	}
	active := 0
	connection.taskOutstanding = make(map[string]struct{})
	for _, report := range snapshot.reports {
		_, reconciling := connection.taskReconcileOutstanding[report.TaskID]
		if report.Status == taskstate.Queued || report.Status == taskstate.Running || (report.Status == taskstate.Unknown && reconciling) {
			active++
			connection.taskOutstanding[report.TaskID] = struct{}{}
		}
	}
	connection.taskSlots = connection.taskCapacity - active
	if connection.taskSlots < 0 {
		connection.taskSlots = 0
	}
	if connection.taskSlots > connection.taskCapacity {
		connection.taskSlots = connection.taskCapacity
	}
	ack := protocol.TaskJournalStatus{Accepted: true, JournalID: snapshot.journalID, Capacity: connection.taskSlots,
		SnapshotID: snapshot.id, SnapshotAccepted: true}
	if err := s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion,
		Type: protocol.TypeTaskJournalStatus, Generation: connection.generation, RequestID: snapshot.id, Payload: marshalAgentPayload(ack)}); err != nil {
		return err
	}
	connection.setTaskJournal(snapshot.journalID, true)
	connection.taskSnapshot = nil
	s.signalAgentTasks(connection.nodeID)
	return nil
}

func (s *Server) acceptAgentTaskReport(ctx context.Context, connection *agentConnection, identity agents.Identity, envelope protocol.Envelope) error {
	journalID, synced := connection.taskBridgeState()
	if !synced || journalID == "" {
		return protocol.ErrInvalidTaskMessage
	}
	var report protocol.TaskReport
	if err := decodeAgentPayload(envelope.Payload, &report); err != nil || protocol.ValidateTaskReport(envelope, report, identity.NodeID, journalID, connection.generation) != nil {
		return protocol.ErrInvalidTaskMessage
	}
	journal := coretasks.AgentConnection{NodeID: identity.NodeID, ConnectionGeneration: connection.generation, JournalID: journalID}
	converted, err := coretasks.AgentTaskFromProtocol(report)
	if err != nil {
		return err
	}
	if _, err := s.tasks.ReconcileAgentJournal(ctx, journal, []coretasks.AgentTask{converted}, false); err != nil {
		return err
	}
	after, err := s.tasks.Get(ctx, identity.NodeID, report.TaskID)
	if err != nil {
		return err
	}
	if err := s.migrateRebuiltContainerPreferences(ctx, after); err != nil {
		return err
	}
	if _, active := connection.taskOutstanding[report.TaskID]; active &&
		(after.Status == taskstate.Unknown || taskstate.IsTerminal(after.Status)) {
		delete(connection.taskOutstanding, report.TaskID)
		if connection.taskSlots < connection.taskCapacity {
			connection.taskSlots++
		}
	}
	if after.Status == taskstate.Unknown {
		// A reconciliation response has finished for this generation. Keep the
		// attempted marker to prevent an unbounded same-generation retry loop,
		// but stop counting the completed request against active task capacity.
		delete(connection.taskReconcileOutstanding, report.TaskID)
	}
	if taskstate.IsTerminal(after.Status) {
		s.clearImageCredentials(report.TaskID)
		delete(connection.taskReconcileOutstanding, report.TaskID)
		delete(connection.taskReconcileAttempted, report.TaskID)
	}
	ack := protocol.TaskReportAck{TaskID: report.TaskID, ReportRevision: report.ReportRevision, Accepted: true}
	if err := s.writeAgentEnvelope(ctx, connection.conn, protocol.Envelope{Version: protocol.CurrentVersion,
		Type: protocol.TypeTaskReportAck, Generation: connection.generation, RequestID: report.TaskID, Payload: marshalAgentPayload(ack)}); err != nil {
		return err
	}
	s.signalAgentTasks(identity.NodeID)
	return nil
}

// migrateRebuiltContainerPreferences moves only a verified successful rebuild's
// display preferences through the production SQLite adapter or an explicitly
// injected test/alternate adapter.
func (s *Server) migrateRebuiltContainerPreferences(ctx context.Context, task coretasks.Task) error {
	if task.Status != taskstate.Succeeded || task.Intent.Action != protocol.TaskRebuild ||
		!protocol.IsFullContainerID(task.Intent.ContainerID) || !protocol.IsFullContainerID(task.Result.ResourceRevision) ||
		task.Intent.ContainerID == task.Result.ResourceRevision {
		return nil
	}
	return s.preferenceMigrator.MigrateContainer(ctx, task.NodeID, task.Intent.ContainerID, task.Result.ResourceRevision)
}

func (s *Server) dispatchAgentTasks(ctx context.Context, connection *agentConnection, identity agents.Identity) error {
	journalID, synced := connection.taskBridgeState()
	if !connection.taskEnabled || !synced || journalID == "" || connection.taskSlots <= 0 {
		return nil
	}
	journal := coretasks.AgentConnection{NodeID: identity.NodeID, ConnectionGeneration: connection.generation, JournalID: journalID}
	for handled := 0; handled < coreTaskDispatchBurst && connection.taskSlots > 0; handled++ {
		task, ok, registryAuth, err := s.claimNextImageTask(ctx, journal)
		if err != nil {
			return err
		}
		if !ok {
			if task.Status == taskstate.Failed && task.Result.Code == coretasks.ResultRegistryCredentialsUnavailable {
				continue
			}
			break
		}
		dispatch := protocol.TaskDispatch{TaskID: task.TaskID, NodeID: task.NodeID, JournalID: journalID,
			TargetID: task.Intent.ContainerID, IdempotencyKey: task.IdempotencyKey, RequestDigest: protocol.DigestString(task.RequestDigest), Intent: task.Intent}
		if task.Intent.Action == protocol.TaskImagePull {
			dispatch.RegistryAuth = registryAuth
		}
		payload, err := json.Marshal(dispatch)
		if err != nil || len(payload) > protocol.MaxTaskPayloadBytes {
			clear(payload)
			if dispatch.RegistryAuth != nil {
				dispatch.RegistryAuth.Username, dispatch.RegistryAuth.Password = "", ""
				dispatch.RegistryAuth = nil
			}
			// The safe typed intent was already persisted, but no malformed frame
			// is allowed to reach an Agent. Marking it unknown is safer than replay.
			return errors.New("durable task cannot be represented by the task wire contract")
		}
		connection.taskSlots--
		connection.taskOutstanding[task.TaskID] = struct{}{}
		message := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskDispatch, Generation: connection.generation,
			RequestID: task.TaskID, Payload: payload}
		if dispatch.RegistryAuth != nil {
			dispatch.RegistryAuth.Username, dispatch.RegistryAuth.Password = "", ""
			dispatch.RegistryAuth = nil
		}
		writeErr := s.writeAgentEnvelope(ctx, connection.conn, message)
		clear(payload)
		message.Payload = nil
		if writeErr != nil {
			return writeErr // Core delivery is already committed; reconnect reconciliation will mark uncertainty.
		}
	}
	if connection.taskSlots > 0 {
		if connection.taskReconcileOutstanding == nil {
			connection.taskReconcileOutstanding = make(map[string]struct{})
		}
		if connection.taskReconcileAttempted == nil {
			connection.taskReconcileAttempted = make(map[string]struct{})
		}
		candidates, err := s.tasks.ReconciliationCandidates(ctx, journal, coreTaskDispatchBurst)
		if err != nil {
			return err
		}
		for _, task := range candidates {
			if connection.taskSlots <= 0 {
				break
			}
			if _, attempted := connection.taskReconcileAttempted[task.TaskID]; attempted {
				continue
			}
			if _, active := connection.taskOutstanding[task.TaskID]; active {
				continue
			}
			request := protocol.TaskReconcileRequest{TaskID: task.TaskID, NodeID: task.NodeID,
				JournalID: journalID, TargetID: task.Intent.ContainerID, IdempotencyKey: task.IdempotencyKey,
				RequestDigest: protocol.DigestString(task.RequestDigest), Intent: task.Intent}
			message := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskReconcile,
				Generation: connection.generation, RequestID: task.TaskID, Payload: marshalAgentPayload(request)}
			if err := protocol.ValidateTaskReconcile(message, request, identity.NodeID, journalID, connection.generation); err != nil {
				return err
			}
			connection.taskSlots--
			connection.taskOutstanding[task.TaskID] = struct{}{}
			connection.taskReconcileOutstanding[task.TaskID] = struct{}{}
			connection.taskReconcileAttempted[task.TaskID] = struct{}{}
			if err := s.writeAgentEnvelope(ctx, connection.conn, message); err != nil {
				return err // Read-only reconciliation is safe to repeat on the next authenticated generation.
			}
		}
	}
	if connection.taskSlots > 0 {
		select {
		case connection.taskSignal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *Server) signalAgentTasks(nodeID string) {
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID != nodeID || !connection.taskEnabled {
			continue
		}
		select {
		case connection.taskSignal <- struct{}{}:
		default:
		}
		return
	}
}

func newAgentTaskSnapshotID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
