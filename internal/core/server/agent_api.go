package server

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type createAgentEnrollmentRequest struct {
	DisplayName string `json:"displayName"`
}

type consumeAgentEnrollmentRequest struct {
	RequestID  string `json:"requestId"`
	Credential string `json:"credential"`
}

type agentIdentityResponse struct {
	AgentID         string `json:"agentId"`
	NodeID          string `json:"nodeId"`
	DisplayName     string `json:"displayName"`
	Status          string `json:"status"`
	CredentialState string `json:"credentialState,omitempty"`
}

func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.secureAgentRequest(r) {
		http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
		return
	}
	enrollmentToken, ok := authorizationCredential(r.Header.Get("Authorization"), "Enrollment")
	if !ok {
		http.Error(w, "enrollment rejected", http.StatusUnauthorized)
		return
	}
	var request consumeAgentEnrollmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if !validUUID(request.RequestID) || !validDeviceCredential(request.Credential) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	identity, err := s.agents.ConsumeEnrollment(r.Context(), auth.DigestToken(enrollmentToken), auth.DigestToken(request.Credential), request.RequestID, s.effectiveRemoteAddr(r))
	if errors.Is(err, agents.ErrEnrollmentRejected) {
		http.Error(w, "enrollment rejected", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "enrollment failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, agentIdentityResponse{AgentID: identity.AgentID, NodeID: identity.NodeID, DisplayName: identity.DisplayName, Status: identity.Status, CredentialState: "active"})
}

func (s *Server) handleAgentIdentity(w http.ResponseWriter, r *http.Request) {
	if !s.secureAgentRequest(r) {
		http.Error(w, "secure Agent transport required", http.StatusUpgradeRequired)
		return
	}
	credential, ok := authorizationCredential(r.Header.Get("Authorization"), "Bearer")
	if !ok || !validDeviceCredential(credential) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	identity, err := s.agents.IdentityByCredential(r.Context(), auth.DigestToken(credential))
	if errors.Is(err, agents.ErrUnauthorized) || errors.Is(err, agents.ErrRevoked) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "identity lookup failed", http.StatusInternalServerError)
		return
	}
	credentialState := "active"
	if identity.CredentialIsPending {
		credentialState = "pending"
	}
	writeJSON(w, http.StatusOK, agentIdentityResponse{AgentID: identity.AgentID, NodeID: identity.NodeID, DisplayName: identity.DisplayName, Status: identity.Status, CredentialState: credentialState})
}

func (s *Server) secureAgentRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	remote := remoteIP(r)
	if s.isTrustedProxy(remote) {
		proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		return proto == "https"
	}
	return s.development && remote != nil && remote.IsLoopback()
}

func authorizationCredential(header, scheme string) (string, bool) {
	if len(header) < len(scheme)+2 || len(header) > 1024 {
		return "", false
	}
	actualScheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(actualScheme, scheme) || value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	return value, true
}

func validDeviceCredential(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	withoutDashes := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(withoutDashes)
	if err != nil || len(decoded) != 16 {
		return false
	}
	return decoded[6]>>4 == 4 && decoded[8]>>6 == 2
}

func (s *Server) adminCreateAgentEnrollment(w http.ResponseWriter, r *http.Request, current *session) {
	var request createAgentEnrollmentRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	name, ok := normalizeDisplayName(request.DisplayName)
	if !ok {
		http.Error(w, "invalid node display name", http.StatusBadRequest)
		return
	}
	enrollment, err := s.agents.CreateEnrollment(r.Context(), name, s.effectiveRemoteAddr(r), sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		http.Error(w, "enrollment creation failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"nodeId": enrollment.NodeID, "displayName": enrollment.DisplayName,
		"token": enrollment.Token, "expiresAt": enrollment.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		"expiresInSeconds": int(agents.EnrollmentLifetime.Seconds()),
	})
}

func (s *Server) adminListAgents(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.agents.ListNodes(r.Context())
	if err != nil {
		http.Error(w, "Agent list unavailable", http.StatusInternalServerError)
		return
	}
	serverTime := s.now().UTC()
	type nodeView struct {
		NodeID          string                       `json:"nodeId"`
		AgentID         string                       `json:"agentId,omitempty"`
		DisplayName     string                       `json:"displayName"`
		Status          string                       `json:"status"`
		Generation      uint64                       `json:"generation"`
		LastSeen        string                       `json:"lastSeen,omitempty"`
		LeaseValidUntil string                       `json:"leaseValidUntil,omitempty"`
		Protocol        int                          `json:"protocolVersion,omitempty"`
		Version         string                       `json:"agentVersion,omitempty"`
		Capabilities    []string                     `json:"capabilities"`
		Permissions     *protocol.RuntimePermissions `json:"runtimePermissions,omitempty"`
	}
	result := make([]nodeView, 0, len(nodes))
	for _, node := range nodes {
		view := nodeView{NodeID: node.NodeID, AgentID: node.AgentID, DisplayName: node.DisplayName, Status: node.Status,
			Generation: node.ConnectionGeneration, Protocol: node.Protocol, Version: node.AgentVersion, Capabilities: node.Capabilities}
		if view.Status == "online" && !node.HasLastSeen {
			view.Status = "offline"
		}
		if node.Permissions.OS != "" {
			permissions := node.Permissions
			view.Permissions = &permissions
		}
		if node.HasLastSeen {
			leaseValidUntil := node.LastSeen.Add(s.agentOfflineTimeout)
			view.LastSeen = node.LastSeen.UTC().Format(time.RFC3339Nano)
			view.LeaseValidUntil = leaseValidUntil.UTC().Format(time.RFC3339Nano)
			// Persisted status is materialized asynchronously by the lease watcher
			// and sweeper. The API must never present an expired heartbeat lease as
			// currently online while that cleanup is pending.
			if view.Status == "online" && (serverTime.Before(node.LastSeen) || !serverTime.Before(leaseValidUntil)) {
				view.Status = "offline"
			}
		}
		result = append(result, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": result, "serverTime": serverTime.Format(time.RFC3339Nano)})
}

func (s *Server) adminAgentAction(w http.ResponseWriter, r *http.Request, current *session) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/agents/"), "/")
	if len(parts) != 2 || !validUUID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	agentID, action := parts[0], parts[1]
	switch action {
	case "revoke":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		identity, err := s.agents.Revoke(r.Context(), agentID, s.effectiveRemoteAddr(r), sql.NullInt64{Int64: 1, Valid: true})
		if errors.Is(err, agents.ErrNotFound) || errors.Is(err, agents.ErrRevoked) {
			http.Error(w, "Agent not found or already revoked", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "Agent revocation failed", http.StatusInternalServerError)
			return
		}
		s.closeAgentConnection(identity.AgentID, 0)
		writeJSON(w, http.StatusOK, agentIdentityResponse{AgentID: identity.AgentID, NodeID: identity.NodeID, DisplayName: identity.DisplayName, Status: identity.Status})
	case "rotate":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		identity, err := s.agents.RequestRotation(r.Context(), agentID, s.effectiveRemoteAddr(r), sql.NullInt64{Int64: 1, Valid: true})
		if errors.Is(err, agents.ErrNotFound) || errors.Is(err, agents.ErrRevoked) {
			http.Error(w, "Agent not found or revoked", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "Agent rotation request failed", http.StatusInternalServerError)
			return
		}
		rotationID := identity.RotationRequestedID
		if rotationID == "" {
			rotationID = identity.CredentialRotationID
		}
		if rotationID != "" {
			s.sendAgentRotation(identity.AgentID, rotationID)
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"agentId": identity.AgentID, "rotationId": rotationID, "status": "requested"})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) sendAgentRotation(agentID, rotationID string) {
	s.agentConnectionsMu.Lock()
	connection := s.agentConnections[agentID]
	s.agentConnectionsMu.Unlock()
	if connection == nil {
		return
	}
	connection.enqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeRotateRequest, Generation: connection.generation, RequestID: rotationID})
}
