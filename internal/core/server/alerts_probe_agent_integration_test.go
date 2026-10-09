package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAlertProbeStateFromRealAgentResults(t *testing.T) {
	work := t.TempDir()
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s15-real-agent-probe-alert", Options{
		DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", Development: true,
		// Keep the test Agent lease valid while the independent 5-second alert
		// evaluator observes the persisted probe transition.
		AgentOfflineTimeout: 30 * time.Second, AgentSweepInterval: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	var agentDone chan error
	var stopAgent context.CancelFunc
	t.Cleanup(func() {
		if stopAgent != nil {
			stopAgent()
			select {
			case runErr := <-agentDone:
				if runErr != nil {
					t.Errorf("real Agent returned an error while stopping: %v", runErr)
				}
			case <-time.After(7 * time.Second):
				t.Error("real Agent did not stop")
			}
		}
		coreHTTP.Close()
		if closeErr := core.Close(); closeErr != nil {
			t.Errorf("close Core: %v", closeErr)
		}
	})

	var fixtureStatus atomic.Int32
	fixtureStatus.Store(http.StatusServiceUnavailable)
	var fixtureCalls atomic.Int64
	webhookNotifications := make(chan corealerts.Notification, 4)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		fixtureCalls.Add(1)
		w.WriteHeader(int(fixtureStatus.Load()))
	}))
	t.Cleanup(fixture.Close)

	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	adminJSON := func(method, path string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		var body []byte
		if payload != nil {
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, "https://panel.test"+path, strings.NewReader(string(body)))
		req.Header.Set("Origin", "https://panel.test")
		req.Header.Set(csrfHeaderName, csrfToken)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
		response := httptest.NewRecorder()
		core.ServeHTTP(response, req)
		return response
	}

	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S15 probe alert Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	agentConfigPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), agentConfigPath); err != nil {
		t.Fatalf("real Agent enrollment failed: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	stopAgent = cancel
	agentDone = make(chan error, 1)
	go func() { agentDone <- agent.Run(runCtx, agentConfigPath, "s15-real-agent-probe-alert", nil) }()
	waitForAlertProbeCondition(t, "enrolled Agent online with service-probe capability", 10*time.Second, func() (bool, error) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.NodeID == enrollment.NodeID {
				return node.Status == "online" && hasCapability(node.Capabilities, protocol.CapabilityProbes), nil
			}
		}
		return false, nil
	})

	channelResponse := adminJSON(http.MethodPost, "/api/v1/alerts/channels", corealerts.ChannelInput{
		Name: "S15 local probe webhook", Kind: corealerts.ChannelWebhook, Enabled: true,
		Config: corealerts.ChannelConfig{WebhookURL: fixtureWebhookURL(t, webhookNotifications), MessageTemplate: "{{event}}|{{ruleName}}|{{nodeName}}|{{message}}"},
	})
	if channelResponse.Code != http.StatusOK {
		t.Fatalf("create local webhook channel status=%d body=%s", channelResponse.Code, channelResponse.Body.String())
	}
	var channelReply struct {
		Channel corealerts.Channel `json:"channel"`
	}
	if err := json.Unmarshal(channelResponse.Body.Bytes(), &channelReply); err != nil || channelReply.Channel.ID == "" {
		t.Fatalf("decode webhook channel response: channel=%+v err=%v", channelReply.Channel, err)
	}

	probeResponse := adminJSON(http.MethodPost, "/api/v1/probes", map[string]any{
		"nodeId": enrollment.NodeID, "name": "S15 controlled HTTP probe", "kind": "http",
		"target": fixture.URL + "/ready", "expectedHttpStatus": http.StatusNoContent,
		"intervalSeconds": 10, "timeoutSeconds": 2, "enabled": false,
	})
	if probeResponse.Code != http.StatusCreated {
		t.Fatalf("create disabled probe status=%d body=%s", probeResponse.Code, probeResponse.Body.String())
	}
	var probe coreprobes.Config
	if err := json.Unmarshal(probeResponse.Body.Bytes(), &probe); err != nil || probe.ID == "" {
		t.Fatalf("decode probe response: probe=%+v err=%v", probe, err)
	}
	ruleResponse := adminJSON(http.MethodPost, "/api/v1/alerts/rules", corealerts.RuleInput{
		Name: "Probe unavailable", Kind: corealerts.KindProbeState, NodeID: enrollment.NodeID,
		SubjectID: probe.ID, Severity: corealerts.SeverityCritical, ExpectedState: protocol.ProbeResultHealthy,
		DurationSeconds: 0, CooldownSeconds: 3600, ChannelIDs: []string{channelReply.Channel.ID}, Enabled: true,
	})
	if ruleResponse.Code != http.StatusOK {
		t.Fatalf("create probe-state alert rule status=%d body=%s", ruleResponse.Code, ruleResponse.Body.String())
	}
	var ruleReply struct {
		Rule corealerts.Rule `json:"rule"`
	}
	if err := json.Unmarshal(ruleResponse.Body.Bytes(), &ruleReply); err != nil || ruleReply.Rule.ID == "" {
		t.Fatalf("decode probe-state rule response: rule=%+v err=%v", ruleReply.Rule, err)
	}
	enableResponse := adminJSON(http.MethodPut, "/api/v1/probes/"+probe.ID, map[string]any{
		"nodeId": enrollment.NodeID, "name": probe.Name, "kind": probe.Kind, "target": probe.Target,
		"expectedHttpStatus": http.StatusNoContent, "intervalSeconds": probe.IntervalSeconds,
		"timeoutSeconds": probe.TimeoutSeconds, "enabled": true, "revision": probe.Revision,
	})
	if enableResponse.Code != http.StatusOK {
		t.Fatalf("enable probe after rule creation status=%d body=%s", enableResponse.Code, enableResponse.Body.String())
	}

	waitForCompleted := func(count int) {
		t.Helper()
		waitForAlertProbeCondition(t, "persisted completed probe run", 10*time.Second, func() (bool, error) {
			runs, err := core.probes.History(context.Background(), probe.ID, 20)
			completed := 0
			for _, run := range runs {
				if run.Status != "pending" {
					completed++
				}
			}
			return completed >= count, err
		})
	}
	setProbeDueIn := func(delay time.Duration) {
		t.Helper()
		_, err := core.store.DB.ExecContext(context.Background(), `UPDATE service_probes SET next_due_at=? WHERE id=?`, time.Now().Add(delay).UTC().UnixNano(), probe.ID)
		if err != nil {
			t.Fatalf("set next probe due time: %v", err)
		}
	}
	waitForProbeFailures := func(count int) {
		t.Helper()
		waitForAlertProbeCondition(t, "probe failure counter", 5*time.Second, func() (bool, error) {
			state, err := core.probes.Get(context.Background(), probe.ID)
			return err == nil && state.ConsecutiveFailures == count, err
		})
	}
	waitForProbeSuccesses := func(count int) {
		t.Helper()
		waitForAlertProbeCondition(t, "probe success counter", 5*time.Second, func() (bool, error) {
			state, err := core.probes.Get(context.Background(), probe.ID)
			return err == nil && state.ConsecutiveSuccesses == count, err
		})
	}
	for run := 1; run <= 3; run++ {
		if run > 1 {
			setProbeDueIn(0)
		}
		waitForCompleted(run)
		waitForProbeFailures(run)
	}
	state, err := core.probes.Get(context.Background(), probe.ID)
	if err != nil || state.Status != protocol.ProbeResultUnhealthy {
		t.Fatalf("three Agent-reported failures did not persist unhealthy state: state=%+v err=%v", state, err)
	}
	leaseState, lease, err := core.currentMetricsState(context.Background(), enrollment.NodeID)
	if err != nil || leaseState.Status != "online" || lease == nil {
		t.Fatalf("Agent lease expired before S15 alert evaluation: state=%+v lease=%+v err=%v", leaseState, lease, err)
	}
	persistedRule, err := core.alerts.GetRule(context.Background(), ruleReply.Rule.ID)
	if err != nil || persistedRule.Kind != corealerts.KindProbeState || persistedRule.NodeID != enrollment.NodeID || persistedRule.SubjectID != probe.ID || persistedRule.ExpectedState != protocol.ProbeResultHealthy || persistedRule.DurationSeconds != 0 {
		t.Fatalf("probe alert rule scope/condition mismatch: rule=%+v err=%v", persistedRule, err)
	}
	setProbeDueIn(24 * time.Hour)
	backgroundFired := false
	backgroundDeadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(backgroundDeadline) {
		found, findErr := findActiveProbeAlert(core, ruleReply.Rule.ID)
		if findErr != nil {
			t.Fatal(findErr)
		}
		if found != nil {
			backgroundFired = true
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	nodesAtEval, err := core.agents.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var nodeStatusAtEval string
	for _, node := range nodesAtEval {
		if node.NodeID == enrollment.NodeID {
			nodeStatusAtEval = node.Status
		}
	}
	probesAtEval, err := core.probes.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var probeAtEval *coreprobes.Config
	for i := range probesAtEval {
		if probesAtEval[i].ID == probe.ID {
			probeAtEval = &probesAtEval[i]
		}
	}
	rulesAtEval, err := core.alerts.ListRules(context.Background(), enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var ruleAtEval *corealerts.Rule
	for i := range rulesAtEval {
		if rulesAtEval[i].ID == ruleReply.Rule.ID {
			ruleAtEval = &rulesAtEval[i]
		}
	}
	leaseStateAtEval, leaseAtEval, err := core.currentMetricsState(context.Background(), enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var conditionSince, previousSampleAt int64
	stateErr := core.store.DB.QueryRowContext(context.Background(), `SELECT condition_since,last_sample_at FROM alert_rule_state WHERE rule_id=? AND subject_id=?`, ruleReply.Rule.ID, probe.ID).Scan(&conditionSince, &previousSampleAt)
	activeBefore, err := findActiveProbeAlert(core, ruleReply.Rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	probeCheckedAt, parseErr := time.Parse(time.RFC3339Nano, state.LastCheckedAt)
	if parseErr != nil {
		t.Fatalf("parse persisted probe observation time %q: %v", state.LastCheckedAt, parseErr)
	}
	// Run exactly the same Core evaluator once with the real persisted sample,
	// then inspect its durable state. The background scheduler has already had
	// several ticks by this point; this distinguishes sample/rule defects from
	// a runtime scheduling failure.
	evaluatorErr := core.evaluateAlerts(context.Background())
	var conditionSinceAfter, previousSampleAtAfter int64
	stateErrAfter := core.store.DB.QueryRowContext(context.Background(), `SELECT condition_since,last_sample_at FROM alert_rule_state WHERE rule_id=? AND subject_id=?`, ruleReply.Rule.ID, probe.ID).Scan(&conditionSinceAfter, &previousSampleAtAfter)
	activeAfter, listErr := findActiveProbeAlert(core, ruleReply.Rule.ID)
	if listErr != nil {
		t.Fatal(listErr)
	}
	leaseValidUntilAtEval := "unavailable"
	if leaseAtEval != nil {
		leaseValidUntilAtEval = leaseAtEval.ValidUntil.UTC().Format(time.RFC3339Nano)
	}
	t.Logf("S15 evaluator diagnostic: background_fired=%t probe=%+v checked_at=%s current_node_status=%q current_lease_state=%q current_lease_valid_until=%s probe_list_entry=%+v rule_get_enabled=%t rule_list_entry=%+v rule_state_before=(since=%d last=%d err=%v) active_before=%t direct_eval_err=%v rule_state_after=(since=%d last=%d err=%v) active_after=%t",
		backgroundFired, state, probeCheckedAt.UTC().Format(time.RFC3339Nano), nodeStatusAtEval, leaseStateAtEval.Status, leaseValidUntilAtEval, probeAtEval, persistedRule.Enabled, ruleAtEval,
		conditionSince, previousSampleAt, stateErr, activeBefore != nil, evaluatorErr, conditionSinceAfter, previousSampleAtAfter, stateErrAfter, activeAfter != nil)
	if nodeStatusAtEval != "online" || leaseStateAtEval.Status != "online" || leaseAtEval == nil {
		t.Fatalf("node was not online at direct alert evaluation: node_status=%q lease_state=%q lease=%+v", nodeStatusAtEval, leaseStateAtEval.Status, leaseAtEval)
	}
	if probeAtEval == nil || !probeAtEval.Enabled || probeAtEval.NodeID != enrollment.NodeID || probeAtEval.ID != probe.ID || probeAtEval.Status != protocol.ProbeResultUnhealthy {
		t.Fatalf("probe list omitted or changed the real unhealthy probe: entry=%+v", probeAtEval)
	}
	if ruleAtEval == nil || !ruleAtEval.Enabled || ruleAtEval.NodeID != enrollment.NodeID || ruleAtEval.SubjectID != probe.ID || ruleAtEval.Kind != corealerts.KindProbeState || ruleAtEval.ExpectedState != protocol.ProbeResultHealthy {
		t.Fatalf("alert rule list omitted or changed the enabled probe rule: entry=%+v", ruleAtEval)
	}
	if evaluatorErr != nil {
		t.Fatalf("direct Core alert evaluation failed for persisted unhealthy probe: %v", evaluatorErr)
	}
	if activeAfter == nil {
		t.Fatalf("Core alert evaluator did not create a probe alert after direct evaluation; background_fired=%t probe=%+v online=%t lease_valid_until=%s rule=%+v rule_state_before=(since=%d last=%d err=%v) rule_state_after=(since=%d last=%d err=%v)",
			backgroundFired, state, leaseState.Status == "online", lease.ValidUntil.UTC().Format(time.RFC3339Nano), persistedRule, conditionSince, previousSampleAt, stateErr, conditionSinceAfter, previousSampleAtAfter, stateErrAfter)
	}
	activeAlert := *activeAfter
	initialEvents, err := core.alerts.ListEvents(context.Background(), activeAlert.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(initialEvents) != 1 || initialEvents[0].Kind != "firing" {
		t.Fatalf("new real probe alert must have exactly one firing event, got %+v", initialEvents)
	}
	firingNotice := receiveProbeWebhook(t, webhookNotifications, "firing")
	if firingNotice.AlertID != activeAlert.ID || firingNotice.RuleID != ruleReply.Rule.ID || firingNotice.SubjectID != probe.ID || firingNotice.NodeID != enrollment.NodeID || !strings.HasPrefix(firingNotice.Message, "firing|Probe unavailable|S15 probe alert Agent|") {
		t.Fatalf("unexpected firing webhook notification: %+v", firingNotice)
	}
	waitForAlertProbeCondition(t, "firing webhook outbox delivery recorded", 5*time.Second, func() (bool, error) {
		return probeAlertDeliverySent(core, activeAlert.ID, "firing"), nil
	})

	fixtureStatus.Store(http.StatusNoContent)
	for run := 4; run <= 5; run++ {
		setProbeDueIn(0)
		waitForCompleted(run)
		waitForProbeSuccesses(run - 3)
	}
	state, err = core.probes.Get(context.Background(), probe.ID)
	if err != nil || state.Status != protocol.ProbeResultHealthy {
		t.Fatalf("two Agent-reported successes did not persist healthy state: state=%+v err=%v", state, err)
	}
	setProbeDueIn(24 * time.Hour)
	waitForAlertProbeCondition(t, "probe alert recovery persisted", 12*time.Second, func() (bool, error) {
		active, err := core.alerts.ListAlerts(context.Background(), true, 50)
		if err != nil {
			return false, err
		}
		for _, alert := range active {
			if alert.RuleID == ruleReply.Rule.ID {
				return false, nil
			}
		}
		resolved, err := core.alerts.ListAlerts(context.Background(), false, 50)
		if err != nil {
			return false, err
		}
		for _, alert := range resolved {
			if alert.ID == activeAlert.ID && alert.Status == "resolved" && alert.ResolvedAt != nil {
				return true, nil
			}
		}
		return false, nil
	})
	recoveryNotice := receiveProbeWebhook(t, webhookNotifications, "recovered")
	if recoveryNotice.AlertID != activeAlert.ID || recoveryNotice.RuleID != ruleReply.Rule.ID || recoveryNotice.SubjectID != probe.ID || recoveryNotice.NodeID != enrollment.NodeID || !strings.HasPrefix(recoveryNotice.Message, "recovered|Probe unavailable|S15 probe alert Agent|") {
		t.Fatalf("unexpected recovery webhook notification: %+v", recoveryNotice)
	}
	waitForAlertProbeCondition(t, "recovery webhook outbox delivery recorded", 5*time.Second, func() (bool, error) {
		return probeAlertDeliverySent(core, activeAlert.ID, "recovered"), nil
	})

	events, err := core.alerts.ListEvents(context.Background(), activeAlert.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	eventCounts := map[string]int{}
	for _, event := range events {
		eventCounts[event.Kind]++
	}
	if len(events) != 2 || eventCounts["firing"] != 1 || eventCounts["recovered"] != 1 {
		t.Fatalf("probe alert history should contain exactly one firing and one recovery event, got %+v", events)
	}
	completed, err := core.probes.History(context.Background(), probe.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	statusCounts := map[string]int{}
	for _, run := range completed {
		if run.Status == "pending" {
			continue
		}
		statusCounts[run.Status]++
		if run.HTTPStatus == nil {
			t.Fatalf("real Agent probe result omitted HTTP status: %+v", run)
		}
		switch run.Status {
		case protocol.ProbeResultUnhealthy:
			if *run.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("unhealthy persisted result has HTTP status %d", *run.HTTPStatus)
			}
		case protocol.ProbeResultHealthy:
			if *run.HTTPStatus != http.StatusNoContent {
				t.Fatalf("healthy persisted result has HTTP status %d", *run.HTTPStatus)
			}
		default:
			t.Fatalf("unexpected completed probe result: %+v", run)
		}
	}
	if len(completed) != 5 || statusCounts[protocol.ProbeResultUnhealthy] != 3 || statusCounts[protocol.ProbeResultHealthy] != 2 || fixtureCalls.Load() != 5 {
		t.Fatalf("real Agent HTTP fixture history mismatch: counts=%v calls=%d history=%+v", statusCounts, fixtureCalls.Load(), completed)
	}
	if extra, ok := tryReceiveProbeWebhook(webhookNotifications); ok {
		t.Fatalf("unexpected duplicate probe alert webhook notification: %+v", extra)
	}
	t.Logf("S15 real-Agent probe alert PASS: HTTP results [503,503,503,204,204] created one firing and one recovered alert/event, with successful webhook deliveries; node=%s probe=%s", enrollment.NodeID, probe.ID)
}

func fixtureWebhookURL(t *testing.T, notifications chan<- corealerts.Notification) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.Method != http.MethodPost || r.URL.Path != "/hooks" {
			http.NotFound(w, r)
			return
		}
		var notification corealerts.Notification
		if err := json.NewDecoder(r.Body).Decode(&notification); err != nil {
			http.Error(w, "invalid notification", http.StatusBadRequest)
			return
		}
		select {
		case notifications <- notification:
		default:
			http.Error(w, "unexpected extra notification", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/hooks"
}

func waitForAlertProbeCondition(t *testing.T, description string, timeout time.Duration, check func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := check()
		if err != nil {
			t.Fatalf("%s: %v", description, err)
		}
		if ok {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func findActiveProbeAlert(core *Server, ruleID string) (*corealerts.Alert, error) {
	active, err := core.alerts.ListAlerts(context.Background(), true, 50)
	if err != nil {
		return nil, err
	}
	for i := range active {
		if active[i].RuleID == ruleID {
			return &active[i], nil
		}
	}
	return nil, nil
}

func receiveProbeWebhook(t *testing.T, notifications <-chan corealerts.Notification, event string) corealerts.Notification {
	t.Helper()
	select {
	case notification := <-notifications:
		if notification.Event != event {
			t.Fatalf("webhook event=%q, want %q; notification=%+v", notification.Event, event, notification)
		}
		return notification
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for %q webhook notification", event)
		return corealerts.Notification{}
	}
}

func tryReceiveProbeWebhook(notifications <-chan corealerts.Notification) (corealerts.Notification, bool) {
	select {
	case notification := <-notifications:
		return notification, true
	default:
		return corealerts.Notification{}, false
	}
}

func probeAlertDeliverySent(core *Server, alertID, event string) bool {
	rows, err := core.store.DB.QueryContext(context.Background(), `SELECT status,http_status,attempts,payload_json FROM alert_deliveries WHERE alert_id=? ORDER BY created_at`, alertID)
	if err != nil {
		return false
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var status string
		var httpStatus sql.NullInt64
		var attempts int
		var payload string
		if rows.Scan(&status, &httpStatus, &attempts, &payload) != nil {
			return false
		}
		var notification corealerts.Notification
		if json.Unmarshal([]byte(payload), &notification) != nil || notification.Event != event {
			continue
		}
		if status != "sent" || !httpStatus.Valid || int(httpStatus.Int64) != http.StatusNoContent || attempts != 1 {
			return false
		}
		found = true
	}
	return found && rows.Err() == nil
}
