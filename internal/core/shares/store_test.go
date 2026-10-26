package shares_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/shares"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestShareLimitsPublicationAndCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.DB.Exec(`INSERT INTO users(id,username,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,'owner',X'01',X'02','test','Owner',1)`)
	if e != nil {
		t.Fatal(e)
	}
	s := shares.Store{DB: db.DB, Dir: db.Dir, Now: func() time.Time { return now }}
	if e = s.Init(ctx); e != nil {
		t.Fatal(e)
	}
	entry, e := s.PutBlob(ctx, strings.NewReader("same content"), 1024)
	if e != nil {
		t.Fatal(e)
	}
	entry.Path = "file.txt"
	second, e := s.PutBlob(ctx, strings.NewReader("same content"), 1024)
	if e != nil || entry.Blob != second.Blob {
		t.Fatal("deduplication failed", e)
	}
	password := "secret"
	in := shares.Input{RequestID: shares.Token(), Name: "File", ExpiryDays: 1, MaxDownloads: 1, Password: &password, Paths: []string{"/file.txt"}}
	v, e := s.Create(ctx, 1, in, []shares.Entry{entry})
	if e != nil {
		t.Fatal(e)
	}
	if !s.Available(ctx, v) || s.CheckPassword(v, "bad") || !s.CheckPassword(v, password) {
		t.Fatal("password or availability failed")
	}
	public := s.Public(v)
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), entry.Blob) || strings.Contains(string(raw), "/file.txt") || strings.Contains(string(raw), "passwordHash") {
		t.Fatal("private data escaped", string(raw))
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if s.Consume(ctx, v) == nil {
				accepted.Add(1)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d downloads", accepted.Load())
	}
	v, e = s.Get(ctx, v.ID)
	if e != nil || s.Available(ctx, v) {
		t.Fatal("exhausted share still available", e)
	}
	other := in
	other.RequestID = shares.Token()
	other.MaxDownloads = 0
	other.Password = nil
	active, e := s.Create(ctx, 1, other, []shares.Entry{entry})
	if e != nil {
		t.Fatal(e)
	}
	now = now.Add(2 * time.Hour)
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	f, e := s.Blob(entry)
	if e != nil {
		t.Fatal("live deduplicated content removed", e)
	}
	f.Close()
	if e = s.Revoke(ctx, 1, active.ID, active.Revision); e != nil {
		t.Fatal(e)
	}
	if e = s.Cleanup(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(db.Dir, "shares", entry.Blob)); !os.IsNotExist(e) {
		t.Fatal("unreferenced content retained", e)
	}
	for _, p := range []string{"../escape", "/absolute", "a/../escape", "a\\escape", "."} {
		if shares.SafePath(p) {
			t.Fatal("unsafe path accepted", p)
		}
	}
}
func TestCollectionIsBoundedAndDisabledOwnerWithdraws(t *testing.T) {
	ctx := context.Background()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.DB.Exec(`INSERT INTO users(id,username,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,'owner',X'01',X'02','test','Owner',1)`)
	if e != nil {
		t.Fatal(e)
	}
	s := shares.Store{DB: db.DB, Dir: db.Dir, Now: time.Now}
	r, e := s.SaveRequest(ctx, 1, "", shares.RequestInput{RequestID: shares.Token(), Name: "Collect", MaxFiles: 1, MaxBytes: 5})
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.AppendRequest(ctx, r, shares.Entry{Path: "one.txt", Size: 5, Blob: strings.Repeat("a", 64)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AppendRequest(ctx, r, shares.Entry{Path: "two.txt", Size: 1}); e == nil {
		t.Fatal("collection overrun accepted")
	}
	if _, e = db.DB.Exec(`UPDATE users SET enabled=0 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if s.RequestAvailable(ctx, r) {
		t.Fatal("disabled owner still publishes collection")
	}
}

func TestSourceAndOwnerWithdrawalAndExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	_, e = db.DB.Exec(`INSERT INTO users(id,username,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,'owner',X'01',X'02','test','Owner',1)`)
	if e != nil {
		t.Fatal(e)
	}
	node := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	_, e = db.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at)VALUES(?,'Source','online',1,1)`, node)
	if e != nil {
		t.Fatal(e)
	}
	s := shares.Store{DB: db.DB, Dir: db.Dir, Now: func() time.Time { return now }}
	v, e := s.Create(ctx, 1, shares.Input{RequestID: shares.Token(), NodeID: node, Name: "Snapshot", ExpiryDays: 1}, []shares.Entry{{Path: "file.txt", Blob: strings.Repeat("a", 64), Size: 1}})
	if e != nil {
		t.Fatal(e)
	}
	if !s.Available(ctx, v) {
		t.Fatal("live source unavailable")
	}
	if _, e = db.DB.Exec(`UPDATE nodes SET status='revoked' WHERE id=?`, node); e != nil {
		t.Fatal(e)
	}
	if s.Available(ctx, v) || s.Consume(ctx, v) == nil {
		t.Fatal("revoked node still publishes")
	}
	if _, e = db.DB.Exec(`UPDATE nodes SET status='online' WHERE id=?`, node); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.Exec(`UPDATE users SET enabled=0 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if s.Available(ctx, v) || s.Consume(ctx, v) == nil {
		t.Fatal("disabled owner still publishes")
	}
	if _, e = db.DB.Exec(`UPDATE users SET enabled=1 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	now = now.Add(48 * time.Hour)
	if s.Available(ctx, v) || s.Consume(ctx, v) == nil {
		t.Fatal("expired share still publishes")
	}
}
