package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/guest"
	coremetrics "github.com/CST-Cat/NodeDance/internal/core/metrics"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func guestFixtureNode(t *testing.T, s *Server, index int) agents.Identity {
	t.Helper()
	ctx := context.Background()
	enrollment, err := s.agents.CreateEnrollment(ctx, "PRIVATE-HOST-NAME", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	digest := auth.DigestToken(fmt.Sprintf("%064x", index+1))
	identity, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), digest, fmt.Sprintf("guest-node-%d", index), "test")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.agents.BeginConnection(ctx, identity, digest, protocol.CurrentVersion, "PRIVATE-VERSION", `["agent.metrics.v2"]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64", EffectiveUID: 0, EffectiveGID: 0, SupplementaryGroups: []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	id := coremetrics.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}
	if err = s.metrics.BindConnection(id, lease.ConnectionGeneration); err != nil {
		t.Fatal(err)
	}
	snapshot := testMetricsSnapshot(time.Now().UTC())
	cpu := 48.5
	snapshot.CPU.UsagePercent = protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &cpu, SampledAt: snapshot.CollectedAt}
	privateHost := "PRIVATE-HOST-NAME"
	snapshot.System.Hostname = protocol.Metric[string]{Status: protocol.MetricKnown, Value: &privateHost, SampledAt: snapshot.CollectedAt}
	if err = s.metrics.Accept(id, lease.ConnectionGeneration, 1, snapshot, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cpu.usage_percent", "memory.used_percent", "disk:/PRIVATE-MOUNT:used_percent"} {
		_, err = s.store.DB.Exec(`INSERT INTO metrics_minute(node_id,metric_key,bucket_at,sample_count,sample_sum,minimum,maximum) VALUES(?,?,?,1,25,25,25)`, identity.NodeID, key, time.Now().Add(-2*time.Minute).Truncate(time.Minute).Unix())
		if err != nil {
			t.Fatal(err)
		}
	}
	return identity
}
func TestGuestProjectionAndAnonymousBoundaries(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	if err := s.BootstrapAdministrator(ctx, "guest-admin", "guest-password-strong"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s)
	defer host.Close()
	read := func(method, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, host.URL+path, strings.NewReader(`{"mode":"admin"}`))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if status, raw := read("GET", "/api/v1/public/guest"); status != 200 || !strings.Contains(raw, `"enabled":false`) {
		t.Fatal(status, raw)
	}
	if status, _ := read("GET", "/api/v1/public/guest/display"); status != 404 {
		t.Fatal(status)
	}
	first := guestFixtureNode(t, s, 0)
	second := guestFixtureNode(t, s, 1)
	probe, err := s.probes.Create(ctx, coreprobes.Config{NodeID: first.NodeID, Name: "PRIVATE-SERVICE-NAME", Kind: "tcp", Target: "tcp://127.0.0.1:9", IntervalSeconds: 60, TimeoutSeconds: 3, Enabled: false}, audit.Event{Action: "probe_create", Outcome: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "session")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call := func(name string, input control.Input) control.Result {
		t.Helper()
		op, err := control.FindIn(catalog, name)
		if err != nil {
			t.Fatal(err)
		}
		return client.Execute(ctx, op, input, nil, nil)
	}
	if result := call("auth.login", control.Input{Body: json.RawMessage(`{"username":"guest-admin","password":"guest-password-strong"}`)}); result.Err() != nil {
		t.Fatal(result.Error)
	}
	initial := call("guest.settings", control.Input{})
	var options struct {
		Settings guest.Settings `json:"settings"`
	}
	if initial.Err() != nil || json.Unmarshal(initial.Data, &options) != nil {
		t.Fatal(initial)
	}
	if options.Settings.Enabled || options.Settings.ShowStatus || options.Settings.HistoryHours != 0 || len(options.Settings.Metrics)+len(options.Settings.Nodes)+len(options.Settings.Probes) != 0 {
		t.Fatal("not opt-in", options)
	}
	policy := options.Settings
	policy.Enabled = true
	policy.ShowStatus = true
	policy.HistoryHours = 1
	policy.Metrics = []string{"cpu.usage_percent"}
	policy.Nodes = []guest.Resource{{ID: first.NodeID, Name: "Public node"}}
	policy.Probes = []guest.Resource{{ID: probe.ID, Name: "Public service"}}
	raw, _ := json.Marshal(policy)
	saved := call("guest.configure", control.Input{Body: raw})
	if saved.Err() != nil {
		t.Fatal(saved)
	}
	json.Unmarshal(saved.Data, &policy)
	status, public := read("GET", "/api/v1/public/guest/display")
	if status != 200 {
		t.Fatal(status, public)
	}
	for _, private := range []string{first.NodeID, first.AgentID, second.NodeID, probe.ID, "PRIVATE-HOST", "PRIVATE-SERVICE", "PRIVATE-MOUNT", "tcp://", "memory.used_percent", "agentId", "nodeId", "generation", "capabilities", "reason", "credential", "sshHost"} {
		if strings.Contains(public, private) {
			t.Fatalf("private data %q leaked: %s", private, public)
		}
	}
	var view guest.Display
	if err = json.Unmarshal([]byte(public), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Nodes) != 1 || view.Nodes[0].Metrics["cpu.usage_percent"].Value == nil || *view.Nodes[0].Metrics["cpu.usage_percent"].Value != 48.5 || view.Nodes[0].History == nil || len(view.Nodes[0].History.Series) != 1 || view.Nodes[0].History.Series[0].Key != "cpu.usage_percent" || len(view.Probes) != 1 || view.Probes[0].Status != "unknown" {
		t.Fatal(view)
	}
	preview := call("guest.preview", control.Input{})
	if preview.Err() != nil {
		t.Fatal(preview)
	}
	for _, private := range []string{first.NodeID, first.AgentID, "PRIVATE-HOST"} {
		if strings.Contains(string(preview.Data), private) {
			t.Fatal("admin preview differs from guest boundary")
		}
	}
	third := guestFixtureNode(t, s, 2)
	_, after := read("GET", "/api/v1/public/guest/display")
	if strings.Contains(after, third.NodeID) {
		t.Fatal("new resource auto published")
	}
	for _, path := range []string{"/api/v1/nodes", "/api/v1/guest/settings", "/api/v1/guest/preview", "/api/v1/nodes/" + first.NodeID + "/metrics"} {
		if status, _ = read("GET", path); status != 401 {
			t.Fatal(path, status)
		}
	}
	for _, path := range []string{"/api/v1/guest/settings", "/api/v1/nodes/" + first.NodeID + "/commands", "/api/v1/updates/apply"} {
		if status, _ = read("POST", path); status != 401 {
			t.Fatal(path, status)
		}
	}
	if status, _ = read("POST", "/api/v1/public/guest/display"); status != 405 {
		t.Fatal(status)
	}
	if status, _ = read("GET", "/api/v1/public/guest/display?nodeId="+second.NodeID); status != 400 {
		t.Fatal(status)
	}
	for i := 0; i < 4; i++ {
		s.guestSlots <- struct{}{}
	}
	if status, _ = read("GET", "/api/v1/public/guest/display"); status != 503 {
		t.Fatal(status)
	}
	for i := 0; i < 4; i++ {
		<-s.guestSlots
	}
	if status, _ = read("GET", "/api/v1/health"); status != 200 {
		t.Fatal("guest capacity blocked management health")
	}
	old, _ := json.Marshal(options.Settings)
	if result := call("guest.configure", control.Input{Body: old}); result.Status != 409 {
		t.Fatal("lost-update guard", result)
	}
	policy.ShowStatus = false
	policy.Metrics = []string{"memory.used_percent"}
	policy.HistoryHours = 0
	raw, _ = json.Marshal(policy)
	saved = call("guest.configure", control.Input{Body: raw})
	if saved.Err() != nil {
		t.Fatal(saved)
	}
	json.Unmarshal(saved.Data, &policy)
	_, changed := read("GET", "/api/v1/public/guest/display")
	if strings.Contains(changed, "cpu.usage_percent") || strings.Contains(changed, `"status":"online"`) || !strings.Contains(changed, "memory.used_percent") {
		t.Fatal("publication cache ignored revision", changed)
	}
	policy.Enabled = false
	raw, _ = json.Marshal(policy)
	saved = call("guest.configure", control.Input{Body: raw})
	if saved.Err() != nil {
		t.Fatal(saved)
	}
	if status, _ = read("GET", "/api/v1/public/guest/display"); status != 404 {
		t.Fatal("disable retained cached data", status)
	}
	var count int
	if err = s.store.DB.QueryRow("SELECT COUNT(*) FROM audit_entries WHERE action='guest_display_update'").Scan(&count); err != nil || count != 3 {
		t.Fatal("publication audit", count, err)
	}
}
func TestGuestUnknownMetricsNeverExposeStaleValues(t *testing.T) {
	s := featureCore(t)
	identity := guestFixtureNode(t, s, 0)
	_, err := s.store.DB.Exec("UPDATE nodes SET status='offline',last_seen_at=? WHERE id=?", time.Now().Add(-time.Hour).UnixNano(), identity.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	policy := guest.Defaults()
	policy.Nodes = []guest.Resource{{ID: identity.NodeID, Name: "Public"}}
	policy.Metrics = []string{"cpu.usage_percent"}
	policy.ShowStatus = true
	out, err := s.buildGuestDisplay(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Nodes) != 1 || out.Nodes[0].Status != "offline" || out.Nodes[0].Metrics["cpu.usage_percent"].Value != nil {
		t.Fatal("stale value masquerades as current", out)
	}
}

func TestGuestSettingsPersistWithoutAutoPublishingResources(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	s, err := New("test", Options{DataDir: directory, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	store := guest.Store{DB: s.store.DB}
	settings := guest.Defaults()
	settings.Title = "Saved display"
	saved, err := store.Save(ctx, settings, audit.Event{Action: "guest_display_update", Outcome: "succeeded"})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	reopened, err := New("test", Options{DataDir: directory, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := (guest.Store{DB: reopened.store.DB}).Get(ctx)
	if err != nil || loaded.Title != saved.Title || loaded.Revision != saved.Revision || loaded.Enabled || len(loaded.Nodes) != 0 {
		t.Fatal(loaded, err)
	}
}

func TestGuestHistoryFiltersSelectedSeriesAcrossRollups(t *testing.T) {
	s := featureCore(t)
	identity := guestFixtureNode(t, s, 0)
	ctx := context.Background()
	bucket := time.Now().Add(-2 * time.Hour).Truncate(time.Hour).Unix()
	for _, key := range []string{"cpu.usage_percent", "memory.used_percent"} {
		if _, err := s.store.DB.Exec(`INSERT INTO metrics_hour(node_id,metric_key,bucket_at,sample_count,sample_sum,minimum,maximum) VALUES(?,?,?,2,40,10,30)`, identity.NodeID, key, bucket); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.DB.Exec(`INSERT INTO metrics_minute(node_id,metric_key,bucket_at,sample_count,sample_sum,minimum,maximum) VALUES(?,?,?,1,30,30,30)`, identity.NodeID, key, bucket+300); err != nil {
			t.Fatal(err)
		}
	}
	policy := guest.Defaults()
	policy.Nodes = []guest.Resource{{ID: identity.NodeID, Name: "Public"}}
	policy.Metrics = []string{"cpu.usage_percent"}
	for _, hours := range []int{6, 24} {
		policy.HistoryHours = hours
		view, err := s.buildGuestDisplay(ctx, policy)
		if err != nil {
			t.Fatal(err)
		}
		history := view.Nodes[0].History
		if history.Resolution != "hour" || len(history.Series) != 1 || history.Series[0].Key != "cpu.usage_percent" {
			t.Fatal(history)
		}
		found := false
		for _, p := range history.Series[0].Points {
			if p.BucketAt.Unix() == bucket {
				found = true
				if p.Samples != 3 || p.Value < 23.33 || p.Value > 23.34 {
					t.Fatal("weighted rollup changed", p)
				}
			}
		}
		if !found {
			t.Fatal("rollup bucket lost", history)
		}
	}
}
