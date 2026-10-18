package observe

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCounterStatesAndRateDenominator(t *testing.T) {
	c := Collector{previous: map[string]counter{}}
	at := time.Now()
	if m := c.rate("x", 100, at); m.Value != nil || m.Reason != "warming_up" {
		t.Fatal(m)
	}
	if m := c.rate("x", 140, at.Add(5*time.Second)); m.Value == nil || *m.Value != 8 {
		t.Fatal(m)
	}
	if m := c.rate("x", 1, at.Add(10*time.Second)); m.Value != nil || m.Reason != "counter_reset" {
		t.Fatal(m)
	}
	if m := c.rate("x", 1, at.Add(15*time.Second)); m.Value == nil || *m.Value != 0 {
		t.Fatal("A real zero delta must stay known", m)
	}
}
func TestPSIIndependentUnknownAndNoSystemCPUFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pressure")
	at := time.Now()
	values := map[string]protocol.Metric[float64]{"pressure.memory.some10": unknown("sampling", at), "pressure.memory.full10": unknown("sampling", at)}
	if err := os.WriteFile(path, []byte("some avg10=12.34 avg60=3.00 avg300=1.00 total=100\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	collectPressure(path, "memory", at, values)
	if *values["pressure.memory.some10"].Value != 12.34 || *values["pressure.memory.full10"].Value != 0 {
		t.Fatal(values)
	}
	collectPressure(path+"-missing", "memory", at, values)
	for _, m := range values {
		if m.Value != nil || m.Reason != "unsupported" {
			t.Fatal(m)
		}
	}
	if _, ok := protocol.DiagnosticDefinitions["pressure.cpu.full10"]; ok {
		t.Fatal("system CPU full must not be published")
	}
}
func TestApplicationCollectorBoundsMissingAndNoRedirect(t *testing.T) {
	var redirected bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, destination.URL, 302)
			return
		}
		if r.URL.Path == "/large" {
			_, _ = w.Write(make([]byte, 300<<10))
			return
		}
		_, _ = w.Write([]byte("# TYPE requests_total counter\nrequests_total 42\nmissing_label{db=\"secret\"} 123\nnot_requested 999\n"))
	}))
	defer server.Close()
	for _, path := range []string{"/metrics", "/redirect", "/large"} {
		o := &protocol.Observability{}
		collectApplications(context.Background(), time.Now(), []protocol.ApplicationCollector{{Name: "app", Kind: "prometheus", URL: server.URL + path, Metrics: []string{"requests_total", "absent"}, MetricUnits: map[string]string{"requests_total": "count", "absent": "count"}}}, o)
		if len(o.Entities) != 1 {
			t.Fatal(o)
		}
		v := o.Entities[0].Values
		if path == "/metrics" {
			if v["requests_total"].Value == nil || *v["requests_total"].Value != 42 || v["absent"].Value != nil {
				t.Fatal(v)
			}
			if _, ok := v["not_requested"]; ok {
				t.Fatal("unselected metric leaked")
			}
		} else if v["collector_up"].Value != nil {
			t.Fatal(v)
		}
	}
	if redirected {
		t.Fatal("application endpoint followed a redirect")
	}
}
func TestObservationSettingsRejectInvalidBoundsAndEndpoints(t *testing.T) {
	s := protocol.ObservationDefaults()
	s.Services = []string{"--malicious"}
	if protocol.ValidateObservationSettings(&s) == nil {
		t.Fatal("accepted command flag")
	}
	s = protocol.ObservationDefaults()
	s.Applications = []protocol.ApplicationCollector{{Name: "db", Kind: "prometheus", URL: "http://user:password@localhost/metrics"}}
	if protocol.ValidateObservationSettings(&s) == nil {
		t.Fatal("accepted credentials in URL")
	}
	s.Applications[0].URL = "http://localhost/metrics"
	s.Applications[0].Metrics = []string{"x"}
	if protocol.ValidateObservationSettings(&s) == nil {
		t.Fatal("accepted metric without unit")
	}
}

func BenchmarkFastDiagnostics(b *testing.B) {
	c := Collector{running: true}
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Collect(ctx, time.Now(), 5, nil)
	}
}

func TestLiveDiagnosticSamplesMeetWireContract(t *testing.T) {
	c := &Collector{}
	for i := 0; i < 3; i++ {
		o := c.Collect(context.Background(), time.Now(), 1, nil)
		if err := protocol.ValidateObservability(o, false); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestProcessNameNeverExpandsCommandArguments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOST_PROC", dir)
	if err := os.Mkdir(filepath.Join(dir, "123"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "123", "comm"), []byte("worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "123", "cmdline"), []byte("worker\x00--password=private\x00"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := processName(123)
	if err != nil || got != "worker" {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "123", "comm"), make([]byte, 300), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := processName(123); err == nil {
		t.Fatal("oversized process name accepted")
	}
}
