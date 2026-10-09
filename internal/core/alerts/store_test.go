package alerts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

const testNodeID = "11111111-1111-4111-8111-111111111111"

func newTestStore(t *testing.T, now *time.Time) (*Store, *storage.Store) {
	t.Helper()
	db, err := storage.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,?,'online',?,?)`, testNodeID, "test node", now.UnixNano(), now.UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return *now }
	store, err := NewStore(db.DB, []byte("0123456789abcdef0123456789abcdef"), clock)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func newRule(t *testing.T, store *Store, now time.Time, kind string, duration, cooldown int, threshold *float64, channels []string) Rule {
	t.Helper()
	rule, err := store.SaveRule(context.Background(), "", RuleInput{Name: "test " + kind, Kind: kind, NodeID: testNodeID, Severity: SeverityWarning, Threshold: threshold, DurationSeconds: duration, CooldownSeconds: cooldown, ChannelIDs: channels, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return rule
}

func evaluate(t *testing.T, s *Store, now time.Time, samples ...Sample) {
	t.Helper()
	if err := s.Evaluate(context.Background(), samples); err != nil {
		t.Fatal(err)
	}
}
func cpuSample(at time.Time, value float64) Sample {
	return Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindCPU, Known: true, Value: &value, ObservedAt: at}
}

func TestEvaluationDurationRecoveryAndRealWebhookDelivery(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	received := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		received = append(received, string(body))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	store, _ := newTestStore(t, &now)
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "local receiver", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: server.URL}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	threshold := 90.0
	rule := newRule(t, store, now, KindCPU, 10, 30, &threshold, []string{channel.ID})
	evaluate(t, store, now, cpuSample(now, 95))
	if items, _ := store.ListAlerts(context.Background(), true, 10); len(items) != 0 {
		t.Fatalf("alert fired before duration: %#v", items)
	}
	now = now.Add(5 * time.Second)
	evaluate(t, store, now, cpuSample(now, 96))
	if items, _ := store.ListAlerts(context.Background(), true, 10); len(items) != 0 {
		t.Fatalf("alert fired before duration: %#v", items)
	}
	now = now.Add(5 * time.Second)
	evaluate(t, store, now, cpuSample(now, 97))
	items, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("active alerts=%d err=%v", len(items), err)
	}
	if items[0].RuleID != rule.ID || items[0].CurrentValue == nil || *items[0].CurrentValue != 97 {
		t.Fatalf("unexpected active alert: %#v", items[0])
	}
	// Re-evaluating the same sample must not create an immediate cooldown reminder.
	evaluate(t, store, now, cpuSample(now, 97))
	deliveries, err := store.ListDeliveries(context.Background(), 20)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "queued" {
		t.Fatalf("unexpected firing deliveries: %#v, err=%v", deliveries, err)
	}
	delivery, sendChannel, secret, claimed, err := store.ClaimDelivery(context.Background(), now)
	if err != nil || !claimed {
		t.Fatalf("ClaimDelivery claimed=%t err=%v", claimed, err)
	}
	status, sendErr := NewSender().Send(context.Background(), sendChannel, secret, delivery.PayloadJSON)
	if sendErr != nil {
		t.Fatal(sendErr)
	}
	if err := store.CompleteDelivery(context.Background(), delivery, true, status, "", now); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(received) != 1 || !strings.Contains(received[0], `"event":"firing"`) {
		t.Fatalf("webhook did not receive firing event: %q", received)
	}
	mu.Unlock()
	now = now.Add(time.Second)
	evaluate(t, store, now, cpuSample(now, 40))
	resolved, err := store.ListAlerts(context.Background(), false, 10)
	if err != nil || len(resolved) != 1 || resolved[0].Status != "resolved" || resolved[0].ResolvedAt == nil {
		t.Fatalf("alert not recovered: %#v err=%v", resolved, err)
	}
	if ev, err := store.ListEvents(context.Background(), resolved[0].ID, 10); err != nil || len(ev) < 2 || ev[0].Kind != "recovered" {
		t.Fatalf("recovery event missing: %#v err=%v", ev, err)
	}
	firstAlertID := resolved[0].ID
	now = now.Add(time.Second)
	evaluate(t, store, now, cpuSample(now, 95))
	now = now.Add(10 * time.Second)
	evaluate(t, store, now, cpuSample(now, 96))
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].ID == firstAlertID {
		t.Fatalf("second failure did not create a new active alert: %#v err=%v", active, err)
	}
}

func TestCooldownSuppressesRepeatedNotificationButAllowsReminder(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	threshold := 90.0
	newRule(t, store, now, KindCPU, 0, 10, &threshold, nil)
	evaluate(t, store, now, cpuSample(now, 95))
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 {
		t.Fatalf("initial firing missing: %#v err=%v", active, err)
	}
	if events, err := store.ListEvents(context.Background(), active[0].ID, 10); err != nil || len(events) != 1 || events[0].Kind != "firing" {
		t.Fatalf("unexpected initial events: %#v err=%v", events, err)
	}
	now = now.Add(5 * time.Second)
	evaluate(t, store, now, cpuSample(now, 96))
	if events, err := store.ListEvents(context.Background(), active[0].ID, 10); err != nil || len(events) != 1 {
		t.Fatalf("cooldown did not suppress repeat: %#v err=%v", events, err)
	}
	now = now.Add(5 * time.Second)
	evaluate(t, store, now, cpuSample(now, 97))
	if events, err := store.ListEvents(context.Background(), active[0].ID, 10); err != nil || len(events) != 2 || events[0].Kind != "reminder" {
		t.Fatalf("cooldown reminder missing: %#v err=%v", events, err)
	}
}

func TestAcknowledgeAndSilenceSuppressPendingDelivery(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "pending receiver", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: "http://127.0.0.1:1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	threshold := 90.0
	newRule(t, store, now, KindCPU, 0, 60, &threshold, []string{channel.ID})
	evaluate(t, store, now, cpuSample(now, 95))
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 {
		t.Fatalf("alert did not fire: %#v err=%v", active, err)
	}
	if err := store.Acknowledge(context.Background(), active[0].ID, "administrator", "checked"); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "suppressed" {
		t.Fatalf("acknowledgement left pending delivery: %#v err=%v", deliveries, err)
	}
	if _, err := store.SaveRule(context.Background(), "", RuleInput{Name: "memory pending", Kind: KindMemory, NodeID: testNodeID, Severity: SeverityWarning, Threshold: &threshold, DurationSeconds: 0, CooldownSeconds: 60, ChannelIDs: []string{channel.ID}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	memory := 95.0
	evaluate(t, store, now, Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindMemory, Known: true, Value: &memory, ObservedAt: now})
	active, err = store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 2 {
		t.Fatalf("second alert did not fire: %#v err=%v", active, err)
	}
	var memoryAlert *Alert
	for i := range active {
		if active[i].RuleName == "memory pending" {
			memoryAlert = &active[i]
		}
	}
	if memoryAlert == nil {
		t.Fatalf("memory alert missing: %#v", active)
	}
	if err := store.SilenceAlert(context.Background(), memoryAlert.ID, now.Add(time.Hour), "pending validation"); err != nil {
		t.Fatal(err)
	}
	deliveries, err = store.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 2 || deliveries[0].Status != "suppressed" || deliveries[1].Status != "suppressed" {
		t.Fatalf("acknowledgement or silence left pending delivery: %#v err=%v", deliveries, err)
	}
}

func TestOfflineSuppressionAcknowledgementWindowsAndDefaults(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	if err := store.EnsureDefaultRules(context.Background(), testNodeID); err != nil {
		t.Fatal(err)
	}
	rules, err := store.ListRules(context.Background(), testNodeID)
	if err != nil || len(rules) != 3 {
		t.Fatalf("default rules=%d err=%v", len(rules), err)
	}
	for _, rule := range rules {
		if rule.Threshold == nil || *rule.Threshold != 90 || rule.DurationSeconds != 300 {
			t.Fatalf("unexpected default rule: %#v", rule)
		}
	}
	threshold := 90.0
	child := newRule(t, store, now, KindCPU, 0, 0, &threshold, nil)
	containerID := strings.Repeat("a", 64)
	container, err := store.SaveRule(context.Background(), "", RuleInput{Name: "container expected running", Kind: KindContainerState, NodeID: testNodeID, SubjectID: containerID, Severity: SeverityWarning, DurationSeconds: 0, CooldownSeconds: 0, ExpectedState: "running", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	probeID := "22222222-2222-4222-8222-222222222222"
	probe, err := store.SaveRule(context.Background(), "", RuleInput{Name: "probe expected healthy", Kind: KindProbeState, NodeID: testNodeID, SubjectID: probeID, Severity: SeverityWarning, DurationSeconds: 0, CooldownSeconds: 0, ExpectedState: "healthy", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, store, now, cpuSample(now, 99), Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindContainerState, SubjectID: containerID, Known: true, State: "stopped", Health: "none", ObservedAt: now}, Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindProbeState, SubjectID: probeID, Known: true, State: "unhealthy", ObservedAt: now})
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 3 {
		t.Fatalf("expected active cpu alert: %#v err=%v", active, err)
	}
	cpuAlertID := ""
	foundRules := map[string]bool{}
	for _, item := range active {
		foundRules[item.RuleID] = true
		if item.RuleID == child.ID {
			cpuAlertID = item.ID
		}
	}
	if !foundRules[container.ID] || !foundRules[probe.ID] {
		t.Fatalf("container or probe rule did not alert: %#v", active)
	}
	if cpuAlertID == "" {
		t.Fatal("CPU alert missing")
	}
	now = now.Add(time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindNodeOffline, Known: true, State: "offline", ObservedAt: now}, Sample{NodeID: testNodeID, Kind: KindCPU, Known: false, ObservedAt: now}, Sample{NodeID: testNodeID, Kind: KindContainerState, SubjectID: containerID, Known: false, ObservedAt: now}, Sample{NodeID: testNodeID, Kind: KindProbeState, SubjectID: probeID, Known: false, ObservedAt: now})
	active, err = store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 3 {
		t.Fatalf("child alerts missing: %#v err=%v", active, err)
	}
	for _, item := range active {
		if item.SuppressionReason != "node_offline" {
			t.Fatalf("child alert not suppressed: %#v", item)
		}
	}
	deliveries, err := store.ListDeliveries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range deliveries {
		if d.AlertID != "" && d.Status != "suppressed" {
			t.Fatalf("queued child delivery was not suppressed: %#v", d)
		}
	}
	if err := store.Acknowledge(context.Background(), cpuAlertID, "administrator", "checked"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindNodeOffline, Known: true, State: "online", ObservedAt: now}, cpuSample(now, 99), Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindContainerState, SubjectID: containerID, Known: true, State: "running", Health: "healthy", ObservedAt: now}, Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindProbeState, SubjectID: probeID, Known: true, State: "healthy", ObservedAt: now})
	active, err = store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].AcknowledgedAt == nil {
		t.Fatalf("acknowledgement did not persist: %#v err=%v", active, err)
	}
	if ev, err := store.ListEvents(context.Background(), cpuAlertID, 20); err != nil || len(ev) < 3 {
		t.Fatalf("expected lifecycle events, got %#v err=%v", ev, err)
	}
	now = now.Add(time.Second)
	w, err := store.AddWindow(context.Background(), WindowInput{Kind: "maintenance", ScopeType: "global", StartsAt: now.Add(-time.Second), EndsAt: now.Add(time.Hour), Reason: "test maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListWindows(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWindow(context.Background(), w.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteRule(context.Background(), child.ID, child.Revision); err != nil {
		t.Fatal(err)
	}
}

func TestContainerHealthRuleSkipsNoHealthcheckButStateRuleStillEvaluates(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, db := newTestStore(t, &now)
	containerID := strings.Repeat("a", 64)
	healthRule, err := store.SaveRule(context.Background(), "", RuleInput{
		Name: "expected healthy", Kind: KindContainerState, NodeID: testNodeID, SubjectID: containerID,
		Severity: SeverityWarning, ExpectedState: "healthy", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runningRule, err := store.SaveRule(context.Background(), "", RuleInput{
		Name: "expected running", Kind: KindContainerState, NodeID: testNodeID, SubjectID: containerID,
		Severity: SeverityWarning, ExpectedState: "running", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, store, now, Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindContainerState,
		SubjectID: containerID, Known: true, State: "stopped", Health: "none", ObservedAt: now})
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].RuleID != runningRule.ID {
		t.Fatalf("no-healthcheck sample should skip health rule but evaluate running rule: alerts=%#v err=%v", active, err)
	}
	var healthRuleStateCount int
	if err := db.DB.QueryRow(`SELECT count(*) FROM alert_rule_state WHERE rule_id=? AND subject_id=?`, healthRule.ID, containerID).Scan(&healthRuleStateCount); err != nil {
		t.Fatal(err)
	}
	if healthRuleStateCount != 0 {
		t.Fatalf("no-healthcheck sample retained health-rule duration state: count=%d", healthRuleStateCount)
	}
	now = now.Add(time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, NodeName: "test node", Kind: KindContainerState,
		SubjectID: containerID, Known: true, State: "running", Health: "none", ObservedAt: now})
	active, err = store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 0 {
		t.Fatalf("running state did not resolve the expected-running alert: alerts=%#v err=%v", active, err)
	}
}

func TestSilenceWindowAndOfflineMetricRemainUnknown(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	threshold := 90.0
	rule := newRule(t, store, now, KindCPU, 0, 0, &threshold, nil)
	window, err := store.AddWindow(context.Background(), WindowInput{Kind: "silence", ScopeType: "rule", ScopeID: rule.ID, StartsAt: now.Add(-time.Second), EndsAt: now.Add(time.Hour), Reason: "change window"})
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, store, now, cpuSample(now, 99))
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].SuppressionReason != "window" {
		t.Fatalf("window did not suppress notifications: %#v err=%v", active, err)
	}
	deliveries, err := store.ListDeliveries(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range deliveries {
		if item.Status != "suppressed" {
			t.Fatalf("window delivery not suppressed: %#v", item)
		}
	}
	if err := store.SilenceAlert(context.Background(), active[0].ID, now.Add(30*time.Minute), "operator maintenance"); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWindow(context.Background(), window.ID); err != nil {
		t.Fatal(err)
	}
	// Unknown/stale samples reset pending duration and never become a zero or a low value.
	second, err := store.SaveRule(context.Background(), "", RuleInput{Name: "fresh sample required", Kind: KindMemory, NodeID: testNodeID, Severity: SeverityWarning, Threshold: &threshold, DurationSeconds: 0, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindMemory, Known: false, Value: &threshold, ObservedAt: now})
	items, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.RuleID == second.ID {
			t.Fatalf("unknown stale metric created an alert: %#v", item)
		}
	}
}

func TestOfflineNodeResetsPendingChildDuration(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	threshold := 90.0
	newRule(t, store, now, KindCPU, 10, 60, &threshold, nil)
	evaluate(t, store, now, cpuSample(now, 95))
	now = now.Add(5 * time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindNodeOffline, Known: true, State: "offline", ObservedAt: now}, Sample{NodeID: testNodeID, Kind: KindCPU, Known: false, ObservedAt: now})
	now = now.Add(10 * time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindNodeOffline, Known: true, State: "online", ObservedAt: now}, cpuSample(now, 95))
	if active, err := store.ListAlerts(context.Background(), true, 10); err != nil || len(active) != 0 {
		t.Fatalf("stale pre-offline duration fired immediately: %#v err=%v", active, err)
	}
	now = now.Add(10 * time.Second)
	evaluate(t, store, now, Sample{NodeID: testNodeID, Kind: KindNodeOffline, Known: true, State: "online", ObservedAt: now}, cpuSample(now, 95))
	if active, err := store.ListAlerts(context.Background(), true, 10); err != nil || len(active) != 1 {
		t.Fatalf("fresh post-reconnect duration did not fire: %#v err=%v", active, err)
	}
}

func TestAlertAndOutboxStateSurviveRestartAndRecoverSending(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, db := newTestStore(t, &now)
	key := []byte("0123456789abcdef0123456789abcdef")
	threshold := 90.0
	rule := newRule(t, store, now, KindCPU, 0, 60, &threshold, nil)
	evaluate(t, store, now, cpuSample(now, 95))
	active, err := store.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 {
		t.Fatalf("expected one active alert before restart: %#v err=%v", active, err)
	}
	// Simulate a claimed delivery interrupted by process exit.
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "restart receiver", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: "http://127.0.0.1:1"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE alert_rules SET channel_ids_json=? WHERE id=?`, fmt.Sprintf(`["%s"]`, channel.ID), rule.ID); err != nil {
		t.Fatal(err)
	}
	// The existing alert snapshots no channels, so create a test delivery explicitly.
	if err := store.EnqueueTest(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, claimed, err := store.ClaimDelivery(context.Background(), now); err != nil || !claimed {
		t.Fatalf("claim before restart: %t err=%v", claimed, err)
	}
	restarted, err := NewStore(db.DB, key, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RecoverDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	rules, err := restarted.ListRules(context.Background(), testNodeID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules lost after restart: %#v err=%v", rules, err)
	}
	active, err = restarted.ListAlerts(context.Background(), true, 10)
	if err != nil || len(active) != 1 || active[0].ID == "" {
		t.Fatalf("active alert lost after restart: %#v err=%v", active, err)
	}
	deliveries, err := restarted.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "retry" || deliveries[0].Attempts != 1 {
		t.Fatalf("interrupted delivery not recovered: %#v err=%v", deliveries, err)
	}
}

func TestWebhookOutboxBoundedRetriesAndChannelSecretEncryption(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, db := newTestStore(t, &now)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusServiceUnavailable) }))
	defer server.Close()
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "failing receiver", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: server.URL}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveChannel(context.Background(), "", ChannelInput{Name: "SMTP secret", Kind: ChannelSMTP, Config: ChannelConfig{SMTPHost: "localhost", SMTPPort: 25, SMTPFrom: "admin@example.test", SMTPTo: "ops@example.test", SMTPUsername: "nodedance"}, Secret: "secret-password-value", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.DB.QueryRow(`SELECT secret_ciphertext FROM alert_channels WHERE name='SMTP secret'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "secret-password-value") {
		t.Fatal("SMTP secret stored as plaintext")
	}
	channels, err := store.ListChannels(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(channels)
	if strings.Contains(string(wire), "secret-password-value") {
		t.Fatal("SMTP secret exposed in channel API model")
	}
	if err := store.EnqueueTest(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		d, c, secret, claimed, err := store.ClaimDelivery(context.Background(), now)
		if err != nil || !claimed {
			t.Fatalf("claim attempt %d: claimed=%t err=%v", attempt, claimed, err)
		}
		status, sendErr := NewSender().Send(context.Background(), c, secret, d.PayloadJSON)
		if sendErr == nil || status != http.StatusServiceUnavailable {
			t.Fatalf("expected controlled HTTP failure, status=%d err=%v", status, sendErr)
		}
		if err := store.CompleteDelivery(context.Background(), d, false, status, sendErr.Error(), now); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			now = now.Add(time.Duration(5*(1<<uint(attempt-1))) * time.Second)
		}
	}
	if requests != 3 {
		t.Fatalf("webhook attempts=%d, want bounded 3", requests)
	}
	deliveries, err := store.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "failed" || deliveries[0].Attempts != 3 {
		t.Fatalf("retry history unexpected: %#v err=%v", deliveries, err)
	}
}

func TestDeliverySecretDecryptionFailureIsPersisted(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "encrypted channel", Kind: ChannelSMTP, Config: ChannelConfig{SMTPHost: "localhost", SMTPPort: 25, SMTPFrom: "admin@example.test", SMTPTo: "ops@example.test", SMTPUsername: "operator"}, Secret: "original-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueTest(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	wrongKeyStore, err := NewStore(store.db, []byte("abcdef0123456789abcdef0123456789"), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, claimed, err := wrongKeyStore.ClaimDelivery(context.Background(), now); err != nil || claimed {
		t.Fatalf("wrong key must not claim delivery: claimed=%t err=%v", claimed, err)
	}
	deliveries, err := wrongKeyStore.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "failed" || deliveries[0].Attempts != 1 || !strings.Contains(deliveries[0].LastError, "cannot be decrypted") {
		t.Fatalf("decryption failure was not persisted: %#v err=%v", deliveries, err)
	}
}

func TestSMTPControlledLocalReceiver(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	var message string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "220 local fixture ESMTP\r\n")
		reader := bufio.NewReader(conn)
		data := false
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				return
			}
			if data {
				if line == ".\r\n" {
					data = false
					_, _ = io.WriteString(conn, "250 queued\r\n")
					continue
				}
				mu.Lock()
				message += line
				mu.Unlock()
				continue
			}
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				_, _ = io.WriteString(conn, "250-local fixture\r\n250 SIZE 1048576\r\n")
			case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RCPT TO"):
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case strings.HasPrefix(upper, "DATA"):
				data = true
				_, _ = io.WriteString(conn, "354 end with dot\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				_, _ = io.WriteString(conn, "221 bye\r\n")
				return
			default:
				_, _ = io.WriteString(conn, "250 ok\r\n")
			}
		}
	}()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portText, "%d", &port)
	now := time.Now().UTC()
	store, _ := newTestStore(t, &now)
	channel, err := store.SaveChannel(context.Background(), "", ChannelInput{Name: "local smtp", Kind: ChannelSMTP, Config: ChannelConfig{SMTPHost: "127.0.0.1", SMTPPort: port, SMTPFrom: "NodeDance <admin@example.test>", SMTPTo: "ops@example.test", MessageTemplate: "{{event}} {{ruleName}}: {{message}}"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	threshold := 90.0
	newRule(t, store, now, KindCPU, 0, 60, &threshold, []string{channel.ID})
	sample := cpuSample(now, 95)
	sample.Message = "<img src=x>\r\nSubject: forged"
	evaluate(t, store, now, sample)
	d, c, secret, claimed, err := store.ClaimDelivery(context.Background(), now)
	if err != nil || !claimed {
		t.Fatalf("claim smtp: %t %v", claimed, err)
	}
	if _, err := NewSender().Send(context.Background(), c, secret, d.PayloadJSON); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDelivery(context.Background(), d, true, 0, "", now); err != nil {
		t.Fatal(err)
	}
	deliveries, err := store.ListDeliveries(context.Background(), 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "sent" || deliveries[0].Attempts != 1 {
		t.Fatalf("SMTP delivery result was not persisted: %#v err=%v", deliveries, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("local SMTP receiver timed out")
	}
	mu.Lock()
	got := message
	mu.Unlock()
	if !strings.Contains(got, "test cpu") || !strings.Contains(got, "Event: firing") || !strings.Contains(got, `\r\nSubject: forged`) || strings.Contains(got, "\r\nSubject: forged") || !strings.Contains(got, "text/plain; charset=utf-8") {
		t.Fatalf("SMTP fixture received wrong message: %q", got)
	}
}

func TestChannelMessageTemplatePersistsAndSnapshotsSafeWebhookPayload(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store, _ := newTestStore(t, &now)
	legacy, err := store.SaveChannel(context.Background(), "", ChannelInput{
		Name: "legacy", Kind: ChannelWebhook,
		Config: ChannelConfig{WebhookURL: "http://127.0.0.1:1"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	template := "{{event}}|{{alertId}}|{{ruleName}}|{{nodeName}}|{{nodeId}}|{{severity}}|{{message}}"
	custom, err := store.SaveChannel(context.Background(), "", ChannelInput{
		Name: "custom", Kind: ChannelWebhook,
		Config: ChannelConfig{WebhookURL: "http://127.0.0.1:1", MessageTemplate: template}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.GetChannel(context.Background(), custom.ID)
	if err != nil || reloaded.Config.MessageTemplate != template {
		t.Fatalf("channel template was not persisted: %#v err=%v", reloaded.Config, err)
	}
	threshold := 90.0
	newRule(t, store, now, KindCPU, 0, 60, &threshold, []string{legacy.ID, custom.ID})
	malicious := "<script>alert(1)</script> {{event}}\r\nSubject: forged & more"
	sample := cpuSample(now, 95)
	sample.Message = malicious
	evaluate(t, store, now, sample)

	for range 2 {
		delivery, _, _, claimed, err := store.ClaimDelivery(context.Background(), now)
		if err != nil || !claimed {
			t.Fatalf("claim channel delivery claimed=%v err=%v", claimed, err)
		}
		var notification Notification
		if err := json.Unmarshal([]byte(delivery.PayloadJSON), &notification); err != nil {
			t.Fatalf("stored Webhook payload is not JSON: %v", err)
		}
		if delivery.ChannelID == legacy.ID {
			if notification.Message != malicious {
				t.Fatalf("empty template changed legacy notification: %q", notification.Message)
			}
		} else if delivery.ChannelID == custom.ID {
			wantPrefix := "firing|" + notification.AlertID + "|test cpu|test node|" + testNodeID + "|warning|"
			if !strings.HasPrefix(notification.Message, wantPrefix) || !strings.HasSuffix(notification.Message, "|"+malicious) {
				t.Fatalf("custom template rendered incorrectly: %q", notification.Message)
			}
			if strings.Contains(delivery.PayloadJSON, "\r\nSubject: forged") {
				t.Fatal("rendered Webhook payload did not JSON escape injected line breaks")
			}
		} else {
			t.Fatalf("unexpected channel delivery %q", delivery.ChannelID)
		}
	}
}
