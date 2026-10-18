package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/history"
)

func (s *Server) handleObservabilityAPI(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") {
		return false
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if len(p) != 2 || (p[1] != "recording" && p[1] != "analysis" && p[1] != "correlations" && p[1] != "incidents") {
		return false
	}
	if !validUUID(p[0]) {
		http.NotFound(w, r)
		return true
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return true
	}
	node := p[0]
	var count int
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM nodes WHERE id=?`, node).Scan(&count); err != nil {
		http.Error(w, "node lookup failed", 500)
		return true
	}
	if count == 0 {
		http.NotFound(w, r)
		return true
	}
	now := s.now().UTC()
	key := r.URL.Query().Get("key")
	if key == "" {
		key = "cpu.usage_percent"
	}
	if !history.ValidKey(key) {
		http.Error(w, "invalid metric key", 400)
		return true
	}
	switch p[1] {
	case "incidents":
		rows, err := s.store.DB.QueryContext(r.Context(), `SELECT occurred_at,metric_key,value FROM observation_incidents WHERE node_id=? AND occurred_at>=? ORDER BY occurred_at DESC LIMIT 100`, node, now.Add(-7*24*time.Hour).UnixNano())
		if err != nil {
			http.Error(w, "incident history unavailable", 500)
			return true
		}
		defer rows.Close()
		items := []history.Incident{}
		for rows.Next() {
			var item history.Incident
			var at int64
			if err = rows.Scan(&at, &item.Key, &item.Value); err != nil {
				http.Error(w, "incident history unavailable", 500)
				return true
			}
			item.At = time.Unix(0, at).UTC()
			items = append(items, item)
		}
		if rows.Err() != nil {
			http.Error(w, "incident history unavailable", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"incidents": items})
	case "recording":
		from, to := now.Add(-10*time.Minute), now
		var err error
		if v := r.URL.Query().Get("from"); v != "" {
			from, err = time.Parse(time.RFC3339Nano, v)
		}
		if err == nil {
			if v := r.URL.Query().Get("to"); v != "" {
				to, err = time.Parse(time.RFC3339Nano, v)
			}
		}
		if err != nil || from.Before(now.Add(-7*24*time.Hour)) || to.After(now.Add(time.Minute)) {
			http.Error(w, "invalid recording window", 400)
			return true
		}
		result, err := s.history.Recording(r.Context(), node, from, to)
		if err != nil {
			status := 500
			if errors.Is(err, history.ErrInvalidQuery) {
				status = 400
			}
			http.Error(w, "recording unavailable", status)
			return true
		}
		writeJSON(w, 200, result)
	case "analysis":
		var capacity *float64
		var changeAt *time.Time
		if v := r.URL.Query().Get("capacity"); v != "" {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				http.Error(w, "invalid capacity", 400)
				return true
			}
			capacity = &n
		}
		if v := r.URL.Query().Get("changeAt"); v != "" {
			n, err := time.Parse(time.RFC3339Nano, v)
			if err != nil || n.Before(now.Add(-24*time.Hour)) || n.After(now) {
				http.Error(w, "invalid change timestamp", 400)
				return true
			}
			changeAt = &n
		}
		result, err := s.history.Analyze(r.Context(), node, key, now, capacity, changeAt)
		if err != nil {
			http.Error(w, "analysis unavailable", 400)
			return true
		}
		state, _, stateErr := s.currentMetricsState(r.Context(), node)
		if stateErr != nil {
			http.Error(w, "node state unavailable", 500)
			return true
		}
		if state.Status != "online" {
			result.ExhaustionAt = nil
			result.Status = "unknown"
			result.Reason = "node_offline"
		}
		writeJSON(w, 200, result)
	case "correlations":
		ids := strings.Split(r.URL.Query().Get("nodes"), ",")
		if len(ids) > 8 {
			http.Error(w, "at most eight comparison nodes", 400)
			return true
		}
		for _, id := range ids {
			if !validUUID(id) || id == node {
				http.Error(w, "invalid comparison node", 400)
				return true
			}
		}
		base, err := s.history.QueryKeys(r.Context(), node, "minute", now.Add(-time.Hour), now, []string{key})
		if err != nil {
			http.Error(w, "history unavailable", 500)
			return true
		}
		points := []history.Point{}
		if len(base.Series) > 0 {
			points = base.Series[0].Points
		}
		items := []history.Correlation{}
		for _, id := range ids {
			var exists int
			if err = s.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM nodes WHERE id=? AND status!='revoked'`, id).Scan(&exists); err != nil || exists == 0 {
				http.Error(w, "comparison node unavailable", 404)
				return true
			}
			other, err := s.history.QueryKeys(r.Context(), id, "minute", now.Add(-time.Hour), now, []string{key})
			if err != nil {
				http.Error(w, "comparison history unavailable", 500)
				return true
			}
			p := []history.Point{}
			if len(other.Series) > 0 {
				p = other.Series[0].Points
			}
			items = append(items, history.Correlate(id, key, points, p))
		}
		writeJSON(w, 200, map[string]any{"correlations": items, "interpretation": "correlation_is_not_causation"})
	}
	return true
}
