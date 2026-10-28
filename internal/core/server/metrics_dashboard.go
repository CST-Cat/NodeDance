package server

import (
	"context"
	"database/sql"
	"errors"
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
	var status string
	var lastSeen sql.NullInt64
	err := s.store.Reader().QueryRowContext(ctx, `SELECT n.status,n.connection_generation,n.last_seen_at,COALESCE(d.id,'')
		FROM nodes n LEFT JOIN agent_devices d ON d.node_id=n.id WHERE n.id=?`, nodeID).
		Scan(&status, &state.Generation, &lastSeen, &state.AgentID)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil, nil
	}
	if err != nil {
		return state, nil, err
	}
	state.Exists = true
	if status == "pending" || state.AgentID == "" {
		state.Status, state.Reason = "pending", "awaiting_agent_registration"
		return state, nil, nil
	}
	if status == "revoked" {
		state.Status, state.Reason = "revoked", "agent_revoked"
		return state, nil, nil
	}
	seen := time.Unix(0, lastSeen.Int64).UTC()
	if lastSeen.Valid {
		state.LeaseValidUntil = seen.Add(s.agentOfflineTimeout).UTC()
	}
	if status == "online" && state.Generation != 0 && lastSeen.Valid &&
		!now.Before(seen) && now.Before(state.LeaseValidUntil) {
		state.Status = "online"
		return state, &coremetrics.Lease{
			Identity:   coremetrics.Identity{AgentID: state.AgentID, NodeID: nodeID},
			Generation: state.Generation, ValidUntil: state.LeaseValidUntil, Status: coremetrics.LeaseOnline,
		}, nil
	}
	state.Reason = "agent_offline"
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
