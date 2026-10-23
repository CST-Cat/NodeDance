package domains

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/google/uuid"
)

func normalizePolicy(p *Policy) error {
	if p.RenewBeforeDays == 0 {
		p.RenewBeforeDays = 30
	}
	if p.NotifyBeforeDays == 0 {
		p.NotifyBeforeDays = 14
	}
	if p.RenewBeforeDays < 1 || p.RenewBeforeDays > 90 || p.NotifyBeforeDays < 1 || p.NotifyBeforeDays > 90 || len(p.ChannelIDs) > 20 {
		return ErrInvalid
	}
	if p.ChannelIDs == nil {
		p.ChannelIDs = []string{}
	}
	seen := map[string]bool{}
	for _, id := range p.ChannelIDs {
		if !validID(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}
func (s *Store) certificateState(c *Certificate, revoked bool) {
	c.DaysRemaining = int(c.NotAfter.Sub(s.now()).Hours() / 24)
	switch {
	case revoked:
		c.Status = "revoked"
	case s.now().Before(c.NotBefore):
		c.Status = "not-yet-valid"
	case !s.now().Before(c.NotAfter):
		c.Status = "expired"
	case c.NotAfter.Sub(s.now()) <= time.Duration(c.NotifyBeforeDays)*24*time.Hour:
		c.Status = "expiring"
	default:
		c.Status = "valid"
	}
}
func scanCertificate(row interface{ Scan(...any) error }, s *Store) (Certificate, error) {
	var c Certificate
	var raw string
	var revoked bool
	var created, updated int64
	e := row.Scan(&c.ID, &raw, &c.Revision, &revoked, &created, &updated, &c.NotificationError)
	if e != nil {
		return c, e
	}
	rev := c.Revision
	notification := c.NotificationError
	if e = json.Unmarshal([]byte(raw), &c); e != nil {
		return c, e
	}
	c.Revision = rev
	c.NotificationError = notification
	c.CreatedAt = time.Unix(created, 0).UTC()
	c.UpdatedAt = time.Unix(updated, 0).UTC()
	s.certificateState(&c, revoked)
	return c, nil
}

const certificateColumns = `id,metadata_json,revision,revoked,created_at,updated_at,notification_error`

func (s *Store) Certificate(ctx context.Context, id string) (Certificate, error) {
	return scanCertificate(s.db.QueryRowContext(ctx, `SELECT `+certificateColumns+` FROM managed_certificates WHERE id=?`, id), s)
}
func (s *Store) Certificates(ctx context.Context) ([]Certificate, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+certificateColumns+` FROM managed_certificates ORDER BY created_at DESC,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Certificate{}
	for rows.Next() {
		c, e := scanCertificate(rows, s)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) Material(ctx context.Context, id string) ([]byte, []byte, error) {
	var cert, key []byte
	e := s.db.QueryRowContext(ctx, `SELECT certificate_pem,key_pem FROM managed_certificates WHERE id=?`, id).Scan(&cert, &key)
	return cert, key, e
}
func (s *Store) validateCoverage(ctx context.Context, c Certificate, certPEM, keyPEM []byte) error {
	names, e := s.Names(ctx, c.DomainIDs)
	if e != nil && len(c.DomainIDs) != 0 {
		return e
	}
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		return ErrInvalid
	}
	leaf, e := x509.ParseCertificate(pair.Certificate[0])
	if e != nil {
		return ErrInvalid
	}
	for _, name := range names {
		if strings.HasPrefix(name, "*.") {
			found := false
			for _, san := range leaf.DNSNames {
				if strings.EqualFold(san, name) {
					found = true
				}
			}
			if !found {
				return errors.New("certificate must contain the selected wildcard SAN")
			}
		} else if leaf.VerifyHostname(name) != nil {
			return errors.New("certificate does not cover every selected domain")
		}
	}
	return nil
}
func (s *Store) Import(ctx context.Context, in ImportInput, event audit.Event) (Certificate, error) {
	if strings.TrimSpace(in.Name) == "" || !validateText(in.Name, 200) {
		return Certificate{}, ErrInvalid
	}
	c, e := parseCertificate([]byte(in.CertificatePEM), []byte(in.PrivateKeyPEM))
	if e != nil {
		return c, e
	}
	c.Name = in.Name
	c.Source = "imported"
	c.DomainIDs = in.DomainIDs
	if c.DomainIDs == nil {
		c.DomainIDs = []string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Policy = Policy{NotifyBeforeDays: in.NotifyBeforeDays, ChannelIDs: in.ChannelIDs}
	if e = s.policyChannels(ctx, c.ChannelIDs); e != nil {
		return c, e
	}
	if e = normalizePolicy(&c.Policy); e != nil {
		return c, e
	}
	if e = s.validateCoverage(ctx, c, []byte(in.CertificatePEM), []byte(in.PrivateKeyPEM)); e != nil {
		return c, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return c, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_certificates`).Scan(&count); e != nil {
		return c, e
	}
	if count >= 5000 {
		return c, errors.New("certificate inventory limit is 5000")
	}
	c.ID = uuid.NewString()
	c.Revision = 1
	raw, _ := json.Marshal(c)
	now := s.now().Unix()
	if _, e = tx.ExecContext(ctx, `INSERT INTO managed_certificates(id,metadata_json,certificate_pem,key_pem,revision,created_at,updated_at) VALUES(?,?,?,?,1,?,?)`, c.ID, string(raw), []byte(in.CertificatePEM), []byte(in.PrivateKeyPEM), now, now); e != nil {
		return c, e
	}
	if e = record(ctx, tx, event, "certificate_import", audit.TargetCertificate, c.ID); e != nil {
		return c, e
	}
	if e = tx.Commit(); e != nil {
		return c, e
	}
	return s.Certificate(ctx, c.ID)
}
func (s *Store) UpdatePolicy(ctx context.Context, id string, in PolicyInput, event audit.Event) (Certificate, error) {
	if e := normalizePolicy(&in.Policy); e != nil {
		return Certificate{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.policyChannels(ctx, in.ChannelIDs); e != nil {
		return Certificate{}, e
	}
	var active int
	if e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_jobs WHERE certificate_id=? AND status IN ('queued','running')`, id).Scan(&active); e != nil {
		return Certificate{}, e
	}
	if active > 0 {
		return Certificate{}, ErrConflict
	}
	c, e := s.Certificate(ctx, id)
	if e != nil {
		return c, e
	}
	if c.Revision != in.Revision {
		return c, ErrConflict
	}
	if in.AutoRenew && (c.Source != "acme" || c.Status == "revoked") {
		return c, errors.New("only active ACME certificates support automatic renewal")
	}
	c.Policy = in.Policy
	raw, _ := json.Marshal(c)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return c, e
	}
	defer tx.Rollback()
	r, e := tx.ExecContext(ctx, `UPDATE managed_certificates SET metadata_json=?,revision=revision+1,updated_at=?,notification_bucket='' WHERE id=? AND revision=?`, string(raw), s.now().Unix(), id, in.Revision)
	if e != nil {
		return c, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return c, ErrConflict
	}
	if e = record(ctx, tx, event, "certificate_policy", audit.TargetCertificate, id); e != nil {
		return c, e
	}
	if e = tx.Commit(); e != nil {
		return c, e
	}
	return s.Certificate(ctx, id)
}
func (s *Store) Delete(ctx context.Context, kind, id string, in ConfirmInput, event audit.Event) error {
	if !in.Confirm {
		return errors.New("explicit confirmation is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	table, action, target := "", "", audit.TargetDomain
	switch kind {
	case "domains":
		table = "managed_domains"
		action = "domain_delete"
	case "certificates":
		table = "managed_certificates"
		action = "certificate_delete"
		target = audit.TargetCertificate
	case "accounts":
		table = "certificate_accounts"
		action = "certificate_account_delete"
		target = audit.TargetCertificateAccount
	case "providers":
		table = "certificate_dns_providers"
		action = "certificate_provider_delete"
		target = audit.TargetDNSProvider
	default:
		return ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var used int
	if kind == "domains" {
		e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_certificates,json_each(managed_certificates.metadata_json,'$.domainIds') WHERE json_each.value=?`, id).Scan(&used)
	} else if kind == "accounts" || kind == "providers" {
		field := "accountId"
		if kind == "providers" {
			field = "providerId"
		}
		e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_certificates WHERE json_extract(metadata_json,?)=?`, "$."+field, id).Scan(&used)
	}
	if e != nil {
		return e
	}
	if used > 0 {
		return ErrConflict
	}
	if kind == "providers" {
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_dns_challenges WHERE provider_id=?`, id).Scan(&used); e != nil {
			return e
		}
		if used > 0 {
			return ErrConflict
		}
	}
	e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_jobs WHERE status IN ('queued','running') AND (certificate_id=? OR json_extract(input_json,'$.accountId')=? OR json_extract(input_json,'$.providerId')=? OR EXISTS (SELECT 1 FROM json_each(certificate_jobs.input_json,'$.domainIds') WHERE value=?))`, id, id, id, id).Scan(&used)
	if e != nil {
		return e
	}
	if used > 0 {
		return ErrConflict
	}
	r, e := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id=? AND revision=?`, id, in.Revision)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	if e = record(ctx, tx, event, action, target, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) CheckDNS(ctx context.Context, id string) (DNSResult, error) {
	d, e := s.Domain(ctx, id)
	if e != nil {
		return DNSResult{}, e
	}
	name := strings.TrimPrefix(d.Name, "*.")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := DNSResult{Name: d.Name, Addresses: []string{}, CheckedAt: s.now().UTC()}
	out.Addresses, e = netLookupHost(ctx, name)
	if e != nil {
		out.Error = "DNS lookup failed"
		out.Addresses = []string{}
	}
	out.CNAME, _ = netLookupCNAME(ctx, name)
	return out, nil
}

// Resolver variables remain package-private; tests run against protocol-only fixtures.
var defaultResolver = net.DefaultResolver
var netLookupHost = func(ctx context.Context, name string) ([]string, error) { return defaultResolver.LookupHost(ctx, name) }
var netLookupCNAME = func(ctx context.Context, name string) (string, error) { return defaultResolver.LookupCNAME(ctx, name) }

func (s *Store) validateIssue(ctx context.Context, in *IssueInput) error {
	if !in.Confirm {
		return errors.New("certificate issuance requires confirmation")
	}
	if strings.TrimSpace(in.Name) == "" || !validateText(in.Name, 200) || !validID(in.AccountID) || (in.KeyType != "ec256" && in.KeyType != "rsa2048") || (in.Challenge != "http-01" && in.Challenge != "dns-01") {
		return ErrInvalid
	}
	if e := normalizePolicy(&in.Policy); e != nil {
		return e
	}
	names, e := s.Names(ctx, in.DomainIDs)
	if e != nil {
		return e
	}
	var n int
	if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_accounts WHERE id=?`, in.AccountID).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return sql.ErrNoRows
	}
	if in.Challenge == "dns-01" {
		if !validID(in.ProviderID) {
			return errors.New("DNS-01 requires a DNS provider")
		}
		if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_dns_providers WHERE id=?`, in.ProviderID).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return sql.ErrNoRows
		}
	} else {
		if in.ProviderID != "" {
			return errors.New("HTTP-01 does not use a DNS provider")
		}
		for _, name := range names {
			if strings.HasPrefix(name, "*.") {
				return errors.New("wildcard certificates require DNS-01")
			}
		}
	}
	return nil
}
func (s *Store) policyChannels(ctx context.Context, ids []string) error {
	for _, id := range ids {
		var enabled bool
		if e := s.db.QueryRowContext(ctx, `SELECT enabled FROM alert_channels WHERE id=? AND deleted_at IS NULL`, id).Scan(&enabled); e != nil || !enabled {
			return errors.New("notification channel must exist and be enabled")
		}
	}
	return nil
}
func safeJobError(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, context.Canceled) {
		return "operation canceled"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "certificate operation timed out"
	}
	return fmt.Sprintf("certificate operation failed: %s", redactError(e.Error()))
}

// External services may echo credentials or unbounded response text. Public jobs
// expose stable failure classes; detailed secrets are never put in audit logs.
func redactError(message string) string {
	switch {
	case strings.Contains(message, "DNS propagation"):
		return "DNS propagation timed out"
	case strings.Contains(message, "HTTP-01"):
		return "HTTP-01 challenge could not be completed"
	case strings.Contains(message, "ACME authorization"):
		return "ACME authorization failed"
	case strings.Contains(message, "canceled"):
		return "operation canceled"
	default:
		return "check domain routing, DNS provider permissions and CA account; retry with a new request identity"
	}
}

// Export binds explicit revision confirmation, material read and audit to one
// transaction. Concurrent renewals cannot swap the confirmed material.
func (s *Store) Export(ctx context.Context, id string, in ConfirmInput, event audit.Event) ([]byte, []byte, error) {
	if !in.Confirm {
		return nil, nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, nil, e
	}
	defer tx.Rollback()
	var revision int64
	var cert, key []byte
	if e = tx.QueryRowContext(ctx, `SELECT revision,certificate_pem,key_pem FROM managed_certificates WHERE id=?`, id).Scan(&revision, &cert, &key); e != nil {
		return nil, nil, e
	}
	if revision != in.Revision {
		return nil, nil, ErrConflict
	}
	if e = record(ctx, tx, event, "certificate_export", audit.TargetCertificate, id); e != nil {
		return nil, nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, nil, e
	}
	return cert, key, nil
}
