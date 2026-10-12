package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	coreretention "github.com/CST-Cat/NodeDance/internal/core/retention"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// This measures real TCP/Core/SQLite behavior with simulated Agent producers.
// It does not claim host sampling or root/Docker execution at this node count.
func TestCoreCapacitySoak(t *testing.T) {
	seconds, _ := strconv.Atoi(os.Getenv("NODEDANCE_TEST_SOAK_SECONDS"))
	if seconds < 1 {
		t.Skip("set NODEDANCE_TEST_SOAK_SECONDS for sustained network acceptance")
	}
	count, _ := strconv.Atoi(os.Getenv("NODEDANCE_TEST_SOAK_NODES"))
	if count < 1 {
		count = 100
	}
	if count > 200 || seconds > 3600 {
		t.Fatal("acceptance limit: 200 nodes, 3600 seconds")
	}
	s, err := New("acceptance", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(s)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 100
	transport.MaxIdleConns = 100
	transport.MaxConnsPerHost = 100
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer func() { transport.CloseIdleConnections(); s.Close(); host.Close() }()
	ctx := context.Background()
	caps := []string{"agent.heartbeat.v1", "agent.os-permissions.v1", protocol.CapabilityMetrics}
	type producer struct {
		node, credential, path string
		generation, write, ack uint64
	}
	producers := make([]producer, count)
	var mu sync.Mutex
	latencies := []time.Duration{}
	request := func(p *producer, method, path string, frame any) ([]byte, http.Header, error) {
		var body io.Reader
		if frame != nil {
			raw, e := json.Marshal(frame)
			if e != nil {
				return nil, nil, e
			}
			body = bytes.NewReader(raw)
		}
		req, e := http.NewRequest(method, host.URL+path, body)
		if e != nil {
			return nil, nil, e
		}
		req.Header.Set("Authorization", "Bearer "+p.credential)
		if p.write > 0 {
			req.Header.Set("X-Channel-Sequence", strconv.FormatUint(p.write, 10))
		}
		if p.ack > 0 {
			req.Header.Set("X-Channel-Ack", strconv.FormatUint(p.ack, 10))
		}
		start := time.Now()
		r, e := httpClient.Do(req)
		if e != nil {
			return nil, nil, e
		}
		defer r.Body.Close()
		raw, e := io.ReadAll(r.Body)
		mu.Lock()
		latencies = append(latencies, time.Since(start))
		mu.Unlock()
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			return nil, nil, fmt.Errorf("%s %s: %d %s", method, path, r.StatusCode, raw)
		}
		return raw, r.Header, e
	}
	for i := range producers {
		enrollment, e := s.agents.CreateEnrollment(ctx, fmt.Sprintf("soak-%d", i), "acceptance", sql.NullInt64{})
		if e != nil {
			t.Fatal(e)
		}
		credential := fmt.Sprintf("%064x", i+1)
		id, e := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken(credential), fmt.Sprintf("soak-request-%d", i), "acceptance")
		if e != nil {
			t.Fatal(e)
		}
		p := &producers[i]
		p.node = id.NodeID
		p.credential = credential
		hello, _ := json.Marshal(protocol.Hello{AgentID: id.AgentID, NodeID: id.NodeID, AgentVersion: "simulated-acceptance", Capabilities: caps, Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64", EffectiveUID: 0, EffectiveGID: 0, SupplementaryGroups: []int{}}})
		raw, _, e := request(p, "POST", "/api/v1/agents/channel", protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: hello})
		if e != nil {
			t.Fatal(e)
		}
		var opened struct {
			ID string `json:"channelId"`
		}
		if e = json.Unmarshal(raw, &opened); e != nil {
			t.Fatal(e)
		}
		p.path = "/api/v1/agents/channel/" + opened.ID
		raw, header, e := request(p, "GET", p.path, nil)
		if e != nil {
			t.Fatal(e)
		}
		var envelope protocol.Envelope
		var welcome protocol.Welcome
		json.Unmarshal(raw, &envelope)
		json.Unmarshal(envelope.Payload, &welcome)
		p.generation = welcome.Generation
		p.ack, _ = strconv.ParseUint(header.Get("X-Channel-Sequence"), 10, 64)
		if p.generation == 0 {
			t.Fatal("negotiation did not establish a generation")
		}
	}
	if err := s.BootstrapAdministrator(ctx, "acceptance-admin", "acceptance-password-123"); err != nil {
		t.Fatal(err)
	}
	admin, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	catalog, err := admin.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	login, _ := control.FindIn(catalog, "auth.login")
	r := admin.Execute(ctx, login, control.Input{Body: json.RawMessage(`{"username":"acceptance-admin","password":"acceptance-password-123"}`)}, nil, nil)
	if r.Error != "" {
		t.Fatal(r.Error)
	}
	nodes, _ := control.FindIn(catalog, "nodes.list")
	history, _ := control.FindIn(catalog, "nodes.history")
	sample := func() (uint64, int, int) {
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		fds, _ := os.ReadDir("/proc/self/fd")
		return stats.HeapAlloc, runtime.NumGoroutine(), len(fds)
	}
	heap0, g0, fd0 := sample()
	started := time.Now()
	deadline := started.Add(time.Duration(seconds) * time.Second)
	sequence := uint64(0)
	reports := 0
	for time.Now().Before(deadline) {
		sequence++
		var wg sync.WaitGroup
		slots := make(chan struct{}, 20)
		failures := make(chan error, count)
		for i := range producers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				slots <- struct{}{}
				defer func() { <-slots }()
				p := &producers[i]
				heartbeat, _ := json.Marshal(protocol.Heartbeat{Capabilities: caps})
				p.write++
				if _, _, e := request(p, "POST", p.path, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat, Generation: p.generation, Sequence: sequence, Payload: heartbeat}); e != nil {
					failures <- e
					return
				}
				_, header, e := request(p, "GET", p.path, nil)
				if e != nil {
					failures <- e
					return
				}
				p.ack, _ = strconv.ParseUint(header.Get("X-Channel-Sequence"), 10, 64)
				metrics := testMetricsSnapshot(time.Now().UTC())
				cpu := float64(25)
				metrics.CPU.UsagePercent = protocol.Metric[float64]{Status: protocol.MetricKnown, Value: &cpu, SampledAt: metrics.CollectedAt}
				cores := 2
				metrics.CPU.LogicalCores = protocol.Metric[int]{Status: protocol.MetricKnown, Value: &cores, SampledAt: metrics.CollectedAt}
				if err := protocol.ValidateAgentMetricsSnapshot(metrics); err != nil {
					failures <- err
					return
				}
				payload, _ := json.Marshal(metrics)
				p.write++
				if _, _, e := request(p, "POST", p.path, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeMetrics, Generation: p.generation, Sequence: sequence, Payload: payload}); e != nil {
					failures <- e
				}
			}(i)
		}
		wg.Wait()
		close(failures)
		for e := range failures {
			t.Fatal(e)
		}
		reports += count
		appliedDeadline := time.Now().Add(2 * time.Second)
		for _, p := range producers {
			for {
				view, ok := s.metrics.SnapshotAt(p.node, nil, time.Now())
				if ok && view.Sequence == sequence {
					break
				}
				if time.Now().After(appliedDeadline) {
					t.Fatal("metrics not applied within two seconds", p.node)
				}
				time.Sleep(time.Millisecond)
			}
		}
		result := admin.Execute(ctx, nodes, control.Input{}, nil, nil)
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		var list struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		json.Unmarshal(result.Data, &list)
		if len(list.Nodes) != count {
			t.Fatal("node isolation lost", len(list.Nodes))
		}
		result = admin.Execute(ctx, history, control.Input{Params: map[string]string{"nodeId": producers[0].node}, Query: map[string]string{"from": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), "to": time.Now().UTC().Format(time.RFC3339Nano), "resolution": "minute"}}, nil, nil)
		if result.Error != "" {
			t.Fatal(result.Error)
		}
		if _, e := coreretention.Cleanup(ctx, s.store.DB, time.Now(), 90*24*time.Hour); e != nil {
			t.Fatal(e)
		}
		if sequence == 1 {
			heap0, g0, fd0 = sample()
		}
		t.Logf("accepted %d reports across %d simulated nodes", reports, count)
		wait := time.Until(deadline)
		if wait > 5*time.Second {
			wait = 5 * time.Second
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
	heap1, g1, fd1 := sample()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[(len(latencies)-1)*95/100]
	if fd1 > fd0+32 || g1 > g0+64 {
		t.Fatalf("resource growth exceeded acceptance bounds: FD %d -> %d; goroutines %d -> %d", fd0, fd1, g0, g1)
	}
	if heap1 > heap0+(64<<20) {
		t.Fatal("heap grew by more than 64 MiB")
	}
	info, _ := os.Stat(filepath.Join(s.store.Dir, "nodedance.sqlite"))
	t.Logf("RESULT nodes=%d seconds=%.1f reports=%d requests=%d p95_ms=%.3f heap_bytes=%d->%d goroutines=%d->%d fd=%d->%d db_bytes=%d", count, time.Since(started).Seconds(), reports, len(latencies), float64(p95)/float64(time.Millisecond), heap0, heap1, g0, g1, fd0, fd1, info.Size())
}
