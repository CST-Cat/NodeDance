package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
)

type probeConfigRequest struct {
	NodeID             string `json:"nodeId"`
	Name               string `json:"name"`
	Kind               string `json:"kind"`
	Target             string `json:"target"`
	ExpectedHTTPStatus *int   `json:"expectedHttpStatus,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds"`
	TimeoutSeconds     int    `json:"timeoutSeconds"`
	Enabled            *bool  `json:"enabled,omitempty"`
	Revision           int64  `json:"revision,omitempty"`
}

var errProbeNodeNotFound = errors.New("managed node not found")

func (s *Server) handleProbeAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path == "/api/v1/probes" {
		switch r.Method {
		case http.MethodGet:
			items, err := s.probes.List(r.Context())
			if err != nil {
				http.Error(w, "service probes could not be loaded", http.StatusInternalServerError)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"probes": items})
		case http.MethodPost:
			var request probeConfigRequest
			if !decodeJSON(w, r, &request) {
				return true
			}
			if err := requireManagedNode(r.Context(), s, request.NodeID); err != nil {
				if errors.Is(err, errProbeNodeNotFound) {
					http.Error(w, "managed node not found", http.StatusNotFound)
				} else {
					http.Error(w, "managed node lookup failed", http.StatusInternalServerError)
				}
				return true
			}
			config, err := configFromRequest(request, true)
			if err != nil {
				http.Error(w, "invalid service probe configuration", http.StatusBadRequest)
				return true
			}
			event := probeAuditEvent(s, current, "probe_create")
			created, err := s.probes.Create(r.Context(), config, event)
			if err != nil {
				http.Error(w, "service probe could not be saved", http.StatusInternalServerError)
				return true
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/probes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/probes/"), "/")
	if len(parts) < 1 || len(parts) > 2 || !validUUID(parts[0]) {
		http.NotFound(w, r)
		return true
	}
	probeID := parts[0]
	if len(parts) == 2 {
		if parts[1] != "history" || r.Method != http.MethodGet {
			if parts[1] == "history" {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			} else {
				http.NotFound(w, r)
			}
			return true
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		runs, err := s.probes.History(r.Context(), probeID, limit)
		if errors.Is(err, coreprobes.ErrNotFound) {
			http.NotFound(w, r)
		} else if err != nil {
			http.Error(w, "service probe history could not be loaded", http.StatusInternalServerError)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
		}
		return true
	}
	switch r.Method {
	case http.MethodGet:
		item, err := s.probes.Get(r.Context(), probeID)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else if err != nil {
			http.Error(w, "service probe could not be loaded", http.StatusInternalServerError)
		} else {
			writeJSON(w, http.StatusOK, item)
		}
	case http.MethodPut:
		var request probeConfigRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		config, err := configFromRequest(request, false)
		if err != nil || request.Revision < 1 || request.Enabled == nil {
			http.Error(w, "invalid service probe configuration or missing revision", http.StatusBadRequest)
			return true
		}
		config.ID, config.Revision = probeID, request.Revision
		before, getErr := s.probes.Get(r.Context(), probeID)
		if errors.Is(getErr, sql.ErrNoRows) {
			http.NotFound(w, r)
			return true
		}
		if getErr != nil {
			http.Error(w, "service probe could not be loaded", http.StatusInternalServerError)
			return true
		}
		if before.NodeID != config.NodeID {
			http.Error(w, "probe ownership cannot be changed", http.StatusBadRequest)
			return true
		}
		action := "probe_update"
		if before.Enabled != config.Enabled {
			if config.Enabled {
				action = "probe_enable"
			} else {
				action = "probe_disable"
			}
		}
		updated, err := s.probes.Update(r.Context(), config, probeAuditEvent(s, current, action))
		writeProbeMutationResult(w, r, updated, err)
	case http.MethodDelete:
		revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || revision < 1 {
			http.Error(w, "probe revision is required", http.StatusBadRequest)
			return true
		}
		event := probeAuditEvent(s, current, "probe_delete")
		err = s.probes.Delete(r.Context(), probeID, revision, event)
		if errors.Is(err, coreprobes.ErrNotFound) {
			http.NotFound(w, r)
		} else if errors.Is(err, coreprobes.ErrConflict) {
			http.Error(w, "service probe revision changed", http.StatusConflict)
		} else if err != nil {
			http.Error(w, "service probe could not be deleted", http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
	return true
}

func configFromRequest(request probeConfigRequest, isCreate bool) (coreprobes.Config, error) {
	status := 200
	if request.ExpectedHTTPStatus != nil {
		status = *request.ExpectedHTTPStatus
	}
	enabled := isCreate
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	config := coreprobes.Config{NodeID: request.NodeID, Name: request.Name, Kind: request.Kind,
		Target: request.Target, ExpectedHTTPStatus: status, IntervalSeconds: request.IntervalSeconds,
		TimeoutSeconds: request.TimeoutSeconds, Enabled: enabled}
	if request.Kind == "tcp" {
		config.ExpectedHTTPStatus = 0
	}
	return config, coreprobes.ValidateConfig(config)
}

func probeAuditEvent(s *Server, current *session, action string) audit.Event {
	return audit.Event{OccurredAt: s.now().UTC(), Action: action, Outcome: "succeeded",
		ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}
}

func requireManagedNode(ctx context.Context, s *Server, nodeID string) error {
	if !validUUID(nodeID) {
		return errors.New("invalid node ID")
	}
	nodes, err := s.agents.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.NodeID == nodeID {
			return nil
		}
	}
	return errProbeNodeNotFound
}

func writeProbeMutationResult(w http.ResponseWriter, r *http.Request, item coreprobes.Config, err error) {
	if errors.Is(err, coreprobes.ErrNotFound) {
		http.NotFound(w, r)
	} else if errors.Is(err, coreprobes.ErrConflict) {
		http.Error(w, "service probe revision changed", http.StatusConflict)
	} else if err != nil {
		http.Error(w, "service probe could not be updated", http.StatusBadRequest)
	} else {
		writeJSON(w, http.StatusOK, item)
	}
}
