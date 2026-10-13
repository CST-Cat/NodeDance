package server

import (
	"context"
	"testing"
	"time"
)

func TestMetricsStateUsesCurrentTargetLease(t *testing.T) {
	now := time.Now().UTC()
	s, err := New("test", Options{DataDir: t.TempDir(), Development: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	id := guestFixtureNode(t, s, 99)
	for _, test := range []struct {
		name, status string
		seen         time.Time
		generation   int
		want         string
		online       bool
	}{
		{"live", "online", now, 7, "online", true},
		{"expired", "online", now.Add(-time.Hour), 7, "offline", false},
		{"future", "online", now.Add(time.Second), 7, "offline", false},
		{"zero generation", "online", now, 0, "offline", false},
		{"pending", "pending", now, 7, "pending", false},
		{"revoked", "revoked", now, 7, "revoked", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.store.DB.Exec(`UPDATE nodes SET status=?,last_seen_at=?,connection_generation=? WHERE id=?`, test.status, test.seen.UnixNano(), test.generation, id.NodeID); err != nil {
				t.Fatal(err)
			}
			state, lease, err := s.currentMetricsState(context.Background(), id.NodeID)
			if err != nil || !state.Exists || state.Status != test.want || (lease != nil) != test.online {
				t.Fatal(state, lease, err)
			}
		})
	}
	missing, lease, err := s.currentMetricsState(context.Background(), "missing")
	if err != nil || missing.Exists || lease != nil {
		t.Fatal(missing, lease, err)
	}
}
