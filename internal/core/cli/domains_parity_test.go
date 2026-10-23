package cli

import (
	"archive/zip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"github.com/CST-Cat/NodeDance/internal/core/domains"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/server"
)

func TestDomainsCLIAndTUIBatchUseDiscoveredOperations(t *testing.T) {
	core, e := server.New("test", server.Options{DataDir: t.TempDir(), Development: true})
	if e != nil {
		t.Fatal(e)
	}
	defer core.Close()
	if e = core.BootstrapAdministrator(context.Background(), "domain-admin", "domain-password-strong"); e != nil {
		t.Fatal(e)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	state := filepath.Join(t.TempDir(), "client")
	invoke := func(args []string, body string) (string, error) {
		t.Helper()
		var out strings.Builder
		args = append(args, "--server", host.URL, "--dev", "--state-dir", state)
		e := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader(body), &out, io.Discard, "test")
		return out.String(), e
	}
	if out, e := invoke([]string{"auth", "login", "--body", "-"}, `{"username":"domain-admin","password":"domain-password-strong"}`); e != nil {
		t.Fatal(e, out)
	}
	batch := `{"operation":"domains.create","body":{"name":"one.batch.example"}}` + "\n" + `{"operation":"domains.create","body":{"name":"two.batch.example"}}` + "\n"
	if out, e := invoke([]string{"batch", "--input", "-", "--dry-run"}, batch); e != nil || !strings.Contains(out, `"dryRun":true`) {
		t.Fatal(e, out)
	}
	if out, e := invoke([]string{"call", "domains.list"}, ""); e != nil || strings.Contains(out, "one.batch.example") {
		t.Fatal("dry run mutated Core", e, out)
	}
	if out, e := invoke([]string{"batch", "--input", "-"}, batch); e != nil || !strings.Contains(out, `"index":2`) {
		t.Fatal(e, out)
	}
	invalid := `{"operation":"domains.create","body":{"name":"must-not-run.example"}}` + "\n" + `{"operation":"certificates.issue","body":{"confirm":false}}` + "\n"
	if out, e := invoke([]string{"batch", "--input", "-"}, invalid); e == nil || out != "" {
		t.Fatal("preflight allowed partial execution", e, out)
	}
	conflictBatch := `{"operation":"domains.create","body":{"name":"one.batch.example","label":"conflicting edit"}}` + "\n" + `{"operation":"domains.create","body":{"name":"continued.batch.example"}}` + "\n"
	if out, e := invoke([]string{"batch", "--input", "-", "--continue-on-error"}, conflictBatch); e == nil || !strings.Contains(out, `"index":2`) || !strings.Contains(out, "continued.batch.example") {
		t.Fatal("batch continuation/exit status", e, out)
	}
	file := filepath.Join(t.TempDir(), "batch.jsonl")
	if e = os.WriteFile(file, []byte(`{"operation":"domains.create","body":{"name":"file.batch.example"}}`+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if out, e := invoke([]string{"batch", "--input", file}, ""); e != nil || !strings.Contains(out, "file.batch.example") {
		t.Fatal(e, out)
	}
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/auth.login"}, {"Filter: auth.login", "1"}, {"Administrator username", "domain-admin"}, {"Password (hidden)", "domain-password-strong"}, {"Enter to return:", ""}, {"Select:", "/domains.create"}, {"Filter: domains.create", "1"}, {"JSON body or @private-file", `{"name":"tui.example","label":"TUI domain"}`}, {"Enter to return:", ""}, {"Select:", "/client.batch"}, {"Filter: client.batch", "1"}, {"Arguments as JSON array", `["--input","` + file + `","--dry-run"]`}, {"Enter to return:", ""}, {"Select:", "q"}})
	out, e := invoke([]string{"call", "domains.list"}, "")
	if e != nil || !strings.Contains(out, "TUI domain") || strings.Contains(out, "must-not-run.example") {
		t.Fatal(e, out)
	}
	out, e = invoke([]string{"operations", "certificates.export"}, "")
	if e != nil {
		t.Fatal(e)
	}
	var op control.Operation
	if e = json.Unmarshal([]byte(out), &op); e != nil || op.Transport != "download" || op.Method != "POST" {
		t.Fatal(op, e)
	}
}

// The real Core/CLI worker and observer run here; the CA alone is isolated.
type cliCertificateIssuer struct{}

func (cliCertificateIssuer) Issue(ctx context.Context, in domains.IssueInput, names []string) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		return nil, nil, err
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), err
}
func (cliCertificateIssuer) Revoke(context.Context, domains.Certificate, []byte, []byte) error {
	return nil
}
func TestCertificateCLIWorkflowAndTUIPrivateDownload(t *testing.T) {
	core, err := server.New("test", server.Options{DataDir: t.TempDir(), Development: true, CertificateIssuer: cliCertificateIssuer{}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err = core.BootstrapAdministrator(context.Background(), "cert-admin", "certificate-password-strong"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	state := filepath.Join(t.TempDir(), "client")
	invoke := func(args []string) (string, error) {
		t.Helper()
		var out strings.Builder
		args = append(args, "--server", host.URL, "--dev", "--state-dir", state, "--timeout", "0")
		e := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, strings.NewReader(""), &out, io.Discard, "test")
		return out.String(), e
	}
	call := func(name string, body any, extra ...string) json.RawMessage {
		t.Helper()
		args := []string{"call", name}
		if body != nil {
			raw, _ := json.Marshal(body)
			args = append(args, "--body", string(raw))
		}
		args = append(args, extra...)
		out, e := invoke(args)
		if e != nil {
			t.Fatal(name, e, out)
		}
		var result control.Result
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if e = json.Unmarshal([]byte(line), &result); e != nil {
				t.Fatal(e, out)
			}
		}
		return result.Data
	}
	call("auth.login", map[string]any{"username": "cert-admin", "password": "certificate-password-strong"})
	var d domains.Domain
	json.Unmarshal(call("domains.create", map[string]any{"name": "cli-cert.example"}), &d)
	var account domains.Account
	json.Unmarshal(call("certificates.account-create", map[string]any{"name": "isolated CA", "email": "test@example.test", "directory": "letsencrypt-staging", "termsAgreed": true}), &account)
	issue := map[string]any{"name": "CLI certificate", "domainIds": []string{d.ID}, "accountId": account.ID, "challenge": "http-01", "keyType": "ec256", "confirm": true}
	var job domains.Job
	json.Unmarshal(call("certificates.issue", issue, "--idempotency-key", "cli-cert-issue-001", "--wait"), &job)
	if job.Status != "succeeded" || job.CertificateID == "" {
		t.Fatal(job)
	}
	params := `{"id":"` + job.CertificateID + `"}`
	var certificate domains.Certificate
	json.Unmarshal(call("certificates.get", nil, "--params", params), &certificate)
	json.Unmarshal(call("certificates.renew", map[string]any{"confirm": true, "revision": certificate.Revision}, "--params", params, "--idempotency-key", "cli-cert-renew-001", "--wait"), &job)
	if job.Status != "succeeded" {
		t.Fatal(job)
	}
	json.Unmarshal(call("certificates.get", nil, "--params", params), &certificate)
	archive := filepath.Join(t.TempDir(), "cli.zip")
	export := map[string]any{"confirm": true, "revision": certificate.Revision}
	call("certificates.export", export, "--params", params, "--output", archive)
	info, e := os.Stat(archive)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	raw, _ := json.Marshal(export)
	if out, e := invoke([]string{"call", "certificates.export", "--body", string(raw), "--params", params, "--output", archive}); e == nil {
		t.Fatal("overwrote private export", out)
	}
	tuiArchive := filepath.Join(t.TempDir(), "tui.zip")
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/auth.login"}, {"Filter: auth.login", "1"}, {"Administrator username", "cert-admin"}, {"Password (hidden)", "certificate-password-strong"}, {"Enter to return:", ""}, {"Select:", "/certificates.export"}, {"Filter: certificates.export", "1"}, {"id []:", certificate.ID}, {"JSON body or @private-file", string(raw)}, {"New output file", tuiArchive}, {"Enter to return:", ""}, {"Select:", "q"}})
	z, e := zip.OpenReader(tuiArchive)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	if len(z.File) != 2 {
		t.Fatal(z.File)
	}
	for _, f := range z.File {
		if f.Mode().Perm() != 0600 {
			t.Fatal(f.Mode())
		}
	}
	json.Unmarshal(call("certificates.revoke", map[string]any{"confirm": true, "revision": certificate.Revision}, "--params", params, "--idempotency-key", "cli-cert-revoke-001", "--wait"), &job)
	if job.Status != "succeeded" {
		t.Fatal(job)
	}
	json.Unmarshal(call("certificates.get", nil, "--params", params), &certificate)
	if certificate.Status != "revoked" {
		t.Fatal(certificate)
	}
}
