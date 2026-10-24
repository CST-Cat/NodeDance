package alerts

import (
	"context"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestNodeThresholdOverridesKeepDefaultsAndRestartEvaluation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	store, err := NewStore(db.DB, make([]byte, 32), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	nodes := []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	for _, node := range nodes {
		if _, err := db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,'fixture','online',1,1)`, node); err != nil {
			t.Fatal(err)
		}
		if err := store.EnsureDefaultRules(ctx, node); err != nil {
			t.Fatal(err)
		}
	}
	var cpu Rule
	for _, node := range nodes {
		rules, err := store.ListRules(ctx, node)
		if err != nil {
			t.Fatal(err)
		}
		if len(rules) != 3 {
			t.Fatalf("unexpected built-in rules: %#v", rules)
		}
		for _, rule := range rules {
			if rule.Threshold == nil || *rule.Threshold != 90 || rule.DurationSeconds != 300 {
				t.Fatalf("unexpected default: %#v", rule)
			}
			if node == nodes[0] && rule.Kind == KindCPU {
				cpu = rule
			}
		}
	}
	input := RuleInput{Name: "NAT CPU", Kind: cpu.Kind, NodeID: cpu.NodeID, Severity: cpu.Severity, Threshold: floatPtr(98), DurationSeconds: 300, CooldownSeconds: 300, Enabled: true, Revision: cpu.Revision}
	saved, err := store.SaveRule(ctx, cpu.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureDefaultRules(ctx, nodes[0]); err != nil {
		t.Fatal(err)
	}
	rules, err := store.ListRules(ctx, nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3 {
		t.Fatal("customization duplicated a default rule")
	}
	current, err := store.GetRule(ctx, cpu.ID)
	if err != nil || current.Threshold == nil || *current.Threshold != 98 {
		t.Fatalf("default initialization replaced the node threshold: %#v %v", current, err)
	}
	evaluate := func() {
		t.Helper()
		samples := []Sample{}
		for _, node := range nodes {
			samples = append(samples, Sample{NodeID: node, Kind: KindCPU, Known: true, Value: floatPtr(95), ObservedAt: now})
		}
		if err := store.Evaluate(ctx, samples); err != nil {
			t.Fatal(err)
		}
	}
	evaluate()
	now = now.Add(301 * time.Second)
	evaluate()
	active, err := store.ListAlerts(ctx, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].NodeID != nodes[1] {
		t.Fatalf("node thresholds were not isolated: %#v", active)
	}
	input.Threshold = floatPtr(94)
	input.DurationSeconds = 60
	input.Revision = saved.Revision
	saved, err = store.SaveRule(ctx, cpu.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	evaluate()
	active, _ = store.ListAlerts(ctx, true, 100)
	if len(active) != 1 {
		t.Fatal("a rule edit reused the old duration window")
	}
	now = now.Add(60 * time.Second)
	evaluate()
	active, _ = store.ListAlerts(ctx, true, 100)
	if len(active) != 2 {
		t.Fatalf("sustained custom threshold did not trigger: %#v", active)
	}
	input.Threshold = floatPtr(98)
	input.Revision = saved.Revision
	saved, err = store.SaveRule(ctx, cpu.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	active, _ = store.ListAlerts(ctx, true, 100)
	if len(active) != 1 || active[0].NodeID != nodes[1] {
		t.Fatal("raising a node threshold did not withdraw the previous active alert")
	}
	input.Threshold = floatPtr(50)
	if _, err := store.SaveRule(ctx, cpu.ID, input); err == nil {
		t.Fatal("stale revision overwrote a threshold")
	}
	current, _ = store.GetRule(ctx, cpu.ID)
	if current.Threshold == nil || *current.Threshold != 98 || current.Revision != saved.Revision {
		t.Fatal("failed revision check changed the rule")
	}
}
