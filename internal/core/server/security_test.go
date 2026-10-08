package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/coder/websocket"
)

func TestCSRFSigningKeySurvivesRestartAndAtomicInstall(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".csrf-key-interrupted"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadOrCreateSigningKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, signingKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("signing key mode=%#o size=%d", info.Mode().Perm(), info.Size())
	}
	second, err := loadOrCreateSigningKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if subtle.ConstantTimeCompare(key, second) != 1 {
		t.Fatal("an existing complete signing key was replaced")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".csrf-key-") && entry.Name() != ".csrf-key-interrupted" {
			t.Errorf("temporary signing key was left behind: %s", entry.Name())
		}
	}
}

func TestLimiterReservesConcurrentThresholdAndRecovers(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newLoginLimiter(4, time.Second, func() time.Time { return now })
	var mu sync.Mutex
	var allowed int
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if limiter.reserve("192.0.2.5") {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if allowed != 4 {
		t.Fatalf("concurrent attempts admitted=%d, want 4", allowed)
	}
	if limiter.reserve("192.0.2.5") {
		t.Fatal("request after threshold was admitted during cooldown")
	}
	now = now.Add(2 * time.Second)
	if !limiter.reserve("192.0.2.5") {
		t.Fatal("address stayed locked after cooldown")
	}
	limiter.succeeded("192.0.2.5")
	if !limiter.reserve("192.0.2.5") {
		t.Fatal("successful login did not clear prior failures")
	}
}

func TestLimiterHasBoundedEntriesAndPrunesExpiredAddresses(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newLoginLimiter(10, time.Second, func() time.Time { return now })
	limiter.capacity = 32
	for index := 0; index < limiter.capacity; index++ {
		if !limiter.reserve(fmt.Sprintf("192.0.2.%d", index)) {
			t.Fatalf("address %d was rejected before capacity", index)
		}
	}
	if limiter.reserve("198.51.100.1") {
		t.Fatal("new address exceeded the limiter capacity")
	}
	if len(limiter.entries) != limiter.capacity {
		t.Fatalf("limiter entries=%d, capacity=%d", len(limiter.entries), limiter.capacity)
	}
	now = now.Add(2 * time.Second)
	if !limiter.reserve("198.51.100.1") {
		t.Fatal("expired addresses were not pruned")
	}
	if len(limiter.entries) > limiter.capacity {
		t.Fatalf("limiter entries=%d exceeded capacity=%d", len(limiter.entries), limiter.capacity)
	}
}

func TestPasswordWorkHasBoundedConcurrentMemorySlots(t *testing.T) {
	s := &Server{passwordSlots: make(chan struct{}, 2)}
	release := make(chan struct{})
	admitted := make(chan bool, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if s.acquirePasswordSlot() {
				admitted <- true
				<-release
				s.releasePasswordSlot()
				return
			}
			admitted <- false
		}()
	}
	allowed := 0
	for range 32 {
		if <-admitted {
			allowed++
		}
	}
	close(release)
	wait.Wait()
	if allowed > 2 {
		t.Fatalf("concurrently admitted password work slots=%d, want at most 2", allowed)
	}
}

func TestProxyHeadersRequireExplicitTrustedPeer(t *testing.T) {
	s := newTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.test/", nil)
	request.RemoteAddr = "198.51.100.20:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.99")
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := s.effectiveRemoteAddr(request); got != "198.51.100.20" {
		t.Fatalf("untrusted X-Forwarded-For was used: %s", got)
	}
	request.Header.Set("Origin", "https://panel.example.test")
	if s.validOrigin(request) {
		t.Fatal("untrusted X-Forwarded-Proto changed the origin scheme")
	}
	trusted, err := New("test", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true, TrustedProxies: []string{"198.51.100.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	defer trusted.Close()
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.11")
	if got := trusted.effectiveRemoteAddr(request); got != "203.0.113.9" {
		t.Fatalf("trusted chain client IP=%s, want 203.0.113.9", got)
	}
}

func TestSessionAndCSRFRemainValidAcrossRestartButRevocationDoesNot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	options := Options{DataDir: dir, Development: true, PublicOrigin: "http://panel.example.test"}
	first, err := New("test", options)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := first.store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, make([]byte, 32), make([]byte, 16), auth.CurrentPasswordVersion, "Safe name", now); err != nil {
		t.Fatal(err)
	}
	sessionRaw, sessionDigest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	csrfRaw, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('0123456789abcdef0123456789abcdef', ?, ?, ?, ?, 'test browser', '127.0.0.1')`, sessionDigest, auth.DigestToken(csrfRaw), now, now); err != nil {
		t.Fatal(err)
	}
	preAuthCSRF, err := first.signedCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New("test", options)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !second.verifySignedCSRFToken(preAuthCSRF) {
		t.Fatal("CSRF signing key changed during Core restart")
	}
	if _, err := os.Stat(setupCredentialPath(second.dataDir)); !os.IsNotExist(err) {
		t.Fatalf("consumed setup credential remains after initialized restart: err=%v", err)
	}
	get := httptest.NewRequest(http.MethodGet, "http://panel.example.test/api/v1/auth/me", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	w := httptest.NewRecorder()
	second.ServeHTTP(w, get)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Safe name") {
		t.Fatalf("old persisted session status=%d body=%s", w.Code, w.Body.String())
	}
	put := httptest.NewRequest(http.MethodPut, "http://panel.example.test/api/v1/settings/appearance", strings.NewReader(`{"displayName":"After restart","theme":"dark","backgroundColor":"#101827"}`))
	put.Header.Set("Origin", "http://panel.example.test")
	put.Header.Set(csrfHeaderName, csrfRaw)
	put.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	put.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfRaw})
	w = httptest.NewRecorder()
	second.ServeHTTP(w, put)
	if w.Code != http.StatusOK {
		t.Fatalf("persisted session CSRF write status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := second.store.DB.Exec(`DELETE FROM browser_sessions WHERE token_digest=?`, sessionDigest); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	second.ServeHTTP(w, get)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d, want 401", w.Code)
	}
}

func TestWebSocketHeartbeatCannotExtendIdleSession(t *testing.T) {
	var clockMu sync.RWMutex
	now := time.Now()
	clock := func() time.Time {
		clockMu.RLock()
		defer clockMu.RUnlock()
		return now
	}
	s, err := New("test", Options{
		DataDir:                filepath.Join(t.TempDir(), "data"),
		Development:            true,
		PublicOrigin:           "http://panel.example.test",
		SessionIdleTimeout:     10 * time.Minute,
		WebSocketCheckInterval: 10 * time.Millisecond,
		Now:                    clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	started := clock().UnixNano()
	if _, err := s.store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, make([]byte, 32), make([]byte, 16), auth.CurrentPasswordVersion, "WS test", started); err != nil {
		t.Fatal(err)
	}
	raw, digest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('abcdef0123456789abcdef0123456789', ?, ?, ?, ?, 'test', '127.0.0.1')`, digest, auth.DigestToken(csrf), started, started); err != nil {
		t.Fatal(err)
	}
	webServer := httptest.NewServer(s)
	defer webServer.Close()
	header := http.Header{}
	header.Set("Origin", "http://panel.example.test")
	header.Set("Cookie", sessionCookieName+"="+raw)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(webServer.URL, "http")+"/ws/v1/dashboard", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	readDone := make(chan error, 1)
	go func() {
		_, _, err := conn.Read(ctx)
		readDone <- err
	}()
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := s.store.DB.QueryRow(`SELECT last_seen_at FROM browser_sessions WHERE token_digest=?`, digest).Scan(&before); err != nil {
		t.Fatal(err)
	}
	clockMu.Lock()
	now = now.Add(11 * time.Minute)
	clockMu.Unlock()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("WebSocket closed without an error after idle Session expiry")
		}
	case <-ctx.Done():
		t.Fatal("WebSocket remained open after idle Session expiry")
	}
	if err := s.store.DB.QueryRow(`SELECT last_seen_at FROM browser_sessions WHERE token_digest=?`, digest).Scan(&after); err == nil && after != before {
		t.Fatalf("WebSocket heartbeat changed session activity from %d to %d", before, after)
	}
}

func TestLoginVerifiedBeforePasswordChangeCannotCreateSession(t *testing.T) {
	s := newTestServer(t)
	oldPassword := "current-secret-123"
	newPassword := "replacement-secret-456"
	sessionRaw, csrfRaw := seedAdminSession(t, s, oldPassword)
	verified := make(chan struct{})
	release := make(chan struct{})
	s.loginVerifiedHook = func() {
		close(verified)
		<-release
	}

	preAuthCSRF, err := s.signedCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	loginResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := newJSONRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/login", map[string]string{"password": oldPassword}, preAuthCSRF)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		loginResult <- response
	}()
	select {
	case <-verified:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not reach the post-verification barrier")
	}

	change := newJSONRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/password", map[string]string{"currentPassword": oldPassword, "newPassword": newPassword}, csrfRaw)
	change.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	response := httptest.NewRecorder()
	s.ServeHTTP(response, change)
	if response.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d body=%s", response.Code, response.Body.String())
	}

	close(release)
	select {
	case result := <-loginResult:
		if result.Code != http.StatusUnauthorized {
			t.Fatalf("stale login status=%d body=%s", result.Code, result.Body.String())
		}
		for _, cookie := range result.Result().Cookies() {
			if cookie.Name == sessionCookieName && cookie.Value != "" {
				t.Fatal("stale login issued a session cookie after password revocation")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale login did not finish")
	}
	assertSessionUnauthorized(t, s, sessionRaw)
}

func TestConcurrentPasswordChangesOnlyOneWinsAndRevokesOldSession(t *testing.T) {
	s := newTestServer(t)
	oldPassword := "current-secret-123"
	sessionRaw, csrfRaw := seedAdminSession(t, s, oldPassword)
	verified := make(chan struct{}, 2)
	release := make(chan struct{})
	s.passwordChangeVerifiedHook = func() {
		verified <- struct{}{}
		<-release
	}

	results := make(chan int, 2)
	for _, newPassword := range []string{"replacement-secret-456", "replacement-secret-789"} {
		newPassword := newPassword
		go func() {
			request := newJSONRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/password", map[string]string{"currentPassword": oldPassword, "newPassword": newPassword}, csrfRaw)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
			response := httptest.NewRecorder()
			s.ServeHTTP(response, request)
			results <- response.Code
		}()
	}
	for range 2 {
		select {
		case <-verified:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("both password changes did not verify the old password")
		}
	}
	close(release)
	statuses := []int{<-results, <-results}
	changes := 0
	for _, status := range statuses {
		if status == http.StatusNoContent {
			changes++
		} else if status != http.StatusUnauthorized && status != http.StatusConflict {
			t.Fatalf("unexpected concurrent password change status: %d", status)
		}
	}
	if changes != 1 {
		t.Fatalf("successful password changes=%d, statuses=%v", changes, statuses)
	}
	assertSessionUnauthorized(t, s, sessionRaw)
}

func TestSetupChecksInitializationAndCredentialBeforeArgon2(t *testing.T) {
	unauthorized := newTestServer(t)
	var rejectedHashCalls int
	unauthorized.hashSetupPassword = func(string) ([]byte, []byte, error) {
		rejectedHashCalls++
		return nil, nil, fmt.Errorf("unexpected Argon2 work")
	}
	badCSRF, err := unauthorized.signedCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	badSetup := newJSONRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/setup", map[string]string{
		"credential": strings.Repeat("x", 64), "password": "valid-password-123", "displayName": "Test Admin",
	}, badCSRF)
	badResponse := httptest.NewRecorder()
	unauthorized.ServeHTTP(badResponse, badSetup)
	if badResponse.Code != http.StatusUnauthorized || rejectedHashCalls != 0 {
		t.Fatalf("invalid setup status=%d Argon2 calls=%d body=%s", badResponse.Code, rejectedHashCalls, badResponse.Body.String())
	}

	s := newTestServer(t)
	credentialBytes, err := os.ReadFile(setupCredentialPath(s.dataDir))
	if err != nil {
		t.Fatal(err)
	}
	credential := strings.TrimSpace(string(credentialBytes))
	var hashCalls int
	s.hashSetupPassword = func(password string) ([]byte, []byte, error) {
		hashCalls++
		return auth.HashPassword(password)
	}
	setup := func() *httptest.ResponseRecorder {
		t.Helper()
		csrf, err := s.signedCSRFToken()
		if err != nil {
			t.Fatal(err)
		}
		request := newJSONRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/setup", map[string]string{
			"credential": credential, "password": "valid-password-123", "displayName": "Test Admin",
		}, csrf)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		return response
	}
	first := setup()
	if first.Code != http.StatusCreated || hashCalls != 1 {
		t.Fatalf("initial setup status=%d Argon2 calls=%d body=%s", first.Code, hashCalls, first.Body.String())
	}
	var before []byte
	if err := s.store.DB.QueryRow(`SELECT password_hash FROM admin_user WHERE id=1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	second := setup()
	if second.Code != http.StatusConflict || hashCalls != 1 {
		t.Fatalf("repeated setup status=%d Argon2 calls=%d body=%s", second.Code, hashCalls, second.Body.String())
	}
	var after []byte
	if err := s.store.DB.QueryRow(`SELECT password_hash FROM admin_user WHERE id=1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if subtle.ConstantTimeCompare(before, after) != 1 {
		t.Fatal("repeated setup changed the administrator password hash")
	}
}

func seedAdminSession(t *testing.T, s *Server, password string) (string, string) {
	t.Helper()
	hash, salt, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := s.store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, hash, salt, auth.CurrentPasswordVersion, "Concurrency test", now); err != nil {
		t.Fatal(err)
	}
	sessionRaw, sessionDigest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	csrfRaw, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('fedcba9876543210fedcba9876543210', ?, ?, ?, ?, 'test', '127.0.0.1')`, sessionDigest, auth.DigestToken(csrfRaw), now, now); err != nil {
		t.Fatal(err)
	}
	return sessionRaw, csrfRaw
}

func newJSONRequest(method, target string, value any, csrf string) *http.Request {
	encoded, _ := json.Marshal(value)
	request := httptest.NewRequest(method, target, strings.NewReader(string(encoded)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://panel.example.test")
	request.Header.Set(csrfHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	return request
}

func assertSessionUnauthorized(t *testing.T, s *Server, raw string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.test/api/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestOriginAndSessionBoundCSRFRejectCrossSiteAndAnonymousToken(t *testing.T) {
	s, err := New("test", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true, PublicOrigin: "http://panel.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixNano()
	if _, err := s.store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, make([]byte, 32), make([]byte, 16), auth.CurrentPasswordVersion, "CSRF test", now); err != nil {
		t.Fatal(err)
	}
	raw, digest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, _, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('feedface0123456789abcdef01234567', ?, ?, ?, ?, 'test', '127.0.0.1')`, digest, auth.DigestToken(csrf), now, now); err != nil {
		t.Fatal(err)
	}
	makeRequest := func(origin, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "http://panel.example.test/api/v1/settings/appearance", strings.NewReader(`{"displayName":"Good","theme":"dark","backgroundColor":"#101827"}`))
		r.Header.Set("Origin", origin)
		r.Header.Set(csrfHeaderName, token)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: raw})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: token})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if w := makeRequest("http://attacker.example", csrf); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write status=%d, want 403", w.Code)
	}
	preAuth, err := s.signedCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if w := makeRequest("http://panel.example.test", preAuth); w.Code != http.StatusForbidden {
		t.Fatalf("anonymous CSRF token authorized a Session write: status=%d", w.Code)
	}
	if w := makeRequest("http://panel.example.test", csrf); w.Code != http.StatusOK {
		t.Fatalf("session-bound same-origin write status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestProductionCookiesAreSecureHttpOnlyAndStrict(t *testing.T) {
	s, err := New("test", Options{DataDir: filepath.Join(t.TempDir(), "data"), PublicOrigin: "https://panel.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := httptest.NewRecorder()
	s.setSessionCookie(w, "raw-session")
	s.setCSRFCookie(w, "csrf-token", time.Hour)
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies=%d, want 2", len(cookies))
	}
	byName := map[string]*http.Cookie{}
	for _, cookie := range cookies {
		byName[cookie.Name] = cookie
	}
	sessionCookie := byName[sessionCookieName]
	csrfCookie := byName[csrfCookieName]
	if sessionCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe production session cookie: %#v", sessionCookie)
	}
	if csrfCookie == nil || !csrfCookie.Secure || csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe production CSRF cookie: %#v", csrfCookie)
	}
}

func TestStoredSessionTokensAreDigestsOnly(t *testing.T) {
	s := newTestServer(t)
	raw, digest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	csrf, csrfDigest, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixNano()
	if _, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('0123456789abcdef0123456789abcdef', ?, ?, ?, ?, 'test', '127.0.0.1')`, digest, csrfDigest, now, now); err != nil {
		t.Fatal(err)
	}
	var saved, savedCSRF []byte
	if err := s.store.DB.QueryRow(`SELECT token_digest, csrf_digest FROM browser_sessions`).Scan(&saved, &savedCSRF); err != nil {
		t.Fatal(err)
	}
	if subtle.ConstantTimeCompare(saved, auth.DigestToken(raw)) != 1 || subtle.ConstantTimeCompare(savedCSRF, auth.DigestToken(csrf)) != 1 {
		t.Fatal("session token verifier digest was not persisted")
	}
	if subtle.ConstantTimeCompare(saved, []byte(raw)) == 1 || subtle.ConstantTimeCompare(savedCSRF, []byte(csrf)) == 1 {
		t.Fatal("raw Session/CSRF token was stored")
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM audit_entries`).Scan(&count); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
}
