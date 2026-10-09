package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	corecompose "github.com/CST-Cat/NodeDance/internal/core/compose"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	composeListTimeout        = 15 * time.Second
	composeOperationTimeout   = 12 * time.Minute
	maxComposeWaitersPerAgent = 16
)

var composeProjectKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type composeWaiter struct {
	request protocol.ComposeRequest
	result  chan protocol.ComposeResponse
}

type composeOperationRequest struct {
	Action   protocol.ComposeAction `json:"action"`
	EnvFiles []string               `json:"envFiles,omitempty"`
	Profiles []string               `json:"profiles,omitempty"`
}

type composeOperationView struct {
	OperationID string                   `json:"operationId"`
	Status      corecompose.Status       `json:"status"`
	ErrorCode   string                   `json:"errorCode,omitempty"`
	Verified    bool                     `json:"verified"`
	Project     *protocol.ComposeProject `json:"project,omitempty"`
}

func (connection *agentConnection) addComposeWaiter(request protocol.ComposeRequest) (*composeWaiter, error) {
	if connection == nil || !connection.composeEnabled || connection.ctx == nil || connection.ctx.Err() != nil {
		return nil, errors.New("Agent Compose capability is unavailable")
	}
	connection.composeMu.Lock()
	defer connection.composeMu.Unlock()
	if connection.composeWaiters == nil {
		connection.composeWaiters = make(map[string]composeWaiter)
	}
	if len(connection.composeWaiters) >= maxComposeWaitersPerAgent {
		return nil, errors.New("Agent Compose operation limit reached")
	}
	if _, exists := connection.composeWaiters[request.OperationID]; exists {
		return nil, errors.New("duplicate Compose operation ID")
	}
	waiter := composeWaiter{request: request, result: make(chan protocol.ComposeResponse, 1)}
	connection.composeWaiters[request.OperationID] = waiter
	return &waiter, nil
}

func (connection *agentConnection) takeComposeWaiter(operationID string) (composeWaiter, bool) {
	connection.composeMu.Lock()
	defer connection.composeMu.Unlock()
	waiter, ok := connection.composeWaiters[operationID]
	if ok {
		delete(connection.composeWaiters, operationID)
	}
	return waiter, ok
}

func (connection *agentConnection) removeComposeWaiter(operationID string) {
	connection.composeMu.Lock()
	delete(connection.composeWaiters, operationID)
	connection.composeMu.Unlock()
}

func (connection *agentConnection) enqueueCompose(envelope protocol.Envelope) bool {
	if connection == nil || connection.ctx == nil || connection.ctx.Err() != nil || !connection.composeEnabled {
		return false
	}
	select {
	case connection.commands <- envelope:
		return true
	case <-connection.ctx.Done():
		return false
	default:
		return false
	}
}

func (s *Server) handleComposeAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if len(parts) < 3 || !validUUID(parts[0]) || parts[1] != "compose" {
		return false
	}
	nodeID := parts[0]
	switch {
	case len(parts) == 3 && parts[2] == "projects":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleComposeProjects(w, r, nodeID)
	case len(parts) == 5 && parts[2] == "projects" && parts[4] == "actions":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		if !composeProjectKeyPattern.MatchString(parts[3]) {
			http.Error(w, "invalid Compose project key", http.StatusBadRequest)
			return true
		}
		s.handleCreateComposeOperation(w, r, current, nodeID, parts[3])
	case len(parts) == 4 && parts[2] == "operations":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleComposeOperation(w, r, nodeID, parts[3])
	case len(parts) == 5 && parts[2] == "operations" && parts[4] == "events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleComposeOperationEvents(w, r, nodeID, parts[3])
	default:
		http.NotFound(w, r)
	}
	return true
}

func (s *Server) handleComposeProjects(w http.ResponseWriter, r *http.Request, nodeID string) {
	state, view, err := s.dockerViewForNode(r.Context(), nodeID)
	if err != nil {
		http.Error(w, "Compose project inventory is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !state.Exists {
		http.NotFound(w, r)
		return
	}
	projects, err := s.composeOps.Projects(r.Context(), nodeID)
	if err != nil {
		http.Error(w, "Compose registry is unavailable", http.StatusServiceUnavailable)
		return
	}
	projectByKey := make(map[string]protocol.ComposeProject, len(projects))
	for _, project := range projects {
		projectByKey[project.Value.Ref.Key] = project.Value
	}
	serverTime := view.ServerTime.UTC()

	connection := s.activeComposeConnectionForNode(nodeID, view.ActiveGeneration)
	inventoryFresh := false
	if connection != nil && !view.DataStale {
		operationID, idErr := newContainerStreamRequestID()
		if idErr == nil {
			request := protocol.ComposeRequest{OperationID: operationID, Action: protocol.ComposeList}
			response, requestErr := s.requestCompose(r.Context(), connection, request, composeListTimeout)
			if requestErr == nil && response.Status == "succeeded" {
				if s.composeOps.RegisterProjects(r.Context(), nodeID, response.Projects) == nil {
					inventoryFresh = true
					for key, project := range projectByKey {
						project.Services = []protocol.ComposeService{}
						project.DataStale = false
						projectByKey[key] = project
					}
					s.reconcileObservedComposeOperations(r.Context(), nodeID, response.Projects)
					for _, project := range response.Projects {
						projectByKey[project.Ref.Key] = project
					}
				}
			}
		}
	}
	augmentComposeProjects(projectByKey, view)
	items := make([]protocol.ComposeProject, 0, len(projectByKey))
	for _, project := range projectByKey {
		project.DataStale = view.DataStale || !inventoryFresh
		items = append(items, project)
	}
	// Stable ordering makes UI diffs and acceptance evidence deterministic.
	sortComposeProjects(items)
	writeJSON(w, http.StatusOK, map[string]any{"projects": items, "serverTime": serverTime, "dataStale": view.DataStale || !inventoryFresh})
}

func (s *Server) reconcileObservedComposeOperations(ctx context.Context, nodeID string, projects []protocol.ComposeProject) {
	operations, err := s.composeOps.PendingResultChecks(ctx, nodeID)
	if err != nil {
		return
	}
	for _, operation := range operations {
		if !composeObservedPostcondition(operation.Request, projects) {
			continue
		}
		_, _ = s.composeOps.SetStatus(ctx, nodeID, operation.ID, corecompose.StatusSucceeded, "", true)
	}
}

// composeObservedPostcondition intentionally resolves only states that a fresh
// Agent Engine inventory can establish without replay. Inventory cannot prove
// a restart occurred, and project labels alone cannot enumerate every desired
// service for up/start, so those actions remain result_pending.
func composeObservedPostcondition(request protocol.ComposeRequest, projects []protocol.ComposeProject) bool {
	var observed *protocol.ComposeProject
	for index := range projects {
		if projects[index].Ref.Key == request.Project.Key {
			observed = &projects[index]
			break
		}
	}
	if request.Action == protocol.ComposeDown {
		return observed == nil
	}
	if request.Action != protocol.ComposeStop || observed == nil || len(observed.Services) == 0 {
		return false
	}
	instances := 0
	for _, service := range observed.Services {
		for _, instance := range service.Instances {
			instances++
			if instance.State != "exited" {
				return false
			}
		}
	}
	return instances > 0
}

func (s *Server) handleCreateComposeOperation(w http.ResponseWriter, r *http.Request, current *session, nodeID, projectKey string) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		http.Error(w, "Idempotency-Key is required", http.StatusBadRequest)
		return
	}
	var body composeOperationRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Action == protocol.ComposeList || body.Action == "" {
		http.Error(w, "invalid Compose action", http.StatusBadRequest)
		return
	}
	project, err := s.composeOps.Project(r.Context(), nodeID, projectKey)
	if errors.Is(err, corecompose.ErrOperationNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Compose project context is unavailable", http.StatusServiceUnavailable)
		return
	}
	if !project.Value.ConfigAvailable {
		http.Error(w, "Compose configuration is unavailable; container-level management remains available", http.StatusConflict)
		return
	}
	operationID, err := newContainerStreamRequestID()
	if err != nil {
		http.Error(w, "Compose operation could not be created", http.StatusInternalServerError)
		return
	}
	request := protocol.ComposeRequest{OperationID: operationID, Action: body.Action, Project: project.Value.Ref, EnvFiles: body.EnvFiles, Profiles: body.Profiles}
	if err := protocol.ValidateComposeRequest(request); err != nil {
		http.Error(w, "invalid Compose request", http.StatusBadRequest)
		return
	}
	var pinned *agentConnection
	operation, created, err := s.composeOps.EnqueueWithGate(r.Context(), nodeID, idempotencyKey, request,
		sql.NullInt64{Int64: 1, Valid: true}, current.RemoteAddr, func(context.Context) (func(), error) {
			connection, release, lockErr := s.lockComposeReady(nodeID)
			if lockErr != nil {
				return nil, lockErr
			}
			pinned = connection
			return release, nil
		})
	if err != nil {
		s.writeComposeStoreError(w, err)
		return
	}
	if created {
		s.dispatchComposeOperation(pinned, operation)
	}
	status := http.StatusAccepted
	writeJSON(w, status, composeOperationView{OperationID: operation.ID, Status: operation.Status, ErrorCode: operation.ErrorCode, Verified: operation.Verified})
}

func (s *Server) lockComposeReady(nodeID string) (*agentConnection, func(), error) {
	s.agentConnectionsMu.Lock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID && connection.composeEnabled && connection.ctx != nil && connection.ctx.Err() == nil {
			return connection, func() { s.agentConnectionsMu.Unlock() }, nil
		}
	}
	s.agentConnectionsMu.Unlock()
	return nil, nil, errors.New("Agent Compose capability is unavailable")
}

func (s *Server) activeComposeConnectionForNode(nodeID string, generation uint64) *agentConnection {
	s.agentConnectionsMu.Lock()
	defer s.agentConnectionsMu.Unlock()
	for _, connection := range s.agentConnections {
		if connection.nodeID == nodeID && connection.composeEnabled && (generation == 0 || connection.generation == generation) && connection.ctx != nil && connection.ctx.Err() == nil {
			return connection
		}
	}
	return nil
}

func (s *Server) dispatchComposeOperation(connection *agentConnection, operation corecompose.Operation) {
	if connection == nil {
		_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusFailed, "agent_unavailable", false)
		return
	}
	if _, err := s.composeOps.SetStatus(connection.ctx, operation.NodeID, operation.ID, corecompose.StatusRunning, "", false); err != nil {
		return
	}
	waiter, err := connection.addComposeWaiter(operation.Request)
	if err != nil {
		_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusFailed, "agent_busy", false)
		return
	}
	payload, err := json.Marshal(operation.Request)
	if err != nil || len(payload) > protocol.MaxMessageBytes-1024 {
		connection.removeComposeWaiter(operation.ID)
		_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusFailed, "invalid_request", false)
		return
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeRequest, Generation: connection.generation,
		RequestID: operation.ID, Payload: payload}
	if !connection.enqueueCompose(envelope) {
		connection.removeComposeWaiter(operation.ID)
		_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusFailed, "agent_busy", false)
		return
	}
	go func() {
		timer := time.NewTimer(composeOperationTimeout)
		defer timer.Stop()
		select {
		case <-connection.ctx.Done():
			connection.removeComposeWaiter(operation.ID)
			_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusUnknown, "result_pending", false)
		case <-timer.C:
			connection.removeComposeWaiter(operation.ID)
			_, _ = s.composeOps.SetStatus(context.Background(), operation.NodeID, operation.ID, corecompose.StatusUnknown, "result_pending", false)
		case <-waiter.result:
		}
	}()
}

func (s *Server) requestCompose(ctx context.Context, connection *agentConnection, request protocol.ComposeRequest, timeout time.Duration) (protocol.ComposeResponse, error) {
	waiter, err := connection.addComposeWaiter(request)
	if err != nil {
		return protocol.ComposeResponse{}, err
	}
	defer connection.removeComposeWaiter(request.OperationID)
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxMessageBytes-1024 {
		return protocol.ComposeResponse{}, errors.New("Compose request exceeds the protocol size limit")
	}
	if !connection.enqueueCompose(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeRequest,
		Generation: connection.generation, RequestID: request.OperationID, Payload: payload}) {
		return protocol.ComposeResponse{}, errors.New("Agent Compose command queue is unavailable")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case response := <-waiter.result:
		return response, nil
	case <-ctx.Done():
		return protocol.ComposeResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.ComposeResponse{}, errors.New("Agent disconnected during Compose request")
	case <-timer.C:
		return protocol.ComposeResponse{}, errors.New("Agent Compose response timed out")
	}
}

func (s *Server) handleAgentComposeResponse(ctx context.Context, connection *agentConnection, nodeID string, envelope protocol.Envelope) error {
	var response protocol.ComposeResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.OperationID != envelope.RequestID {
		return errors.New("invalid Compose response")
	}
	waiter, waiting := connection.takeComposeWaiter(envelope.RequestID)
	var request protocol.ComposeRequest
	if waiting {
		request = waiter.request
	} else {
		operation, err := s.composeOps.Get(ctx, nodeID, envelope.RequestID)
		if err != nil {
			return nil
		} // Late list response or already-pruned operation.
		request = operation.Request
	}
	if err := protocol.ValidateComposeResponse(response, request); err != nil {
		return err
	}
	if isComposeEditorAction(request.Action) {
		if waiting {
			waiter.result <- response
		}
		return nil
	}
	if request.Action != protocol.ComposeList {
		if response.Project != nil {
			if err := s.composeOps.RegisterProjects(ctx, nodeID, []protocol.ComposeProject{*response.Project}); err != nil {
				return err
			}
		}
		status := corecompose.StatusFailed
		switch response.Status {
		case "succeeded":
			status = corecompose.StatusSucceeded
		case "failed":
			status = corecompose.StatusFailed
		case "unknown":
			status = corecompose.StatusUnknown
		case "timed_out":
			status = corecompose.StatusTimedOut
		}
		if _, err := s.composeOps.SetStatus(ctx, nodeID, request.OperationID, status, response.ErrorCode, response.Verified); err != nil {
			return err
		}
	}
	if waiting {
		waiter.result <- response
	}
	return nil
}

func (s *Server) handleComposeOperation(w http.ResponseWriter, r *http.Request, nodeID, operationID string) {
	if !validUUID(operationID) {
		http.Error(w, "invalid Compose operation ID", http.StatusBadRequest)
		return
	}
	operation, err := s.composeOps.Get(r.Context(), nodeID, operationID)
	if errors.Is(err, corecompose.ErrOperationNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Compose operation is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, composeOperationView{OperationID: operation.ID, Status: operation.Status,
		ErrorCode: operation.ErrorCode, Verified: operation.Verified})
}

func (s *Server) handleComposeOperationEvents(w http.ResponseWriter, r *http.Request, nodeID, operationID string) {
	if !validUUID(operationID) {
		http.Error(w, "invalid Compose operation ID", http.StatusBadRequest)
		return
	}
	events, err := s.composeOps.Events(r.Context(), nodeID, operationID)
	if errors.Is(err, corecompose.ErrOperationNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "Compose operation history is unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *Server) writeComposeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, corecompose.ErrIdempotencyConflict), errors.Is(err, corecompose.ErrOperationConflict):
		http.Error(w, "Compose operation conflicts with an existing request", http.StatusConflict)
	default:
		http.Error(w, "Compose Agent is not ready or the operation could not be persisted", http.StatusServiceUnavailable)
	}
}

func augmentComposeProjects(projects map[string]protocol.ComposeProject, view coredocker.View) {
	for _, record := range view.Containers {
		container := record.Container
		identity := container.Compose
		if identity == nil || identity.Project == "" || identity.WorkingDir == "" || identity.ConfigFiles == "" {
			continue
		}
		files := strings.Split(identity.ConfigFiles, ",")
		ref := protocol.ComposeProjectRef{Name: identity.Project, WorkingDirectory: identity.WorkingDir, ConfigFiles: files}
		ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
		project, ok := projects[ref.Key]
		if !ok {
			continue
		}
		serviceIndex := -1
		for index := range project.Services {
			if project.Services[index].Name == identity.Service {
				serviceIndex = index
				break
			}
		}
		if serviceIndex < 0 {
			project.Services = append(project.Services, protocol.ComposeService{Name: identity.Service, Instances: []protocol.ComposeServiceInstance{}})
			serviceIndex = len(project.Services) - 1
		}
		seen := false
		for _, instance := range project.Services[serviceIndex].Instances {
			if instance.ContainerID == container.ID {
				seen = true
				break
			}
		}
		if !seen {
			project.Services[serviceIndex].Instances = append(project.Services[serviceIndex].Instances, protocol.ComposeServiceInstance{
				ContainerID: container.ID, ContainerName: container.Name, State: container.State, Health: string(container.Health)})
		}
		project.DataStale = view.DataStale
		projects[ref.Key] = project
	}
}

func sortComposeProjects(projects []protocol.ComposeProject) {
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Ref.Name == projects[j].Ref.Name {
			return projects[i].Ref.WorkingDirectory < projects[j].Ref.WorkingDirectory
		}
		return projects[i].Ref.Name < projects[j].Ref.Name
	})
}
