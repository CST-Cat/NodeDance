package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const composePayloadLifetime = 10 * time.Minute

type composeTaskRequest struct {
	Action     string `json:"action"`
	FileIndex  int    `json:"fileIndex,omitempty"`
	Content    string `json:"content,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type composeValidateRequest struct {
	Content string `json:"content"`
}

type pendingComposeContent struct {
	nodeID  string
	digest  string
	content []byte
	expires time.Time
}

func (s *Server) handleComposeProjectAPI(w http.ResponseWriter, r *http.Request, current *session, nodeID string, parts []string) bool {
	if len(parts) < 2 || parts[0] != "projects" || !protocol.IsFullContainerID(parts[1]) {
		return false
	}
	projectKey := parts[1]
	if len(parts) == 3 && parts[2] == "tasks" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleCreateComposeTask(w, r, current, nodeID, projectKey)
		return true
	}
	if len(parts) < 4 || parts[2] != "config" {
		http.NotFound(w, r)
		return true
	}
	fileIndex, err := strconv.Atoi(parts[3])
	if err != nil || fileIndex < 0 || fileIndex >= protocol.MaxComposeConfigFiles {
		http.NotFound(w, r)
		return true
	}
	if len(parts) == 4 && r.Method == http.MethodGet {
		s.handleReadComposeConfig(w, r, nodeID, projectKey, fileIndex)
		return true
	}
	if len(parts) == 5 && parts[4] == "validate" && r.Method == http.MethodPost {
		request, ok := decodeComposeJSON[composeValidateRequest](w, r)
		if !ok {
			return true
		}
		s.handleValidateComposeConfig(w, r, nodeID, projectKey, fileIndex, request.Content)
		return true
	}
	http.NotFound(w, r)
	return true
}

func (s *Server) handleReadComposeConfig(w http.ResponseWriter, r *http.Request, nodeID, projectKey string, fileIndex int) {
	_, _, connection, project, ok := s.freshComposeProject(r, nodeID, projectKey)
	if !ok {
		http.Error(w, "Compose project is offline, stale, or unavailable", http.StatusServiceUnavailable)
		return
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "Compose request could not be created", http.StatusInternalServerError)
		return
	}
	response, err := s.requestCompose(r.Context(), connection, protocol.ComposeRequest{OperationID: requestID,
		Action: "config_read", Project: &project.Ref, FileIndex: fileIndex}, composeEditorTimeout)
	if err != nil || response.Status != "succeeded" || !response.Verified {
		http.Error(w, "Compose config file could not be read", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fileIndex": fileIndex, "content": response.Content, "sha256": response.ContentSHA256})
}

func (s *Server) handleValidateComposeConfig(w http.ResponseWriter, r *http.Request, nodeID, projectKey string, fileIndex int, content string) {
	if len(content) > protocol.MaxComposeFileBytes {
		http.Error(w, "Compose config exceeds the supported size", http.StatusRequestEntityTooLarge)
		return
	}
	_, _, connection, project, ok := s.freshComposeProject(r, nodeID, projectKey)
	if !ok {
		http.Error(w, "Compose project is offline, stale, or unavailable", http.StatusServiceUnavailable)
		return
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "Compose request could not be created", http.StatusInternalServerError)
		return
	}
	response, err := s.requestCompose(r.Context(), connection, protocol.ComposeRequest{OperationID: requestID,
		Action: "config_validate", Project: &project.Ref, FileIndex: fileIndex, Content: content}, composeEditorTimeout)
	if err != nil {
		http.Error(w, "Compose validation could not reach the Agent", http.StatusServiceUnavailable)
		return
	}
	if response.Status != "succeeded" || !response.Verified {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"valid": false, "errorCode": response.ErrorCode})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true})
}

func (s *Server) freshComposeProject(r *http.Request, nodeID, projectKey string) (dashboardNodeState, coredocker.View, *agentConnection, protocol.ComposeProject, bool) {
	state, view, _, err := s.dockerViewStateForNode(r.Context(), nodeID)
	if err != nil || !state.Exists || state.Status != "online" || !view.AgentOnline || view.DataStale ||
		view.DockerAvailability != "available" || !view.DockerSnapshotFresh {
		return state, view, nil, protocol.ComposeProject{}, false
	}
	connection := s.activeComposeConnectionForNode(nodeID, view.ActiveGeneration)
	if connection == nil {
		return state, view, nil, protocol.ComposeProject{}, false
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		return state, view, connection, protocol.ComposeProject{}, false
	}
	response, err := s.requestCompose(r.Context(), connection, protocol.ComposeRequest{OperationID: requestID}, composeListTimeout)
	if err != nil || response.Status != "succeeded" || !response.Verified || s.composeProjects.RegisterProjects(r.Context(), nodeID, response.Projects) != nil {
		return state, view, connection, protocol.ComposeProject{}, false
	}
	for _, project := range response.Projects {
		if project.Ref.Key == projectKey && project.ConfigAvailable && !project.DataStale {
			return state, view, connection, project, true
		}
	}
	return state, view, connection, protocol.ComposeProject{}, false
}

func (s *Server) handleCreateComposeTask(w http.ResponseWriter, r *http.Request, current *session, nodeID, projectKey string) {
	request, ok := decodeComposeJSON[composeTaskRequest](w, r)
	if !ok {
		return
	}
	if request.Action != "start" && request.Action != "stop" && request.Action != "restart" && request.Action != "deploy" && request.Action != "config_save" {
		http.Error(w, "invalid Compose task action", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	state, _, connection, project, fresh := s.freshComposeProject(r, nodeID, projectKey)
	if !fresh {
		http.Error(w, "Compose project is offline, stale, or unavailable", http.StatusServiceUnavailable)
		return
	}
	var action protocol.TaskAction
	switch request.Action {
	case "start":
		action = protocol.TaskComposeStart
	case "stop":
		action = protocol.TaskComposeStop
	case "restart":
		action = protocol.TaskComposeRestart
	case "deploy":
		action = protocol.TaskComposeDeploy
	case "config_save":
		action = protocol.TaskComposeSave
	}
	intent := protocol.TaskIntent{Action: action, ContainerID: project.Ref.Key}
	var content []byte
	if action == protocol.TaskComposeSave {
		if request.FileIndex < 0 || request.FileIndex >= len(project.Ref.ConfigFiles) || len(request.Content) > protocol.MaxComposeFileBytes ||
			!composeContentHashPattern.MatchString(request.BaseSHA256) {
			http.Error(w, "invalid Compose config update", http.StatusBadRequest)
			return
		}
		newDigest := sha256.Sum256([]byte(request.Content))
		newDigestText := hex.EncodeToString(newDigest[:])
		intent.Compose = &protocol.ComposeTaskSpec{Project: project.Ref, FileIndex: request.FileIndex,
			BaseSHA256: request.BaseSHA256, ContentSHA256: newDigestText}
		content = []byte(request.Content)
		validateID, err := newContainerStreamRequestID()
		if err != nil {
			http.Error(w, "Compose validation could not be created", http.StatusInternalServerError)
			return
		}
		validation, err := s.requestCompose(r.Context(), connection, protocol.ComposeRequest{OperationID: validateID,
			Action: "config_validate", Project: &project.Ref, FileIndex: request.FileIndex, Content: request.Content}, composeEditorTimeout)
		if err != nil {
			http.Error(w, "Compose validation could not reach the Agent", http.StatusServiceUnavailable)
			return
		}
		if validation.Status != "succeeded" || !validation.Verified {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"errorCode": validation.ErrorCode, "status": "rejected"})
			return
		}
	} else if request.FileIndex != 0 || request.Content != "" || request.BaseSHA256 != "" {
		http.Error(w, "config fields are only valid for config_save", http.StatusBadRequest)
		return
	} else {
		intent.Compose = &protocol.ComposeTaskSpec{Project: project.Ref}
	}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		http.Error(w, "invalid Compose task intent", http.StatusBadRequest)
		return
	}
	gate := func(ctx context.Context) (bool, func(), error) {
		currentConnection := s.activeComposeConnectionForNode(nodeID, state.Generation)
		if currentConnection == nil || currentConnection != connection {
			return false, nil, coretasks.ErrNodeOffline
		}
		release, err := s.lockTaskBridgeReady(nodeID, state.Generation)
		if err != nil {
			return false, nil, err
		}
		return false, release, nil
	}
	contentLocked := action == protocol.TaskComposeSave
	if contentLocked {
		// Keep the dispatcher from claiming a durable save task before its
		// one-use content body has been installed in memory.
		s.composeContentMu.Lock()
		s.expireComposeContentLocked()
	}
	result, err := s.tasks.EnqueueWithGate(r.Context(), coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: key,
		Intent: intent, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}, gate)
	if err != nil {
		if contentLocked {
			s.composeContentMu.Unlock()
		}
		if errors.Is(err, coretasks.ErrNodeOffline) || errors.Is(err, coretasks.ErrNodeNotFound) {
			http.Error(w, "Agent task bridge is unavailable", http.StatusServiceUnavailable)
		} else {
			s.writeTaskStoreError(w, err)
		}
		return
	}
	if action == protocol.TaskComposeSave && result.Task.Status == taskstate.Queued && result.Task.DeliveryState == "ready" && !result.Task.Evidence.DeliveryCommitted {
		s.storeComposeContentLocked(result.Task.TaskID, nodeID, intent.Compose.ContentSHA256, content)
	}
	if contentLocked {
		s.composeContentMu.Unlock()
	}
	if result.Created || action == protocol.TaskComposeSave {
		s.signalAgentTasks(nodeID)
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"taskId": result.Task.TaskID, "status": result.Task.Status})
}

func decodeComposeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var value T
	r.Body = http.MaxBytesReader(w, r.Body, 2*protocol.MaxComposeFileBytes+16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		var tooLarge *http.MaxBytesError
		status := http.StatusBadRequest
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid Compose request", status)
		return value, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid Compose request", http.StatusBadRequest)
		return value, false
	}
	return value, true
}

func (s *Server) storeComposeContentLocked(taskID, nodeID, digest string, content []byte) {
	s.expireComposeContentLocked()
	if previous, ok := s.composeContents[taskID]; ok {
		clear(previous.content)
	}
	s.composeContents[taskID] = pendingComposeContent{nodeID: nodeID, digest: digest,
		content: append([]byte(nil), content...), expires: s.now().Add(composePayloadLifetime)}
}

func (s *Server) takeComposeContent(taskID, nodeID, digest string) *protocol.ComposeContent {
	s.composeContentMu.Lock()
	defer s.composeContentMu.Unlock()
	s.expireComposeContentLocked()
	entry, ok := s.composeContents[taskID]
	if !ok {
		return nil
	}
	delete(s.composeContents, taskID)
	defer clear(entry.content)
	if entry.nodeID != nodeID || entry.digest != digest || !entry.expires.After(s.now()) {
		return nil
	}
	return &protocol.ComposeContent{SHA256: entry.digest, Content: append([]byte(nil), entry.content...)}
}

func (s *Server) expireComposeContentLocked() {
	now := s.now()
	for taskID, entry := range s.composeContents {
		if !entry.expires.After(now) {
			clear(entry.content)
			delete(s.composeContents, taskID)
		}
	}
}

func (s *Server) clearComposeContent(taskID string) {
	s.composeContentMu.Lock()
	if entry, ok := s.composeContents[taskID]; ok {
		clear(entry.content)
	}
	delete(s.composeContents, taskID)
	s.composeContentMu.Unlock()
}

func (s *Server) clearAllComposeContent() {
	s.composeContentMu.Lock()
	for taskID, entry := range s.composeContents {
		clear(entry.content)
		delete(s.composeContents, taskID)
	}
	s.composeContentMu.Unlock()
}

var composeContentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
