package domains

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/acme"
)

type challenge struct {
	Host, Response string
	Expires        time.Time
}
type jobContextKey struct{}

var challengeToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)

// HTTPChallenge serves only an active host-bound ACME token. It exposes neither
// account keys nor inventory, and cannot be used as a filesystem path.
func (s *Store) HTTPChallenge(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "/.well-known/acme-challenge/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	token := strings.TrimPrefix(r.URL.Path, prefix)
	if r.Method != "GET" || r.URL.RawQuery != "" || r.URL.RawPath != "" || !challengeToken.MatchString(token) {
		http.NotFound(w, r)
		return true
	}
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	s.mu.Lock()
	c, ok := s.challenges[token]
	s.mu.Unlock()
	if !ok || c.Host != host || !s.now().Before(c.Expires) {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, c.Response)
	return true
}

type acmeIssuer struct {
	store               *Store
	client              *http.Client
	directory           func(string) string
	cloudflareBase      string
	lookupTXT           func(context.Context, string) ([]string, error)
	propagationInterval time.Duration
}

func (a *acmeIssuer) httpClient() *http.Client {
	if a.client != nil {
		return a.client
	}
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("external redirect is not allowed") }}
}
func (a *acmeIssuer) account(ctx context.Context, id string) (*acme.Client, error) {
	var raw, uri string
	var key []byte
	if e := a.store.db.QueryRowContext(ctx, `SELECT config_json,key_pem,account_url FROM certificate_accounts WHERE id=?`, id).Scan(&raw, &key, &uri); e != nil {
		return nil, e
	}
	var in AccountInput
	if e := json.Unmarshal([]byte(raw), &in); e != nil {
		return nil, e
	}
	signing, e := signer(key)
	if e != nil {
		return nil, e
	}
	directory := "https://acme-v02.api.letsencrypt.org/directory"
	if in.Directory == "letsencrypt-staging" {
		directory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	if a.directory != nil {
		directory = a.directory(in.Directory)
	}
	c := &acme.Client{Key: signing, DirectoryURL: directory, HTTPClient: a.httpClient(), KID: acme.KeyID(uri), UserAgent: "NodeDance"}
	if uri == "" {
		account, e := c.Register(ctx, &acme.Account{Contact: []string{"mailto:" + in.Email}}, func(string) bool { return in.TermsAgreed })
		if e != nil {
			return nil, e
		}
		if _, e = a.store.db.ExecContext(ctx, `UPDATE certificate_accounts SET account_url=? WHERE id=?`, account.URI, id); e != nil {
			return nil, e
		}
		c.KID = acme.KeyID(account.URI)
	}
	return c, nil
}
func (a *acmeIssuer) Issue(ctx context.Context, in IssueInput, names []string) (certificate, key []byte, err error) {
	c, e := a.account(ctx, in.AccountID)
	if e != nil {
		return nil, nil, e
	}
	key, e = newKey(in.KeyType)
	if e != nil {
		return nil, nil, e
	}
	signing, e := signer(key)
	if e != nil {
		return nil, nil, e
	}
	identifiers := []acme.AuthzID{}
	for _, name := range names {
		identifiers = append(identifiers, acme.AuthzID{Type: "dns", Value: name})
	}
	order, e := c.AuthorizeOrder(ctx, identifiers)
	if e != nil {
		return nil, nil, e
	}
	cleanups := []func() error{}
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			_ = cleanups[i]()
		}
	}()
	for _, authorization := range order.AuthzURLs {
		auth, e := c.GetAuthorization(ctx, authorization)
		if e != nil {
			return nil, nil, e
		}
		if auth.Status == acme.StatusValid {
			continue
		}
		var selected *acme.Challenge
		for _, ch := range auth.Challenges {
			if ch.Type == in.Challenge {
				selected = ch
				break
			}
		}
		if selected == nil {
			return nil, nil, errors.New("ACME authorization does not offer the selected challenge")
		}
		if in.Challenge == "http-01" {
			response, e := c.HTTP01ChallengeResponse(selected.Token)
			if e != nil {
				return nil, nil, e
			}
			if !challengeToken.MatchString(selected.Token) {
				return nil, nil, errors.New("invalid HTTP-01 token")
			}
			a.store.mu.Lock()
			a.store.challenges[selected.Token] = challenge{Host: strings.ToLower(auth.Identifier.Value), Response: response, Expires: a.store.now().Add(10 * time.Minute)}
			a.store.mu.Unlock()
			token := selected.Token
			cleanups = append(cleanups, func() error {
				a.store.mu.Lock()
				defer a.store.mu.Unlock()
				delete(a.store.challenges, token)
				return nil
			})
		} else {
			value, e := c.DNS01ChallengeRecord(selected.Token)
			if e != nil {
				return nil, nil, e
			}
			name := "_acme-challenge." + strings.TrimPrefix(auth.Identifier.Value, "*.")
			cleanup, e := a.presentDNS(ctx, in.ProviderID, name, value)
			if e != nil {
				return nil, nil, e
			}
			cleanups = append(cleanups, cleanup)
			if e = a.waitDNS(ctx, name, value); e != nil {
				return nil, nil, e
			}
		}
		if _, e = c.Accept(ctx, selected); e != nil {
			return nil, nil, e
		}
		if _, e = c.WaitAuthorization(ctx, authorization); e != nil {
			return nil, nil, errors.New("ACME authorization failed")
		}
	}
	order, e = c.WaitOrder(ctx, order.URI)
	if e != nil {
		return nil, nil, e
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: names}, signing)
	if e != nil {
		return nil, nil, e
	}
	chain, _, e := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if e != nil {
		return nil, nil, e
	}
	var out bytes.Buffer
	for _, der := range chain {
		_ = pem.Encode(&out, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	return out.Bytes(), key, nil
}
func (a *acmeIssuer) Revoke(ctx context.Context, c Certificate, cert, key []byte) error {
	return a.revoke(ctx, c, cert, key, 0)
}
func (a *acmeIssuer) revoke(ctx context.Context, c Certificate, cert, key []byte, reason int) error {
	client, e := a.account(ctx, c.AccountID)
	if e != nil {
		return e
	}
	block, _ := pem.Decode(cert)
	if block == nil {
		return ErrInvalid
	}
	signing, e := signer(key)
	if e != nil {
		return e
	}
	return client.RevokeCert(ctx, signing, block.Bytes, acme.CRLReasonCode(reason))
}
func (a *acmeIssuer) waitDNS(ctx context.Context, name, value string) error {
	lookup := a.lookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	delay := a.propagationInterval
	if delay == 0 {
		delay = 5 * time.Second
	}
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		records, e := lookup(ctx, name)
		if e == nil {
			for _, record := range records {
				if record == value {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("DNS propagation timed out")
		case <-ticker.C:
		}
	}
}
func (a *acmeIssuer) cf(ctx context.Context, providerID, method, path string, input, output any) error {
	var zone string
	var token []byte
	if e := a.store.db.QueryRowContext(ctx, `SELECT zone_id,token FROM certificate_dns_providers WHERE id=?`, providerID).Scan(&zone, &token); e != nil {
		return e
	}
	base := a.cloudflareBase
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	request, e := http.NewRequestWithContext(ctx, method, base+"/zones/"+zone+"/dns_records"+path, body)
	if e != nil {
		return e
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	response, e := a.httpClient().Do(request)
	if e != nil {
		return errors.New("DNS provider request failed")
	}
	defer response.Body.Close()
	if method == "DELETE" && response.StatusCode == 404 {
		return nil
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if e != nil || len(raw) > 1<<20 {
		return errors.New("DNS provider response exceeded limits")
	}
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.Unmarshal(raw, &envelope) != nil || !envelope.Success {
		return errors.New("DNS provider rejected the request; check zone and token permissions")
	}
	if output != nil && json.Unmarshal(envelope.Result, output) != nil {
		return errors.New("invalid DNS provider response")
	}
	return nil
}
func (a *acmeIssuer) presentDNS(ctx context.Context, providerID, name, value string) (func() error, error) {
	id := uuid.NewString()
	jobID, _ := ctx.Value(jobContextKey{}).(string)
	if _, e := a.store.db.ExecContext(ctx, `INSERT INTO certificate_dns_challenges(id,job_id,provider_id,name,value) VALUES(?,?,?,?,?)`, id, jobID, providerID, name, value); e != nil {
		return nil, e
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return a.cleanDNS(cleanupCtx, id)
	}
	var result struct {
		ID string `json:"id"`
	}
	e := a.cf(ctx, providerID, "POST", "", map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60, "comment": "nodedance:" + id}, &result)
	if e != nil {
		_ = cleanup()
		return nil, e
	}
	if !hexZone.MatchString(result.ID) {
		_ = cleanup()
		return nil, errors.New("invalid DNS provider record identity")
	}
	if _, e = a.store.db.ExecContext(ctx, `UPDATE certificate_dns_challenges SET record_id=? WHERE id=?`, result.ID, id); e != nil {
		_ = cleanup()
		return nil, e
	}
	return cleanup, nil
}
func (a *acmeIssuer) cleanDNS(ctx context.Context, id string) error {
	var providerID, name, value, recordID string
	if e := a.store.db.QueryRowContext(ctx, `SELECT provider_id,name,value,record_id FROM certificate_dns_challenges WHERE id=?`, id).Scan(&providerID, &name, &value, &recordID); e != nil {
		return e
	}
	if recordID != "" {
		if e := a.cf(ctx, providerID, "DELETE", "/"+recordID, nil, nil); e != nil {
			return e
		}
	} else {
		// A lost create response is recovered by its unique ownership comment. Never
		// remove another controller's TXT record or a user's pre-existing value.
		var records []struct {
			ID      string `json:"id"`
			Comment string `json:"comment"`
			Content string `json:"content"`
		}
		query := url.Values{"type": {"TXT"}, "name": {name}, "per_page": {"5000"}}
		if e := a.cf(ctx, providerID, "GET", "?"+query.Encode(), nil, &records); e != nil {
			return e
		}
		for _, r := range records {
			if r.Comment == "nodedance:"+id && r.Content == value && hexZone.MatchString(r.ID) {
				if e := a.cf(ctx, providerID, "DELETE", "/"+r.ID, nil, nil); e != nil {
					return e
				}
			}
		}
	}
	_, e := a.store.db.ExecContext(ctx, `DELETE FROM certificate_dns_challenges WHERE id=?`, id)
	return e
}
func (a *acmeIssuer) recoverDNS(ctx context.Context) {
	rows, e := a.store.db.QueryContext(ctx, `SELECT c.id FROM certificate_dns_challenges c LEFT JOIN certificate_jobs j ON j.id=c.job_id WHERE j.id IS NULL OR j.status NOT IN ('queued','running')`)
	if e != nil {
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		cleanupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_ = a.cleanDNS(cleanupCtx, id)
		cancel()
	}
}
