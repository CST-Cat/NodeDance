package domains

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func TestSecretMigrationEncryptsLegacyPlaintextRows(t *testing.T) {
	ctx := context.Background()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	key := bytes.Repeat([]byte{9}, 32)
	if _, e = New(db.DB, time.Now, nil, key); e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.ExecContext(ctx, `INSERT INTO certificate_dns_providers VALUES('legacy','Legacy','cloudflare','zone','LEGACY-TOKEN',1)`); e != nil {
		t.Fatal(e)
	}
	first, e := New(db.DB, time.Now, nil, key)
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := first.seal(aadDNSProviderToken, []byte("MODERN-TOKEN"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.DB.ExecContext(ctx, `INSERT INTO certificate_dns_providers VALUES('modern','Modern','cloudflare','zone',?,2)`, sealed); e != nil {
		t.Fatal(e)
	}
	// A fresh store on the same database runs the idempotent migration again
	// without touching already sealed values.
	second, e := New(db.DB, time.Now, nil, key)
	if e != nil {
		t.Fatal(e)
	}
	rows := map[string]string{}
	for _, id := range []string{"legacy", "modern"} {
		var token []byte
		if e = db.DB.QueryRowContext(ctx, `SELECT token FROM certificate_dns_providers WHERE id=?`, id).Scan(&token); e != nil {
			t.Fatal(e)
		}
		rows[id] = string(token)
	}
	if strings.Contains(rows["legacy"], "LEGACY-TOKEN") {
		t.Fatal("legacy token was not migrated to ciphertext")
	}
	plain, e := second.open(aadDNSProviderToken, []byte(rows["legacy"]))
	if e != nil || string(plain) != "LEGACY-TOKEN" {
		t.Fatalf("migrated token does not decrypt: %q %v", plain, e)
	}
	plain, e = second.open(aadDNSProviderToken, []byte(rows["modern"]))
	if e != nil || string(plain) != "MODERN-TOKEN" {
		t.Fatalf("previously sealed token changed: %q %v", plain, e)
	}
}
