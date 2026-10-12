package server

import (
	"context"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/retention"
)

func TestBatchRetentionPreservesPartialAndUncertainExecution(t *testing.T) {
	s, err := New("audit", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	for _, tc := range []struct{ id, results string }{
		{"completed", `[{"job":{"status":"succeeded"}}]`},
		{"partial", `[]`},
		{"unknown", `[{"job":{"status":"unknown"}}]`},
		{"interrupted", `[{"job":{"status":"interrupted"}}]`},
		{"stale", `[{"job":{"status":"succeeded"},"stale":true}]`},
	} {
		_, err = s.store.DB.Exec(`INSERT INTO host_batches(id,kind,request_hash,nodes_json,results_json,created_at,updated_at) VALUES(?,'command','hash','["node"]',?,?,?)`, tc.id, tc.results, now.Add(-100*24*time.Hour).UnixNano(), now.UnixNano())
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := retention.Cleanup(context.Background(), s.store.DB, now, 90*24*time.Hour)
	if err != nil || result.HostBatches != 1 {
		t.Fatal(result, err)
	}
	var count int
	if err = s.store.DB.QueryRow(`SELECT COUNT(*) FROM host_batches WHERE id <> 'completed'`).Scan(&count); err != nil || count != 4 {
		t.Fatal(count, err)
	}
}
