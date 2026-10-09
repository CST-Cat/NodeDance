package server

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	current, ok := s.authenticateRequest(w, r, false)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path == "/ws/v1/streams/terminal" {
		s.handleTerminalWebSocket(w, r, current)
		return
	}
	if r.URL.Path != "/ws/v1/dashboard" {
		// The Agent channel cannot inherit browser-session
		// authority. Unknown browser channels stay unavailable as well.
		http.NotFound(w, r)
		return
	}
	if !s.validOrigin(r) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	metricsEvents, unsubscribe := s.metrics.Subscribe()
	defer unsubscribe()
	readDone := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				readDone <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(s.websocketCheckInterval)
	defer ticker.Stop()
	dockerFingerprints := make(map[string]dockerPushFingerprint)
	if !s.dashboardSessionStillValid(ctx, current.ID) {
		_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
		return
	}
	listCtx, cancelList := context.WithTimeout(ctx, 5*time.Second)
	nodes, listErr := s.agents.ListNodes(listCtx)
	cancelList()
	if listErr != nil {
		return
	}
	for _, node := range nodes {
		if !s.dashboardSessionStillValid(ctx, current.ID) {
			_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
			return
		}
		if err := s.writeDashboardMetricsMessage(ctx, conn, node.NodeID); err != nil {
			return
		}
		if !s.dashboardSessionStillValid(ctx, current.ID) {
			_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
			return
		}
		fingerprint, _, err := s.writeDashboardDockerMessage(ctx, conn, node.NodeID, nil)
		if err != nil {
			return
		}
		dockerFingerprints[node.NodeID] = fingerprint
	}
	for {
		select {
		case <-readDone:
			return
		case <-r.Context().Done():
			return
		case nodeID, ok := <-metricsEvents:
			if !ok {
				return
			}
			if !s.dashboardSessionStillValid(ctx, current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
			if err := s.writeDashboardMetricsMessage(ctx, conn, nodeID); err != nil {
				return
			}
			if !s.dashboardSessionStillValid(ctx, current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
			fingerprint, _, err := s.writeDashboardDockerMessage(ctx, conn, nodeID, dockerFingerprintPointer(dockerFingerprints, nodeID))
			if err != nil {
				return
			}
			dockerFingerprints[nodeID] = fingerprint
		case <-ticker.C:
			if !s.dashboardSessionStillValid(ctx, current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
		}
	}
}

func dockerFingerprintPointer(fingerprints map[string]dockerPushFingerprint, nodeID string) *dockerPushFingerprint {
	previous, ok := fingerprints[nodeID]
	if !ok {
		return nil
	}
	return &previous
}

func (s *Server) dashboardSessionStillValid(parent context.Context, sessionID string) bool {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	return s.sessionStillValid(ctx, sessionID)
}
