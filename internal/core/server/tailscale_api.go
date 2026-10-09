package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func (s *Server) tailscaleService(current *session) *tailscale.Service {
	service := &tailscale.Service{
		State: tailscale.NewStateStore(s.dataDir),
		Dependencies: tailscale.Dependencies{
			Discovery: tailscale.Discovery{},
			Artifacts: tailscale.DirectoryArtifacts{},
			Transport: tailscale.RealSSHTransport{},
			Create: func(ctx context.Context, name string) (tailscale.Enrollment, error) {
				enrollment, err := s.agents.CreateEnrollment(ctx, name, current.RemoteAddr, sql.NullInt64{Int64: 1, Valid: true})
				if err != nil {
					return tailscale.Enrollment{}, err
				}
				return tailscale.Enrollment{NodeID: enrollment.NodeID, Token: enrollment.Token}, nil
			},
			WaitOnline: func(ctx context.Context, nodeID string) error {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					nodes, err := s.agents.ListNodes(ctx)
					if err == nil {
						for _, node := range nodes {
							if node.NodeID == nodeID && node.Status == "online" && node.HasLastSeen && s.now().Sub(node.LastSeen) <= 30*time.Second {
								return nil
							}
						}
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ticker.C:
					}
				}
			},
			WaitDocker: func(ctx context.Context, nodeID string) (bool, error) {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					nodes, err := s.agents.ListNodes(ctx)
					if err == nil {
						for _, node := range nodes {
							if node.NodeID != nodeID || !node.HasLastSeen {
								continue
							}
							lease := &coredocker.Lease{Identity: coredocker.Identity{AgentID: node.AgentID, NodeID: node.NodeID}, Generation: node.ConnectionGeneration, ValidUntil: node.LastSeen.Add(s.agentOfflineTimeout), Status: coredocker.LeaseOnline}
							view, found := s.docker.SnapshotAt(nodeID, lease, s.now())
							if !found || view.Health == nil {
								continue
							}
							switch view.DockerAvailability {
							case protocol.DockerAvailabilityAvailable:
								return true, nil
							case protocol.DockerAvailabilityUnavailable:
								return false, nil
							}
						}
					}
					select {
					case <-ctx.Done():
						return false, ctx.Err()
					case <-ticker.C:
					}
				}
			},
		},
	}
	return service
}

func (s *Server) tailscaleCoordinator(current *session) (*tailscale.Coordinator, error) {
	service := s.tailscaleService(current)
	dataDir, err := filepath.Abs(s.dataDir)
	if err != nil {
		return nil, err
	}
	return tailscale.SharedCoordinator(dataDir, service)
}

func (s *Server) handleTailscaleAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path == "/api/v1/discovery/tailscale" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		service := s.tailscaleService(current)
		peers, err := service.ListPeers(r.Context())
		if err != nil {
			tailscaleAPIError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"peers": peers, "discoveredAt": s.now().UTC().Format(time.RFC3339Nano)})
		return true
	}
	if r.URL.Path == "/api/v1/discovery/tailscale/host-key" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request struct {
			PeerIdentity string `json:"peerIdentity"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		fingerprint, err := s.tailscaleService(current).Probe(r.Context(), request.PeerIdentity)
		if err != nil {
			tailscaleAPIError(w, err)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fingerprint})
		return true
	}
	if r.URL.Path == "/api/v1/discovery/enrollments" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request struct {
			DisplayName string `json:"displayName"`
		}
		if !decodeJSON(w, r, &request) {
			return true
		}
		name, ok := normalizeDisplayName(request.DisplayName)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid node display name"})
			return true
		}
		enrollment, err := s.agents.CreateEnrollment(r.Context(), name, s.effectiveRemoteAddr(r), sql.NullInt64{Int64: 1, Valid: true})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "Agent enrollment creation failed"})
			return true
		}
		writeJSON(w, http.StatusCreated, map[string]any{"nodeId": enrollment.NodeID, "displayName": enrollment.DisplayName, "token": enrollment.Token, "expiresAt": enrollment.ExpiresAt.UTC().Format(time.RFC3339), "expiresInSeconds": int(agents.EnrollmentLifetime.Seconds())})
		return true
	}
	if r.URL.Path == "/api/v1/discovery/deployments" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request tailscale.DeployRequest
		if !decodeBoundedJSON(w, r, &request, 128<<10) {
			return true
		}
		coordinator, err := s.tailscaleCoordinator(current)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "deployment manager could not load its private state"})
			return true
		}
		task, err := coordinator.Start(r.Context(), request)
		clearTailscaleSecrets(&request)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
			return true
		}
		s.auditEvent(r, "tailscale_ssh_deploy", "accepted", sql.NullInt64{Int64: 1, Valid: true})
		writeJSON(w, http.StatusAccepted, task)
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/discovery/deployments/") {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/discovery/deployments/")
		if !validUUID(id) {
			http.NotFound(w, r)
			return true
		}
		coordinator, err := s.tailscaleCoordinator(current)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "deployment manager could not load its private state"})
			return true
		}
		task, ok := coordinator.Task(id)
		if !ok {
			http.NotFound(w, r)
			return true
		}
		writeJSON(w, http.StatusOK, task)
		return true
	}
	return false
}

func decodeBoundedJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid request", status)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func clearTailscaleSecrets(request *tailscale.DeployRequest) {
	if request == nil {
		return
	}
	request.Credentials.Password = ""
	request.Credentials.PrivateKey = ""
	request.Credentials.Passphrase = ""
}

func tailscaleAPIError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := err.Error()
	if errors.Is(err, tailscale.ErrChangedHostKey) {
		status = http.StatusConflict
	}
	if errors.Is(err, tailscale.ErrUnavailable) {
		message = "Core 机器未安装 Tailscale CLI。节点手动安装仍可使用。"
	}
	if errors.Is(err, tailscale.ErrNotLoggedIn) {
		message = "Core 机器尚未登录 Tailscale，或当前账号没有可见节点。"
	}
	writeJSON(w, status, map[string]string{"message": message})
}
