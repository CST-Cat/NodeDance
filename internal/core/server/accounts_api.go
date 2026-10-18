package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type accountContextKey struct{}

func requestActor(r *http.Request) sql.NullInt64 {
	id, ok := r.Context().Value(accountContextKey{}).(int64)
	return sql.NullInt64{Int64: id, Valid: ok && id > 0}
}

type accountView struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Enabled     bool   `json:"enabled"`
}

func (s *Server) closeAccountSessions(ids []string, reason string) {
	for _, id := range ids {
		if s.terminals != nil {
			s.terminals.closeBrowserSession(s, id, reason)
		}
		s.closeFileTransfersForSession(id)
	}
}

func (s *Server) handleAccountAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path == "/api/v1/auth/switch" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
		} else {
			s.signIn(w, r, current)
		}
		return true
	}
	if r.URL.Path != "/api/v1/accounts" && !strings.HasPrefix(r.URL.Path, "/api/v1/accounts/") {
		return false
	}
	if r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodGet {
		query := `SELECT id,username,display_name,role,enabled FROM users`
		if current.Role != "administrator" {
			query += ` WHERE enabled=1`
		}
		rows, err := s.store.DB.QueryContext(r.Context(), query+` ORDER BY id`)
		if err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		defer rows.Close()
		items := []accountView{}
		for rows.Next() {
			var item accountView
			if err := rows.Scan(&item.ID, &item.Username, &item.DisplayName, &item.Role, &item.Enabled); err != nil {
				http.Error(w, "request failed", 500)
				return true
			}
			items = append(items, item)
		}
		if rows.Err() != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"accounts": items})
		return true
	}
	if current.Role != "administrator" {
		http.Error(w, "permission denied", 403)
		return true
	}
	if r.URL.Path == "/api/v1/accounts" && r.Method == http.MethodPost {
		var request protocol.AccountCreateRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		name, ok := normalizeDisplayName(request.DisplayName)
		if !ok || !auth.ValidUsername(request.Username) || (request.Role != "administrator" && request.Role != "viewer") {
			http.Error(w, "invalid account", 400)
			return true
		}
		if !s.acquirePasswordSlot() {
			http.Error(w, "password service is busy", 429)
			return true
		}
		defer s.releasePasswordSlot()
		hash, salt, err := auth.HashPassword(request.Password)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		tx, err := s.store.DB.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		defer tx.Rollback()
		if !activeAdministrator(tx, r, current) {
			http.Error(w, "permission denied", 403)
			return true
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO users(username,password_hash,password_salt,password_version,display_name,role,enabled,updated_at) VALUES(?,?,?,?,?,?,1,?)`, request.Username, hash, salt, auth.CurrentPasswordVersion, name, request.Role, s.now().UnixNano())
		if err != nil {
			http.Error(w, "account username already exists", 409)
			return true
		}
		id, err := result.LastInsertId()
		if err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "account_create", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: current.UserID, Valid: true}, RemoteAddr: current.RemoteAddr, Target: audit.Target{Kind: audit.TargetAccount, ID: strconv.FormatInt(id, 10)}}); err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		writeJSON(w, 201, map[string]any{"account": accountView{ID: id, Username: request.Username, DisplayName: name, Role: request.Role, Enabled: true}})
		return true
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/accounts/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != parts[0] {
		http.NotFound(w, r)
		return true
	}
	var update protocol.AccountUpdateRequest
	var hash, salt []byte
	action := ""
	switch {
	case len(parts) == 1 && r.Method == http.MethodPut:
		if !decodeJSON(w, r, &update) {
			return true
		}
		name, ok := normalizeDisplayName(update.DisplayName)
		update.DisplayName = name
		if !update.Confirmed {
			http.Error(w, "confirmation required", 400)
			return true
		}
		if !ok || !auth.ValidUsername(update.Username) || (update.Role != "administrator" && update.Role != "viewer") {
			http.Error(w, "invalid account", 400)
			return true
		}
		action = "account_update"
	case len(parts) == 1 && r.Method == http.MethodDelete:
		var request protocol.AccountDeleteRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		if !request.Confirmed {
			http.Error(w, "confirmation required", 400)
			return true
		}
		if id == current.UserID {
			http.Error(w, "cannot delete current account", 409)
			return true
		}
		action = "account_delete"
	case len(parts) == 2 && parts[1] == "password" && r.Method == http.MethodPost:
		var request protocol.AccountPasswordRequest
		if !decodeJSON(w, r, &request) {
			return true
		}
		if !request.Confirmed {
			http.Error(w, "confirmation required", 400)
			return true
		}
		if !s.acquirePasswordSlot() {
			http.Error(w, "password service is busy", 429)
			return true
		}
		defer s.releasePasswordSlot()
		hash, salt, err = auth.HashPassword(request.Password)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		action = "account_password_reset"
	default:
		http.Error(w, "method not allowed", 405)
		return true
	}
	s.terminalSessionMu.Lock()
	defer s.terminalSessionMu.Unlock()
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	defer tx.Rollback()
	// Revalidate the acting administrator under the same lock as the mutation.
	if !activeAdministrator(tx, r, current) {
		http.Error(w, "permission denied", 403)
		return true
	}
	var role string
	var enabled bool
	if err := tx.QueryRowContext(r.Context(), `SELECT role,enabled FROM users WHERE id=?`, id).Scan(&role, &enabled); errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return true
	} else if err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	losesAdmin := action == "account_delete" || action == "account_update" && (!update.Enabled || update.Role != "administrator")
	if role == "administrator" && enabled && losesAdmin {
		var count int
		if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM users WHERE role='administrator' AND enabled=1`).Scan(&count); err != nil {
			http.Error(w, "request failed", 500)
			return true
		}
		if count <= 1 {
			http.Error(w, "at least one enabled administrator is required", 409)
			return true
		}
	}
	ids := []string{}
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM browser_sessions WHERE user_id=?`, id)
	if err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			http.Error(w, "request failed", 500)
			return true
		}
		ids = append(ids, sid)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	switch action {
	case "account_update":
		_, err = tx.ExecContext(r.Context(), `UPDATE users SET username=?,display_name=?,role=?,enabled=?,updated_at=? WHERE id=?`, update.Username, update.DisplayName, update.Role, update.Enabled, s.now().UnixNano(), id)
	case "account_delete":
		_, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE id=?`, id)
	case "account_password_reset":
		_, err = tx.ExecContext(r.Context(), `UPDATE users SET password_hash=?,password_salt=?,password_version=?,updated_at=? WHERE id=?`, hash, salt, auth.CurrentPasswordVersion, s.now().UnixNano(), id)
	}
	if err != nil {
		http.Error(w, "account update failed or username already exists", 409)
		return true
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM browser_sessions WHERE user_id=?`, id); err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: action, Outcome: "succeeded", ActorID: sql.NullInt64{Int64: current.UserID, Valid: true}, RemoteAddr: current.RemoteAddr, Target: audit.Target{Kind: audit.TargetAccount, ID: strconv.FormatInt(id, 10)}}); err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", 500)
		return true
	}
	s.closeAccountSessions(ids, "account credentials or permissions changed")
	if id == current.UserID {
		s.clearCookies(w)
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (s *Server) writeProfile(w http.ResponseWriter, current *session) {
	var value appearance
	var avatar, background []byte
	if err := s.store.DB.QueryRow(`SELECT u.display_name,u.theme,u.avatar_png,a.background_color,a.background_png FROM users u CROSS JOIN appearance a WHERE u.id=? AND a.id=1`, current.UserID).Scan(&value.DisplayName, &value.Theme, &avatar, &value.BackgroundColor, &background); err != nil {
		http.Error(w, "request failed", 500)
		return
	}
	if len(avatar) > 0 {
		value.AvatarURL = "/api/v1/auth/profile/avatar"
	}
	if len(background) > 0 {
		value.BackgroundURL = "/api/v1/public/appearance/background"
	}
	writeJSON(w, 200, value)
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, current *session) {
	switch r.Method {
	case http.MethodGet:
		s.writeProfile(w, current)
	case http.MethodPut:
		var request protocol.ProfileRequest
		if !decodeJSON(w, r, &request) {
			return
		}
		name, ok := normalizeDisplayName(request.DisplayName)
		if !ok || (request.Theme != "system" && request.Theme != "light" && request.Theme != "dark") {
			http.Error(w, "invalid profile", 400)
			return
		}
		if _, err := s.store.DB.ExecContext(r.Context(), `UPDATE users SET display_name=?,theme=?,updated_at=? WHERE id=?`, name, request.Theme, s.now().UnixNano(), current.UserID); err != nil {
			http.Error(w, "request failed", 500)
			return
		}
		s.writeProfile(w, current)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func activeAdministrator(tx *sql.Tx, r *http.Request, current *session) bool {
	var exists int
	return tx.QueryRowContext(r.Context(), `SELECT 1 FROM users u JOIN browser_sessions s ON s.user_id=u.id WHERE u.id=? AND u.role='administrator' AND u.enabled=1 AND s.id=? AND s.token_digest=?`, current.UserID, current.ID, current.Digest).Scan(&exists) == nil
}
