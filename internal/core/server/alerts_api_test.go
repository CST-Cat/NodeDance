package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/alerts"
)

func TestAlertBackgroundReadsDoNotExtendIdleSession(t *testing.T) {
	for _, path := range []string{"/api/v1/alerts", "/api/v1/alerts/history", "/api/v1/alerts/events", "/api/v1/alerts/deliveries"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if !isMetricsTelemetryRead(request) {
			t.Errorf("background alert read %s extends idle session", path)
		}
	}
	write := httptest.NewRequest(http.MethodPost, "/api/v1/alerts/11111111-1111-4111-8111-111111111111/acknowledge", nil)
	if isMetricsTelemetryRead(write) {
		t.Fatal("alert acknowledgement must count as administrator activity")
	}
}

func TestAlertAPISessionCSRFDeliveryAndAuditing(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	core, err := New("s15-alert-api", Options{DataDir: dataDir, Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	unauth := httptest.NewRecorder()
	core.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "https://panel.test/api/v1/alerts", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated alert API status=%d", unauth.Code)
	}
	token, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "alert api node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any, withCSRF bool) *httptest.ResponseRecorder {
		var encoded []byte
		if body != nil {
			encoded, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "https://panel.test"+path, bytes.NewReader(encoded))
		r.Header.Set("Origin", "https://panel.test")
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		if withCSRF {
			r.Header.Set(csrfHeaderName, csrf)
		}
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	template := "{{severity}} {{ruleName}} on {{nodeName}}: {{message}}"
	channelPayload := map[string]any{"name": "local S15 webhook", "kind": "webhook", "config": map[string]any{"webhookUrl": "http://127.0.0.1:1/hooks", "messageTemplate": template}, "enabled": true}
	if got := request(http.MethodPost, "/api/v1/alerts/channels", channelPayload, false); got.Code != http.StatusForbidden {
		t.Fatalf("channel write without CSRF status=%d", got.Code)
	}
	createdChannel := request(http.MethodPost, "/api/v1/alerts/channels", channelPayload, true)
	if createdChannel.Code != http.StatusOK {
		t.Fatalf("channel create status=%d body=%s", createdChannel.Code, createdChannel.Body.String())
	}
	var channelReply struct {
		Channel alerts.Channel `json:"channel"`
	}
	if err := json.Unmarshal(createdChannel.Body.Bytes(), &channelReply); err != nil || channelReply.Channel.ID == "" {
		t.Fatalf("invalid channel response: %+v err=%v", channelReply, err)
	}
	if channelReply.Channel.Config.MessageTemplate != template {
		t.Fatalf("channel API did not preserve message template: %q", channelReply.Channel.Config.MessageTemplate)
	}
	invalidTemplate := map[string]any{"name": "invalid template", "kind": "webhook", "config": map[string]any{"webhookUrl": "http://127.0.0.1:1/hooks", "messageTemplate": "{{unknownField}}"}, "enabled": true}
	if got := request(http.MethodPost, "/api/v1/alerts/channels", invalidTemplate, true); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown channel template field status=%d body=%s", got.Code, got.Body.String())
	}
	listedChannels := request(http.MethodGet, "/api/v1/alerts/channels", nil, false)
	if listedChannels.Code != http.StatusOK || !bytes.Contains(listedChannels.Body.Bytes(), []byte(template)) {
		t.Fatalf("channel list did not expose persisted template: status=%d body=%s", listedChannels.Code, listedChannels.Body.String())
	}
	rulePayload := map[string]any{"name": "node unavailable", "kind": "node_offline", "nodeId": enrollment.NodeID, "severity": "critical", "durationSeconds": 0, "cooldownSeconds": 60, "channelIds": []string{channelReply.Channel.ID}, "enabled": true}
	createdRule := request(http.MethodPost, "/api/v1/alerts/rules", rulePayload, true)
	if createdRule.Code != http.StatusOK {
		t.Fatalf("rule create status=%d body=%s", createdRule.Code, createdRule.Body.String())
	}
	var ruleReply struct {
		Rule alerts.Rule `json:"rule"`
	}
	if err := json.Unmarshal(createdRule.Body.Bytes(), &ruleReply); err != nil || ruleReply.Rule.ID == "" {
		t.Fatalf("invalid rule response: %+v err=%v", ruleReply, err)
	}
	list := request(http.MethodGet, "/api/v1/alerts/rules?nodeId="+enrollment.NodeID, nil, false)
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(ruleReply.Rule.ID)) {
		t.Fatalf("rule list response status=%d body=%s", list.Code, list.Body.String())
	}
	var auditCount int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action IN ('alert_rule_save','alert_channel_save')`).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("accepted and rejected alert writes not audited: count=%d err=%v", auditCount, err)
	}
	keyInfo, err := os.Stat(filepath.Join(dataDir, alertEncryptionKeyName))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("alert encryption key mode=%o", keyInfo.Mode().Perm())
	}
}

func TestAlertChannelSecretSurvivesCoreRestartEncrypted(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	core, err := New("s15-alert-secret", Options{DataDir: dataDir, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := core.alerts.SaveChannel(context.Background(), "", alerts.ChannelInput{Name: "encrypted smtp", Kind: alerts.ChannelSMTP, Config: alerts.ChannelConfig{SMTPHost: "localhost", SMTPPort: 25, SMTPFrom: "admin@example.test", SMTPTo: "ops@example.test", SMTPUsername: "operator"}, Secret: "test-secret-not-plaintext", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var cipherText []byte
	if err := core.store.DB.QueryRow(`SELECT secret_ciphertext FROM alert_channels WHERE id=?`, channel.ID).Scan(&cipherText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipherText), "test-secret-not-plaintext") {
		t.Fatal("channel password persisted as plaintext")
	}
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New("s15-alert-secret-restarted", Options{DataDir: dataDir, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	channels, err := restarted.alerts.ListChannels(context.Background(), channel.ID)
	if err != nil || len(channels) != 1 || !channels[0].HasSecret {
		t.Fatalf("encrypted channel did not survive restart: %#v err=%v", channels, err)
	}
	wire, _ := json.Marshal(channels)
	if strings.Contains(string(wire), "test-secret-not-plaintext") {
		t.Fatal("API output exposed channel password")
	}
	if err := restarted.alerts.EnqueueTest(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	// The outbox has a real queued item after restart; without a usable local SMTP service
	// it remains a real failed/retrying attempt rather than a mocked success.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		items, _ := restarted.alerts.ListDeliveries(context.Background(), 10)
		if len(items) > 0 && items[0].Attempts > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("delivery worker did not attempt the persisted test send")
}
