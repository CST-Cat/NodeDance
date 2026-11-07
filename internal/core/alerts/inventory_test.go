package alerts

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestAlertInventoryLimits(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db.DB, make([]byte, 32), func() time.Time { return time.Unix(0, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	node := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?,'fixture','offline',1,1)`, node); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if _, err := db.DB.Exec(`INSERT INTO alert_channels(id,name,kind,config_json,enabled,revision,created_at,updated_at) VALUES(?,?, 'webhook','{}',1,1,1,1)`, fmt.Sprintf("channel-%04d", i), fmt.Sprintf("channel %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveChannel(ctx, "", ChannelInput{Name: "over limit", Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: "https://example.com/notify"}, Enabled: true}); err == nil || !strings.Contains(err.Error(), "inventory limit is 200") {
		t.Fatalf("channel inventory cap did not trip: %v", err)
	}
	for i := 0; i < 5000; i++ {
		if _, err := db.DB.Exec(`INSERT INTO alert_rules(id,name,kind,node_id,severity,duration_seconds,cooldown_seconds,channel_ids_json,enabled,revision,created_at,updated_at) VALUES(?,?,'node_offline',?,'warning',0,60,'[]',1,1,1,1)`, fmt.Sprintf("rule-%05d", i), fmt.Sprintf("rule %d", i), node); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveRule(ctx, "", RuleInput{Name: "over limit", Kind: KindNodeOffline, NodeID: node, Severity: SeverityWarning, CooldownSeconds: 60, Enabled: true}); err == nil || !strings.Contains(err.Error(), "inventory limit is 5000") {
		t.Fatalf("rule inventory cap did not trip: %v", err)
	}
}
