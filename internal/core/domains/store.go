package domains

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/google/uuid"
	"golang.org/x/net/idna"
)

var ErrConflict = errors.New("resource changed or is in use; refresh its revision and retry")
var ErrInvalid = errors.New("invalid domain or certificate input")
var hexZone = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

type Issuer interface {
	Issue(context.Context, IssueInput, []string) ([]byte, []byte, error)
	Revoke(context.Context, Certificate, []byte, []byte) error
}

// Internal HTTP customization is an injection point for isolated protocol tests,
// never an API-controlled directory or DNS-provider endpoint.
type Store struct {
	db         *sql.DB
	now        func() time.Time
	issuer     Issuer
	mu         sync.Mutex
	active     map[string]context.CancelFunc
	challenges map[string]challenge
}

func New(db *sql.DB, now func() time.Time, issuer Issuer) *Store {
	if now == nil {
		now = time.Now
	}
	s := &Store{db: db, now: now, active: map[string]context.CancelFunc{}, challenges: map[string]challenge{}}
	s.issuer = issuer
	if s.issuer == nil {
		s.issuer = &acmeIssuer{store: s}
	}
	return s
}
func NormalizeName(name string) (string, error) {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	wildcard := strings.HasPrefix(name, "*.")
	name = strings.TrimPrefix(name, "*.")
	name, e := idna.Lookup.ToASCII(name)
	name = strings.ToLower(name)
	if e != nil || len(name) > 253 || net.ParseIP(name) != nil || !strings.Contains(name, ".") {
		return "", ErrInvalid
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalid
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrInvalid
			}
		}
	}
	if wildcard {
		name = "*." + name
	}
	return name, nil
}
func validID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }
func validateText(name string, max int) bool {
	return len(name) <= max && !strings.ContainsAny(name, "\x00\r")
}
func record(ctx context.Context, tx audit.Execer, event audit.Event, action string, kind audit.TargetKind, id string) error {
	event.Action = action
	event.Outcome = "succeeded"
	event.Target = audit.Target{Kind: kind, ID: id}
	return audit.Record(ctx, tx, event)
}
func (s *Store) Domains(ctx context.Context) ([]Domain, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,name,label,notes,revision,created_at,updated_at FROM managed_domains ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		var c, u int64
		if e = rows.Scan(&d.ID, &d.Name, &d.Label, &d.Notes, &d.Revision, &c, &u); e != nil {
			return nil, e
		}
		d.CreatedAt = time.Unix(c, 0).UTC()
		d.UpdatedAt = time.Unix(u, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) Domain(ctx context.Context, id string) (Domain, error) {
	var d Domain
	var c, u int64
	e := s.db.QueryRowContext(ctx, `SELECT id,name,label,notes,revision,created_at,updated_at FROM managed_domains WHERE id=?`, id).Scan(&d.ID, &d.Name, &d.Label, &d.Notes, &d.Revision, &c, &u)
	d.CreatedAt = time.Unix(c, 0).UTC()
	d.UpdatedAt = time.Unix(u, 0).UTC()
	return d, e
}
func normalizeDomain(in *DomainInput) error {
	var e error
	in.Name, e = NormalizeName(in.Name)
	if e != nil || !validateText(in.Label, 200) || !validateText(in.Notes, 4096) {
		return ErrInvalid
	}
	return nil
}
func (s *Store) SaveDomain(ctx context.Context, id string, in DomainInput, event audit.Event) (Domain, error) {
	if e := normalizeDomain(&in); e != nil {
		return Domain{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Domain{}, e
	}
	defer tx.Rollback()
	now := s.now().Unix()
	if id == "" {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_domains`).Scan(&count); e != nil {
			return Domain{}, e
		}
		if count >= 5000 {
			return Domain{}, errors.New("domain inventory limit is 5000")
		}
		id = uuid.NewString()
		_, e = tx.ExecContext(ctx, `INSERT INTO managed_domains VALUES(?,?,?,?,1,?,?)`, id, in.Name, in.Label, in.Notes, now, now)
	} else {
		var currentName string
		if e = tx.QueryRowContext(ctx, `SELECT name FROM managed_domains WHERE id=? AND revision=?`, id, in.Revision).Scan(&currentName); e != nil {
			return Domain{}, ErrConflict
		}
		if currentName != in.Name {
			return Domain{}, errors.New("domain name is immutable; create a separate domain")
		}
		var result sql.Result
		result, e = tx.ExecContext(ctx, `UPDATE managed_domains SET label=?,notes=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, in.Label, in.Notes, now, id, in.Revision)
		if e == nil {
			n, _ := result.RowsAffected()
			if n != 1 {
				e = ErrConflict
			}
		}
	}
	if e != nil {
		return Domain{}, fmt.Errorf("domain save failed (name must be unique): %w", ErrInvalid)
	}
	if e = record(ctx, tx, event, "domain_save", audit.TargetDomain, id); e != nil {
		return Domain{}, e
	}
	if e = tx.Commit(); e != nil {
		return Domain{}, e
	}
	return s.Domain(ctx, id)
}

// Bulk creation is all-or-nothing and deterministic. Existing names are returned
// only when the supplied label/notes match; a retry cannot silently overwrite.
func (s *Store) BulkDomains(ctx context.Context, in BulkDomains, event audit.Event) ([]Domain, error) {
	if len(in.Domains) == 0 || len(in.Domains) > 500 {
		return nil, errors.New("provide 1..500 domains")
	}
	for i := range in.Domains {
		if e := normalizeDomain(&in.Domains[i]); e != nil {
			return nil, e
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	out := []Domain{}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_domains`).Scan(&count); e != nil {
		return nil, e
	}
	for _, d := range in.Domains {
		var existing Domain
		var c, u int64
		e = tx.QueryRowContext(ctx, `SELECT id,label,notes,revision,created_at,updated_at FROM managed_domains WHERE name=?`, d.Name).Scan(&existing.ID, &existing.Label, &existing.Notes, &existing.Revision, &c, &u)
		if e == nil {
			if existing.Label != d.Label || existing.Notes != d.Notes {
				return nil, ErrConflict
			}
			existing.Name = d.Name
			existing.CreatedAt = time.Unix(c, 0).UTC()
			existing.UpdatedAt = time.Unix(u, 0).UTC()
			out = append(out, existing)
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return nil, e
		}
		count++
		if count > 5000 {
			return nil, errors.New("domain inventory limit is 5000")
		}
		now := s.now().UTC()
		id := uuid.NewString()
		if _, e = tx.ExecContext(ctx, `INSERT INTO managed_domains VALUES(?,?,?,?,1,?,?)`, id, d.Name, d.Label, d.Notes, now.Unix(), now.Unix()); e != nil {
			return nil, e
		}
		if e = record(ctx, tx, event, "domain_save", audit.TargetDomain, id); e != nil {
			return nil, e
		}
		d.Revision = 1
		out = append(out, Domain{ID: id, DomainInput: d, CreatedAt: now, UpdatedAt: now})
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,config_json,revision,account_url FROM certificate_accounts ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var raw, url string
		if e = rows.Scan(&a.ID, &raw, &a.Revision, &url); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &a.AccountInput); e != nil {
			return nil, e
		}
		a.Registered = url != ""
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) SaveAccount(ctx context.Context, in AccountInput, event audit.Event) (Account, error) {
	a, e := mail.ParseAddress(in.Email)
	if e != nil || a.Address != in.Email || len(in.Email) > 254 || strings.TrimSpace(in.Name) == "" || !validateText(in.Name, 200) || !in.TermsAgreed || (in.Directory != "letsencrypt" && in.Directory != "letsencrypt-staging") {
		return Account{}, ErrInvalid
	}
	key, e := newKey("ec256")
	if e != nil {
		return Account{}, e
	}
	in.Revision = 1
	raw, _ := json.Marshal(in)
	id := uuid.NewString()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Account{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_accounts`).Scan(&count); e != nil {
		return Account{}, e
	}
	if count >= 500 {
		return Account{}, errors.New("ACME account limit is 500")
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO certificate_accounts(id,config_json,key_pem,revision) VALUES(?,?,?,1)`, id, string(raw), key); e != nil {
		return Account{}, e
	}
	if e = record(ctx, tx, event, "certificate_account_create", audit.TargetCertificateAccount, id); e != nil {
		return Account{}, e
	}
	if e = tx.Commit(); e != nil {
		return Account{}, e
	}
	return Account{ID: id, AccountInput: in}, nil
}
func (s *Store) Providers(ctx context.Context) ([]Provider, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,name,kind,zone_id,revision FROM certificate_dns_providers ORDER BY name`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Provider{}
	for rows.Next() {
		var p Provider
		if e = rows.Scan(&p.ID, &p.Name, &p.Kind, &p.ZoneID, &p.Revision); e != nil {
			return nil, e
		}
		p.HasToken = true
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) SaveProvider(ctx context.Context, id string, in ProviderInput, event audit.Event) (Provider, error) {
	if strings.TrimSpace(in.Name) == "" || !validateText(in.Name, 200) || in.Kind != "cloudflare" || !hexZone.MatchString(in.ZoneID) || len(in.APIToken) > 4096 || strings.ContainsAny(in.APIToken, "\r\n\x00") {
		return Provider{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Provider{}, e
	}
	defer tx.Rollback()
	revision := int64(1)
	if id == "" {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_dns_providers`).Scan(&count); e != nil {
			return Provider{}, e
		}
		if count >= 500 {
			return Provider{}, errors.New("DNS provider limit is 500")
		}
		if in.APIToken == "" {
			return Provider{}, errors.New("API token is required")
		}
		id = uuid.NewString()
		_, e = tx.ExecContext(ctx, `INSERT INTO certificate_dns_providers VALUES(?,?,?,?,?,1)`, id, in.Name, in.Kind, strings.ToLower(in.ZoneID), []byte(in.APIToken))
	} else {
		var busy int
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM certificate_dns_challenges WHERE provider_id=?) + (SELECT COUNT(*) FROM certificate_jobs WHERE status IN ('queued','running') AND json_extract(input_json,'$.providerId')=?)`, id, id).Scan(&busy); e != nil {
			return Provider{}, e
		}
		if busy > 0 {
			return Provider{}, ErrConflict
		}
		var token []byte
		var rev int64
		if e = tx.QueryRowContext(ctx, `SELECT token,revision FROM certificate_dns_providers WHERE id=?`, id).Scan(&token, &rev); e != nil {
			return Provider{}, e
		}
		if rev != in.Revision {
			return Provider{}, ErrConflict
		}
		if in.APIToken != "" {
			token = []byte(in.APIToken)
		}
		revision = rev + 1
		_, e = tx.ExecContext(ctx, `UPDATE certificate_dns_providers SET name=?,kind=?,zone_id=?,token=?,revision=? WHERE id=?`, in.Name, in.Kind, strings.ToLower(in.ZoneID), token, revision, id)
	}
	if e != nil {
		return Provider{}, e
	}
	if e = record(ctx, tx, event, "certificate_provider_save", audit.TargetDNSProvider, id); e != nil {
		return Provider{}, e
	}
	if e = tx.Commit(); e != nil {
		return Provider{}, e
	}
	return Provider{ID: id, Name: in.Name, Kind: in.Kind, ZoneID: strings.ToLower(in.ZoneID), HasToken: true, Revision: revision}, nil
}
func (s *Store) Names(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("provide 1..100 domain IDs")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validID(id) || seen[id] {
			return nil, ErrInvalid
		}
		seen[id] = true
		d, e := s.Domain(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, d.Name)
	}
	return out, nil
}
func newKey(kind string) ([]byte, error) {
	var key crypto.Signer
	var e error
	switch kind {
	case "ec256":
		key, e = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "rsa2048":
		key, e = rsa.GenerateKey(rand.Reader, 2048)
	default:
		return nil, ErrInvalid
	}
	if e != nil {
		return nil, e
	}
	der, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}
func signer(raw []byte) (crypto.Signer, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, ErrInvalid
	}
	var key any
	var e error
	switch block.Type {
	case "PRIVATE KEY":
		key, e = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, e = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, e = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, ErrInvalid
	}
	if e != nil {
		return nil, ErrInvalid
	}
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		if k.Curve.Params().BitSize < 256 {
			return nil, ErrInvalid
		}
		return k, nil
	case *rsa.PrivateKey:
		if k.N.BitLen() < 2048 {
			return nil, ErrInvalid
		}
		return k, nil
	}
	return nil, ErrInvalid
}
func parseCertificate(certPEM, keyPEM []byte) (Certificate, error) {
	if len(certPEM) == 0 || len(certPEM) > MaxPEMBytes || len(keyPEM) == 0 || len(keyPEM) > MaxPEMBytes {
		return Certificate{}, ErrInvalid
	}
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil || len(pair.Certificate) == 0 || len(pair.Certificate) > 10 {
		return Certificate{}, errors.New("certificate chain and private key must be valid and match")
	}
	if _, e = signer(keyPEM); e != nil {
		return Certificate{}, e
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil || leaf.IsCA || len(leaf.DNSNames) == 0 {
		return Certificate{}, errors.New("a leaf certificate with DNS subject alternative names is required")
	}
	for i := 0; i < len(pair.Certificate)-1; i++ {
		child, childErr := x509.ParseCertificate(pair.Certificate[i])
		parent, e := x509.ParseCertificate(pair.Certificate[i+1])
		if e != nil || childErr != nil || child.CheckSignatureFrom(parent) != nil {
			return Certificate{}, errors.New("certificate chain is not ordered or signatures do not match")
		}
	}
	rest := certPEM
	for len(strings.TrimSpace(string(rest))) > 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(rest)), "-----BEGIN CERTIFICATE-----") {
			return Certificate{}, ErrInvalid
		}
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return Certificate{}, errors.New("certificate PEM must contain only CERTIFICATE blocks")
		}
	}
	fp := sha256.Sum256(leaf.Raw)
	kind := ""
	switch key := leaf.PublicKey.(type) {
	case *ecdsa.PublicKey:
		kind = fmt.Sprintf("ec%d", key.Curve.Params().BitSize)
	case *rsa.PublicKey:
		kind = fmt.Sprintf("rsa%d", key.N.BitLen())
	}
	return Certificate{DNSNames: leaf.DNSNames, Issuer: leaf.Issuer.String(), Serial: leaf.SerialNumber.String(), Fingerprint: hex.EncodeToString(fp[:]), NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), KeyType: kind}, nil
}
