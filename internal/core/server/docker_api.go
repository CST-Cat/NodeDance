package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type dashboardDockerMessage struct {
	Type                 string                      `json:"type"`
	NodeID               string                      `json:"nodeId"`
	State                *dashboardNodeState         `json:"state,omitempty"`
	Inventory            *coredocker.View            `json:"inventory,omitempty"`
	Container            *coredocker.ContainerRecord `json:"container,omitempty"`
	PreferenceIdentities map[string]string           `json:"preferenceIdentities,omitempty"`
}

type dockerPushFingerprint struct {
	revision        uint64
	generation      uint64
	agentOnline     bool
	availability    string
	eventsConnected bool
	snapshotFresh   bool
	dataStale       bool
	staleReason     string
	containerCount  int
}

func (s *Server) dockerViewForNode(ctx context.Context, nodeID string) (dashboardNodeState, coredocker.View, error) {
	state, view, _, err := s.dockerViewStateForNode(ctx, nodeID)
	return state, view, err
}

func (s *Server) dockerViewStateForNode(ctx context.Context, nodeID string) (dashboardNodeState, coredocker.View, uint64, error) {
	state, lease, err := s.currentMetricsState(ctx, nodeID)
	if err != nil {
		return state, coredocker.View{}, 0, err
	}
	if !state.Exists {
		return state, coredocker.View{}, 0, nil
	}
	now := s.now()
	if reason := s.dockerRestoreUnavailableReason(nodeID); reason != "" {
		return state, coredocker.View{
			NodeID: nodeID, AgentID: state.AgentID, AgentOnline: state.Status == "online",
			ActiveGeneration: state.Generation, LeaseValidUntil: state.LeaseValidUntil,
			DockerAvailability: protocol.DockerAvailabilityUnavailable, DataStale: true,
			StaleReason: reason, Containers: []coredocker.ContainerRecord{}, ServerTime: now.UTC(),
		}, 0, nil
	}
	var dockerLease *coredocker.Lease
	if lease != nil {
		dockerLease = &coredocker.Lease{
			Identity:   coredocker.Identity{AgentID: lease.AgentID, NodeID: lease.NodeID},
			Generation: lease.Generation, ValidUntil: lease.ValidUntil, Status: coredocker.LeaseOnline,
		}
	}
	view, revision, ok := s.dockerSnapshotWithRevision(nodeID, dockerLease, now)
	if !ok {
		view = coredocker.View{
			NodeID: nodeID, AgentID: state.AgentID, AgentOnline: state.Status == "online",
			ActiveGeneration: state.Generation, LeaseValidUntil: state.LeaseValidUntil,
			DockerAvailability: "unknown", DataStale: true,
			StaleReason: "awaiting_docker_state", Containers: []coredocker.ContainerRecord{}, ServerTime: now.UTC(),
		}
	}
	return state, view, revision, nil
}

func (s *Server) writeDashboardDockerMessage(ctx context.Context, conn *websocket.Conn, nodeID string, previous *dockerPushFingerprint) (dockerPushFingerprint, bool, error) {
	eventCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, view, revision, err := s.dockerViewStateForNode(eventCtx, nodeID)
	if err != nil {
		return dockerPushFingerprint{}, false, err
	}
	fingerprint := dockerPushFingerprint{
		revision: revision, generation: view.ActiveGeneration, agentOnline: view.AgentOnline,
		availability: string(view.DockerAvailability), eventsConnected: view.DockerEventsConnected,
		snapshotFresh: view.DockerSnapshotFresh, dataStale: view.DataStale,
		staleReason: view.StaleReason, containerCount: len(view.Containers),
	}
	if previous != nil && *previous == fingerprint {
		return fingerprint, false, nil
	}
	err = conn.Write(eventCtx, websocket.MessageText, marshalAgentPayload(dashboardDockerMessage{
		Type: "node_containers", NodeID: nodeID, State: &state, Inventory: &view,
		PreferenceIdentities: dashboardPreferenceIdentities(nodeID, view.Containers),
	}))
	return fingerprint, err == nil, err
}

func (s *Server) adminGetNodeContainers(w http.ResponseWriter, r *http.Request, nodeID, containerID string) {
	state, view, err := s.dockerViewForNode(r.Context(), nodeID)
	if err != nil {
		http.Error(w, "container inventory unavailable", http.StatusInternalServerError)
		return
	}
	if !state.Exists {
		http.NotFound(w, r)
		return
	}
	if containerID != "" {
		for _, record := range view.Containers {
			if record.Container.ID == containerID {
				writeJSON(w, http.StatusOK, dashboardDockerMessage{Type: "node_container", NodeID: nodeID, State: &state, Container: &record,
					PreferenceIdentities: dashboardPreferenceIdentities(nodeID, []coredocker.ContainerRecord{record})})
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, dashboardDockerMessage{Type: "node_containers", NodeID: nodeID, State: &state, Inventory: &view,
		PreferenceIdentities: dashboardPreferenceIdentities(nodeID, view.Containers)})
}

func dashboardPreferenceIdentities(nodeID string, containers []coredocker.ContainerRecord) map[string]string {
	identities := make(map[string]string, len(containers))
	for _, record := range containers {
		container := record.Container
		if container.Compose == nil {
			if identity, ok := dashboard.ContainerIdentity(container.ID); ok {
				identities[container.ID] = identity
			}
			continue
		}
		compose := container.Compose
		if identity, ok := dashboard.ComposeServiceIdentity(nodeID, compose.Project, compose.WorkingDir, compose.ConfigFiles, compose.Service); ok {
			identities[container.ID] = identity
		}
	}
	if len(identities) == 0 {
		return nil
	}
	return identities
}

func dockerRoute(path string) (nodeID, containerID string, ok bool) {
	if !strings.HasPrefix(path, "/api/v1/nodes/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/nodes/"), "/")
	if len(parts) != 2 && len(parts) != 3 || !validUUID(parts[0]) || parts[1] != "containers" {
		return "", "", false
	}
	if len(parts) == 3 {
		if len(parts[2]) < 12 || len(parts[2]) > 128 {
			return "", "", false
		}
		for _, char := range parts[2] {
			if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
				return "", "", false
			}
		}
		containerID = parts[2]
	}
	return parts[0], containerID, true
}
