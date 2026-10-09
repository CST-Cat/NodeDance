package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

type containerActionRequest struct {
	Action               protocol.TaskAction `json:"action"`
	NewName              string              `json:"newName,omitempty"`
	DeleteConfirmed      bool                `json:"deleteConfirmed,omitempty"`
	DeleteConfirmationID string              `json:"deleteConfirmationId,omitempty"`
}

type taskView struct {
	TaskID                 string              `json:"taskId"`
	NodeID                 string              `json:"nodeId"`
	TargetID               string              `json:"targetId"`
	Action                 protocol.TaskAction `json:"action"`
	Status                 taskstate.Status    `json:"status"`
	DeliveryState          string              `json:"deliveryState"`
	ReconciliationRequired bool                `json:"reconciliationRequired"`
	Progress               taskProgressView    `json:"progress"`
	Result                 taskResultView      `json:"result"`
	CreatedAt              string              `json:"createdAt"`
	UpdatedAt              string              `json:"updatedAt"`
	StartedAt              *string             `json:"startedAt,omitempty"`
	FinishedAt             *string             `json:"finishedAt,omitempty"`
	CancelRequested        bool                `json:"cancelRequested,omitempty"`
}

type taskProgressView struct {
	Phase     string `json:"phase"`
	Completed uint64 `json:"completed"`
	Total     uint64 `json:"total"`
}

type taskResultView struct {
	Code             string `json:"code,omitempty"`
	ObservedState    string `json:"observedState,omitempty"`
	ResourceRevision string `json:"resourceRevision,omitempty"`
}

type taskAuditEventView struct {
	ID         int64  `json:"id"`
	Event      string `json:"event"`
	FromStatus string `json:"fromStatus,omitempty"`
	ToStatus   string `json:"toStatus,omitempty"`
	ActorID    *int64 `json:"actorId,omitempty"`
	RemoteAddr string `json:"remoteAddress"`
	OccurredAt string `json:"occurredAt"`
}

func (s *Server) handleTaskAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if nodeID, containerID, ok := containerActionRoute(r.URL.Path); ok {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleCreateContainerTask(w, r, current, nodeID, containerID)
		return true
	}
	if nodeID, taskID, ok := nodeTaskAuditRoute(r.URL.Path); ok {
		s.handleTaskAudit(w, r, nodeID, taskID)
		return true
	}
	if nodeID, taskID, ok := nodeTaskRoute(r.URL.Path); ok {
		if r.Method == http.MethodDelete {
			s.handleCancelUndeliveredTask(w, r, current, nodeID, taskID)
			return true
		}
		s.handleTaskLookup(w, r, nodeID, taskID)
		return true
	}
	if nodeID, ok := nodeTaskListRoute(r.URL.Path); ok {
		s.handleTaskList(w, r, nodeID)
		return true
	}
	return false
}

func (s *Server) handleCancelUndeliveredTask(w http.ResponseWriter, r *http.Request, current *session, nodeID, taskID string) {
	currentTask, lookupErr := s.tasks.Get(r.Context(), nodeID, taskID)
	if errors.Is(lookupErr, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if lookupErr != nil {
		http.Error(w, "task cancellation could not be read", http.StatusInternalServerError)
		return
	}
	if currentTask.Intent.Action == protocol.TaskImagePull && currentTask.Status == taskstate.Running {
		if err := s.requestImagePullCancellation(currentTask); err != nil {
			http.Error(w, "image pull cancellation could not be delivered", http.StatusServiceUnavailable)
			return
		}
		s.clearImageCredentials(taskID)
		view := toTaskView(currentTask)
		view.CancelRequested = true
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	task, err := s.tasks.CancelUndelivered(r.Context(), nodeID, taskID, sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr)
	if errors.Is(err, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, coretasks.ErrNotDelivered) || errors.Is(err, coretasks.ErrTaskStateConflict) {
		http.Error(w, "task may already have reached the Agent", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "task cancellation could not be persisted", http.StatusInternalServerError)
		return
	}
	s.clearImageCredentials(taskID)
	writeJSON(w, http.StatusOK, toTaskView(task))
}

func (s *Server) handleTaskAudit(w http.ResponseWriter, r *http.Request, nodeID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	events, err := s.tasks.AuditEvents(r.Context(), nodeID, taskID)
	if errors.Is(err, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "task audit lookup failed", http.StatusInternalServerError)
		return
	}
	items := make([]taskAuditEventView, 0, len(events))
	for _, event := range events {
		item := taskAuditEventView{ID: event.ID, Event: event.Event, RemoteAddr: event.RemoteAddr,
			OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano)}
		if event.FromStatus.Valid {
			item.FromStatus = event.FromStatus.String
		}
		if event.ToStatus.Valid {
			item.ToStatus = event.ToStatus.String
		}
		if event.ActorID.Valid {
			actorID := event.ActorID.Int64
			item.ActorID = &actorID
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": items})
}

func (s *Server) handleCreateContainerTask(w http.ResponseWriter, r *http.Request, current *session, nodeID, containerID string) {
	if !protocol.IsFullContainerID(containerID) {
		http.Error(w, "container ID must be a full Docker ID", http.StatusBadRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	var request containerActionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	// Deletion requires a separate, exact-target confirmation. The boolean is
	// still part of the canonical typed-intent digest; this transient full-ID
	// echo proves that the administrator confirmed the resource shown by this
	// route and is never persisted as a secret or free-form payload.
	if request.Action == protocol.TaskDelete {
		if !request.DeleteConfirmed || request.DeleteConfirmationID != containerID {
			http.Error(w, "delete confirmation must match the full target ID", http.StatusBadRequest)
			return
		}
	} else if request.DeleteConfirmed || request.DeleteConfirmationID != "" {
		http.Error(w, "delete confirmation is only valid for delete", http.StatusBadRequest)
		return
	}
	intent := coretasks.Intent{Action: request.Action, ContainerID: containerID, NewName: request.NewName, DeleteConfirmed: request.DeleteConfirmed}
	if err := protocol.ValidateTaskIntent(intent); err != nil {
		http.Error(w, "invalid container action", http.StatusBadRequest)
		return
	}
	// Capture the latest trusted Docker view before opening the task store's
	// transaction. The gated store may return an idempotent task without
	// invoking gate, even if this view is stale or unavailable.
	state, view, _, viewErr := s.dockerViewStateForNode(r.Context(), nodeID)
	gate := func(ctx context.Context) (bool, func(), error) {
		if viewErr != nil {
			return false, nil, errTaskDockerUnavailable
		}
		if !state.Exists {
			return false, nil, coretasks.ErrNodeNotFound
		}
		if state.Status != "online" || !view.AgentOnline || view.DataStale || view.DockerAvailability != "available" || !view.DockerSnapshotFresh {
			return false, nil, coretasks.ErrNodeOffline
		}
		var composeManaged bool
		found := false
		for _, record := range view.Containers {
			if record.Container.ID != containerID {
				continue
			}
			if record.Container.Stale {
				return false, nil, errTaskContainerStale
			}
			found = true
			composeManaged = record.Container.Compose != nil
			break
		}
		if !found {
			return false, nil, errTaskContainerNotFound
		}
		release, err := s.lockTaskBridgeReady(nodeID, state.Generation)
		if err != nil {
			return false, nil, err
		}
		return composeManaged, release, nil
	}
	result, err := s.tasks.EnqueueWithGate(r.Context(), coretasks.EnqueueRequest{
		NodeID: nodeID, IdempotencyKey: key, Intent: intent,
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr,
	}, gate)
	if err != nil {
		if errors.Is(err, errTaskDockerUnavailable) {
			http.Error(w, "Docker inventory unavailable", http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, errTaskContainerStale) {
			http.Error(w, "container state is stale", http.StatusConflict)
			return
		}
		if errors.Is(err, errTaskContainerNotFound) {
			http.NotFound(w, r)
			return
		}
		s.writeTaskStoreError(w, err)
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

func (s *Server) handleTaskLookup(w http.ResponseWriter, r *http.Request, nodeID, taskID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	task, err := s.tasks.Get(r.Context(), nodeID, taskID)
	if errors.Is(err, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "task lookup failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, toTaskView(task))
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > coretasks.MaxPageSize {
			http.Error(w, "invalid task page limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	var after *coretasks.Cursor
	if encoded := r.URL.Query().Get("after"); encoded != "" {
		data, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			http.Error(w, "invalid task cursor", http.StatusBadRequest)
			return
		}
		var cursor coretasks.Cursor
		if err := json.Unmarshal(data, &cursor); err != nil || cursor.CreatedAtNS <= 0 || cursor.TaskID == "" {
			http.Error(w, "invalid task cursor", http.StatusBadRequest)
			return
		}
		after = &cursor
	}
	page, err := s.tasks.List(r.Context(), nodeID, limit, after)
	if err != nil {
		http.Error(w, "task list failed", http.StatusInternalServerError)
		return
	}
	items := make([]taskView, 0, len(page.Tasks))
	for _, task := range page.Tasks {
		items = append(items, toTaskView(task))
	}
	var next string
	if page.NextCursor != nil {
		data, err := json.Marshal(page.NextCursor)
		if err != nil {
			http.Error(w, "task list failed", http.StatusInternalServerError)
			return
		}
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": items, "nextCursor": next})
}

var (
	errTaskDockerUnavailable = errors.New("Docker inventory unavailable")
	errTaskContainerStale    = errors.New("container state is stale")
	errTaskContainerNotFound = errors.New("container not found in fresh Docker inventory")
)

func (s *Server) taskBridgeReady(nodeID string, generation uint64) bool {
	release, err := s.lockTaskBridgeReady(nodeID, generation)
	if err != nil {
		return false
	}
	release()
	return true
}

// lockTaskBridgeReady pins the active synchronized connection until release.
// Store.EnqueueWithGate calls this only after its durable idempotency lookup,
// then holds the pin through new-task commit.
func (s *Server) lockTaskBridgeReady(nodeID string, generation uint64) (func(), error) {
	s.agentConnectionsMu.Lock()
	for _, connection := range s.agentConnections {
		if connection.nodeID != nodeID || connection.generation != generation || !connection.taskEnabled {
			continue
		}
		connection.taskMu.RLock()
		if !connection.taskSynced {
			connection.taskMu.RUnlock()
			s.agentConnectionsMu.Unlock()
			return nil, coretasks.ErrJournalNotObserved
		}
		return func() {
			connection.taskMu.RUnlock()
			s.agentConnectionsMu.Unlock()
		}, nil
	}
	s.agentConnectionsMu.Unlock()
	return nil, coretasks.ErrJournalNotObserved
}

func (s *Server) writeTaskStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, coretasks.ErrInvalidRequest), errors.Is(err, protocol.ErrInvalidTaskMessage):
		http.Error(w, "invalid container action", http.StatusBadRequest)
	case errors.Is(err, coretasks.ErrIdempotencyConflict), errors.Is(err, coretasks.ErrTaskIDConflict), errors.Is(err, coretasks.ErrResourceBusy), errors.Is(err, coretasks.ErrManagedRename):
		http.Error(w, "task conflicts with existing state", http.StatusConflict)
	case errors.Is(err, coretasks.ErrNotDelivered):
		http.Error(w, "task may already have reached the Agent", http.StatusConflict)
	case errors.Is(err, coretasks.ErrNodeOffline), errors.Is(err, coretasks.ErrNodeNotFound), errors.Is(err, coretasks.ErrJournalNotObserved), errors.Is(err, coretasks.ErrReconciliationNeeded):
		http.Error(w, "Agent is not ready to accept tasks", http.StatusServiceUnavailable)
	default:
		http.Error(w, "task could not be persisted", http.StatusInternalServerError)
	}
}

func toTaskView(task coretasks.Task) taskView {
	view := taskView{TaskID: task.TaskID, NodeID: task.NodeID, TargetID: task.Intent.ContainerID, Action: task.Intent.Action,
		Status: task.Status, DeliveryState: task.DeliveryState, ReconciliationRequired: task.ReconciliationRequired,
		Progress:  taskProgressView{Phase: string(task.Progress.Phase), Completed: task.Progress.Completed, Total: task.Progress.Total},
		Result:    taskResultView{Code: string(task.Result.Code), ObservedState: task.Result.ObservedState, ResourceRevision: task.Result.ResourceRevision},
		CreatedAt: task.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if task.StartedAt != nil {
		value := task.StartedAt.UTC().Format(time.RFC3339Nano)
		view.StartedAt = &value
	}
	if task.FinishedAt != nil {
		value := task.FinishedAt.UTC().Format(time.RFC3339Nano)
		view.FinishedAt = &value
	}
	return view
}

func containerActionRoute(path string) (nodeID, containerID string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "nodes" || parts[4] != "containers" || parts[6] != "actions" || !validUUID(parts[3]) {
		return "", "", false
	}
	if !protocol.IsFullContainerID(parts[5]) {
		return "", "", false
	}
	return parts[3], parts[5], true
}

func nodeTaskRoute(path string) (nodeID, taskID string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 6 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "nodes" || parts[4] != "tasks" || parts[5] == "" || !validUUID(parts[3]) {
		return "", "", false
	}
	return parts[3], parts[5], true
}

func nodeTaskAuditRoute(path string) (nodeID, taskID string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "nodes" || parts[4] != "tasks" ||
		parts[5] == "" || parts[6] != "audit" || !validUUID(parts[3]) {
		return "", "", false
	}
	return parts[3], parts[5], true
}

func nodeTaskListRoute(path string) (nodeID string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "nodes" || parts[4] != "tasks" || !validUUID(parts[3]) {
		return "", false
	}
	return parts[3], true
}
