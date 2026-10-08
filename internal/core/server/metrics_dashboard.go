package server

import (
	"context"
	"net/http"
	"time"

	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/coder/websocket"
)

type dashboardNodeState struct {
	NodeID          string    `json:"nodeId"`
	AgentID         string    `json:"agentId,omitempty"`
	Status          string    `json:"status"`
	Generation      uint64    `json:"generation"`
	ServerTime      time.Time `json:"serverTime"`
	LeaseValidUntil time.Time `json:"leaseValidUntil,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Exists          bool      `json:"-"`
}

type dashboardMetricsMessage struct {
	Type    string              `json:"type"`
	NodeID  string              `json:"nodeId"`
	State   *dashboardNodeState `json:"state,omitempty"`
	Metrics *coremetrics.View   `json:"metrics,omitempty"`
}

func (s *Server) currentMetricsState(ctx context.Context, nodeID string) (dashboardNodeState, *coremetrics.Lease, error) {
	now := s.now()
	state := dashboardNodeState{NodeID: nodeID, Status: "offline", ServerTime: now.UTC()}
	nodes, err := s.agents.ListNodes(ctx)
	if err != nil {
		return state, nil, err
	}
	for _, node := range nodes {
		if node.NodeID != nodeID {
			continue
		}
		state.Exists = true
		state.AgentID = node.AgentID
		state.Generation = node.ConnectionGeneration
		if node.Status == "pending" || node.AgentID == "" {
			state.Status = "pending"
			state.Reason = "awaiting_agent_registration"
			return state, nil, nil
		}
		if node.Status == "revoked" {
			state.Status = "revoked"
			state.Reason = "agent_revoked"
			return state, nil, nil
		}
		if node.HasLastSeen {
			state.LeaseValidUntil = node.LastSeen.Add(s.agentOfflineTimeout).UTC()
		}
		if node.Status == "online" && node.AgentID != "" && node.ConnectionGeneration != 0 && node.HasLastSeen &&
			!now.Before(node.LastSeen) && now.Before(node.LastSeen.Add(s.agentOfflineTimeout)) {
			state.Status = "online"
			return state, &coremetrics.Lease{
				Identity:   coremetrics.Identity{AgentID: node.AgentID, NodeID: node.NodeID},
				Generation: node.ConnectionGeneration, ValidUntil: node.LastSeen.Add(s.agentOfflineTimeout),
				Status: coremetrics.LeaseOnline,
			}, nil
		}
		state.Reason = "agent_offline"
		return state, nil, nil
	}
	return state, nil, nil
}

func (s *Server) metricsViewForNode(ctx context.Context, nodeID string) (dashboardNodeState, *coremetrics.View, error) {
	state, lease, err := s.currentMetricsState(ctx, nodeID)
	if err != nil {
		return state, nil, err
	}
	view, ok := s.metrics.SnapshotAt(nodeID, lease, s.now())
	if !ok {
		if state.Status == "online" {
			state.Reason = "awaiting_first_metrics"
		} else if state.Reason == "" {
			state.Reason = "metrics_unavailable"
		}
		return state, nil, nil
	}
	state.Status = view.NodeStatus
	state.Generation = view.ActiveGeneration
	state.ServerTime = view.ServerTime
	state.LeaseValidUntil = view.LeaseValidUntil
	return state, &view, nil
}

func (s *Server) writeDashboardMetricsMessage(ctx context.Context, conn *websocket.Conn, nodeID string) error {
	eventCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, view, err := s.metricsViewForNode(eventCtx, nodeID)
	if err != nil {
		return err
	}
	message := dashboardMetricsMessage{NodeID: nodeID, State: &state}
	if view != nil {
		message.Type = "node_metrics"
		message.Metrics = view
	} else {
		message.Type = "node_status"
	}
	return conn.Write(eventCtx, websocket.MessageText, marshalAgentPayload(message))
}

func (s *Server) adminGetNodeMetrics(w http.ResponseWriter, r *http.Request, nodeID string) {
	state, view, err := s.metricsViewForNode(r.Context(), nodeID)
	if err != nil {
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	if !state.Exists {
		http.NotFound(w, r)
		return
	}
	if view == nil {
		writeJSON(w, http.StatusOK, dashboardMetricsMessage{Type: "node_status", NodeID: nodeID, State: &state})
		return
	}
	writeJSON(w, http.StatusOK, *view)
}
