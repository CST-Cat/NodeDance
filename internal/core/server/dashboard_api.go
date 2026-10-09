package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
)

func (s *Server) handleDashboardSettingsAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/dashboard/settings" {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.dashboardPreferences.GetSettings(r.Context())
		if err != nil {
			http.Error(w, "dashboard settings unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, settings)
	case http.MethodPut:
		var settings dashboard.Settings
		if !decodeJSON(w, r, &settings) {
			return true
		}
		if err := s.dashboardPreferences.PutSettings(r.Context(), settings); errors.Is(err, dashboard.ErrInvalidPreference) {
			http.Error(w, "invalid dashboard settings", http.StatusBadRequest)
		} else if err != nil {
			http.Error(w, "dashboard settings could not be saved", http.StatusInternalServerError)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
	return true
}

func (s *Server) handleNodeDashboardAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if len(parts) != 2 || !validUUID(parts[0]) || (parts[1] != "preferences" && parts[1] != "history") {
		return false
	}
	nodeID := parts[0]
	exists, err := s.dashboardNodeExists(r, nodeID)
	if err != nil {
		http.Error(w, "node state unavailable", http.StatusInternalServerError)
		return true
	}
	if !exists {
		http.NotFound(w, r)
		return true
	}
	switch parts[1] {
	case "preferences":
		switch r.Method {
		case http.MethodGet:
			items, err := s.dashboardPreferences.List(r.Context(), nodeID)
			if err != nil {
				http.Error(w, "dashboard preferences unavailable", http.StatusInternalServerError)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"preferences": items})
		case http.MethodPut:
			var preference dashboard.Preference
			if !decodeJSON(w, r, &preference) {
				return true
			}
			if preference.NodeID != "" && preference.NodeID != nodeID {
				http.Error(w, "preference node does not match request path", http.StatusBadRequest)
				return true
			}
			preference.NodeID = nodeID
			if err := s.dashboardPreferences.Put(r.Context(), preference); errors.Is(err, dashboard.ErrInvalidPreference) {
				http.Error(w, "invalid dashboard preference", http.StatusBadRequest)
			} else if err != nil {
				http.Error(w, "dashboard preference could not be saved", http.StatusInternalServerError)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "history":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		s.handleNodeMetricHistory(w, r, nodeID)
	}
	return true
}

func (s *Server) handleNodeMetricHistory(w http.ResponseWriter, r *http.Request, nodeID string) {
	query := r.URL.Query()
	resolution := query.Get("resolution")
	from, fromErr := time.Parse(time.RFC3339Nano, query.Get("from"))
	to, toErr := time.Parse(time.RFC3339Nano, query.Get("to"))
	now := s.now().UTC()
	if fromErr != nil || toErr != nil || !from.Before(to) || (resolution != "minute" && resolution != "hour") || to.After(now.Add(time.Minute)) {
		http.Error(w, "history range or resolution is invalid", http.StatusBadRequest)
		return
	}
	retention := 30 * 24 * time.Hour
	if resolution == "hour" {
		retention = 365 * 24 * time.Hour
	}
	if from.Before(now.Add(-retention)) {
		http.Error(w, "history range exceeds retained data", http.StatusBadRequest)
		return
	}
	result, err := s.history.Query(r.Context(), nodeID, resolution, from, to)
	if errors.Is(err, corehistory.ErrInvalidQuery) {
		http.Error(w, "history range or resolution is invalid", http.StatusBadRequest)
	} else if err != nil {
		http.Error(w, "metric history unavailable", http.StatusInternalServerError)
	} else {
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) dashboardNodeExists(r *http.Request, nodeID string) (bool, error) {
	var exists int
	err := s.store.DB.QueryRowContext(r.Context(), `SELECT 1 FROM nodes WHERE id=?`, nodeID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
