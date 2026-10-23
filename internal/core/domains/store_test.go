package domains

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	return New(db.DB, time.Now, nil)
}
func event() audit.Event { return audit.Event{RemoteAddr: "unknown", OccurredAt: time.Now()} }
func pair(t *testing.T, names []string, end time.Time) ([]byte, []byte) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	der, e := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: end, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, PublicKey: key.Public()}, key.Public(), key)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})
}
func TestDomainsAndProtectedMaterial(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	d, e := s.SaveDomain(ctx, "", DomainInput{Name: "BÜCHER.example.", Label: "private domain"}, event())
	if e != nil || d.Name != "xn--bcher-kva.example" {
		t.Fatal(d, e)
	}
	if _, e = s.SaveDomain(ctx, d.ID, DomainInput{Name: d.Name, Revision: 99}, event()); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	input := BulkDomains{Domains: []DomainInput{{Name: "one.example"}, {Name: "two.example"}}}
	created, e := s.BulkDomains(ctx, input, event())
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.BulkDomains(ctx, input, event())
	if e != nil || created[0].ID != again[0].ID {
		t.Fatal(again, e)
	}
	input.Domains[1].Name = "/invalid"
	input.Domains[0].Name = "must-not-exist.example"
	if _, e = s.BulkDomains(ctx, input, event()); e == nil {
		t.Fatal("invalid batch accepted")
	}
	all, _ := s.Domains(ctx)
	if len(all) != 3 {
		t.Fatal(all)
	}
	cert, key := pair(t, []string{d.Name}, time.Now().Add(7*24*time.Hour))
	c, e := s.Import(ctx, ImportInput{Name: "Private certificate", CertificatePEM: string(cert), PrivateKeyPEM: string(key), DomainIDs: []string{d.ID}}, event())
	if e != nil || c.Status != "expiring" || c.Revision != 1 {
		t.Fatal(c, e)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), "certificatePem") {
		t.Fatal("material leaked")
	}
	_, wrongKey := pair(t, []string{d.Name}, time.Now().Add(time.Hour))
	if _, e = s.Import(ctx, ImportInput{Name: "Mismatch", CertificatePEM: string(cert), PrivateKeyPEM: string(wrongKey)}, event()); e == nil {
		t.Fatal("mismatched key accepted")
	}
	if e = s.Delete(ctx, "domains", d.ID, ConfirmInput{Confirm: true, Revision: d.Revision}, event()); !errors.Is(e, ErrConflict) {
		t.Fatal("referenced domain deleted", e)
	}
	account, e := s.SaveAccount(ctx, AccountInput{Name: "CA", Email: "admin@example.com", Directory: "letsencrypt-staging", TermsAgreed: true}, event())
	if e != nil {
		t.Fatal(e)
	}
	accounts, e := s.Accounts(ctx)
	if e != nil || accounts[0].Revision != 1 {
		t.Fatal(accounts, e)
	}
	var secret []byte
	if e = s.db.QueryRowContext(ctx, `SELECT key_pem FROM certificate_accounts WHERE id=?`, account.ID).Scan(&secret); e != nil || !strings.Contains(string(secret), "PRIVATE KEY") {
		t.Fatal(e)
	}
	provider, e := s.SaveProvider(ctx, "", ProviderInput{Name: "DNS", Kind: "cloudflare", ZoneID: strings.Repeat("a", 32), APIToken: "NEVER-RETURN-THIS-SECRET"}, event())
	if e != nil {
		t.Fatal(e)
	}
	providers, _ := s.Providers(ctx)
	raw, _ = json.Marshal(providers)
	if strings.Contains(string(raw), "NEVER-RETURN") {
		t.Fatal("provider token leaked")
	}
	_, e = s.SaveProvider(ctx, provider.ID, ProviderInput{Name: "Updated", Kind: "cloudflare", ZoneID: provider.ZoneID, Revision: provider.Revision}, event())
	if e != nil {
		t.Fatal(e)
	}
	var token []byte
	s.db.QueryRowContext(ctx, `SELECT token FROM certificate_dns_providers WHERE id=?`, provider.ID).Scan(&token)
	if string(token) != "NEVER-RETURN-THIS-SECRET" {
		t.Fatal("blank update discarded token")
	}
	var database string
	s.db.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&database)
	info, e := os.Stat(database)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	info, e = os.Stat(filepath.Dir(database))
	if e != nil || info.Mode().Perm() != 0700 {
		t.Fatal(info, e)
	}
}

type fakeIssuer struct {
	t       *testing.T
	fail    bool
	started chan struct{}
	block   bool
}

func (f *fakeIssuer) Issue(ctx context.Context, in IssueInput, names []string) ([]byte, []byte, error) {
	if f.started != nil {
		close(f.started)
		f.started = nil
	}
	if f.block {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if f.fail {
		return nil, nil, errors.New("echo NEVER-RETURN-THIS-SECRET")
	}
	cert, key := pair(f.t, names, time.Now().Add(90*24*time.Hour))
	return cert, key, nil
}
func (f *fakeIssuer) Revoke(context.Context, Certificate, []byte, []byte) error { return nil }
func setupIssue(t *testing.T, s *Store) IssueInput {
	t.Helper()
	ctx := context.Background()
	d, e := s.SaveDomain(ctx, "", DomainInput{Name: "example.test"}, event())
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.SaveAccount(ctx, AccountInput{Name: "Account", Email: "a@example.test", Directory: "letsencrypt-staging", TermsAgreed: true}, event())
	if e != nil {
		t.Fatal(e)
	}
	return IssueInput{Name: "Example", DomainIDs: []string{d.ID}, AccountID: a.ID, Challenge: "http-01", KeyType: "ec256", Confirm: true}
}
func TestJobIdentityRenewalCancellationAndRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	in := setupIssue(t, s)
	f := &fakeIssuer{t: t}
	s.issuer = f
	j, e := s.QueueIssue(ctx, in, "stable-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	repeat, e := s.QueueIssue(ctx, in, "stable-request-key", event())
	if e != nil || repeat.ID != j.ID {
		t.Fatal(repeat, e)
	}
	changed := in
	changed.Name = "Different"
	if _, e = s.QueueIssue(ctx, changed, "stable-request-key", event()); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	s.work(ctx)
	j, _ = s.Job(ctx, j.ID)
	if j.Status != "succeeded" {
		t.Fatal(j)
	}
	c, e := s.Certificate(ctx, j.CertificateID)
	if e != nil {
		t.Fatal(e)
	}
	originalCert, originalKey, _ := s.Material(ctx, c.ID)
	f.fail = true
	renew, e := s.QueueRenew(ctx, c.ID, ConfirmInput{Confirm: true, Revision: c.Revision}, "renew-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	s.work(ctx)
	renew, _ = s.Job(ctx, renew.ID)
	if renew.Status != "failed" || strings.Contains(renew.Error, "NEVER-RETURN") {
		t.Fatal(renew)
	}
	cert, key, _ := s.Material(ctx, c.ID)
	if string(cert) != string(originalCert) || string(key) != string(originalKey) {
		t.Fatal("failed renewal destroyed original")
	}
	f.fail = false
	retry, e := s.Retry(ctx, renew.ID, "retry-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	s.work(ctx)
	retry, _ = s.Job(ctx, retry.ID)
	if retry.Status != "succeeded" {
		t.Fatal(retry)
	}
	c, _ = s.Certificate(ctx, c.ID)
	if c.Revision != 2 {
		t.Fatal(c)
	}
	started := make(chan struct{})
	f.started = started
	f.block = true
	cancelJob, e := s.QueueRenew(ctx, c.ID, ConfirmInput{Confirm: true, Revision: c.Revision}, "cancel-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { s.work(ctx); close(done) }()
	<-started
	if _, e = s.Cancel(ctx, cancelJob.ID, event()); e != nil {
		t.Fatal(e)
	}
	<-done
	cancelJob, _ = s.Job(ctx, cancelJob.ID)
	if cancelJob.Status != "canceled" {
		t.Fatal(cancelJob)
	}
	c, _ = s.Certificate(ctx, c.ID)
	if c.Revision != 2 {
		t.Fatal("cancel changed certificate", c)
	}
	f.block = false
	recovered, e := s.Retry(ctx, cancelJob.ID, "recover-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	s.db.ExecContext(ctx, `UPDATE certificate_jobs SET status='running' WHERE id=?`, recovered.ID)
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	recovered, _ = s.Job(ctx, recovered.ID)
	if recovered.Status != "failed" {
		t.Fatal(recovered)
	}
	var auditText sql.NullString
	if e = s.db.QueryRowContext(ctx, `SELECT group_concat(action) FROM audit_entries`).Scan(&auditText); e != nil || !strings.Contains(auditText.String, "certificate_job_submit") {
		t.Fatal(auditText, e)
	}
}

func TestExpiryDeliveryBackoffAndAutomaticRenewal(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	clock := time.Now().UTC()
	s.now = func() time.Time { return clock }
	one := "10000000-0000-4000-8000-000000000001"
	two := "10000000-0000-4000-8000-000000000002"
	for _, id := range []string{one, two} {
		if _, e := s.db.ExecContext(ctx, `INSERT INTO alert_channels(id,name,kind,config_json,enabled,revision,created_at,updated_at) VALUES(?,?,'webhook','{}',1,1,?,?)`, id, id, clock.Unix(), clock.Unix()); e != nil {
			t.Fatal(e)
		}
	}
	cert, key := pair(t, []string{"notice.example"}, clock.Add(24*time.Hour))
	c, e := s.Import(ctx, ImportInput{Name: "Expiry notice", CertificatePEM: string(cert), PrivateKeyPEM: string(key), ChannelIDs: []string{one, two}}, event())
	if e != nil {
		t.Fatal(e)
	}
	calls := map[string]int{}
	fail := true
	notify := func(_ context.Context, id, payload string) error {
		calls[id]++
		if !strings.Contains(payload, c.ID) {
			t.Fatal("notice missing certificate identity")
		}
		if id == two && fail {
			return errors.New("simulated transport loss")
		}
		return nil
	}
	s.maintain(ctx, notify)
	s.maintain(ctx, notify)
	if calls[one] != 1 || calls[two] != 1 {
		t.Fatal("backoff or dedup failed", calls)
	}
	clock = clock.Add(6 * time.Minute)
	fail = false
	s.maintain(ctx, notify)
	if calls[one] != 1 || calls[two] != 2 {
		t.Fatal("successful channel was resent", calls)
	}
	updated, _ := s.Certificate(ctx, c.ID)
	if updated.NotificationError != "" {
		t.Fatal(updated)
	}
	if _, e = s.UpdatePolicy(ctx, c.ID, PolicyInput{Policy: Policy{AutoRenew: true}, Revision: c.Revision}, event()); e == nil {
		t.Fatal("imported certificate enabled ACME renewal")
	}
	in := setupIssue(t, s)
	in.AutoRenew = true
	s.issuer = &fakeIssuer{t: t}
	j, e := s.QueueIssue(ctx, in, "scheduled-issue-key", event())
	if e != nil {
		t.Fatal(e)
	}
	s.work(ctx)
	j, _ = s.Job(ctx, j.ID)
	if j.Status != "succeeded" {
		t.Fatal(j)
	}
	clock = clock.Add(70 * 24 * time.Hour)
	s.maintain(ctx, nil)
	s.maintain(ctx, nil)
	var queued int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_jobs WHERE kind='renew' AND status='queued'`).Scan(&queued); e != nil || queued != 1 {
		t.Fatal(queued, e)
	}
}

func TestProviderCannotChangeWhileDNSCleanupPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, e := s.SaveProvider(ctx, "", ProviderInput{Name: "DNS", Kind: "cloudflare", ZoneID: strings.Repeat("a", 32), APIToken: "private"}, event())
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO certificate_dns_challenges(id,job_id,provider_id,name,value) VALUES('cleanup','job',?,'_acme-challenge.example','value')`, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.SaveProvider(ctx, p.ID, ProviderInput{Name: "changed", Kind: "cloudflare", ZoneID: strings.Repeat("b", 32), Revision: p.Revision}, event())
	if !errors.Is(e, ErrConflict) {
		t.Fatal("changed DNS cleanup destination", e)
	}
}
