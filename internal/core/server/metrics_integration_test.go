package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/metrics"
	"github.com/coder/websocket"
)

// This test exercises the production Agent sampler, authenticated Core WSS
// path, private node metrics API, dashboard push and browser-independent
// collection over real Linux host metrics.
func TestRealAgentMetricsAPIAndDashboardStream(t *testing.T) {
	workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s03")
	if err := os.MkdirAll(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(workRoot, "metrics-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	work, err = filepath.Abs(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("metrics-e2e", Options{
		DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test",
		Development: true, AgentOfflineTimeout: 8 * time.Second, AgentSweepInterval: 250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		coreHTTP.Close()
		_ = core.Close()
	})

	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	adminClient := tlsHTTPClient(rootPEM)
	adminRequestMethod := func(method, path string, authenticated bool) (*http.Response, error) {
		request, err := http.NewRequest(method, coreHTTP.URL+path, nil)
		if err != nil {
			return nil, err
		}
		if authenticated {
			request.Header.Set("Origin", "https://panel.test")
			request.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
			if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
				request.Header.Set(csrfHeaderName, csrf)
			}
		}
		return adminClient.Do(request)
	}
	adminRequest := func(path string, authenticated bool) (*http.Response, error) {
		return adminRequestMethod(http.MethodGet, path, authenticated)
	}

	enrollment, err := core.agents.CreateEnrollment(context.Background(), "live-metrics-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	pendingMetricsResponse, err := adminRequest("/api/v1/nodes/"+enrollment.NodeID+"/metrics", true)
	if err != nil {
		t.Fatal(err)
	}
	var pendingMetrics dashboardMetricsMessage
	if pendingMetricsResponse.StatusCode != http.StatusOK {
		_ = pendingMetricsResponse.Body.Close()
		t.Fatalf("metrics API treated a pending enrolled node as missing: HTTP %d", pendingMetricsResponse.StatusCode)
	}
	if err := json.NewDecoder(pendingMetricsResponse.Body).Decode(&pendingMetrics); err != nil {
		_ = pendingMetricsResponse.Body.Close()
		t.Fatal("decode pending node metrics state:", err)
	}
	_ = pendingMetricsResponse.Body.Close()
	if pendingMetrics.Type != "node_status" || pendingMetrics.State == nil || pendingMetrics.State.Status != "pending" || pendingMetrics.State.Reason != "awaiting_agent_registration" {
		t.Fatalf("pending node metrics response did not provide an explicit wait reason: %+v", pendingMetrics)
	}
	missingMetricsResponse, err := adminRequest("/api/v1/nodes/00000000-0000-4000-8000-000000000000/metrics", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = missingMetricsResponse.Body.Close()
	if missingMetricsResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics API returned HTTP %d for a nonexistent node, want 404", missingMetricsResponse.StatusCode)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
		t.Fatalf("real Agent enrollment failed: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	nodeListUnauthenticated, err := adminRequest("/api/v1/nodes", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = nodeListUnauthenticated.Body.Close()
	if nodeListUnauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("node list API bypassed session authentication: HTTP %d", nodeListUnauthenticated.StatusCode)
	}
	wrongNodeListMethod, err := adminRequestMethod(http.MethodPost, "/api/v1/nodes", true)
	if err != nil {
		t.Fatal(err)
	}
	_ = wrongNodeListMethod.Body.Close()
	if wrongNodeListMethod.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("node list API accepted POST: HTTP %d, want 405", wrongNodeListMethod.StatusCode)
	}
	var nodeList struct {
		Nodes []struct {
			NodeID          string `json:"nodeId"`
			Status          string `json:"status"`
			Generation      uint64 `json:"generation"`
			LeaseValidUntil string `json:"leaseValidUntil"`
		} `json:"nodes"`
		ServerTime string `json:"serverTime"`
	}
	readNodeList := func() ([]struct {
		NodeID          string `json:"nodeId"`
		Status          string `json:"status"`
		Generation      uint64 `json:"generation"`
		LeaseValidUntil string `json:"leaseValidUntil"`
	}, error) {
		response, err := adminRequest("/api/v1/nodes", true)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("node list returned HTTP %d", response.StatusCode)
		}
		if err := json.NewDecoder(response.Body).Decode(&nodeList); err != nil {
			return nil, err
		}
		return nodeList.Nodes, nil
	}
	preConnectionNodes, err := readNodeList()
	if err != nil {
		t.Fatal(err)
	}
	if len(preConnectionNodes) != 1 || preConnectionNodes[0].NodeID != config.NodeID || preConnectionNodes[0].Status != "offline" {
		t.Fatalf("node list did not show the enrolled Agent offline before its lease: %+v", preConnectionNodes)
	}
	runCtx, stopAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	agentJoined := false
	startedAt := time.Now()
	go func() { agentDone <- agent.Run(runCtx, configPath, "metrics-e2e", nil) }()
	t.Cleanup(func() {
		if agentJoined {
			return
		}
		stopAgent()
		select {
		case <-agentDone:
			agentJoined = true
		case <-time.After(7 * time.Second):
			t.Error("real Agent did not stop and join its collector/socket writer")
		}
	})

	unauthenticated, err := adminRequest("/api/v1/nodes/"+config.NodeID+"/metrics", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("node metrics API bypassed session authentication: HTTP %d", unauthenticated.StatusCode)
	}

	var view metrics.View
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		response, err := adminRequest("/api/v1/nodes/"+config.NodeID+"/metrics", true)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			t.Fatalf("node metrics API returned HTTP %d", response.StatusCode)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&view)
		_ = response.Body.Close()
		if decodeErr != nil {
			t.Fatal("decode real metrics API response:", decodeErr)
		}
		if view.Sequence >= 2 && view.NodeStatus == "online" && view.Metrics.CPU.UsagePercent.Value != nil && view.Metrics.Memory.Value != nil && view.Metrics.Uptime.Value != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view.Sequence < 2 || view.NodeStatus != "online" || view.Metrics.CPU.UsagePercent.Value == nil || view.Metrics.Memory.Value == nil || view.Metrics.Uptime.Value == nil {
		t.Fatalf("Core did not receive two real host samples: seq=%d status=%s cpu=%+v memory=%+v uptime=%+v",
			view.Sequence, view.NodeStatus, view.Metrics.CPU.UsagePercent, view.Metrics.Memory, view.Metrics.Uptime)
	}
	activeNodes, err := readNodeList()
	if err != nil {
		t.Fatal(err)
	}
	if len(activeNodes) != 1 || activeNodes[0].NodeID != config.NodeID || activeNodes[0].Status != "online" || activeNodes[0].Generation != view.ActiveGeneration || activeNodes[0].LeaseValidUntil == "" {
		t.Fatalf("node list did not expose the Agent's active online lease: %+v metrics=%+v", activeNodes, view)
	}
	ttfb := time.Since(startedAt)
	if ttfb > 8*time.Second {
		t.Fatalf("real sample reached the authenticated API in %s, over the 8s S03 target", ttfb)
	}
	t.Logf("real Agent→Core sample latency=%s cpu=%.2f%% memory=%d/%d bytes boot_id=%s", ttfb,
		*view.Metrics.CPU.UsagePercent.Value, view.Metrics.Memory.Value.UsedBytes, view.Metrics.Memory.Value.TotalBytes, view.BootID)

	browserURL := strings.Replace(coreHTTP.URL, "https://", "wss://", 1) + "/ws/v1/dashboard"
	browser, response, err := websocket.Dial(context.Background(), browserURL, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(rootPEM),
		HTTPHeader: http.Header{"Origin": []string{"https://panel.test"}, "Cookie": []string{sessionCookieName + "=" + session}},
	})
	if err != nil {
		status := ""
		if response != nil {
			status = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		t.Fatalf("authenticated dashboard stream failed (%s): %v", status, err)
	}
	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, raw, err := browser.Read(readCtx)
	cancelRead()
	_ = browser.Close(websocket.StatusNormalClosure, "initial metrics received")
	if err != nil {
		t.Fatal("dashboard did not push the latest metrics view:", err)
	}
	var pushed struct {
		Type    string       `json:"type"`
		NodeID  string       `json:"nodeId"`
		Metrics metrics.View `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &pushed); err != nil || pushed.Type != "node_metrics" || pushed.NodeID != config.NodeID || pushed.Metrics.Sequence == 0 {
		t.Fatalf("dashboard did not push this Agent's real metrics: event=%s err=%v", raw, err)
	}

	before := view.Sequence
	deadline = time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		response, err := adminRequest("/api/v1/nodes/"+config.NodeID+"/metrics", true)
		if err != nil {
			t.Fatal(err)
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&view)
		_ = response.Body.Close()
		if decodeErr == nil && view.Sequence > before {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if view.Sequence <= before {
		t.Fatal("Agent stopped collecting or reporting after the dashboard browser closed")
	}
	stopAgent()
	select {
	case err := <-agentDone:
		agentJoined = true
		if err != nil {
			t.Fatalf("Agent returned an error during clean shutdown: %v", err)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("Agent shutdown did not join periodic collector and socket writer")
	}
	offlineDeadline := time.Now().Add(10 * time.Second)
	offlineObserved := false
	for time.Now().Before(offlineDeadline) {
		offlineNodes, err := readNodeList()
		if err != nil {
			t.Fatal(err)
		}
		if len(offlineNodes) == 1 && offlineNodes[0].NodeID == config.NodeID && offlineNodes[0].Status == "offline" {
			t.Logf("node list showed Agent offline after lease expiry at generation=%d", offlineNodes[0].Generation)
			offlineObserved = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !offlineObserved {
		t.Fatal("node list kept the disconnected Agent online beyond the eight-second lease")
	}

	// Keep the stream busy while the browser deliberately does not read. A
	// session revoked during a pending push must still close within the bounded
	// write timeout plus the next authorization check.
	slowBrowser, _, err := websocket.Dial(context.Background(), browserURL, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(rootPEM),
		HTTPHeader: http.Header{"Origin": []string{"https://panel.test"}, "Cookie": []string{sessionCookieName + "=" + session}},
	})
	if err != nil {
		t.Fatalf("open slow dashboard stream: %v", err)
	}
	defer slowBrowser.CloseNow()
	pushCtx, stopPush := context.WithCancel(context.Background())
	defer stopPush()
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pushCtx.Done():
				return
			case <-ticker.C:
				core.metrics.Notify(config.NodeID)
			}
		}
	}()
	time.Sleep(100 * time.Millisecond)
	logoutRequest, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutRequest.Header.Set("Origin", "https://panel.test")
	logoutRequest.Header.Set(csrfHeaderName, csrf)
	logoutRequest.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
	logoutResponse, err := adminClient.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("real browser logout returned HTTP %d", logoutResponse.StatusCode)
	}
	closedAt := time.Now()
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 15*time.Second)
	var closeErr error
	for {
		_, _, closeErr = slowBrowser.Read(closeCtx)
		if closeErr != nil {
			break
		}
	}
	cancelClose()
	stopPush()
	if closeErr == nil || errors.Is(closeErr, context.DeadlineExceeded) || websocket.CloseStatus(closeErr) != websocket.StatusPolicyViolation {
		t.Fatalf("dashboard session did not close promptly with policy violation after logout: %v", closeErr)
	}
	if elapsed := time.Since(closedAt); elapsed > 15*time.Second {
		t.Fatalf("dashboard logout took %s to close its slow browser stream", elapsed)
	}
}

func TestTelemetryPollingAndDashboardPushDoNotExtendIdleSession(t *testing.T) {
	const idleTimeout = 900 * time.Millisecond
	core, err := New("metrics-session-expiry", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true,
		PublicOrigin: "http://panel.test", SessionIdleTimeout: idleTimeout,
		WebSocketCheckInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	rawSession, _ := seedAdminSession(t, core, "ignored-test-password")
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "session-expiry-node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	readLastSeen := func() (int64, error) {
		var lastSeen int64
		err := core.store.DB.QueryRow(`SELECT last_seen_at FROM browser_sessions WHERE token_digest=?`, auth.DigestToken(rawSession)).Scan(&lastSeen)
		return lastSeen, err
	}
	// A user-facing read still counts as activity. The following dashboard
	// telemetry polls must leave this new activity timestamp unchanged.
	beforeTouch, err := readLastSeen()
	if err != nil {
		t.Fatal(err)
	}
	userRead := httptest.NewRequest(http.MethodGet, "http://panel.test/api/v1/auth/me", nil)
	userRead.AddCookie(&http.Cookie{Name: sessionCookieName, Value: rawSession})
	userReadResponse := httptest.NewRecorder()
	core.ServeHTTP(userReadResponse, userRead)
	if userReadResponse.Code != http.StatusOK {
		t.Fatalf("explicit authenticated read returned HTTP %d: %s", userReadResponse.Code, userReadResponse.Body.String())
	}
	lastSeen, err := readLastSeen()
	if err != nil {
		t.Fatal(err)
	}
	if lastSeen <= beforeTouch {
		t.Fatalf("explicit authenticated read did not touch the session: before=%d after=%d", beforeTouch, lastSeen)
	}
	lastSeenAtStart := lastSeen

	webServer := httptest.NewServer(core)
	defer webServer.Close()
	client := &http.Client{Timeout: time.Second}
	dashboardURL := "ws" + strings.TrimPrefix(webServer.URL, "http") + "/ws/v1/dashboard"
	wsHeader := http.Header{
		"Origin": {"http://panel.test"},
		"Cookie": {sessionCookieName + "=" + rawSession},
	}
	connectCtx, cancelConnect := context.WithTimeout(context.Background(), 2*time.Second)
	dashboard, _, err := websocket.Dial(connectCtx, dashboardURL, &websocket.DialOptions{HTTPHeader: wsHeader})
	cancelConnect()
	if err != nil {
		t.Fatalf("open authenticated dashboard stream: %v", err)
	}
	defer dashboard.CloseNow()
	initialCtx, cancelInitial := context.WithTimeout(context.Background(), time.Second)
	_, initial, err := dashboard.Read(initialCtx)
	cancelInitial()
	if err != nil {
		t.Fatalf("read initial dashboard node status: %v", err)
	}
	var initialMessage dashboardMetricsMessage
	if err := json.Unmarshal(initial, &initialMessage); err != nil || initialMessage.Type != "node_status" || initialMessage.NodeID != enrollment.NodeID {
		t.Fatalf("unexpected initial dashboard message %s (decode err %v)", initial, err)
	}

	readCtx, cancelRead := context.WithTimeout(context.Background(), idleTimeout+2*time.Second)
	defer cancelRead()
	var pushed atomic.Int64
	wsClosed := make(chan error, 1)
	go func() {
		for {
			_, _, readErr := dashboard.Read(readCtx)
			if readErr != nil {
				wsClosed <- readErr
				return
			}
			pushed.Add(1)
		}
	}()

	pushCtx, stopPush := context.WithCancel(context.Background())
	defer stopPush()
	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pushCtx.Done():
				return
			case <-ticker.C:
				core.metrics.Notify(enrollment.NodeID)
			}
		}
	}()

	poll := func(path string) (int, error) {
		request, err := http.NewRequest(http.MethodGet, webServer.URL+path, nil)
		if err != nil {
			return 0, err
		}
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: rawSession})
		response, err := client.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		return response.StatusCode, nil
	}

	started := time.Now()
	deadline := started.Add(idleTimeout + 2*time.Second)
	successfulPolls := 0
	unauthorizedAt := time.Time{}
	for time.Now().Before(deadline) {
		for _, path := range []string{
			"/api/v1/nodes",
			"/api/v1/nodes/" + enrollment.NodeID + "/metrics",
		} {
			status, err := poll(path)
			if err != nil {
				t.Fatal("continuous telemetry poll:", err)
			}
			if status == http.StatusUnauthorized {
				unauthorizedAt = time.Now()
				break
			}
			if status != http.StatusOK {
				t.Fatalf("telemetry poll %s returned HTTP %d", path, status)
			}
			successfulPolls++
		}
		if !unauthorizedAt.IsZero() {
			break
		}
		if successfulPolls >= 4 {
			observed, err := readLastSeen()
			if err != nil {
				t.Fatal(err)
			}
			if observed != lastSeenAtStart {
				t.Fatalf("background telemetry polling extended the session before expiry: started=%d observed=%d", lastSeenAtStart, observed)
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if unauthorizedAt.IsZero() {
		t.Fatalf("continuous telemetry polling kept the session alive beyond %s", idleTimeout)
	}
	if successfulPolls < 4 {
		t.Fatalf("only %d telemetry reads succeeded before session expiry", successfulPolls)
	}

	select {
	case closeErr := <-wsClosed:
		if websocket.CloseStatus(closeErr) != websocket.StatusPolicyViolation {
			t.Fatalf("dashboard stream closed with %v, want policy violation after session expiry", closeErr)
		}
		if closedAfter := time.Since(unauthorizedAt); closedAfter > time.Second {
			t.Fatalf("dashboard stream took %s to close after telemetry returned 401", closedAfter)
		}
	case <-time.After(time.Second):
		t.Fatal("dashboard stream stayed open after the polled session expired")
	}
	stopPush()
	if pushed.Load() == 0 {
		t.Fatal("dashboard stream did not receive any live status pushes during telemetry polling")
	}
	if elapsed := time.Since(started); elapsed > idleTimeout+time.Second {
		t.Fatalf("session expired after %s despite continuous telemetry polling", elapsed)
	}
}
