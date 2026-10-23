package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/domains"
)

func certificateFixture(t *testing.T) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(19), Subject: pkix.Name{CommonName: "PRIVATE-DOMAIN.example"}, DNSNames: []string{"private-domain.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(60 * 24 * time.Hour)}
	der, e := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := x509.MarshalPKCS8PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
}
func TestDomainCertificateAuthenticatedContractAndPrivateExport(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	if e := s.BootstrapAdministrator(ctx, "cert-admin", "cert-password-strong"); e != nil {
		t.Fatal(e)
	}
	host := httptest.NewServer(s)
	defer host.Close()
	client, e := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	catalog, e := client.Catalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	call := func(name string, in control.Input, download io.Writer) control.Result {
		t.Helper()
		op, e := control.FindIn(catalog, name)
		if e != nil {
			t.Fatal(e)
		}
		return client.Execute(ctx, op, in, nil, download)
	}
	for _, op := range catalog.Operations {
		if !strings.HasPrefix(op.Name, "domains.") && !strings.HasPrefix(op.Name, "certificates.") {
			continue
		}
		path := strings.ReplaceAll(op.Path, "{id}", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		request, _ := http.NewRequest(op.Method, host.URL+path, nil)
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatalf("%s anonymous status %d", op.Name, response.StatusCode)
		}
		if op.RequiredRole != "administrator" {
			t.Fatal("certificate operation is not administrator-only", op.Name)
		}
	}
	if r := call("auth.login", control.Input{Body: json.RawMessage(`{"username":"cert-admin","password":"cert-password-strong"}`)}, nil); r.Err() != nil {
		t.Fatal(r)
	}
	r := call("domains.create", control.Input{Body: json.RawMessage(`{"name":"private-domain.example","label":"PRIVATE DOMAIN LABEL"}`)}, nil)
	if r.Err() != nil {
		t.Fatal(r)
	}
	var domain domains.Domain
	json.Unmarshal(r.Data, &domain)
	cert, key := certificateFixture(t)
	body, _ := json.Marshal(domains.ImportInput{Name: "PRIVATE CERTIFICATE", CertificatePEM: cert, PrivateKeyPEM: key, DomainIDs: []string{domain.ID}})
	r = call("certificates.import", control.Input{Body: body}, nil)
	if r.Err() != nil {
		t.Fatal(r)
	}
	var c domains.Certificate
	json.Unmarshal(r.Data, &c)
	if c.ID == "" {
		t.Fatal(r)
	}
	for _, name := range []string{"domains.list", "certificates.list", "certificates.accounts", "certificates.providers", "certificates.jobs"} {
		r = call(name, control.Input{}, nil)
		if r.Err() != nil || strings.Contains(string(r.Data), "PRIVATE KEY") {
			t.Fatal(name, r)
		}
	}
	var archive bytes.Buffer
	r = call("certificates.export", control.Input{Params: map[string]string{"id": c.ID}, Body: json.RawMessage(`{"revision":1,"confirm":true}`)}, &archive)
	if r.Err() != nil {
		t.Fatal(r)
	}
	zipper, e := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if e != nil {
		t.Fatal(e)
	}
	if len(zipper.File) != 2 {
		t.Fatal(zipper.File)
	}
	for _, f := range zipper.File {
		if f.Mode().Perm() != 0600 {
			t.Fatal(f.Mode())
		}
		reader, _ := f.Open()
		raw, _ := io.ReadAll(reader)
		reader.Close()
		if f.Name == "private-key.pem" && string(raw) != key {
			t.Fatal("exported wrong key")
		}
	}
	var bad bytes.Buffer
	r = call("certificates.export", control.Input{Params: map[string]string{"id": c.ID}, Body: json.RawMessage(`{"revision":99,"confirm":true}`)}, &bad)
	if r.Err() == nil {
		t.Fatal("stale export revision accepted")
	}
	// Request bodies are absent from the allowlisted audit table.
	var logs string
	s.store.DB.QueryRowContext(ctx, `SELECT group_concat(action||coalesce(target_id,'')) FROM audit_entries`).Scan(&logs)
	for _, secret := range []string{key, "PRIVATE DOMAIN LABEL", "PRIVATE CERTIFICATE"} {
		if strings.Contains(logs, secret) {
			t.Fatal("audit leaked private input")
		}
	}
	r = call("certificates.delete", control.Input{Params: map[string]string{"id": c.ID}, Body: json.RawMessage(`{"revision":1,"confirm":true}`)}, nil)
	if r.Err() != nil {
		t.Fatal(r)
	}
	r = call("domains.delete", control.Input{Params: map[string]string{"id": domain.ID}, Body: json.RawMessage(`{"revision":1,"confirm":true}`)}, nil)
	if r.Err() != nil {
		t.Fatal(r)
	}
}
