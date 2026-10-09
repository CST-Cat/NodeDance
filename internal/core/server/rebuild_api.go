package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	rebuildPlanTimeout    = 20 * time.Second
	maxRebuildPlanWaiters = 8
)

type rebuildPlanWaiter struct {
	request protocol.ContainerRebuildPlanRequest
	result  chan protocol.ContainerRebuildPlanResponse
}

type rebuildPlanBody struct {
	Spec protocol.RebuildSpec `json:"spec"`
}

func (connection *agentConnection) addRebuildPlanWaiter(requestID string, request protocol.ContainerRebuildPlanRequest) (*rebuildPlanWaiter, error) {
	if connection == nil || !connection.taskEnabled || connection.ctx == nil || connection.ctx.Err() != nil {
		return nil, errors.New("Agent task bridge is unavailable")
	}
	connection.rebuildPlanMu.Lock()
	defer connection.rebuildPlanMu.Unlock()
	if connection.rebuildPlanWaiters == nil {
		connection.rebuildPlanWaiters = make(map[string]rebuildPlanWaiter)
	}
	if len(connection.rebuildPlanWaiters) >= maxRebuildPlanWaiters {
		return nil, errors.New("Agent rebuild plan limit reached")
	}
	if _, exists := connection.rebuildPlanWaiters[requestID]; exists {
		return nil, errors.New("duplicate rebuild plan request ID")
	}
	waiter := rebuildPlanWaiter{request: request, result: make(chan protocol.ContainerRebuildPlanResponse, 1)}
	connection.rebuildPlanWaiters[requestID] = waiter
	return &waiter, nil
}

func (connection *agentConnection) takeRebuildPlanWaiter(requestID string) (rebuildPlanWaiter, bool) {
	connection.rebuildPlanMu.Lock()
	defer connection.rebuildPlanMu.Unlock()
	waiter, ok := connection.rebuildPlanWaiters[requestID]
	if ok {
		delete(connection.rebuildPlanWaiters, requestID)
	}
	return waiter, ok
}

func (connection *agentConnection) removeRebuildPlanWaiter(requestID string) {
	connection.rebuildPlanMu.Lock()
	delete(connection.rebuildPlanWaiters, requestID)
	connection.rebuildPlanMu.Unlock()
}

func (s *Server) handleRebuildAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 8 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "nodes" || parts[4] != "containers" ||
		parts[6] != "rebuild" || parts[7] != "plan" || !validUUID(parts[3]) || !protocol.IsFullContainerID(parts[5]) {
		return false
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	s.handleCreateRebuildPlan(w, r, parts[3], parts[5])
	return true
}

func (s *Server) handleCreateRebuildPlan(w http.ResponseWriter, r *http.Request, nodeID, containerID string) {
	var body rebuildPlanBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := protocol.ValidateTaskIntent(protocol.TaskIntent{Action: protocol.TaskRebuild, ContainerID: containerID, Rebuild: &body.Spec}); err != nil {
		http.Error(w, "invalid rebuild plan request", http.StatusBadRequest)
		return
	}
	state, view, _, err := s.dockerViewStateForNode(r.Context(), nodeID)
	if err != nil {
		http.Error(w, "container inventory unavailable", http.StatusServiceUnavailable)
		return
	}
	if !state.Exists {
		http.NotFound(w, r)
		return
	}
	if state.Status != "online" || !view.AgentOnline || view.DataStale || view.DockerAvailability != protocol.DockerAvailabilityAvailable || !view.DockerSnapshotFresh {
		http.Error(w, "fresh Docker inventory is required", http.StatusServiceUnavailable)
		return
	}
	found := false
	for _, item := range view.Containers {
		if item.Container.ID != containerID {
			continue
		}
		if item.Container.Stale {
			http.Error(w, "container state is stale", http.StatusConflict)
			return
		}
		if item.Container.Compose != nil || item.Container.HostNetwork {
			http.Error(w, "Compose-managed and Host Network containers cannot be rebuilt by this workflow", http.StatusConflict)
			return
		}
		found = true
		break
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	request := protocol.ContainerRebuildPlanRequest{ContainerID: containerID, Spec: body.Spec}
	plan, err := s.requestContainerRebuildPlan(r.Context(), nodeID, view.ActiveGeneration, request)
	if err != nil {
		http.Error(w, "Agent could not create a fresh rebuild plan", http.StatusServiceUnavailable)
		return
	}
	if plan.ErrorCode != "" {
		status := http.StatusConflict
		if plan.ErrorCode == "inspect_unavailable" {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, plan.ErrorCode, status)
		return
	}
	if plan.Plan == nil || plan.Plan.ContainerID != containerID {
		http.Error(w, "Agent rebuild plan target did not match the request", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, plan.Plan)
}

func (s *Server) requestContainerRebuildPlan(ctx context.Context, nodeID string, generation uint64, request protocol.ContainerRebuildPlanRequest) (protocol.ContainerRebuildPlanResponse, error) {
	connection := s.activeAgentConnectionForNode(nodeID)
	if connection == nil || connection.generation != generation || !connection.taskEnabled {
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent task bridge is unavailable")
	}
	if _, synced := connection.taskBridgeState(); !synced {
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent task journal is not synchronized")
	}
	requestID, err := newContainerStreamRequestID()
	if err != nil {
		return protocol.ContainerRebuildPlanResponse{}, err
	}
	waiter, err := connection.addRebuildPlanWaiter(requestID, request)
	if err != nil {
		return protocol.ContainerRebuildPlanResponse{}, err
	}
	defer connection.removeRebuildPlanWaiter(requestID)
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxTaskPayloadBytes {
		return protocol.ContainerRebuildPlanResponse{}, errors.New("rebuild plan request exceeds the protocol size limit")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeContainerRebuildPlanRequest,
		Generation: generation, RequestID: requestID, Payload: payload}
	select {
	case connection.commands <- envelope:
	case <-ctx.Done():
		return protocol.ContainerRebuildPlanResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent disconnected before receiving rebuild plan request")
	default:
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent control command queue is full")
	}
	timer := time.NewTimer(rebuildPlanTimeout)
	defer timer.Stop()
	select {
	case response := <-waiter.result:
		return response, nil
	case <-ctx.Done():
		return protocol.ContainerRebuildPlanResponse{}, ctx.Err()
	case <-connection.ctx.Done():
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent disconnected during rebuild planning")
	case <-timer.C:
		return protocol.ContainerRebuildPlanResponse{}, errors.New("Agent rebuild plan timed out")
	}
}

func (s *Server) handleAgentRebuildPlanResponse(connection *agentConnection, envelope protocol.Envelope) error {
	var response protocol.ContainerRebuildPlanResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil {
		return errors.New("could not decode Agent rebuild plan")
	}
	waiter, ok := connection.takeRebuildPlanWaiter(envelope.RequestID)
	if !ok {
		return nil
	}
	if err := protocol.ValidateContainerRebuildPlanResponse(envelope, response, envelope.RequestID, connection.generation); err != nil {
		return err
	}
	if response.Plan != nil && response.Plan.ContainerID != waiter.request.ContainerID {
		return protocol.ErrInvalidTaskMessage
	}
	select {
	case waiter.result <- response:
	default:
	}
	return nil
}
