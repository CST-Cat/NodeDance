package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
)

func (s *Server) handleAlertAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	const prefix = "/api/v1/alerts"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == "" {
		path = "/"
	}
	switch path {
	case "/":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		items, err := s.alerts.ListAlerts(r.Context(), true, apiLimit(r, 200))
		if err != nil {
			http.Error(w, "alert list unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"alerts": items})
		return true
	case "/history":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		items, err := s.alerts.ListAlerts(r.Context(), false, apiLimit(r, 200))
		if err != nil {
			http.Error(w, "alert history unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"alerts": items})
		return true
	case "/events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		items, err := s.alerts.ListEvents(r.Context(), r.URL.Query().Get("alertId"), apiLimit(r, 200))
		if err != nil {
			http.Error(w, "alert event history unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": items})
		return true
	case "/rules":
		s.handleAlertRules(w, r, current, "")
		return true
	case "/rules/defaults":
		s.handleAlertRuleDefaults(w, r)
		return true
	case "/channels":
		s.handleAlertChannels(w, r, current, "")
		return true
	case "/windows":
		s.handleAlertWindows(w, r, current, "")
		return true
	case "/deliveries":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		items, err := s.alerts.ListDeliveries(r.Context(), apiLimit(r, 200))
		if err != nil {
			http.Error(w, "delivery history unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"deliveries": items})
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 2 && parts[0] == "rules" {
		s.handleAlertRules(w, r, current, parts[1])
		return true
	}
	if len(parts) == 2 && parts[0] == "channels" {
		s.handleAlertChannels(w, r, current, parts[1])
		return true
	}
	if len(parts) == 3 && parts[0] == "channels" && parts[2] == "test" {
		s.handleAlertChannelTest(w, r, current, parts[1])
		return true
	}
	if len(parts) == 2 && parts[0] == "windows" {
		s.handleAlertWindows(w, r, current, parts[1])
		return true
	}
	if len(parts) == 2 && (parts[1] == "acknowledge" || parts[1] == "silence") {
		s.handleAlertAction(w, r, current, parts[0], parts[1])
		return true
	}
	http.NotFound(w, r)
	return true
}

func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request, current *session, ruleID string) {
	if ruleID == "" && r.Method == http.MethodGet {
		items, err := s.alerts.ListRules(r.Context(), r.URL.Query().Get("nodeId"))
		if err != nil {
			http.Error(w, "alert rules unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"rules": items})
		return
	}
	if ruleID != "" && !validUUID(ruleID) {
		http.NotFound(w, r)
		return
	}
	if (ruleID == "" && r.Method != http.MethodPost && r.Method != http.MethodGet) || (ruleID != "" && r.Method != http.MethodPut && r.Method != http.MethodDelete) {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Method == http.MethodDelete {
		var body struct {
			Revision int64 `json:"revision"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		err := s.alerts.DeleteRule(r.Context(), ruleID, body.Revision)
		if err != nil {
			s.auditAlertEvent(r, "alert_rule_delete", "rejected", ruleID)
			writeAlertStoreError(w, err)
			return
		}
		s.auditAlertEvent(r, "alert_rule_delete", "succeeded", ruleID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var body corealerts.RuleInput
	if !decodeJSON(w, r, &body) {
		return
	}
	saved, err := s.alerts.SaveRule(r.Context(), ruleID, body)
	if err != nil {
		s.auditAlertEvent(r, "alert_rule_save", "rejected", ruleID)
		writeAlertStoreError(w, err)
		return
	}
	s.auditAlertEvent(r, "alert_rule_save", "succeeded", saved.ID)
	writeJSON(w, http.StatusOK, map[string]any{"rule": saved})
}

func (s *Server) handleAlertRuleDefaults(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		NodeID string `json:"nodeId"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !validUUID(body.NodeID) {
		http.Error(w, "invalid node ID", http.StatusBadRequest)
		return
	}
	if err := s.alerts.EnsureDefaultRules(r.Context(), body.NodeID); err != nil {
		http.Error(w, "default alert rules unavailable", http.StatusInternalServerError)
		return
	}
	_ = audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: "alert_defaults_create", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: s.effectiveRemoteAddr(r), Target: audit.Target{Kind: audit.TargetNode, ID: body.NodeID}})
	items, err := s.alerts.ListRules(r.Context(), body.NodeID)
	if err != nil {
		http.Error(w, "alert rules unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": items})
}

func (s *Server) handleAlertChannels(w http.ResponseWriter, r *http.Request, current *session, channelID string) {
	if channelID == "" && r.Method == http.MethodGet {
		items, err := s.alerts.ListChannels(r.Context(), "")
		if err != nil {
			http.Error(w, "notification channels unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"channels": items})
		return
	}
	if channelID != "" && !validUUID(channelID) {
		http.NotFound(w, r)
		return
	}
	if channelID != "" && r.Method == http.MethodDelete {
		var body struct {
			Revision int64 `json:"revision"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		err := s.alerts.DeleteChannel(r.Context(), channelID, body.Revision)
		if err != nil {
			s.auditAlertEvent(r, "alert_channel_delete", "rejected", channelID)
			writeAlertStoreError(w, err)
			return
		}
		s.auditAlertEvent(r, "alert_channel_delete", "succeeded", channelID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if (channelID == "" && r.Method != http.MethodPost) || (channelID != "" && r.Method != http.MethodPut) {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body corealerts.ChannelInput
	if !decodeJSON(w, r, &body) {
		return
	}
	saved, err := s.alerts.SaveChannel(r.Context(), channelID, body)
	if err != nil {
		s.auditAlertEvent(r, "alert_channel_save", "rejected", channelID)
		writeAlertStoreError(w, err)
		return
	}
	s.auditAlertEvent(r, "alert_channel_save", "succeeded", saved.ID)
	writeJSON(w, http.StatusOK, map[string]any{"channel": saved})
}

func (s *Server) handleAlertChannelTest(w http.ResponseWriter, r *http.Request, current *session, channelID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !validUUID(channelID) {
		http.NotFound(w, r)
		return
	}
	if err := s.alerts.EnqueueTest(r.Context(), channelID); err != nil {
		s.auditAlertEvent(r, "alert_channel_test", "rejected", channelID)
		writeAlertStoreError(w, err)
		return
	}
	s.auditAlertEvent(r, "alert_channel_test", "succeeded", channelID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) handleAlertWindows(w http.ResponseWriter, r *http.Request, current *session, windowID string) {
	if windowID == "" && r.Method == http.MethodGet {
		items, err := s.alerts.ListWindows(r.Context())
		if err != nil {
			http.Error(w, "alert windows unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"windows": items})
		return
	}
	if windowID != "" {
		if !validUUID(windowID) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", 405)
			return
		}
		if err := s.alerts.DeleteWindow(r.Context(), windowID); err != nil {
			writeAlertStoreError(w, err)
			return
		}
		s.auditAlertEvent(r, "alert_window_delete", "succeeded", windowID)
		w.WriteHeader(204)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body corealerts.WindowInput
	if !decodeJSON(w, r, &body) {
		return
	}
	saved, err := s.alerts.AddWindow(r.Context(), body)
	if err != nil {
		writeAlertStoreError(w, err)
		return
	}
	s.auditAlertEvent(r, "alert_window_create", "succeeded", saved.ID)
	writeJSON(w, 201, map[string]any{"window": saved})
}

func (s *Server) handleAlertAction(w http.ResponseWriter, r *http.Request, current *session, alertID, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !validUUID(alertID) {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "acknowledge":
		var body struct {
			Note string `json:"note"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		err := s.alerts.Acknowledge(r.Context(), alertID, "administrator", body.Note)
		if err != nil {
			writeAlertStoreError(w, err)
			return
		}
		s.auditAlertEvent(r, "alert_acknowledge", "succeeded", alertID)
		w.WriteHeader(204)
	case "silence":
		var body struct {
			Until  time.Time `json:"until"`
			Reason string    `json:"reason"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		err := s.alerts.SilenceAlert(r.Context(), alertID, body.Until, body.Reason)
		if err != nil {
			writeAlertStoreError(w, err)
			return
		}
		s.auditAlertEvent(r, "alert_silence", "succeeded", alertID)
		w.WriteHeader(204)
	}
}

func (s *Server) auditAlertEvent(r *http.Request, action, outcome, targetID string) {
	event := audit.Event{OccurredAt: s.now(), Action: action, Outcome: outcome, ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: s.effectiveRemoteAddr(r)}
	if validUUID(targetID) {
		event.Target = audit.Target{Kind: audit.TargetAlert, ID: targetID}
	}
	_ = audit.Record(r.Context(), s.store.DB, event)
}

func apiLimit(r *http.Request, fallback int) int {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
			return n
		}
	}
	return fallback
}
func writeAlertStoreError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	msg := err.Error()
	if errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
		msg = "resource not found"
	} else if strings.Contains(msg, "changed") || strings.Contains(msg, "already") || strings.Contains(msg, "not active") {
		status = http.StatusConflict
	}
	http.Error(w, msg, status)
}
