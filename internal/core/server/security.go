package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

const (
	sessionCookieName = "nodedance_session"
	csrfCookieName    = "nodedance_csrf"
	csrfHeaderName    = "X-CSRF-Token"
	setupFileName     = "setup-credential.txt"
	signingKeyName    = "csrf-signing.key"
)

func loadOrCreateSigningKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, signingKeyName)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("CSRF signing key must be a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure CSRF signing key: %w", err)
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read CSRF signing key: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("invalid CSRF signing key length")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect CSRF signing key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate CSRF signing key: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".csrf-key-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary CSRF signing key: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("secure temporary CSRF signing key: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write CSRF signing key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("sync CSRF signing key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close CSRF signing key: %w", err)
	}
	// link is an atomic create-if-absent installation: a concurrent Core start
	// may win, but this process never replaces the key already in use.
	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateSigningKey(dataDir)
		}
		return nil, fmt.Errorf("install CSRF signing key: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil {
		return nil, fmt.Errorf("sync data directory after installing CSRF key: %w", err)
	}
	return key, nil
}

func setupCredentialPath(dataDir string) string {
	return filepath.Join(dataDir, setupFileName)
}

func (s *Server) ensureSetupState() error {
	initialized, err := s.isInitialized()
	if err != nil {
		return err
	}
	path := setupCredentialPath(s.dataDir)
	if initialized {
		if _, err := s.store.DB.Exec(`DELETE FROM init_credential`); err != nil {
			return fmt.Errorf("clear consumed setup credential record: %w", err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove consumed setup credential file: %w", err)
		}
		return nil
	}

	var expected []byte
	err = s.store.DB.QueryRow(`SELECT digest FROM init_credential WHERE id=1`).Scan(&expected)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read setup credential record: %w", err)
	}
	fileToken, fileErr := readSetupCredential(path)
	if err == nil && fileErr == nil && subtle.ConstantTimeCompare(auth.DigestToken(fileToken), expected) == 1 {
		return nil
	}
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return fileErr
	}
	raw, digest, err := auth.NewToken()
	if err != nil {
		return fmt.Errorf("generate setup credential: %w", err)
	}
	if err := writeSecretFile(path, raw+"\n"); err != nil {
		return fmt.Errorf("save setup credential file: %w", err)
	}
	_, err = s.store.DB.Exec(`INSERT INTO init_credential(id, digest, created_at) VALUES(1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET digest=excluded.digest, created_at=excluded.created_at`, digest, s.now().Unix())
	if err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("save setup credential digest: %w", err)
	}
	return nil
}

func readSetupCredential(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("setup credential path must be a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("secure setup credential file: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read setup credential file: %w", err)
	}
	token := strings.TrimSpace(string(content))
	if len(token) < 32 || len(token) > 256 {
		return "", errors.New("invalid setup credential file contents")
	}
	return token, nil
}

func writeSecretFile(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".nodedance-secret-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Server) isInitialized() (bool, error) {
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM admin_user`).Scan(&count); err != nil {
		return false, fmt.Errorf("check administrator initialization: %w", err)
	}
	return count > 0, nil
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := normalizeOrigin(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	if s.publicOrigin != "" {
		return origin == normalizeOrigin(s.publicOrigin)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if s.isTrustedProxy(remoteIP(r)) {
		forwardedProto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
		if forwardedProto == "https" || forwardedProto == "http" {
			scheme = forwardedProto
		}
	}
	return origin == normalizeOrigin(scheme+"://"+r.Host)
}

func normalizeOrigin(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return ""
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

func (s *Server) signedCSRFToken() (string, error) {
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	issued := s.now().Unix()
	message := fmt.Sprintf("%d.%s", issued, base64.RawURLEncoding.EncodeToString(nonce))
	mac := hmac.New(sha256.New, s.csrfKey)
	_, _ = mac.Write([]byte(message))
	return message + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifySignedCSRFToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(parts[1]) < 16 {
		return false
	}
	issuedUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return false
	}
	issuedAt := time.Unix(issuedUnix, 0)
	age := s.now().Sub(issuedAt)
	if age < 0 || age > 10*time.Minute {
		return false
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.csrfKey)
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	return hmac.Equal(provided, mac.Sum(nil))
}

func (s *Server) validCSRFCookie(r *http.Request, current *session) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get(csrfHeaderName)
	if header == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
		return false
	}
	if current == nil {
		return s.verifySignedCSRFToken(header)
	}
	return subtle.ConstantTimeCompare(auth.DigestToken(header), current.CSRFDigest) == 1
}

func (s *Server) setCSRFCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: token, Path: "/", Secure: !s.development, HttpOnly: false, SameSite: http.SameSiteStrictMode, MaxAge: int(maxAge.Seconds()), Expires: s.now().Add(maxAge)})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Secure: !s.development, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(s.idleTimeout.Seconds()), Expires: s.now().Add(s.idleTimeout)})
}

func (s *Server) clearCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", Secure: !s.development, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "", Path: "/", Secure: !s.development, HttpOnly: false, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Server) authenticateRequest(w http.ResponseWriter, r *http.Request, touch bool) (*session, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	digest := auth.DigestToken(cookie.Value)
	var current session
	var created, lastSeen int64
	err = s.store.DB.QueryRow(`SELECT id, csrf_digest, created_at, last_seen_at, user_agent, remote_addr
		FROM browser_sessions WHERE token_digest=?`, digest).Scan(&current.ID, &current.CSRFDigest, &created, &lastSeen, &current.UserAgent, &current.RemoteAddr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		return nil, false
	}
	current.Digest = digest
	current.CreatedAt = time.Unix(0, created)
	current.LastSeenAt = time.Unix(0, lastSeen)
	now := s.now()
	if now.Sub(current.LastSeenAt) >= s.idleTimeout || now.Before(current.LastSeenAt) {
		_, _ = s.store.DB.Exec(`DELETE FROM browser_sessions WHERE id=?`, current.ID)
		s.clearCookies(w)
		return nil, false
	}
	if touch {
		if _, err := s.store.DB.Exec(`UPDATE browser_sessions SET last_seen_at=? WHERE id=?`, now.UnixNano(), current.ID); err != nil {
			return nil, false
		}
		current.LastSeenAt = now
		s.setSessionCookie(w, cookie.Value)
	}
	return &current, true
}

func (s *Server) sessionStillValid(id string) bool {
	var lastSeen int64
	if err := s.store.DB.QueryRow(`SELECT last_seen_at FROM browser_sessions WHERE id=?`, id).Scan(&lastSeen); err != nil {
		return false
	}
	last := time.Unix(0, lastSeen)
	return s.now().Sub(last) < s.idleTimeout && !s.now().Before(last)
}

func (s *Server) effectiveRemoteAddr(r *http.Request) string {
	remote := remoteIP(r)
	if remote == nil {
		return "unknown"
	}
	if !s.isTrustedProxy(remote) {
		return remote.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	chain := make([]net.IP, 0, len(forwarded)+1)
	for _, value := range forwarded {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return remote.String()
		}
		chain = append(chain, ip)
	}
	chain = append(chain, remote)
	for index := len(chain) - 1; index >= 0; index-- {
		if !s.isTrustedProxy(chain[index]) {
			return chain[index].String()
		}
	}
	return chain[0].String()
}

func remoteIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(r.RemoteAddr)
}

func (s *Server) isTrustedProxy(ip net.IP) bool {
	for _, network := range s.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
