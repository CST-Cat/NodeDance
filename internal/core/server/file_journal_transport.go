package server

import (
	"context"
	"database/sql"
	"errors"

	corefiletasks "github.com/CST-Cat/NodeDance/internal/core/filetasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const fileJournalQueryBatch = protocol.MaxFileJournalTasks

func (s *Server) reconcileUnknownFileTasks(ctx context.Context, connection *agentConnection) {
	tasks, err := s.fileTasks.ReconciliationCandidates(ctx, connection.nodeID, 10000)
	if err != nil || len(tasks) == 0 {
		return
	}
	for start := 0; start < len(tasks); start += fileJournalQueryBatch {
		if ctx.Err() != nil || connection.ctx.Err() != nil {
			return
		}
		end := min(start+fileJournalQueryBatch, len(tasks))
		query, err := newFileJournalQuery(connection, tasks[start:end])
		if err != nil {
			return
		}
		select {
		case connection.commands <- query:
		case <-ctx.Done():
			return
		case <-connection.ctx.Done():
			return
		}
	}
}

func newFileJournalQuery(connection *agentConnection, tasks []corefiletasks.Task) (protocol.Envelope, error) {
	if connection == nil || len(tasks) == 0 || len(tasks) > protocol.MaxFileJournalTasks {
		return protocol.Envelope{}, errors.New("file journal query batch is invalid")
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		return protocol.Envelope{}, err
	}
	query := protocol.FileJournalQuery{TaskIDs: make([]string, 0, len(tasks))}
	requested := make(map[string]struct{}, len(tasks))
	for _, task := range tasks {
		query.TaskIDs = append(query.TaskIDs, task.TaskID)
		requested[task.TaskID] = struct{}{}
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileJournalQuery, Generation: connection.generation,
		RequestID: requestID, Payload: marshalAgentPayload(query)}
	if protocol.ValidateFileJournalQuery(envelope, connection.generation, query) != nil {
		return protocol.Envelope{}, errors.New("file journal query failed protocol validation")
	}
	connection.fileJournalMu.Lock()
	if connection.fileJournalQueries == nil {
		connection.fileJournalQueries = make(map[string]map[string]struct{})
	}
	connection.fileJournalQueries[requestID] = requested
	connection.fileJournalMu.Unlock()
	return envelope, nil
}

func (s *Server) handleAgentFileJournalReply(connection *agentConnection, envelope protocol.Envelope) error {
	if connection == nil || !connection.fileJournalEnabled || envelope.Sequence != 0 {
		return errors.New("Agent file journal is unavailable")
	}
	var reply protocol.FileJournalReply
	if decodeAgentPayload(envelope.Payload, &reply) != nil || protocol.ValidateFileJournalReply(envelope, connection.generation, reply) != nil {
		return errors.New("Agent file journal response is invalid")
	}
	connection.fileJournalMu.Lock()
	requested, ok := connection.fileJournalQueries[envelope.RequestID]
	if ok {
		delete(connection.fileJournalQueries, envelope.RequestID)
	}
	connection.fileJournalMu.Unlock()
	if !ok {
		return errors.New("Agent file journal response has no outstanding query")
	}
	for _, result := range reply.Results {
		if _, exists := requested[result.TaskID]; !exists {
			return errors.New("Agent file journal response contains an unrequested task")
		}
		status := taskstate.Status(result.Status)
		if status == taskstate.Unknown {
			continue
		}
		task, err := s.fileTasks.Get(context.Background(), connection.nodeID, result.TaskID)
		if errors.Is(err, corefiletasks.ErrNotFound) {
			continue
		}
		if err != nil {
			return errors.New("Core file task could not be read during Agent reconciliation")
		}
		if task.Status != taskstate.Unknown {
			continue
		}
		if err := s.fileTasks.Resolve(context.Background(), connection.nodeID, result.TaskID, status, result.ResultCode, sql.NullInt64{}, "unknown"); err != nil {
			if errors.Is(err, corefiletasks.ErrStateConflict) {
				continue
			}
			return errors.New("Agent file task reconciliation could not be persisted")
		}
	}
	return nil
}
