package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	corecomposeedit "github.com/CST-Cat/NodeDance/internal/core/composeedit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const composeEditorOperationTimeout = 15 * time.Minute

type composeEditorHTTPInput struct {
	EnvFiles []string                    `json:"envFiles,omitempty"`
	Profiles []string                    `json:"profiles,omitempty"`
	Editor   protocol.ComposeEditorInput `json:"editor"`
}

type composeEditorOperationView struct {
	OperationID       string                 `json:"operationId"`
	Status            corecomposeedit.Status `json:"status"`
	ErrorCode         string                 `json:"errorCode,omitempty"`
	Verified          bool                   `json:"verified"`
	AffectedServices  []string               `json:"affectedServices,omitempty"`
	Impact            []string               `json:"impact,omitempty"`
	RollbackConfirmed bool                   `json:"rollbackConfirmed,omitempty"`
	DataBackup        bool                   `json:"dataBackup"`
}

func (s *Server) handleComposeEditorAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if len(parts) < 3 || !validUUID(parts[0]) || parts[1] != "compose" {
		return false
	}
	nodeID := parts[0]
	switch {
	case len(parts) == 6 && parts[2] == "projects" && parts[4] == "editor" && parts[5] == "source":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		if !composeProjectKeyPattern.MatchString(parts[3]) {
			http.Error(w, "invalid Compose project key", http.StatusBadRequest)
			return true
		}
		s.handleComposeEditorSource(w, r, nodeID, parts[3])
	case len(parts) == 6 && parts[2] == "projects" && parts[4] == "editor" && (parts[5] == "preview" || parts[5] == "apply"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		if !composeProjectKeyPattern.MatchString(parts[3]) {
			http.Error(w, "invalid Compose project key", http.StatusBadRequest)
			return true
		}
		if parts[5] == "preview" {
			s.handleComposeEditorPreview(w, r, nodeID, parts[3])
		} else {
			s.handleComposeEditorApply(w, r, current, nodeID, parts[3])
		}
	case len(parts) == 6 && parts[2] == "editor" && parts[3] == "operations" && parts[5] == "events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleComposeEditorEvents(w, r, nodeID, parts[4])
	case len(parts) == 5 && parts[2] == "editor" && parts[3] == "operations":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleComposeEditorOperation(w, r, nodeID, parts[4])
	default:
		return false
	}
	return true
}

func (s *Server) handleComposeEditorSource(w http.ResponseWriter, r *http.Request, nodeID, projectKey string) {
	project, ok := s.composeEditorProject(w, r, nodeID, projectKey)
	if !ok {
		return
	}
	envFiles := r.URL.Query()["envFile"]
	profiles := r.URL.Query()["profile"]
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "could not create source request", http.StatusInternalServerError)
		return
	}
	request := protocol.ComposeRequest{OperationID: requestID, Action: protocol.ComposeEditRead, Project: project.Value.Ref, EnvFiles: envFiles, Profiles: profiles, Editor: &protocol.ComposeEditorInput{}}
	if err := protocol.ValidateComposeRequest(request); err != nil {
		http.Error(w, "invalid editor context", http.StatusBadRequest)
		return
	}
	connection := s.activeComposeEditorConnectionForNode(nodeID)
	if connection == nil {
		http.Error(w, "Compose editor Agent is not ready", http.StatusServiceUnavailable)
		return
	}
	response, err := s.requestCompose(r.Context(), connection, request, 30*time.Second)
	if err != nil || response.Editor == nil {
		http.Error(w, "Compose source files are unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"files": response.Editor.Files, "project": project.Value.Ref})
}

func (s *Server) handleComposeEditorPreview(w http.ResponseWriter, r *http.Request, nodeID, projectKey string) {
	project, ok := s.composeEditorProject(w, r, nodeID, projectKey)
	if !ok {
		return
	}
	input, ok := decodeComposeEditorBody(w, r)
	if !ok {
		return
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "could not create preview request", http.StatusInternalServerError)
		return
	}
	request := editorRequest(requestID, protocol.ComposeEditPreview, project.Value.Ref, input)
	if err := protocol.ValidateComposeRequest(request); err != nil {
		http.Error(w, "invalid Compose editor request", http.StatusBadRequest)
		return
	}
	connection := s.activeComposeEditorConnectionForNode(nodeID)
	if connection == nil {
		http.Error(w, "Compose editor Agent is not ready", http.StatusServiceUnavailable)
		return
	}
	response, err := s.requestCompose(r.Context(), connection, request, 90*time.Second)
	if err != nil {
		http.Error(w, "Compose preview could not be completed", http.StatusServiceUnavailable)
		return
	}
	if response.Status != "succeeded" || response.Editor == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errorCode": response.ErrorCode})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response.Editor)
}

func (s *Server) handleComposeEditorApply(w http.ResponseWriter, r *http.Request, current *session, nodeID, projectKey string) {
	project, ok := s.composeEditorProject(w, r, nodeID, projectKey)
	if !ok {
		return
	}
	input, ok := decodeComposeEditorBody(w, r)
	if !ok {
		return
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "could not create Compose operation", http.StatusInternalServerError)
		return
	}
	request := editorRequest(requestID, protocol.ComposeEditApply, project.Value.Ref, input)
	if err := protocol.ValidateComposeRequest(request); err != nil {
		http.Error(w, "invalid Compose editor request", http.StatusBadRequest)
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	connection, release, err := s.lockComposeEditorReady(nodeID)
	if err != nil {
		http.Error(w, "Compose editor Agent is not ready", http.StatusServiceUnavailable)
		return
	}
	operation, created, err := s.composeEditorOps.Enqueue(r.Context(), nodeID, projectKey, idempotencyKey, request, sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr)
	release()
	if err != nil {
		s.writeComposeEditorStoreError(w, err)
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, editorOperationView(operation))
		return
	}
	s.dispatchComposeEditorOperation(connection, operation, request)
	updated, getErr := s.composeEditorOps.Get(r.Context(), nodeID, operation.ID)
	if getErr == nil {
		operation = updated
	}
	writeJSON(w, http.StatusAccepted, editorOperationView(operation))
}

func (s *Server) composeEditorProject(w http.ResponseWriter, r *http.Request, nodeID, key string) (corecompose.Project, bool) {
	project, err := s.composeOps.Project(r.Context(), nodeID, key)
	if errors.Is(err, corecompose.ErrOperationNotFound) {
		http.NotFound(w, r)
		return corecompose.Project{}, false
	}
	if err != nil {
		http.Error(w, "Compose project context is unavailable", http.StatusServiceUnavailable)
		return corecompose.Project{}, false
	}
	if !project.Value.ConfigAvailable {
		http.Error(w, "Compose source files are unavailable", http.StatusConflict)
		return corecompose.Project{}, false
	}
	return project, true
}

func (s *Server) activeComposeEditorConnectionForNode(nodeID string) *agentConnection {
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID && connection.composeEnabled && connection.composeEditorEnabled && connection.ctx != nil && connection.ctx.Err() == nil {
			return connection
		}
	}
	return nil
}

func (s *Server) lockComposeEditorReady(nodeID string) (*agentConnection, func(), error) {
	s.agentConnectionsMu.Lock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID && connection.composeEnabled && connection.composeEditorEnabled && connection.ctx != nil && connection.ctx.Err() == nil {
			return connection, func() { s.agentConnectionsMu.Unlock() }, nil
		}
	}
	s.agentConnectionsMu.Unlock()
	return nil, nil, errors.New("Agent Compose editor capability is unavailable")
}

func (s *Server) dispatchComposeEditorOperation(connection *agentConnection, operation corecomposeedit.Operation, request protocol.ComposeRequest) {
	if _, err := s.composeEditorOps.SetStatus(connection.ctx, operation.NodeID, operation.ID, corecomposeedit.StatusRunning, "", false, nil); err != nil {
		return
	}
	waiter, err := connection.addComposeWaiter(request)
	if err != nil {
		_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecomposeedit.StatusFailed, "agent_busy", false, nil)
		return
	}
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxMessageBytes-1024 {
		connection.removeComposeWaiter(operation.ID)
		_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecomposeedit.StatusFailed, "invalid_request", false, nil)
		return
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeRequest, Generation: connection.generation, RequestID: operation.ID, Payload: payload}
	if !connection.enqueueCompose(envelope) {
		connection.removeComposeWaiter(operation.ID)
		_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecomposeedit.StatusFailed, "agent_busy", false, nil)
		return
	}
	go func() {
		timer := time.NewTimer(composeEditorOperationTimeout)
		defer timer.Stop()
		var response protocol.ComposeResponse
		select {
		case response = <-waiter.result:
		case <-connection.ctx.Done():
			connection.removeComposeWaiter(operation.ID)
			_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecomposeedit.StatusUnknown, "result_pending", false, nil)
			return
		case <-timer.C:
			connection.removeComposeWaiter(operation.ID)
			_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecomposeedit.StatusUnknown, "result_pending", false, nil)
			return
		}
		status := corecomposeedit.StatusFailed
		verified := false
		code := response.ErrorCode
		switch response.Status {
		case "succeeded":
			status = corecomposeedit.StatusSucceeded
			verified = response.Verified
			code = ""
		case "unknown", "timed_out":
			status = corecomposeedit.StatusUnknown
			if code == "" {
				code = "result_pending"
			}
		case "failed":
			status = corecomposeedit.StatusFailed
			if code == "" {
				code = "deployment_failed"
			}
		}
		_, _ = s.composeEditorOps.SetStatus(context.Background(), operation.NodeID, operation.ID, status, code, verified, response.Editor)
	}()
}

func (s *Server) reconcileUnknownComposeEditorOperations(ctx context.Context, connection *agentConnection) {
	operations, err := s.composeEditorOps.PendingResultChecks(ctx, connection.nodeID)
	if err != nil || len(operations) == 0 {
		return
	}
	for _, operation := range operations {
		if ctx.Err() != nil || connection.ctx.Err() != nil {
			return
		}
		project, err := s.composeOps.Project(ctx, operation.NodeID, operation.ProjectKey)
		if err != nil {
			continue
		}
		requestID, err := newContainerStreamRequestID()
		if err != nil {
			return
		}
		request := protocol.ComposeRequest{OperationID: requestID, Action: protocol.ComposeEditStatus, Project: project.Value.Ref,
			Editor: &protocol.ComposeEditorInput{TargetOperationID: operation.ID}}
		response, err := s.requestCompose(ctx, connection, request, 2*time.Minute)
		if err != nil || response.Editor == nil {
			continue
		}
		status := corecomposeedit.StatusUnknown
		verified := false
		code := response.ErrorCode
		switch response.Status {
		case "succeeded":
			status, verified, code = corecomposeedit.StatusSucceeded, response.Verified, ""
		case "failed":
			status = corecomposeedit.StatusFailed
		case "unknown", "timed_out":
			if code == "" {
				code = "result_pending"
			}
		default:
			continue
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = s.composeEditorOps.SetStatus(persistCtx, operation.NodeID, operation.ID, status, code, verified, response.Editor)
		cancel()
	}
}

func (s *Server) handleComposeEditorOperation(w http.ResponseWriter, r *http.Request, nodeID, operationID string) {
	if !validUUID(operationID) {
		http.Error(w, "invalid Compose editor operation ID", http.StatusBadRequest)
		return
	}
	operation, err := s.composeEditorOps.Get(r.Context(), nodeID, operationID)
	if errors.Is(err, corecomposeedit.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Compose editor operation is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, editorOperationView(operation))
}

func (s *Server) handleComposeEditorEvents(w http.ResponseWriter, r *http.Request, nodeID, operationID string) {
	if !validUUID(operationID) {
		http.Error(w, "invalid Compose editor operation ID", http.StatusBadRequest)
		return
	}
	events, err := s.composeEditorOps.Events(r.Context(), nodeID, operationID)
	if errors.Is(err, corecomposeedit.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Compose editor operation history is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func editorOperationView(operation corecomposeedit.Operation) composeEditorOperationView {
	return composeEditorOperationView{OperationID: operation.ID, Status: operation.Status, ErrorCode: operation.ErrorCode, Verified: operation.Verified, AffectedServices: operation.Summary.AffectedServices, Impact: operation.Summary.Impact, RollbackConfirmed: operation.Summary.RollbackConfirmed, DataBackup: operation.Summary.DataBackup}
}

func editorRequest(id string, action protocol.ComposeAction, project protocol.ComposeProjectRef, input composeEditorHTTPInput) protocol.ComposeRequest {
	return protocol.ComposeRequest{OperationID: id, Action: action, Project: project, EnvFiles: append([]string(nil), input.EnvFiles...), Profiles: append([]string(nil), input.Profiles...), Editor: &input.Editor}
}

func decodeComposeEditorBody(w http.ResponseWriter, r *http.Request) (composeEditorHTTPInput, bool) {
	var input composeEditorHTTPInput
	r.Body = http.MaxBytesReader(w, r.Body, 600<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "Compose editor request is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid Compose editor request", http.StatusBadRequest)
		}
		return input, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid Compose editor request", http.StatusBadRequest)
		return input, false
	}
	return input, true
}

func isComposeEditorAction(action protocol.ComposeAction) bool {
	return action == protocol.ComposeEditRead || action == protocol.ComposeEditPreview || action == protocol.ComposeEditApply || action == protocol.ComposeEditStatus
}

func (s *Server) writeComposeEditorStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corecomposeedit.ErrConflict), errors.Is(err, corecompose.ErrOperationConflict):
		http.Error(w, "Compose editor idempotency key conflicts with an existing request", http.StatusConflict)
	case errors.Is(err, corecomposeedit.ErrSchemaUnavailable):
		http.Error(w, "Compose editor migration is unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "Compose editor operation could not be persisted", http.StatusServiceUnavailable)
	}
}
