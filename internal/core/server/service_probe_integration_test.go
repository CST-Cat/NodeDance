package server

import (
	"bytes"
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
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestRealAgentServiceProbeLifecycle(t *testing.T) {
	work := t.TempDir()
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s14-real-agent", Options{
		DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", Development: true,
		// The Agent heartbeat remains at its production cadence. A shorter
		// development lease makes this lifecycle test finish without mocking
		// Core's lease watcher or offline sweep.
		AgentOfflineTimeout: 8 * time.Second, AgentSweepInterval: 250 * time.Millisecond,
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
			case err := <-agentDone:
				if err != nil {
					t.Errorf("real Agent returned an error while stopping: %v", err)
				}
			case <-time.After(7 * time.Second):
				t.Error("real Agent did not stop")
			}
		}
		coreHTTP.Close()
		_ = core.Close()
	})

	var fixtureCalls atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		call := fixtureCalls.Add(1)
		if call <= 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fixture.Close()
	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S14 service probe Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	agentConfigPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), agentConfigPath); err != nil {
		t.Fatalf("real Agent enrollment failed: %v", err)
	}
	startAgent := func() {
		runCtx, cancel := context.WithCancel(context.Background())
		stopAgent = cancel
		agentDone = make(chan error, 1)
		go func(done chan<- error) { done <- agent.Run(runCtx, agentConfigPath, "s14-real-agent", nil) }(agentDone)
	}
	startAgent()

	waitFor := func(description string, timeout time.Duration, ready func() (bool, error)) {
		t.Helper()
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			ok, err := ready()
			if err != nil {
				t.Fatalf("%s: %v", description, err)
			}
			if ok {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", description)
	}
	waitForAgentOnline := func() {
		t.Helper()
		waitFor("real enrolled Agent online with service-probe capability", 10*time.Second, func() (bool, error) {
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
	}
	waitForAgentOnline()

	payload, err := json.Marshal(map[string]any{"nodeId": enrollment.NodeID, "name": "Loopback lifecycle", "kind": "http",
		"target": fixture.URL + "/ready", "expectedHttpStatus": http.StatusNoContent, "intervalSeconds": 10, "timeoutSeconds": 2, "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/probes", bytes.NewReader(payload))
	request.Header.Set("Origin", "https://panel.test")
	request.Header.Set(csrfHeaderName, csrfToken)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	response := httptest.NewRecorder()
	core.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create probe status=%d body=%s", response.Code, response.Body.String())
	}
	var config coreprobes.Config
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}

	completedRuns := func() ([]coreprobes.Run, error) {
		runs, err := core.probes.History(context.Background(), config.ID, 20)
		if err != nil {
			return nil, err
		}
		completed := make([]coreprobes.Run, 0, len(runs))
		for _, run := range runs {
			if run.Status != "pending" {
				completed = append(completed, run)
			}
		}
		return completed, nil
	}
	waitForCompletedRuns := func(count int, timeout time.Duration) []coreprobes.Run {
		t.Helper()
		var observed []coreprobes.Run
		waitFor("probe run history to contain the expected number of completed runs", timeout, func() (bool, error) {
			runs, err := completedRuns()
			observed = runs
			return len(runs) >= count, err
		})
		return observed
	}
	chronological := func(runs []coreprobes.Run) []coreprobes.Run {
		ordered := append([]coreprobes.Run(nil), runs...)
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
		return ordered
	}
	assertState := func(wantStatus string, wantFailures, wantSuccesses int, wantError string) coreprobes.Config {
		t.Helper()
		current, err := core.probes.Get(context.Background(), config.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status != wantStatus || current.ConsecutiveFailures != wantFailures || current.ConsecutiveSuccesses != wantSuccesses || current.LastErrorCode != wantError {
			t.Fatalf("probe state=%+v, want status=%s failures=%d successes=%d error=%s", current, wantStatus, wantFailures, wantSuccesses, wantError)
		}
		return current
	}

	_ = waitForCompletedRuns(3, 30*time.Second)
	waitFor("third real Agent failure to transition Core probe to unhealthy", 2*time.Second, func() (bool, error) {
		current, err := core.probes.Get(context.Background(), config.ID)
		return err == nil && current.Status == protocol.ProbeResultUnhealthy && current.ConsecutiveFailures == 3, err
	})
	assertState(protocol.ProbeResultUnhealthy, 3, 0, "http_status_mismatch")
	initialRuns := waitForCompletedRuns(4, 15*time.Second)
	assertState(protocol.ProbeResultUnhealthy, 0, 1, "")
	initialRuns = waitForCompletedRuns(5, 15*time.Second)
	ordered := chronological(initialRuns)
	if len(ordered) != 5 {
		t.Fatalf("unexpected probe run count before disconnect: got %d runs=%+v", len(ordered), ordered)
	}
	wantStatuses := []string{protocol.ProbeResultUnhealthy, protocol.ProbeResultUnhealthy, protocol.ProbeResultUnhealthy, protocol.ProbeResultHealthy, protocol.ProbeResultHealthy}
	wantHTTPStatuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusNoContent, http.StatusNoContent}
	for i, run := range ordered {
		if run.Status != wantStatuses[i] || run.HTTPStatus == nil || *run.HTTPStatus != wantHTTPStatuses[i] {
			t.Fatalf("run %d=%+v, want status=%s HTTP=%d; full history=%+v", i+1, run, wantStatuses[i], wantHTTPStatuses[i], ordered)
		}
	}
	assertState(protocol.ProbeResultHealthy, 0, 2, "")
	if got := fixtureCalls.Load(); got != 5 {
		t.Fatalf("controlled HTTP fixture saw %d calls before disconnect, want 5", got)
	}
	t.Logf("candidate S14-04: Agent/Core loopback produced five real HTTP results [503,503,503,204,204], state unhealthy after third failure and healthy after second success; history=%+v", ordered)

	stopAgent()
	select {
	case err := <-agentDone:
		if err != nil {
			t.Fatalf("real Agent failed to stop for lease test: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("real Agent did not stop before lease-expiration test")
	}
	stopAgent = nil
	waitFor("Core lease expiry to mark the registered node offline", 12*time.Second, func() (bool, error) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.NodeID == enrollment.NodeID {
				return node.Status == "offline", nil
			}
		}
		return false, nil
	})
	waitFor("probe lease transition to unknown/node_offline", 2*time.Second, func() (bool, error) {
		current, err := core.probes.Get(context.Background(), config.ID)
		return err == nil && current.Status == protocol.ProbeResultUnknown && current.LastErrorCode == "node_offline", err
	})
	offlineState := assertState(protocol.ProbeResultUnknown, 0, 0, "node_offline")
	offlineHistory, err := core.probes.History(context.Background(), config.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	offlineTransitions := 0
	for _, run := range offlineHistory {
		if run.ErrorCode == "node_offline" {
			offlineTransitions++
		}
	}
	if offlineTransitions != 1 {
		t.Fatalf("offline transition history count=%d, want one; history=%+v", offlineTransitions, offlineHistory)
	}
	var pendingRuns int
	if err := core.store.DB.QueryRow(`SELECT count(*) FROM service_probe_runs WHERE probe_id=? AND status='pending'`, config.ID).Scan(&pendingRuns); err != nil {
		t.Fatal(err)
	}
	if pendingRuns != 0 {
		t.Fatalf("Agent lease expiry left %d pending probe runs", pendingRuns)
	}
	if got := fixtureCalls.Load(); got != 5 {
		t.Fatalf("offline Agent produced an extra HTTP probe: calls=%d, want 5", got)
	}
	t.Logf("candidate S14-05: real Agent lease expiry node=%s marked probe unknown with node_offline, reset failure/success counters, and left no pending runs; state=%+v history=%+v", enrollment.NodeID, offlineState, offlineHistory)

	startAgent()
	waitForAgentOnline()
	waitFor("first real probe success after Agent reconnection", 15*time.Second, func() (bool, error) {
		runs, err := completedRuns()
		if err != nil {
			return false, err
		}
		for _, run := range runs {
			if run.HTTPStatus != nil && *run.HTTPStatus == http.StatusNoContent && run.CheckedAt > offlineState.LastCheckedAt {
				return true, nil
			}
		}
		return false, nil
	})
	assertState(protocol.ProbeResultUnknown, 0, 1, "")
	waitFor("second real probe success after Agent reconnection to restore healthy", 12*time.Second, func() (bool, error) {
		current, err := core.probes.Get(context.Background(), config.ID)
		return err == nil && current.Status == protocol.ProbeResultHealthy && current.ConsecutiveSuccesses == 2, err
	})
	assertState(protocol.ProbeResultHealthy, 0, 2, "")
	if got := fixtureCalls.Load(); got != 7 {
		t.Fatalf("controlled HTTP fixture saw %d calls after recovery, want 7", got)
	}
	recoveredRuns, err := completedRuns()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("candidate S14-04/05 recovery: the same enrolled Agent reconnected and produced two fresh HTTP 204 results; final state healthy, calls=%d, history=%+v", fixtureCalls.Load(), chronological(recoveredRuns))
}
