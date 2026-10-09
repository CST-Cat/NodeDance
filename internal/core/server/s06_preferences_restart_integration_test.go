package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
)

func TestS06PreferencePersistenceAcrossCoreRestart(t *testing.T) {
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal("create HTTPS test certificate:", err)
	}
	dataDir := filepath.Join(t.TempDir(), "core-data")
	const publicOrigin = "https://panel.test"
	const password = "S06 preference persistence password"

	var core *Server
	var httpsServer *httptest.Server
	stopCore := func() error {
		if httpsServer != nil {
			httpsServer.Close()
			httpsServer = nil
		}
		if core != nil {
			current := core
			core = nil
			return current.Close()
		}
		return nil
	}
	t.Cleanup(func() {
		if err := stopCore(); err != nil {
			t.Errorf("close S06 Core fixture: %v", err)
		}
	})
	startCore := func() *http.Client {
		t.Helper()
		current, err := New("s06-preference-restart", Options{DataDir: dataDir, PublicOrigin: publicOrigin})
		if err != nil {
			t.Fatal("start Core with persistent S06 DataDir:", err)
		}
		core = current
		httpsServer = httptest.NewUnstartedServer(core)
		httpsServer.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
		httpsServer.StartTLS()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal("create HTTPS cookie jar:", err)
		}
		return &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsConfigForRoots(rootPEM)},
			Jar:       jar,
			Timeout:   10 * time.Second,
		}
	}
	request := func(client *http.Client, method, path string, body any, csrf string) (*http.Response, []byte) {
		t.Helper()
		var encoded io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal("encode S06 API request:", err)
			}
			encoded = strings.NewReader(string(data))
		}
		req, err := http.NewRequestWithContext(context.Background(), method, httpsServer.URL+path, encoded)
		if err != nil {
			t.Fatal("create S06 API request:", err)
		}
		req.Header.Set("Origin", publicOrigin)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if csrf != "" {
			req.Header.Set(csrfHeaderName, csrf)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("send S06 HTTPS API request:", err)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if err != nil {
			t.Fatal("read S06 HTTPS API response:", err)
		}
		return response, data
	}
	issueCSRF := func(client *http.Client) string {
		t.Helper()
		response, body := request(client, http.MethodGet, "/api/v1/auth/csrf", nil, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET CSRF returned HTTP %d", response.StatusCode)
		}
		var result struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
			t.Fatalf("decode CSRF response: token_present=%t decode_error=%v", result.Token != "", err)
		}
		return result.Token
	}
	setupOrLogin := func(client *http.Client, csrf, endpoint string, body any, wantStatus int) string {
		t.Helper()
		response, data := request(client, http.MethodPost, endpoint, body, csrf)
		if response.StatusCode != wantStatus {
			t.Fatalf("POST %s returned HTTP %d, want %d", endpoint, response.StatusCode, wantStatus)
		}
		var result struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.Unmarshal(data, &result); err != nil || result.CSRFToken == "" {
			t.Fatalf("decode authenticated response: csrf_present=%t decode_error=%v", result.CSRFToken != "", err)
		}
		return result.CSRFToken
	}
	client := startCore()
	setupCredential, err := readSetupCredential(setupCredentialPath(dataDir))
	if err != nil {
		t.Fatal("read local one-time setup credential:", err)
	}
	setupCSRF := issueCSRF(client)
	csrf := setupOrLogin(client, setupCSRF, "/api/v1/auth/setup", map[string]string{
		"credential":  setupCredential,
		"password":    password,
		"displayName": "S06 persistence test",
	}, http.StatusCreated)

	identity := insertDockerTestNode(t, core)
	preference := dashboard.Preference{
		NodeID:     identity.NodeID,
		TargetKind: "container",
		Identity:   "container:" + strings.Repeat("a", 64),
		Alias:      "Hidden service alias",
		Icon:       "shield",
		Notes:      "persisted through a real Core restart",
		ServiceURL: "https://vault.example.test/health",
		SortOrder:  37,
		Visible:    false,
		Pinned:     true,
	}
	preferencesPath := "/api/v1/nodes/" + identity.NodeID + "/preferences"
	writeResponse, writeBody := request(client, http.MethodPut, preferencesPath, preference, csrf)
	if writeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT container preference returned HTTP %d: %s", writeResponse.StatusCode, writeBody)
	}
	assertPreferenceAPI := func(client *http.Client) {
		t.Helper()
		response, body := request(client, http.MethodGet, preferencesPath, nil, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET container preferences returned HTTP %d", response.StatusCode)
		}
		var decoded struct {
			Preferences []dashboard.Preference `json:"preferences"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal("decode container preferences:", err)
		}
		if len(decoded.Preferences) != 1 || decoded.Preferences[0] != preference {
			t.Fatalf("GET preference differs field-by-field: got=%+v want=%+v", decoded.Preferences, preference)
		}
	}
	assertPreferenceSQLite := func(db *sql.DB) {
		t.Helper()
		var rows int
		if err := db.QueryRow(`SELECT count(*) FROM dashboard_preferences WHERE node_id=?`, preference.NodeID).Scan(&rows); err != nil {
			t.Fatal("count persisted dashboard preference rows:", err)
		}
		if rows != 1 {
			t.Fatalf("SQLite contains %d preference rows for node, want exactly one", rows)
		}
		var stored dashboard.Preference
		var visible, pinned int
		var updatedAt int64
		err := db.QueryRow(`SELECT node_id, target_kind, identity_key, alias, icon, notes, service_url, sort_order, visible, pinned, updated_at
			FROM dashboard_preferences WHERE node_id=? AND target_kind=? AND identity_key=?`, preference.NodeID, preference.TargetKind, preference.Identity).
			Scan(&stored.NodeID, &stored.TargetKind, &stored.Identity, &stored.Alias, &stored.Icon, &stored.Notes,
				&stored.ServiceURL, &stored.SortOrder, &visible, &pinned, &updatedAt)
		if err != nil {
			t.Fatal("read exact SQLite preference row:", err)
		}
		stored.Visible, stored.Pinned = visible == 1, pinned == 1
		if stored != preference || visible != 0 || pinned != 1 || updatedAt <= 0 {
			t.Fatalf("SQLite preference fields differ: got=%+v raw_visible=%d raw_pinned=%d updated_at_valid=%t want=%+v",
				stored, visible, pinned, updatedAt > 0, preference)
		}
	}
	assertPreferenceAPI(client)
	assertPreferenceSQLite(core.store.DB)
	if err := stopCore(); err != nil {
		t.Fatal("stop HTTP/Core before persistent restart:", err)
	}

	client = startCore()
	loginCSRF := issueCSRF(client)
	csrf = setupOrLogin(client, loginCSRF, "/api/v1/auth/login", map[string]string{"password": password}, http.StatusOK)
	assertPreferenceAPI(client)
	assertPreferenceSQLite(core.store.DB)
	t.Logf("S06-03_PREFERENCE_RESTART_PASS target=container visible=false pinned=true api_fields_verified=true sqlite_fields_verified=true restart_same_data_dir=true")
}
