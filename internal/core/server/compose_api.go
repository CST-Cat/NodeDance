package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	composeListTimeout        = 15 * time.Second
	maxComposeWaitersPerAgent = 16
)

type composeWaiter struct {
	result chan protocol.ComposeResponse
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
		return nil, errors.New("Agent Compose request limit reached")
	}
	if _, exists := connection.composeWaiters[request.OperationID]; exists {
		return nil, errors.New("duplicate Compose request ID")
	}
	waiter := composeWaiter{result: make(chan protocol.ComposeResponse, 1)}
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

func (s *Server) handleComposeAPI(w http.ResponseWriter, r *http.Request, _ *session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if len(parts) < 3 || !validUUID(parts[0]) || parts[1] != "compose" {
		return false
	}
	if len(parts) != 3 || parts[2] != "projects" {
		http.NotFound(w, r)
		return true
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	s.handleComposeProjects(w, r, parts[0])
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
	projects, err := s.composeProjects.Projects(r.Context(), nodeID)
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
		requestID, idErr := newContainerStreamRequestID()
		if idErr == nil {
			request := protocol.ComposeRequest{OperationID: requestID}
			response, requestErr := s.requestCompose(r.Context(), connection, request, composeListTimeout)
			if requestErr == nil && response.Status == "succeeded" && s.composeProjects.RegisterProjects(r.Context(), nodeID, response.Projects) == nil {
				inventoryFresh = true
				for key, project := range projectByKey {
					project.Services = []protocol.ComposeService{}
					project.DataStale = false
					projectByKey[key] = project
				}
				for _, project := range response.Projects {
					projectByKey[project.Ref.Key] = project
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
	sortComposeProjects(items)
	writeJSON(w, http.StatusOK, map[string]any{"projects": items, "serverTime": serverTime, "dataStale": view.DataStale || !inventoryFresh})
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

func (s *Server) requestCompose(ctx context.Context, connection *agentConnection, request protocol.ComposeRequest, timeout time.Duration) (protocol.ComposeResponse, error) {
	waiter, err := connection.addComposeWaiter(request)
	if err != nil {
		return protocol.ComposeResponse{}, err
	}
	defer connection.removeComposeWaiter(request.OperationID)
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxMessageBytes-1024 {
		return protocol.ComposeResponse{}, errors.New("Compose inventory request exceeds the protocol size limit")
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
		return protocol.ComposeResponse{}, errors.New("Agent disconnected during Compose inventory request")
	case <-timer.C:
		return protocol.ComposeResponse{}, errors.New("Agent Compose inventory response timed out")
	}
}

func (s *Server) handleAgentComposeResponse(connection *agentConnection, envelope protocol.Envelope) error {
	var response protocol.ComposeResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.OperationID != envelope.RequestID {
		return errors.New("invalid Compose response")
	}
	waiter, waiting := connection.takeComposeWaiter(envelope.RequestID)
	if !waiting {
		return nil
	}
	if err := protocol.ValidateComposeResponse(response, protocol.ComposeRequest{OperationID: envelope.RequestID}); err != nil {
		return err
	}
	waiter.result <- response
	return nil
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
				ContainerID: container.ID, ContainerName: container.Name, State: container.State, Health: string(container.Health),
			})
		}
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
