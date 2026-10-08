package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

func (s *Server) handleCSRFIssue(w http.ResponseWriter, r *http.Request) {
	if current, ok := s.authenticateRequest(w, r, false); ok {
		if cookie, err := r.Cookie(csrfCookieName); err == nil && subtle.ConstantTimeCompare(auth.DigestToken(cookie.Value), current.CSRFDigest) == 1 {
			s.setCSRFCookie(w, cookie.Value, s.idleTimeout)
			writeJSON(w, http.StatusOK, map[string]string{"token": cookie.Value})
			return
		}
		raw, _, err := auth.NewToken()
		if err != nil {
			http.Error(w, "request failed", http.StatusInternalServerError)
			return
		}
		if _, err := s.store.DB.Exec(`UPDATE browser_sessions SET csrf_digest=? WHERE id=?`, auth.DigestToken(raw), current.ID); err != nil {
			http.Error(w, "request failed", http.StatusInternalServerError)
			return
		}
		s.setCSRFCookie(w, raw, s.idleTimeout)
		writeJSON(w, http.StatusOK, map[string]string{"token": raw})
		return
	}
	token, err := s.signedCSRFToken()
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	s.setCSRFCookie(w, token, 10*time.Minute)
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) handleSetupStatus(w http.ResponseWriter) {
	initialized, err := s.isInitialized()
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized": initialized,
		"setupHint":   "Read the one-time setup credential from the local file path printed by nodedance serve.",
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Credential  string `json:"credential"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	displayName, ok := normalizeDisplayName(request.DisplayName)
	if !ok {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if len(request.Credential) < 32 || len(request.Credential) > 256 || !s.validOrigin(r) {
		http.Error(w, "setup rejected", http.StatusUnauthorized)
		return
	}
	credentialDigest := auth.DigestToken(request.Credential)
	var initialized int
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT count(*) FROM admin_user`).Scan(&initialized); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if initialized != 0 {
		http.Error(w, "already initialized", http.StatusConflict)
		return
	}
	var storedDigest []byte
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT digest FROM init_credential WHERE id=1`).Scan(&storedDigest); errors.Is(err, sql.ErrNoRows) || err == nil && subtle.ConstantTimeCompare(storedDigest, credentialDigest) != 1 {
		http.Error(w, "setup rejected", http.StatusUnauthorized)
		return
	} else if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if !s.acquirePasswordSlot() {
		http.Error(w, "password service is busy", http.StatusTooManyRequests)
		return
	}
	defer s.releasePasswordSlot()
	passwordHash, salt, err := s.hashSetupPassword(request.Password)
	if err != nil {
		http.Error(w, "password must contain 12 to 1024 UTF-8 bytes", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	rollback := func(status int, message string) {
		_ = tx.Rollback()
		http.Error(w, message, status)
	}
	var currentDigest []byte
	if err := tx.QueryRowContext(ctx, `SELECT digest FROM init_credential WHERE id=1`).Scan(&currentDigest); err != nil || subtle.ConstantTimeCompare(currentDigest, credentialDigest) != 1 {
		rollback(http.StatusUnauthorized, "setup rejected")
		return
	}
	var already int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM admin_user`).Scan(&already); err != nil {
		rollback(http.StatusInternalServerError, "request failed")
		return
	}
	if already != 0 {
		rollback(http.StatusConflict, "already initialized")
		return
	}
	createdAt := s.now().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, passwordHash, salt, auth.CurrentPasswordVersion, displayName, createdAt); err != nil {
		rollback(http.StatusConflict, "already initialized")
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE appearance SET display_name=?, updated_at=? WHERE id=1`, displayName, createdAt); err != nil {
		rollback(http.StatusInternalServerError, "request failed")
		return
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM init_credential WHERE id=1`); err != nil {
		rollback(http.StatusInternalServerError, "request failed")
		return
	}
	token, csrfToken, current, err := s.insertSession(ctx, tx, r, createdAt)
	if err != nil {
		rollback(http.StatusInternalServerError, "request failed")
		return
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: s.now(), Action: "setup", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: s.effectiveRemoteAddr(r)}); err != nil {
		rollback(http.StatusInternalServerError, "request failed")
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := os.Remove(setupCredentialPath(s.dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "setup completed; local credential cleanup failed", http.StatusInternalServerError)
		return
	}
	s.limiter.succeeded(current.RemoteAddr)
	s.setSessionCookie(w, token)
	s.setCSRFCookie(w, csrfToken, s.idleTimeout)
	writeJSON(w, http.StatusCreated, map[string]any{"user": map[string]string{"displayName": displayName}, "csrfToken": csrfToken})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	remote := s.effectiveRemoteAddr(r)
	if !s.limiter.reserve(remote) {
		s.auditEvent(r, "login", "rate_limited", sql.NullInt64{})
		http.Error(w, "too many login attempts", http.StatusTooManyRequests)
		return
	}
	if !s.acquirePasswordSlot() {
		http.Error(w, "password service is busy", http.StatusTooManyRequests)
		return
	}
	defer s.releasePasswordSlot()
	var passwordHash, salt []byte
	var version, displayName string
	err := s.store.DB.QueryRow(`SELECT password_hash, password_salt, password_version, display_name FROM admin_user WHERE id=1`).Scan(&passwordHash, &salt, &version, &displayName)
	if errors.Is(err, sql.ErrNoRows) {
		s.rejectLogin(w, r, remote)
		return
	}
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if !auth.VerifyPasswordVersion(version, request.Password, passwordHash, salt) {
		s.rejectLogin(w, r, remote)
		return
	}
	if s.loginVerifiedHook != nil {
		s.loginVerifiedHook()
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	// Argon2 runs before the transaction to keep SQLite's write lock brief.
	// Re-read the verifier under the session-creation transaction so a login
	// verified just before a password change cannot create a fresh session
	// after that change has revoked every existing session.
	var currentHash, currentSalt []byte
	var currentVersion, currentDisplayName string
	err = tx.QueryRowContext(r.Context(), `SELECT password_hash, password_salt, password_version, display_name FROM admin_user WHERE id=1`).Scan(&currentHash, &currentSalt, &currentVersion, &currentDisplayName)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (currentVersion != version || subtle.ConstantTimeCompare(currentHash, passwordHash) != 1 || subtle.ConstantTimeCompare(currentSalt, salt) != 1) {
		_ = tx.Rollback()
		s.rejectLogin(w, r, remote)
		return
	}
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	displayName = currentDisplayName
	now := s.now().UnixNano()
	token, csrfToken, current, err := s.insertSession(r.Context(), tx, r, now)
	if err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "login", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: remote}); err != nil {
		_ = tx.Rollback()
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "request failed", http.StatusInternalServerError)
		return
	}
	s.limiter.succeeded(remote)
	_ = current
	s.setSessionCookie(w, token)
	s.setCSRFCookie(w, csrfToken, s.idleTimeout)
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"displayName": displayName}, "csrfToken": csrfToken})
}

func (s *Server) rejectLogin(w http.ResponseWriter, r *http.Request, remote string) {
	s.auditEvent(r, "login", "rejected", sql.NullInt64{})
	http.Error(w, "invalid credentials", http.StatusUnauthorized)
}

func (s *Server) insertSession(ctx context.Context, tx *sql.Tx, r *http.Request, now int64) (rawToken, csrfToken string, current *session, err error) {
	rawToken, digest, err := auth.NewToken()
	if err != nil {
		return "", "", nil, err
	}
	csrfToken, _, err = auth.NewToken()
	if err != nil {
		return "", "", nil, err
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return "", "", nil, err
	}
	id := hex.EncodeToString(idBytes)
	ua := sanitizeMetadata(r.UserAgent(), 200)
	remote := s.effectiveRemoteAddr(r)
	if _, err = tx.ExecContext(ctx, `INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, id, digest, auth.DigestToken(csrfToken), now, now, ua, remote); err != nil {
		return "", "", nil, err
	}
	return rawToken, csrfToken, &session{ID: id, Digest: digest, CSRFDigest: auth.DigestToken(csrfToken), CreatedAt: s.now(), LastSeenAt: s.now(), UserAgent: ua, RemoteAddr: remote}, nil
}

func (s *Server) auditEvent(r *http.Request, action, outcome string, actor sql.NullInt64) {
	_ = audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: action, Outcome: outcome, ActorID: actor, RemoteAddr: s.effectiveRemoteAddr(r)})
}

func normalizeDisplayName(value string) (string, bool) {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	runes := []rune(value)
	if len(runes) == 0 || len(runes) > 80 || !utf8.ValidString(value) {
		return "", false
	}
	for _, r := range runes {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return value, true
}

func sanitizeMetadata(value string, maxRunes int) string {
	value = strings.ToValidUTF8(value, "")
	var builder strings.Builder
	count := 0
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		if count >= maxRunes {
			break
		}
		builder.WriteRune(r)
		count++
	}
	return builder.String()
}
