package server

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	_ "image/gif"
	_ "image/jpeg"
)

const (
	maxImageUploadBytes = 2 << 20
	maxImageStoredBytes = 8 << 20
	maxImageDimension   = 1024
	maxImagePixels      = 1024 * 1024
)

type appearance struct {
	DisplayName     string `json:"displayName"`
	Theme           string `json:"theme"`
	BackgroundColor string `json:"backgroundColor"`
	AvatarURL       string `json:"avatarUrl"`
	BackgroundURL   string `json:"backgroundUrl"`
}

type browserSessionView struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	Device     string `json:"device"`
	RemoteAddr string `json:"remoteAddr"`
	Current    bool   `json:"current"`
}

func (s *Server) handlePrivateAPI(w http.ResponseWriter, r *http.Request, current *session) {
	if s.handleDashboardSettingsAPI(w, r) || s.handleNodeDashboardAPI(w, r) {
		return
	}
	switch r.URL.Path {
	case "/api/v1/auth/me":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var name string
		if err := s.store.DB.QueryRow(`SELECT display_name FROM admin_user WHERE id=1`).Scan(&name); err != nil {
			http.Error(w, "request failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"displayName": name}})
		return
	case "/api/v1/auth/logout":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleLogout(w, r, current)
		return
	case "/api/v1/auth/password":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handlePasswordChange(w, r, current)
		return
	case "/api/v1/auth/sessions":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleListSessions(w, current)
		return
	case "/api/v1/settings/appearance":
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleUpdateAppearance(w, r, current)
		return
	case "/api/v1/settings/appearance/avatar", "/api/v1/settings/appearance/background":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleUploadAppearanceImage(w, r, current)
		return
	case "/api/v1/agents/enrollments":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.adminCreateAgentEnrollment(w, r, current)
		return
	case "/api/v1/agents":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.adminListAgents(w, r)
		return
	case "/api/v1/nodes":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The node dashboard exposes the same Core-owned node and lease view as
		// the legacy Agent management list. Keep the latter available for the
		// identity-management screen while giving metrics their node-scoped API.
		s.adminListAgents(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") && strings.HasSuffix(r.URL.Path, "/metrics") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
		if len(parts) != 2 || parts[1] != "metrics" || !validUUID(parts[0]) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.adminGetNodeMetrics(w, r, parts[0])
		return
	}
	if s.handleImageAPI(w, r, current) {
		return
	}
	if s.handleTaskAPI(w, r, current) {
		return
	}
	if s.handleComposeAPI(w, r, current) {
		return
	}
	if nodeID, containerID, ok := dockerRoute(r.URL.Path); ok {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.adminGetNodeContainers(w, r, nodeID, containerID)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/agents/") {
		s.adminAgentAction(w, r, current)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/auth/sessions/") && r.Method == http.MethodDelete {
		s.handleRevokeSession(w, r, current)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, current *session) {
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM browser_sessions WHERE id=?`, current.ID); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "logout", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	s.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request, current *session) {
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	var hash, salt []byte
	var version string
	if err := s.store.DB.QueryRow(`SELECT password_hash, password_salt, password_version FROM admin_user WHERE id=1`).Scan(&hash, &salt, &version); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if !s.acquirePasswordSlot() {
		http.Error(w, "password service is busy", http.StatusTooManyRequests)
		return
	}
	defer s.releasePasswordSlot()
	if !auth.VerifyPasswordVersion(version, request.CurrentPassword, hash, salt) {
		s.auditEvent(r, "password_change", "rejected", sql.NullInt64{Int64: 1, Valid: true})
		http.Error(w, "current password is incorrect", http.StatusUnauthorized)
		return
	}
	if s.passwordChangeVerifiedHook != nil {
		s.passwordChangeVerifiedHook()
	}
	newHash, newSalt, err := auth.HashPassword(request.NewPassword)
	if err != nil {
		http.Error(w, "new password must contain 12 to 1024 UTF-8 bytes", http.StatusBadRequest)
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	var lastSeen int64
	if err := tx.QueryRowContext(r.Context(), `SELECT last_seen_at FROM browser_sessions WHERE id=? AND token_digest=?`, current.ID, current.Digest).Scan(&lastSeen); errors.Is(err, sql.ErrNoRows) || err == nil && (s.now().Before(time.Unix(0, lastSeen)) || s.now().Sub(time.Unix(0, lastSeen)) >= s.idleTimeout) {
		_ = tx.Rollback()
		s.clearCookies(w)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	} else if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	updated, err := tx.ExecContext(r.Context(), `UPDATE admin_user SET password_hash=?, password_salt=?, password_version=?, updated_at=? WHERE id=1 AND password_hash=? AND password_salt=? AND password_version=?`, newHash, newSalt, auth.CurrentPasswordVersion, s.now().UnixNano(), hash, salt, version)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if affected != 1 {
		_ = tx.Rollback()
		http.Error(w, "password credentials changed; sign in again", http.StatusConflict)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM browser_sessions`); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "password_change", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	s.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListSessions(w http.ResponseWriter, current *session) {
	rows, err := s.store.DB.Query(`SELECT id, created_at, last_seen_at, user_agent, remote_addr FROM browser_sessions ORDER BY created_at`)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := make([]browserSessionView, 0)
	for rows.Next() {
		var item browserSessionView
		var createdAt, lastSeenAt int64
		if err := rows.Scan(&item.ID, &createdAt, &lastSeenAt, &item.Device, &item.RemoteAddr); err != nil {
			http.Error(w, "request failed", http.StatusInternalServerError)
			return
		}
		item.CreatedAt = time.Unix(0, createdAt).UTC().Format(time.RFC3339Nano)
		item.LastSeenAt = time.Unix(0, lastSeenAt).UTC().Format(time.RFC3339Nano)
		item.Current = item.ID == current.ID
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request, current *session) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/sessions/")
	if len(id) != 32 {
		http.NotFound(w, r)
		return
	}
	for _, character := range id {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			http.NotFound(w, r)
			return
		}
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	result, err := tx.ExecContext(r.Context(), `DELETE FROM browser_sessions WHERE id=?`, id)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		_ = tx.Rollback()
		http.NotFound(w, r)
		return
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "session_revoke", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if id == current.ID {
		s.clearCookies(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUpdateAppearance(w http.ResponseWriter, r *http.Request, current *session) {
	var request struct {
		DisplayName     string `json:"displayName"`
		Theme           string `json:"theme"`
		BackgroundColor string `json:"backgroundColor"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	name, ok := normalizeDisplayName(request.DisplayName)
	if !ok || (request.Theme != "light" && request.Theme != "dark" && request.Theme != "system") || !validBackgroundColor(request.BackgroundColor) {
		http.Error(w, "invalid appearance settings", http.StatusBadRequest)
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	now := s.now().Unix()
	if _, err := tx.ExecContext(r.Context(), `UPDATE appearance SET display_name=?, theme=?, background_color=?, updated_at=? WHERE id=1`, name, request.Theme, request.BackgroundColor, now); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE admin_user SET display_name=?, updated_at=? WHERE id=1`, name, now); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "appearance_update", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	s.writeAppearance(w)
}

var backgroundColorPattern = regexp.MustCompile(`^#[[:xdigit:]]{6}$`)

func validBackgroundColor(value string) bool {
	return backgroundColorPattern.MatchString(value)
}

func (s *Server) handlePublicAppearance(w http.ResponseWriter) {
	s.writeAppearance(w)
}

func (s *Server) writeAppearance(w http.ResponseWriter) {
	var value appearance
	var avatar, background []byte
	if err := s.store.DB.QueryRow(`SELECT display_name, theme, background_color, avatar_png, background_png FROM appearance WHERE id=1`).Scan(&value.DisplayName, &value.Theme, &value.BackgroundColor, &avatar, &background); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if len(avatar) > 0 {
		value.AvatarURL = "/api/v1/public/appearance/avatar"
	}
	if len(background) > 0 {
		value.BackgroundURL = "/api/v1/public/appearance/background"
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) handleUploadAppearanceImage(w http.ResponseWriter, r *http.Request, current *session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImageUploadBytes+64*1024)
	if err := r.ParseMultipartForm(maxImageUploadBytes); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "image is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid image upload", http.StatusBadRequest)
		}
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image is required", http.StatusBadRequest)
		return
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxImageUploadBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxImageUploadBytes {
		http.Error(w, "image is too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	normalized, err := normalizeRasterImage(encoded)
	if err != nil {
		http.Error(w, "unsupported or unsafe image", http.StatusBadRequest)
		return
	}
	column, publicURL := "", ""
	switch r.URL.Path {
	case "/api/v1/settings/appearance/avatar":
		column, publicURL = "avatar_png", "/api/v1/public/appearance/avatar"
	case "/api/v1/settings/appearance/background":
		column, publicURL = "background_png", "/api/v1/public/appearance/background"
	default:
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	query := `UPDATE appearance SET ` + column + `=?, updated_at=? WHERE id=1`
	if _, err := tx.ExecContext(ctx, query, normalized, s.now().Unix()); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: s.now(), Action: "image_upload", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": publicURL})
}

func normalizeRasterImage(encoded []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(encoded))
	if err != nil || (format != "png" && format != "jpeg" && format != "gif") {
		return nil, errors.New("unsupported image format")
	}
	if config.Width < 1 || config.Height < 1 || config.Width > maxImageDimension || config.Height > maxImageDimension || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return nil, errors.New("image dimensions exceed limit")
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(encoded))
	if err != nil || decodedFormat != format {
		return nil, errors.New("image decode failed")
	}
	var output bytes.Buffer
	if err := png.Encode(&output, decoded); err != nil {
		return nil, err
	}
	if output.Len() > maxImageStoredBytes {
		return nil, errors.New("normalized image exceeds limit")
	}
	return output.Bytes(), nil
}

func (s *Server) handlePublicImage(w http.ResponseWriter, r *http.Request) {
	column := "avatar_png"
	if r.URL.Path == "/api/v1/public/appearance/background" {
		column = "background_png"
	}
	var content []byte
	if err := s.store.DB.QueryRow(`SELECT ` + column + ` FROM appearance WHERE id=1`).Scan(&content); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
		} else {
			http.Error(w, "request failed", http.StatusInternalServerError)
		}
		return
	}
	if len(content) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.Header().Set("Content-Disposition", "inline; filename=appearance.png")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}
