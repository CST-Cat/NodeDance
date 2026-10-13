package alerts

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"testing"
	"time"
)

func TestMaintenanceWindowSuppressesAlreadyQueuedDelivery(t *testing.T) {
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
	node := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,'fixture','offline',1,1)`, node); err != nil {
		t.Fatal(err)
	}
	channel, err := store.SaveChannel(ctx, "", ChannelInput{Name: "fixture", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: "https://example.com/notify"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := store.SaveRule(ctx, "", RuleInput{Name: "offline", Kind: KindNodeOffline, NodeID: node, Severity: SeverityWarning, CooldownSeconds: 60, Enabled: true, ChannelIDs: []string{channel.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sample := func() []Sample {
		return []Sample{{NodeID: node, Kind: KindNodeOffline, Known: true, State: "offline", ObservedAt: now}}
	}
	if err := store.Evaluate(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddWindow(ctx, WindowInput{Kind: "maintenance", ScopeType: "global", StartsAt: now.Add(-time.Second), EndsAt: now.Add(time.Hour), Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, claimed, err := store.ClaimDelivery(ctx, now); err != nil || claimed {
		t.Fatal("queued delivery escaped new window", claimed, err)
	}
	deliveries, err := store.ListDeliveries(ctx, 10)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "suppressed" || deliveries[0].Attempts != 0 {
		t.Fatal(deliveries, err)
	}
	// Keep a sustained condition after cooldown: only the suppression transition
	// belongs in history, not one duplicate event for every scheduler tick.
	now = now.Add(time.Minute)
	for i := 0; i < 4; i++ {
		now = now.Add(time.Second)
		if err := store.Evaluate(ctx, sample()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.DB.QueryRow(`SELECT count(*) FROM alert_events e JOIN alerts a ON a.id=e.alert_id WHERE a.rule_id=? AND e.kind='suppressed'`, rule.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("repeated suppression events", count, err)
	}
}
