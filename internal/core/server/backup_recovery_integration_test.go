package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	corebackup "github.com/CST-Cat/NodeDance/internal/core/backup"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
)

// TestS17BackupRestoreRealCoreAgentReconnect exercises the production backup
// and restore workflow around two real Core instances and one real Agent
// runtime. The HTTPS test listener remains up while the Core is stopped so the
// same Agent endpoint becomes available again after restoration into a new
// data directory.
func TestS17BackupRestoreRealCoreAgentReconnect(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s17-backup-test-docker.sock")
	workDir := t.TempDir()
	if err := os.Chmod(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(workDir, "source-core-data")
	restoredDir := filepath.Join(workDir, "restored-core-data")
	backupPath := filepath.Join(workDir, "core-backup.tar")
	if err := os.Mkdir(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(workDir, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	core, err := New("s17-backup-source", Options{DataDir: sourceDir, Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core is stopped", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		coreHTTP.Close()
		if active := current.Swap(nil); active != nil {
			_ = active.Close()
		}
	})

	adminClient := tlsHTTPClient(rootPEM)
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	admin := func(method, path string, payload any) (*http.Response, error) {
		var body io.Reader
		if payload != nil {
			encoded, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return nil, marshalErr
			}
			body = bytes.NewReader(encoded)
		}
		request, requestErr := http.NewRequest(method, coreHTTP.URL+path, body)
		if requestErr != nil {
			return nil, requestErr
		}
		request.Header.Set("Origin", "https://panel.test")
		request.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
		if payload != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			request.Header.Set(csrfHeaderName, csrf)
		}
		return adminClient.Do(request)
	}

	// Register through the authenticated Core API and enroll the actual Agent
	// over TLS. The Agent credential remains in the same private config path
	// across the Core data-directory replacement.
	enrollmentResponse, err := admin(http.MethodPost, "/api/v1/agents/enrollments", map[string]string{"displayName": "S17 restore node"})
	if err != nil {
		t.Fatal("create enrollment over Core HTTPS:", err)
	}
	var enrollment struct {
		NodeID string `json:"nodeId"`
		Token  string `json:"token"`
	}
	if enrollmentResponse.StatusCode != http.StatusCreated {
		_ = enrollmentResponse.Body.Close()
		t.Fatalf("create enrollment returned HTTP %d", enrollmentResponse.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(enrollmentResponse.Body, 4096)).Decode(&enrollment); err != nil {
		_ = enrollmentResponse.Body.Close()
		t.Fatal("decode enrollment response:", err)
	}
	_ = enrollmentResponse.Body.Close()
	if enrollment.NodeID == "" || enrollment.Token == "" {
		t.Fatal("Core returned an incomplete one-time enrollment")
	}
	configPath := filepath.Join(workDir, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatal("real Agent TLS enrollment:", err)
	}
	agentConfig, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if agentConfig.NodeID != enrollment.NodeID || agentConfig.AgentID == "" {
		t.Fatal("Agent enrollment did not persist its assigned node and device identity")
	}

	agentCtx, stopAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s17-backup-agent", nil) }()
	agentStopped := false
	t.Cleanup(func() {
		if agentStopped {
			return
		}
		stopAgent()
		select {
		case <-agentDone:
		case <-time.After(8 * time.Second):
			t.Error("real Agent runtime did not stop and join")
		}
	})

	waitForS17AdminNode(t, admin, enrollment.NodeID, "online", 20*time.Second)
	settings := dashboard.Settings{ViewMode: "manage", GroupBy: "node", SortBy: "custom", FeaturedLimit: 7, CustomFields: []string{"state", "ports", "health"}}
	settingsResponse, err := admin(http.MethodPut, "/api/v1/dashboard/settings", settings)
	if err != nil {
		t.Fatal("save dashboard settings over Core HTTPS:", err)
	}
	if settingsResponse.StatusCode != http.StatusNoContent {
		_ = settingsResponse.Body.Close()
		t.Fatalf("save dashboard settings returned HTTP %d", settingsResponse.StatusCode)
	}
	_ = settingsResponse.Body.Close()
	preference := dashboard.Preference{
		NodeID: enrollment.NodeID, TargetKind: "node", Identity: dashboard.NodeIdentity(enrollment.NodeID),
		Alias: "Restored primary", Icon: "server", Notes: "preserve this node preference",
		ServiceURL: "https://node.example.test", SortOrder: 3, Visible: true, Pinned: true,
	}
	preferenceResponse, err := admin(http.MethodPut, "/api/v1/nodes/"+enrollment.NodeID+"/preferences", preference)
	if err != nil {
		t.Fatal("save node preference over Core HTTPS:", err)
	}
	if preferenceResponse.StatusCode != http.StatusNoContent {
		_ = preferenceResponse.Body.Close()
		t.Fatalf("save node preference returned HTTP %d", preferenceResponse.StatusCode)
	}
	_ = preferenceResponse.Body.Close()

	// Wait for the real Agent runtime to send metrics through the authenticated
	// WebSocket path; Core persists accepted samples in SQLite history.
	waitForS17MetricHistory(t, admin, enrollment.NodeID, 20*time.Second)

	const webhookSecret = "s17-only-webhook-canary-6f8d2c9a"
	webhookRequests := make(chan string, 1)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 16<<10))
		select {
		case webhookRequests <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(webhook.Close)
	channelInput := alerts.ChannelInput{
		Name: "S17 encrypted recovery fixture", Kind: alerts.ChannelWebhook,
		Config: alerts.ChannelConfig{WebhookURL: webhook.URL + "/notify", MessageTemplate: "{{event}} {{nodeName}} {{message}}"},
		Secret: webhookSecret, Enabled: true,
	}
	channelResponse, err := admin(http.MethodPost, "/api/v1/alerts/channels", channelInput)
	if err != nil {
		t.Fatal("save encrypted notification channel over Core HTTPS:", err)
	}
	var channelReply struct {
		Channel alerts.Channel `json:"channel"`
	}
	if channelResponse.StatusCode != http.StatusOK {
		_ = channelResponse.Body.Close()
		t.Fatalf("save notification channel returned HTTP %d", channelResponse.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(channelResponse.Body, 8192)).Decode(&channelReply); err != nil {
		_ = channelResponse.Body.Close()
		t.Fatal("decode notification channel response:", err)
	}
	_ = channelResponse.Body.Close()
	if channelReply.Channel.ID == "" || !channelReply.Channel.HasSecret || channelReply.Channel.Config.MessageTemplate != channelInput.Config.MessageTemplate {
		t.Fatal("Core did not save notification config and encrypted-secret state")
	}
	var ciphertext []byte
	if err := core.store.DB.QueryRow(`SELECT secret_ciphertext FROM alert_channels WHERE id=?`, channelReply.Channel.ID).Scan(&ciphertext); err != nil {
		t.Fatal("read encrypted channel secret:", err)
	}
	if len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte(webhookSecret)) {
		t.Fatal("notification secret was not stored as ciphertext")
	}
	var auditBefore int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='alert_channel_save' AND outcome='succeeded'`).Scan(&auditBefore); err != nil || auditBefore != 1 {
		t.Fatalf("expected one persisted notification configuration audit event, count=%d err=%v", auditBefore, err)
	}
	alertKeyBefore, err := os.ReadFile(filepath.Join(sourceDir, alertEncryptionKeyName))
	if err != nil {
		t.Fatal(err)
	}
	csrfKeyBefore, err := os.ReadFile(filepath.Join(sourceDir, signingKeyName))
	if err != nil {
		t.Fatal(err)
	}
	deviceDigest := auth.DigestToken(agentConfig.Credential)
	var agentIDBefore, nodeIDBefore string
	if err := core.store.DB.QueryRow(`SELECT v.agent_id, d.node_id FROM agent_credential_verifiers v
		JOIN agent_devices d ON d.id=v.agent_id WHERE v.digest=? AND v.state='active'`, deviceDigest).Scan(&agentIDBefore, &nodeIDBefore); err != nil {
		t.Fatal("read registered Agent identity verifier:", err)
	}
	if agentIDBefore != agentConfig.AgentID || nodeIDBefore != enrollment.NodeID {
		t.Fatal("registered Agent verifier does not match the private Agent config identity")
	}

	// Stop the source Core before creating a consistency backup. The output is
	// outside both Core data directories and all artifacts are under TempDir.
	if active := current.Swap(nil); active != core {
		t.Fatal("unexpected active Core before stop")
	}
	if err := core.Close(); err != nil {
		t.Fatal("stop source Core:", err)
	}
	core = nil
	if err := corebackup.Create(context.Background(), sourceDir, backupPath); err != nil {
		t.Fatal("create stopped Core backup:", err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode=%v err=%v; want private mode 0600", backupInfo, err)
	}
	if err := corebackup.Restore(context.Background(), backupPath, restoredDir); err != nil {
		t.Fatal("restore into a new Core data directory:", err)
	}
	restoredAlertKey, err := os.ReadFile(filepath.Join(restoredDir, alertEncryptionKeyName))
	if err != nil || !bytes.Equal(restoredAlertKey, alertKeyBefore) {
		t.Fatal("restored notification encryption key differs from source")
	}
	restoredCSRFKey, err := os.ReadFile(filepath.Join(restoredDir, signingKeyName))
	if err != nil || !bytes.Equal(restoredCSRFKey, csrfKeyBefore) {
		t.Fatal("restored CSRF signing key differs from source")
	}
	for _, name := range []string{alertEncryptionKeyName, signingKeyName} {
		info, statErr := os.Stat(filepath.Join(restoredDir, name))
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("restored %s mode=%v err=%v; want 0600", name, info, statErr)
		}
	}
	backupHash, err := hashFileForS17(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	alertKeyHash := sha256.Sum256(restoredAlertKey)
	csrfKeyHash := sha256.Sum256(restoredCSRFKey)

	restoredCore, err := New("s17-backup-restored", Options{DataDir: restoredDir, Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal("start restored Core from new data directory:", err)
	}
	current.Store(restoredCore)
	waitForS17AdminNode(t, admin, enrollment.NodeID, "online", 25*time.Second)

	var agentIDAfter, nodeIDAfter string
	if err := restoredCore.store.DB.QueryRow(`SELECT v.agent_id, d.node_id FROM agent_credential_verifiers v
		JOIN agent_devices d ON d.id=v.agent_id WHERE v.digest=? AND v.state='active'`, deviceDigest).Scan(&agentIDAfter, &nodeIDAfter); err != nil {
		t.Fatal("restored Core lost the Agent credential verifier:", err)
	}
	if agentIDAfter != agentIDBefore || nodeIDAfter != nodeIDBefore || agentIDAfter != agentConfig.AgentID || nodeIDAfter != enrollment.NodeID {
		t.Fatal("restored Core changed the original Node/Agent identity")
	}

	settingsRead, err := admin(http.MethodGet, "/api/v1/dashboard/settings", nil)
	if err != nil {
		t.Fatal("read restored dashboard settings:", err)
	}
	var restoredSettings dashboard.Settings
	if settingsRead.StatusCode != http.StatusOK {
		_ = settingsRead.Body.Close()
		t.Fatalf("read dashboard settings after restore returned HTTP %d", settingsRead.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(settingsRead.Body, 8192)).Decode(&restoredSettings); err != nil {
		_ = settingsRead.Body.Close()
		t.Fatal(err)
	}
	_ = settingsRead.Body.Close()
	if !equalS17DashboardSettings(settings, restoredSettings) {
		t.Fatalf("restored dashboard settings differ: got=%+v want=%+v", restoredSettings, settings)
	}
	preferenceRead, err := admin(http.MethodGet, "/api/v1/nodes/"+enrollment.NodeID+"/preferences", nil)
	if err != nil {
		t.Fatal("read restored node preferences:", err)
	}
	var preferencesReply struct {
		Preferences []dashboard.Preference `json:"preferences"`
	}
	if preferenceRead.StatusCode != http.StatusOK {
		_ = preferenceRead.Body.Close()
		t.Fatalf("read node preferences after restore returned HTTP %d", preferenceRead.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(preferenceRead.Body, 8192)).Decode(&preferencesReply); err != nil {
		_ = preferenceRead.Body.Close()
		t.Fatal(err)
	}
	_ = preferenceRead.Body.Close()
	if len(preferencesReply.Preferences) != 1 || preferencesReply.Preferences[0] != preference {
		t.Fatalf("restored node preference differs: got=%+v want=%+v", preferencesReply.Preferences, preference)
	}
	waitForS17MetricHistory(t, admin, enrollment.NodeID, 10*time.Second)
	var auditAfter int
	if err := restoredCore.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='alert_channel_save' AND outcome='succeeded'`).Scan(&auditAfter); err != nil || auditAfter != auditBefore {
		t.Fatalf("restored audit count=%d err=%v; want %d", auditAfter, err, auditBefore)
	}

	// Reuse the old admin session and CSRF token after the data-directory swap,
	// enqueue a real notification, and observe the exact decrypted secret only
	// at this test-owned loopback receiver.
	testChannel, err := admin(http.MethodPost, "/api/v1/alerts/channels/"+channelReply.Channel.ID+"/test", map[string]any{})
	if err != nil {
		t.Fatal("enqueue restored encrypted notification using preserved CSRF session:", err)
	}
	if testChannel.StatusCode != http.StatusAccepted {
		_ = testChannel.Body.Close()
		t.Fatalf("enqueue restored notification returned HTTP %d", testChannel.StatusCode)
	}
	_ = testChannel.Body.Close()
	select {
	case authorization := <-webhookRequests:
		if authorization != "Bearer "+webhookSecret {
			t.Fatal("restored notification sender could not decrypt the original channel secret")
		}
	case <-time.After(8 * time.Second):
		t.Fatal("restored notification worker did not deliver to its loopback receiver")
	}

	stopAgent()
	select {
	case runErr := <-agentDone:
		if runErr != nil {
			t.Fatal("stop real Agent runtime:", runErr)
		}
		agentStopped = true
	case <-time.After(8 * time.Second):
		t.Fatal("real Agent runtime did not stop after backup recovery")
	}

	backupInfo, err = os.Stat(backupPath)
	if err != nil || backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("temporary backup disappeared or permissions changed: info=%v err=%v", backupInfo, err)
	}
	t.Logf("S17-07 candidate PASS: source_core_stopped=true new_data_dir=true agent_reconnected=true node_id=%s agent_id=%s dashboard_settings=true node_preferences=true metric_history=true audit_rows=%d encrypted_webhook_secret_decrypted=true old_csrf_session_accepted=true backup_sha256=%s alert_key_sha256=%s csrf_key_sha256=%s artifact_root=tempdir backup_mode=%#o restored_key_modes=0600",
		enrollment.NodeID, agentConfig.AgentID, auditAfter, backupHash, hex.EncodeToString(alertKeyHash[:]), hex.EncodeToString(csrfKeyHash[:]), backupInfo.Mode().Perm())
}

func waitForS17AdminNode(t *testing.T, admin func(string, string, any) (*http.Response, error), nodeID, wantedStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		response, err := admin(http.MethodGet, "/api/v1/nodes", nil)
		if err == nil {
			var body struct {
				Nodes []struct {
					NodeID string `json:"nodeId"`
					Status string `json:"status"`
				} `json:"nodes"`
			}
			if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body) == nil {
				_ = response.Body.Close()
				for _, node := range body.Nodes {
					if node.NodeID == nodeID && node.Status == wantedStatus {
						return
					}
				}
			} else {
				_ = response.Body.Close()
			}
		} else if !strings.Contains(err.Error(), "connection refused") {
			t.Logf("waiting for Core HTTPS node state: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("Agent node %s did not reach %q through Core HTTPS within %s", nodeID, wantedStatus, timeout)
}

func waitForS17MetricHistory(t *testing.T, admin func(string, string, any) (*http.Response, error), nodeID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	from := time.Now().Add(-3 * time.Minute).UTC().Format(time.RFC3339Nano)
	to := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	path := fmt.Sprintf("/api/v1/nodes/%s/history?resolution=minute&from=%s&to=%s", nodeID, from, to)
	for time.Now().Before(deadline) {
		response, err := admin(http.MethodGet, path, nil)
		if err == nil {
			var body struct {
				Series []struct {
					Key    string `json:"key"`
					Points []any  `json:"points"`
				} `json:"series"`
			}
			if response.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body) == nil {
				_ = response.Body.Close()
				for _, series := range body.Series {
					if strings.HasPrefix(series.Key, "cpu.") && len(series.Points) > 0 {
						return
					}
				}
			} else {
				_ = response.Body.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("real Agent runtime did not persist a CPU metric sample through Core HTTPS history API")
}

func equalS17DashboardSettings(left, right dashboard.Settings) bool {
	if left.ViewMode != right.ViewMode || left.GroupBy != right.GroupBy || left.SortBy != right.SortBy || left.FeaturedLimit != right.FeaturedLimit || len(left.CustomFields) != len(right.CustomFields) {
		return false
	}
	for index := range left.CustomFields {
		if left.CustomFields[index] != right.CustomFields[index] {
			return false
		}
	}
	return true
}

func hashFileForS17(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
