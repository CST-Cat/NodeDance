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
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
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
	return tailscale.SharedCoordinator(dataDir, service, s.tasks)
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
		if r.Method == http.MethodGet {
			coordinator, err := s.tailscaleCoordinator(current)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "deployment manager could not load its private state"})
				return true
			}
			tasks, err := coordinator.UnresolvedTasks(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "unresolved deployment tasks could not be read"})
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
			return true
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		var request tailscale.DeployRequest
		if !decodeBoundedJSON(w, r, &request, 128<<10) {
			return true
		}
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		if idempotencyKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "Idempotency-Key is required"})
			return true
		}
		coordinator, err := s.tailscaleCoordinator(current)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "deployment manager could not load its private state"})
			return true
		}
		task, err := coordinator.Start(r.Context(), request, idempotencyKey, sql.NullInt64{Int64: 1, Valid: true}, s.effectiveRemoteAddr(r))
		clearTailscaleSecrets(&request)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, coretasks.ErrIdempotencyConflict) || errors.Is(err, coretasks.ErrResourceBusy) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]string{"message": err.Error()})
			return true
		}
		writeJSON(w, http.StatusAccepted, task)
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/discovery/deployments/") {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/discovery/deployments/")
		if strings.HasSuffix(id, "/resolve") {
			id = strings.TrimSuffix(id, "/resolve")
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return true
			}
			s.handleResolveTailscaleDeployment(w, r, current, id)
			return true
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		coordinator, err := s.tailscaleCoordinator(current)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "deployment manager could not load its private state"})
			return true
		}
		task, ok := coordinator.Task(r.Context(), id)
		if !ok {
			http.NotFound(w, r)
			return true
		}
		writeJSON(w, http.StatusOK, task)
		return true
	}
	return false
}

func (s *Server) handleResolveTailscaleDeployment(w http.ResponseWriter, r *http.Request, current *session, taskID string) {
	s.tailscaleResolveMu.Lock()
	defer s.tailscaleResolveMu.Unlock()

	var request struct {
		Outcome       string `json:"outcome"`
		ObservedState string `json:"observedState"`
		Confirmed     bool   `json:"confirmed"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !request.Confirmed {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "confirm that you inspected the target VPS before resolving this task"})
		return
	}
	var status taskstate.Status
	var evidence coretasks.Evidence
	var result coretasks.Result
	switch {
	case request.Outcome == "succeeded" && (request.ObservedState == "connected" || request.ObservedState == "unavailable" || request.ObservedState == "absent"):
		status = taskstate.Succeeded
		evidence = coretasks.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true, PostconditionVerified: true}
		result = coretasks.Result{Code: coretasks.ResultVerified, ObservedState: request.ObservedState}
	case request.Outcome == "failed" && (request.ObservedState == "installation_failed" || request.ObservedState == "not_installed"):
		status = taskstate.Failed
		evidence = coretasks.Evidence{ExecutionAttempted: true, ExecutionCompleted: true, ActualResultConfirmed: true, FailureConfirmed: true}
		result = coretasks.Result{Code: coretasks.ResultFailed, ObservedState: request.ObservedState}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "select a verified deployment outcome and matching observed state"})
		return
	}
	task, err := s.tasks.GetByID(r.Context(), taskID)
	if errors.Is(err, coretasks.ErrTaskNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "deployment task could not be read", http.StatusInternalServerError)
		return
	}
	if task.Intent.Action != coretasks.ActionAgentDeploy {
		http.NotFound(w, r)
		return
	}
	if task.Status != taskstate.Unknown || task.DeliveryState != "done" || task.ReconciliationRequired {
		http.Error(w, "deployment task is not awaiting administrator review", http.StatusConflict)
		return
	}
	coordinator, err := s.tailscaleCoordinator(current)
	if err != nil {
		http.Error(w, "deployment task could not access its synchronized state", http.StatusInternalServerError)
		return
	}
	if status == taskstate.Succeeded {
		if task.Intent.AgentDeploy == nil || task.NodeID == "" {
			http.Error(w, "deployment peer association could not be verified", http.StatusConflict)
			return
		}
		if err := coordinator.AssociatePeer(task.Intent.AgentDeploy.PeerIdentity, task.NodeID); err != nil {
			http.Error(w, "deployment peer association could not be persisted", http.StatusInternalServerError)
			return
		}
	} else {
		if task.Intent.AgentDeploy == nil || task.NodeID == "" {
			http.Error(w, "deployment peer association could not be verified", http.StatusConflict)
			return
		}
		if err := coordinator.DisassociatePeer(task.Intent.AgentDeploy.PeerIdentity, task.NodeID); err != nil {
			http.Error(w, "failed deployment peer association could not be cleared", http.StatusInternalServerError)
			return
		}
	}
	if err := s.tasks.ResolveUncertainTask(r.Context(), task.NodeID, task.TaskID, status, evidence, result, sql.NullInt64{Int64: 1, Valid: true}, s.effectiveRemoteAddr(r)); err != nil {
		if errors.Is(err, coretasks.ErrTaskStateConflict) {
			http.Error(w, "deployment task is not awaiting administrator review", http.StatusConflict)
		} else {
			http.Error(w, "deployment task outcome could not be recorded", http.StatusInternalServerError)
		}
		return
	}
	resolved, ok := coordinator.Task(r.Context(), task.TaskID)
	if !ok {
		http.Error(w, "deployment task could not be read after resolution", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resolved)
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
