package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const composePayloadLifetime = 10 * time.Minute
const maxPendingComposeContents = 32

type composeTaskRequest struct {
	Action     string `json:"action"`
	FileIndex  int    `json:"fileIndex,omitempty"`
	Content    string `json:"content,omitempty"`
	BaseSHA256 string `json:"baseSha256,omitempty"`
}

type composeValidateRequest struct {
	Content string `json:"content"`
}

type composeCreateRequest struct {
	Name             string `json:"name"`
	WorkingDirectory string `json:"workingDirectory"`
	Content          string `json:"content"`
}

type pendingComposeContent struct {
	nodeID     string
	generation uint64
	journalID  string
	digest     string
	content    []byte
	expires    time.Time
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

func (s *Server) handleCreateComposeProjectTask(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	request, ok := decodeComposeJSON[composeCreateRequest](w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	if len(request.Content) == 0 || len(request.Content) > protocol.MaxComposeFileBytes ||
		filepath.Clean(request.WorkingDirectory) != request.WorkingDirectory || !filepath.IsAbs(request.WorkingDirectory) ||
		request.WorkingDirectory == string(filepath.Separator) {
		http.Error(w, "invalid Compose project directory or configuration", http.StatusBadRequest)
		return
	}
	configPath := filepath.Join(request.WorkingDirectory, "compose.yaml")
	ref := protocol.ComposeProjectRef{Name: request.Name, WorkingDirectory: request.WorkingDirectory, ConfigFiles: []string{configPath}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	if protocol.ValidateComposeProjectRef(ref) != nil {
		http.Error(w, "invalid Compose project identity", http.StatusBadRequest)
		return
	}
	state, view, _, viewErr := s.dockerViewStateForNode(r.Context(), nodeID)
	if viewErr != nil || !state.Exists {
		http.Error(w, "node Docker inventory is unavailable", http.StatusServiceUnavailable)
		return
	}
	if state.Status != "online" || !view.AgentOnline || view.DataStale || view.DockerAvailability != "available" || !view.DockerSnapshotFresh {
		http.Error(w, "node Agent or Docker Engine is unavailable", http.StatusServiceUnavailable)
		return
	}
	connection := s.activeComposeConnectionForNode(nodeID, state.Generation)
	if connection == nil {
		http.Error(w, "Agent Compose capability is unavailable", http.StatusServiceUnavailable)
		return
	}
	digest := sha256.Sum256([]byte(request.Content))
	digestText := hex.EncodeToString(digest[:])
	intent := protocol.TaskIntent{Action: protocol.TaskComposeCreate, ContainerID: ref.Key,
		Compose: &protocol.ComposeTaskSpec{Project: ref, FileIndex: 0, ContentSHA256: digestText}}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		http.Error(w, "invalid Compose create task", http.StatusBadRequest)
		return
	}
	var boundJournalID string
	gate := func(context.Context) (bool, func(), error) {
		if s.activeComposeConnectionForNode(nodeID, state.Generation) != connection {
			return false, nil, coretasks.ErrNodeOffline
		}
		journalID, release, err := s.lockTaskBridgeReadySession(nodeID, state.Generation)
		if err != nil {
			return false, nil, err
		}
		boundJournalID = journalID
		return false, release, nil
	}
	_, knownRetry, lookupErr := s.tasks.FindByIdempotency(r.Context(), nodeID, key)
	if lookupErr != nil {
		http.Error(w, "Compose task idempotency could not be checked", http.StatusInternalServerError)
		return
	}
	s.composeContentMu.Lock()
	s.expireComposeContentLocked()
	if !knownRetry && len(s.composeContents) >= maxPendingComposeContents {
		s.composeContentMu.Unlock()
		http.Error(w, "Compose task payload queue is full", http.StatusServiceUnavailable)
		return
	}
	result, err := s.tasks.EnqueueWithGate(r.Context(), coretasks.EnqueueRequest{
		NodeID: nodeID, IdempotencyKey: key, Intent: intent,
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr,
	}, gate)
	if err == nil && result.Created && result.Task.Status == taskstate.Queued && result.Task.DeliveryState == "ready" && !result.Task.Evidence.DeliveryCommitted {
		s.storeComposeContentLocked(result.Task.TaskID, nodeID, state.Generation, boundJournalID, digestText, []byte(request.Content))
	}
	s.composeContentMu.Unlock()
	if err != nil {
		if errors.Is(err, coretasks.ErrNodeOffline) || errors.Is(err, coretasks.ErrNodeNotFound) || errors.Is(err, coretasks.ErrJournalNotObserved) {
			http.Error(w, "Agent task bridge is unavailable", http.StatusServiceUnavailable)
		} else {
			s.writeTaskStoreError(w, err)
		}
		return
	}
	if result.Created {
		s.signalAgentTasks(nodeID)
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"taskId": result.Task.TaskID, "status": result.Task.Status})
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
	// A Compose project registration and its source files outlive its containers.
	// Resolve only the exact node-scoped project key saved by Core; never accept a
	// working directory or config path from the Web request. Ask this live Agent
	// to prove each registered file still exists at its canonical path.
	registered, err := s.composeProjects.Projects(r.Context(), nodeID)
	if err != nil {
		return state, view, connection, protocol.ComposeProject{}, false
	}
	for _, saved := range registered {
		project := saved.Value
		if saved.NodeID != nodeID || project.Ref.Key != projectKey || protocol.ValidateComposeProjectRef(project.Ref) != nil {
			continue
		}
		available := len(project.Ref.ConfigFiles) > 0
		for index := range project.Ref.ConfigFiles {
			readID, idErr := newContainerStreamRequestID()
			if idErr != nil {
				available = false
				break
			}
			read, readErr := s.requestCompose(r.Context(), connection, protocol.ComposeRequest{
				OperationID: readID, Action: "config_read", Project: &project.Ref, FileIndex: index,
			}, composeEditorTimeout)
			if readErr != nil || read.Status != "succeeded" || !read.Verified {
				available = false
				break
			}
		}
		if !available {
			return state, view, connection, protocol.ComposeProject{}, false
		}
		project.ConfigAvailable = true
		project.ConfigReason = ""
		project.DataStale = false
		project.Services = []protocol.ComposeService{}
		if err := s.composeProjects.RegisterProjects(r.Context(), nodeID, []protocol.ComposeProject{project}); err != nil {
			return state, view, connection, protocol.ComposeProject{}, false
		}
		return state, view, connection, project, true
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
	var boundJournalID string
	gate := func(ctx context.Context) (bool, func(), error) {
		currentConnection := s.activeComposeConnectionForNode(nodeID, state.Generation)
		if currentConnection == nil || currentConnection != connection {
			return false, nil, coretasks.ErrNodeOffline
		}
		journalID, release, err := s.lockTaskBridgeReadySession(nodeID, state.Generation)
		if err != nil {
			return false, nil, err
		}
		boundJournalID = journalID
		return false, release, nil
	}
	contentLocked := action == protocol.TaskComposeSave
	if contentLocked {
		// Keep the dispatcher from claiming a durable save task before its
		// one-use content body has been installed in memory.
		s.composeContentMu.Lock()
		s.expireComposeContentLocked()
		_, knownRetry, lookupErr := s.tasks.FindByIdempotency(r.Context(), nodeID, key)
		if lookupErr != nil {
			s.composeContentMu.Unlock()
			http.Error(w, "Compose task idempotency could not be checked", http.StatusInternalServerError)
			return
		}
		if !knownRetry && len(s.composeContents) >= maxPendingComposeContents {
			s.composeContentMu.Unlock()
			http.Error(w, "Compose task payload queue is full", http.StatusServiceUnavailable)
			return
		}
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
	if action == protocol.TaskComposeSave && result.Created && result.Task.Status == taskstate.Queued && result.Task.DeliveryState == "ready" && !result.Task.Evidence.DeliveryCommitted {
		s.storeComposeContentLocked(result.Task.TaskID, nodeID, state.Generation, boundJournalID, intent.Compose.ContentSHA256, content)
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

func (s *Server) storeComposeContentLocked(taskID, nodeID string, generation uint64, journalID, digest string, content []byte) {
	s.expireComposeContentLocked()
	if previous, ok := s.composeContents[taskID]; ok {
		clear(previous.content)
	}
	s.composeContents[taskID] = pendingComposeContent{nodeID: nodeID, generation: generation, journalID: journalID, digest: digest,
		content: append([]byte(nil), content...), expires: s.now().Add(composePayloadLifetime)}
}

func (s *Server) takeComposeContent(taskID, nodeID string, generation uint64, journalID, digest string) *protocol.ComposeContent {
	s.composeContentMu.Lock()
	defer s.composeContentMu.Unlock()
	s.expireComposeContentLocked()
	entry, ok := s.composeContents[taskID]
	if !ok {
		return nil
	}
	delete(s.composeContents, taskID)
	defer clear(entry.content)
	if entry.nodeID != nodeID || entry.generation != generation || entry.journalID != journalID || entry.digest != digest || !entry.expires.After(s.now()) {
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

func (s *Server) discardComposeContentForSession(nodeID string, generation uint64) {
	if s == nil || nodeID == "" || generation == 0 {
		return
	}
	s.composeContentMu.Lock()
	taskIDs := make([]string, 0)
	for taskID, entry := range s.composeContents {
		if entry.nodeID != nodeID || entry.generation != generation {
			continue
		}
		clear(entry.content)
		delete(s.composeContents, taskID)
		taskIDs = append(taskIDs, taskID)
	}
	s.composeContentMu.Unlock()
	for _, taskID := range taskIDs {
		_, err := s.tasks.CancelUndelivered(context.Background(), nodeID, taskID, sql.NullInt64{}, "unknown")
		if err != nil && !errors.Is(err, coretasks.ErrNotDelivered) && !errors.Is(err, coretasks.ErrTaskStateConflict) && !errors.Is(err, coretasks.ErrTaskNotFound) {
			log.Printf("NodeDance could not resolve undelivered Compose task %s after Agent disconnect: %v", taskID, err)
		}
	}
}

var composeContentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
