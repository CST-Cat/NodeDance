package observe

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
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
	values := map[string]protocol.Metric[float64]{"pressure.cpu.some10": unknown("sampling", at), "pressure.cpu.some60": unknown("sampling", at)}
	if err := os.WriteFile(path, []byte("some avg10=0.00 avg60=3.00 avg300=1.00 total=100\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	collectPressure(path, "cpu", at, values)
	if *values["pressure.cpu.some10"].Value != 0 || *values["pressure.cpu.some60"].Value != 3 {
		t.Fatal(values)
	}
	collectPressure(path+"-missing", "cpu", at, values)
	for _, m := range values {
		if m.Value != nil || m.Reason != "unsupported" {
			t.Fatal(m)
		}
	}
	if _, ok := protocol.DiagnosticDefinitions["pressure.cpu.full10"]; ok {
		t.Fatal("system CPU full must not be published")
	}
}
func TestObservationSettingsBounds(t *testing.T) {
	defaults := protocol.ObservationDefaults()
	if err := protocol.ValidateObservationSettings(&defaults); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*protocol.ObservationSettings){
		func(s *protocol.ObservationSettings) { s.IntervalSeconds = 14 },
		func(s *protocol.ObservationSettings) { s.IntervalSeconds = 301 },
		func(s *protocol.ObservationSettings) { s.ProcessLimit = -1 },
		func(s *protocol.ObservationSettings) { s.ProcessLimit = 21 },
	} {
		settings := defaults
		change(&settings)
		if protocol.ValidateObservationSettings(&settings) == nil {
			t.Fatal("accepted invalid limits", settings)
		}
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
