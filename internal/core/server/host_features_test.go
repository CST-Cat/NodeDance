package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func featureCore(t *testing.T) *Server {
	t.Helper()
	s, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestBoundedSharedEnrollmentConcurrentAndRecoverable(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	g, err := s.agents.CreateGrant(ctx, agents.GrantRequest{DisplayPrefix: "fleet", MaxUses: 2, LifetimeSeconds: 60}, "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []agents.Identity{}
	credentials := []string{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cred := fmt.Sprintf("%064x", i+1)
			id, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(g.Token), auth.DigestToken(cred), fmt.Sprintf("request-%d", i), "test")
			if err == nil {
				mu.Lock()
				accepted = append(accepted, id)
				credentials = append(credentials, cred)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if len(accepted) != 2 || accepted[0].NodeID == accepted[1].NodeID {
		t.Fatalf("bounded identities %+v", accepted)
	}
	for i, cred := range credentials {
		recovered, err := s.agents.IdentityByCredential(ctx, auth.DigestToken(cred))
		if err != nil || recovered.AgentID != accepted[i].AgentID {
			t.Fatal("lost-response identity recovery", err)
		}
	}
	list, err := s.agents.ListGrants(ctx)
	if err != nil || len(list) != 1 || list[0].Uses != 2 || list[0].Token != "" {
		t.Fatal("grant listing leaks secret or loses usage", err)
	}
	g, err = s.agents.CreateGrant(ctx, agents.GrantRequest{DisplayPrefix: "revoked", MaxUses: 2, LifetimeSeconds: 60}, "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.agents.RevokeGrant(ctx, g.ID, "test", sql.NullInt64{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(g.Token), auth.DigestToken(strings.Repeat("a", 64)), "request-revoked", "test"); err != agents.ErrEnrollmentRejected {
		t.Fatal("revoked grant accepted", err)
	}
}
func TestHTTPAgentNegotiationAndPollReplay(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	enroll, err := s.agents.CreateEnrollment(ctx, "http-node", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	cred := strings.Repeat("b", 64)
	id, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(enroll.Token), auth.DigestToken(cred), "request-http", "test")
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := json.Marshal(protocol.Hello{AgentID: id.AgentID, NodeID: id.NodeID, AgentVersion: "test", Capabilities: []string{protocol.CapabilityHost}, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64", EffectiveUID: 0, EffectiveGID: 0, SupplementaryGroups: []int{}}})
	frame, _ := json.Marshal(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: hello})
	call := func(method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+cred)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		s.handleHTTPAgentChannel(w, r)
		return w
	}
	opened := call("POST", "/api/v1/agents/channel", frame, nil)
	if opened.Code != 201 {
		t.Fatal(opened.Code, opened.Body.String())
	}
	var session struct {
		ID string `json:"channelId"`
	}
	json.Unmarshal(opened.Body.Bytes(), &session)
	path := "/api/v1/agents/channel/" + session.ID
	first := call("GET", path, nil, nil)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var envelope protocol.Envelope
	if json.Unmarshal(first.Body.Bytes(), &envelope) != nil || envelope.Type != protocol.TypeWelcome {
		t.Fatal("missing welcome", first.Body.String())
	}
	replay := call("GET", path, nil, nil)
	if !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) || first.Header().Get("X-Channel-Sequence") != replay.Header().Get("X-Channel-Sequence") {
		t.Fatal("lost poll response cannot be replayed")
	}
	var welcome protocol.Welcome
	json.Unmarshal(envelope.Payload, &welcome)
	heartbeat, _ := json.Marshal(protocol.Heartbeat{Capabilities: []string{protocol.CapabilityHost}})
	frame, _ = json.Marshal(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat, Generation: welcome.Generation, Sequence: 1, Payload: heartbeat})
	for i := 0; i < 2; i++ {
		res := call("POST", path, frame, map[string]string{"X-Channel-Sequence": "1"})
		if res.Code != 204 {
			t.Fatal("replayed write", res.Code, res.Body.String())
		}
	}
	changed := append([]byte{}, frame...)
	changed = append(changed, ' ')
	if res := call("POST", path, changed, map[string]string{"X-Channel-Sequence": "1"}); res.Code != 409 {
		t.Fatal("same sequence accepted different body", res.Code)
	}
	if res := call("GET", path, nil, map[string]string{"Origin": "https://browser.example"}); res.Code != 403 {
		t.Fatal("browser channel accepted")
	}
	call("DELETE", path, nil, nil)
}
func TestAssetReminderUnknownTrafficAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(24 * time.Hour)
	a := defaultNodeAsset()
	a.ExpiresAt = &expiry
	a.TrafficLimitBytes = 1000
	samples := assetSamples("node", "name", a, nil, now)
	if !samples[0].Known || samples[0].State != "warning" || samples[1].Known {
		t.Fatal("offline traffic was treated as zero")
	}
	v := protocol.Traffic{PeriodReceivedBytes: 850, PeriodSentBytes: 100}
	h := &protocol.MetricsHost{Traffic: protocol.Metric[protocol.Traffic]{Status: protocol.MetricKnown, Value: &v, SampledAt: now, Reason: "partial_coverage"}}
	samples = assetSamples("node", "name", a, h, now)
	if !samples[1].Known || samples[1].State != "warning" || samples[1].Value == nil || *samples[1].Value != 95 || samples[1].Message == "" {
		t.Fatal("quota lower bound lost")
	}
	a.TrafficAccounting = "max"
	if assetSamples("node", "name", a, h, now)[1].State != "healthy" {
		t.Fatal("accounting policy ignored")
	}
}

func TestProbeQualitySeparatesOfflineGaps(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	e, err := s.agents.CreateEnrollment(ctx, "quality", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.probes.Create(ctx, coreprobes.Config{NodeID: e.NodeID, Name: "echo", Kind: protocol.ProbeICMP, Target: "icmp://127.0.0.1", IntervalSeconds: 30, TimeoutSeconds: 3, Enabled: true}, audit.Event{Action: "probe_create", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i, status := range []string{"healthy", "healthy", "unhealthy", "unknown"} {
		_, err := s.store.DB.ExecContext(ctx, `INSERT INTO service_probe_runs(run_id,probe_id,node_id,probe_revision,kind,status,checked_at,completed_at,latency_ms,timeout_seconds) VALUES(?,?,?,1,'icmp',?,?,?,?,3)`, fmt.Sprintf("run-%d", i), p.ID, e.NodeID, status, now.Add(-time.Minute).UnixNano(), now.UnixNano(), (i+1)*10)
		if err != nil {
			t.Fatal(err)
		}
	}
	q, err := s.probes.Quality(ctx, p.ID, now.Add(-time.Hour), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if q.Samples != 4 || q.Unknown != 1 || q.Failures != 1 || q.Successes != 2 || q.FailurePercent == nil || *q.FailurePercent < 33 || *q.FailurePercent > 34 || q.P95LatencyMS == nil || *q.P95LatencyMS != 20 {
		t.Fatalf("quality denominator %+v", q)
	}
}

func TestMultiNodeProbeBatchIsDurableAndIdempotent(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	if err := s.BootstrapAdministrator(ctx, "batch-admin", "strong-batch-password"); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 0; i < 2; i++ {
		e, err := s.agents.CreateEnrollment(ctx, fmt.Sprintf("node-%d", i), "test", sql.NullInt64{})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.NodeID)
	}
	input := protocol.BatchProbe{NodeIDs: ids, Name: "shared", Kind: "icmp", Target: "icmp://127.0.0.1", IntervalSeconds: 30, TimeoutSeconds: 3}
	body, _ := json.Marshal(input)
	key := "batch-probe-retry"
	var batch hostBatch
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/api/v1/probes/batch", bytes.NewReader(body))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		if !s.handleHostBatchAPI(w, r, &session{RemoteAddr: "127.0.0.1"}) || w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil {
			t.Fatal(err)
		}
		if len(batch.Results) != 2 || batch.Results[0].ProbeID == "" || batch.Results[1].ProbeID == "" {
			t.Fatal(batch)
		}
	}
	configs, err := s.probes.List(ctx)
	if err != nil || len(configs) != 2 {
		t.Fatal("retry created duplicate probes", err, len(configs))
	}
	input.Target = "icmp://localhost"
	body, _ = json.Marshal(input)
	r := httptest.NewRequest("POST", "/api/v1/probes/batch", bytes.NewReader(body))
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	s.handleHostBatchAPI(w, r, &session{RemoteAddr: "127.0.0.1"})
	if w.Code != 400 {
		t.Fatal("different request reused batch key", w.Code)
	}
}

func TestRootAgentHTTPHostControlEndToEnd(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("run this integration test binary as root")
	}
	for _, transport := range []string{"http", "auto"} {
		t.Run(transport, func(t *testing.T) {
			s := featureCore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			e, err := s.agents.CreateEnrollment(ctx, "root-http", "test", sql.NullInt64{})
			if err != nil {
				t.Fatal(err)
			}
			cred := strings.Repeat("c", 64)
			id, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(e.Token), auth.DigestToken(cred), "root-http-request", "test")
			if err != nil {
				t.Fatal(err)
			}
			var live atomic.Pointer[Server]
			live.Store(s)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ws/v1/agent" {
					http.Error(w, "WebSocket unavailable", 502)
					return
				}
				live.Load().ServeHTTP(w, r)
			}))
			defer host.Close()
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "agent.json")
			config := agentruntime.Config{Server: host.URL, Development: true, Credential: cred, AgentID: id.AgentID, NodeID: id.NodeID, Shell: "/bin/sh"}
			if err := agentruntime.SaveConfig(path, config, true); err != nil {
				t.Fatal(err)
			}
			settings := protocol.DefaultAgentSettings()
			settings.Transport = transport
			settings.EnableGPU = false
			settings.FastIntervalSeconds = 1
			if err := localstate.Write(filepath.Join(dir, "settings.json"), settings); err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			go func() { finished <- agentruntime.Run(ctx, path, "agent-test", io.Discard) }()
			defer func() {
				cancel()
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("Agent shutdown did not finish")
				}
			}()
			for s.agentConnectionForNode(id.NodeID) == nil {
				select {
				case err := <-finished:
					t.Fatalf("Agent failed: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			result, err := s.agentHost(ctx, id.NodeID, protocol.HostRequest{Action: "settings.get"})
			if err != nil || result.Settings == nil || result.Settings.Transport != transport {
				t.Fatalf("settings RPC %+v %v", result, err)
			}
			spec := protocol.CommandSpec{Script: "printf root-http-success", TimeoutSeconds: 3, Confirm: true}
			req := protocol.HostRequest{Action: "commands.submit", IdempotencyKey: "e2e-command-key", Command: &spec}
			result, err = s.agentHost(ctx, id.NodeID, req)
			if err != nil || result.Job == nil {
				t.Fatal("command submit", err, result.Error)
			}
			jobID := result.Job.ID
			for {
				result, err = s.agentHost(ctx, id.NodeID, protocol.HostRequest{Action: "commands.get", JobID: jobID})
				if err != nil {
					t.Fatal(err)
				}
				if result.Job.Status != "running" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if result.Job.Status != "succeeded" || result.Job.Output != "root-http-success" {
				t.Fatal(result.Job)
			}
			retry, err := s.agentHost(ctx, id.NodeID, req)
			if err != nil || retry.Job == nil || retry.Job.ID != jobID {
				t.Fatal("HTTP command retry lost job", err)
			}
			batchMarker := filepath.Join(dir, "batch-marker")
			batchSpec := protocol.CommandSpec{Script: fmt.Sprintf("printf x >> '%s'", batchMarker), TimeoutSeconds: 3, Confirm: true}
			batchRequest := protocol.BatchCommand{NodeIDs: []string{id.NodeID}, Script: batchSpec.Script, TimeoutSeconds: 3, Confirm: true}
			batch, err := s.beginHostBatch(ctx, "lost-batch-response", "command", batchRequest, batchRequest.NodeIDs)
			if err != nil {
				t.Fatal(err)
			}
			child, err := s.agentHost(ctx, id.NodeID, protocol.HostRequest{Action: "commands.submit", Command: &batchSpec, IdempotencyKey: batchNodeKey(batch.ID, id.NodeID)})
			if err != nil || child.Job == nil {
				t.Fatal("batch child not accepted", err)
			}
			// Leave results_json empty, then restart Core before it records
			// the accepted child identity. Observation must recover it.
			dataDir := s.store.Dir
			s.Close()
			s, err = New("restarted", Options{DataDir: dataDir, Development: true})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			live.Store(s)
			for s.agentConnectionForNode(id.NodeID) == nil {
				select {
				case <-ctx.Done():
					t.Fatal("Agent did not reconnect", ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			for {
				request := httptest.NewRequest("GET", "/api/v1/commands/batches/"+batch.ID, nil)
				response := httptest.NewRecorder()
				if !s.handleHostBatchAPI(response, request, &session{}) || response.Code != 200 {
					t.Fatal("batch recovery", response.Code, response.Body.String())
				}
				var recovered hostBatch
				if err := json.Unmarshal(response.Body.Bytes(), &recovered); err != nil {
					t.Fatal(err)
				}
				if len(recovered.Results) == 1 && recovered.Results[0].Job != nil && recovered.Results[0].Job.Status == "succeeded" {
					if recovered.Results[0].JobID != child.Job.ID {
						t.Fatal("child identity changed")
					}
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("batch result not recovered")
				case <-time.After(10 * time.Millisecond):
				}
			}
			contents, err := os.ReadFile(batchMarker)
			if err != nil || string(contents) != "x" {
				t.Fatal("batch child replayed", string(contents), err)
			}
			cleanupRequest := httptest.NewRequest("POST", "/api/v1/nodes/"+id.NodeID+"/commands/cleanup", nil)
			cleanupResponse := httptest.NewRecorder()
			if !s.handleHostAPI(cleanupResponse, cleanupRequest, &session{RemoteAddr: "127.0.0.1"}) || cleanupResponse.Code != 200 {
				t.Fatal("cleanup API unavailable", cleanupResponse.Code, cleanupResponse.Body.String())
			}
			var cleanup protocol.HostResponse
			if err := json.Unmarshal(cleanupResponse.Body.Bytes(), &cleanup); err != nil || cleanup.Cleanup == nil {
				t.Fatal("cleanup API result", cleanup, err)
			}
			for {
				state, lease, err := s.currentMetricsState(ctx, id.NodeID)
				if err != nil {
					t.Fatal(err)
				}
				if state.Status == "online" && lease != nil {
					if snapshot, ok := s.metrics.SnapshotAt(id.NodeID, lease, time.Now()); ok && snapshot.Metrics.Host != nil {
						if snapshot.Metrics.Host.Processes.Status != protocol.MetricKnown {
							t.Fatal("host metrics not collected")
						}
						break
					}
				}
				select {
				case <-ctx.Done():
					t.Fatal("extended metrics did not arrive")
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
	}
}

func TestAssetExpiryFiresWhileNodeIsOffline(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	e, err := s.agents.CreateEnrollment(ctx, "expiry", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.alerts.SaveRule(ctx, "", corealerts.RuleInput{Name: "Renew subscription", Kind: corealerts.KindAssetExpiry, NodeID: e.NodeID, Severity: corealerts.SeverityWarning, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = s.alerts.Evaluate(ctx, []corealerts.Sample{{NodeID: e.NodeID, Kind: corealerts.KindNodeOffline, Known: true, State: "offline", ObservedAt: now}, {NodeID: e.NodeID, Kind: corealerts.KindAssetExpiry, Known: true, State: "warning", ObservedAt: now}})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM alerts WHERE status='active' AND rule_name='Renew subscription'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("expiry incorrectly suppressed by offline node", count, err)
	}
}

// Exercise the authenticated HTTP entry, including CSRF and audit, without a
// remote resource. An offline Agent should yield 503 after recording the action.
func TestCommandCleanupHTTPAudit(t *testing.T) {
	s := featureCore(t)
	token, csrf := "cleanup-test-session", "cleanup-test-csrf"
	now := s.now().UnixNano()
	_, err := s.store.DB.Exec(`INSERT INTO browser_sessions(id,token_digest,csrf_digest,created_at,last_seen_at,user_agent,remote_addr) VALUES(?,?,?,?,?,?,?)`, "cleanup-session", auth.DigestToken(token), auth.DigestToken(csrf), now, now, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/v1/nodes/8d99ec14-437a-4442-924f-1d110d0a546e/commands/cleanup", nil)
	r.Header.Set("Origin", "http://example.com")
	r.Header.Set(csrfHeaderName, csrf)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Agent host control unavailable") {
		t.Fatalf("cleanup HTTP: %d %s", w.Code, w.Body.String())
	}
	var action, outcome string
	if err := s.store.DB.QueryRow(`SELECT action,outcome FROM audit_entries WHERE action='commands.cleanup'`).Scan(&action, &outcome); err != nil || outcome != "accepted" {
		t.Fatalf("cleanup audit: %s %s %v", action, outcome, err)
	}
}
