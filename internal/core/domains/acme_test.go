package domains

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestACMEHTTP01RealProtocolAndHostBoundary(t *testing.T) {
	s := testStore(t)
	in := setupIssue(t, s)
	var host *httptest.Server
	valid, finalized := false, false
	var certificate []byte
	token := "http01_test_token_0123456789"
	nonce := 0
	host = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce++
		w.Header().Set("Replay-Nonce", fmt.Sprint(nonce))
		w.Header().Set("Content-Type", "application/json")
		send := func(status int, v any) { w.WriteHeader(status); json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/directory":
			send(200, map[string]any{"newNonce": host.URL + "/nonce", "newAccount": host.URL + "/account", "newOrder": host.URL + "/order", "revokeCert": host.URL + "/revoke"})
		case "/nonce":
			w.WriteHeader(200)
		case "/account":
			w.Header().Set("Location", host.URL+"/account/1")
			send(201, map[string]any{"status": "valid"})
		case "/order":
			w.Header().Set("Location", host.URL+"/order/1")
			send(201, map[string]any{"status": "pending", "authorizations": []string{host.URL + "/auth"}, "finalize": host.URL + "/finalize"})
		case "/auth":
			status := "pending"
			if valid {
				status = "valid"
			}
			send(200, map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": "example.test"}, "challenges": []any{map[string]string{"type": "http-01", "url": host.URL + "/challenge", "token": token, "status": "pending"}}})
		case "/challenge":
			request := httptest.NewRequest("GET", "http://example.test/.well-known/acme-challenge/"+token, nil)
			response := httptest.NewRecorder()
			if !s.HTTPChallenge(response, request) || response.Code != 200 || !strings.HasPrefix(response.Body.String(), token+".") {
				t.Errorf("challenge not served: %d %s", response.Code, response.Body.String())
			}
			wrong := httptest.NewRequest("GET", "http://attacker.test/.well-known/acme-challenge/"+token, nil)
			denied := httptest.NewRecorder()
			s.HTTPChallenge(denied, wrong)
			if denied.Code != 404 {
				t.Error("challenge crossed host boundary")
			}
			valid = true
			send(200, map[string]string{"type": "http-01", "url": host.URL + "/challenge", "token": token, "status": "valid"})
		case "/order/1":
			status := "ready"
			if finalized {
				status = "valid"
			}
			send(200, map[string]any{"status": status, "finalize": host.URL + "/finalize", "certificate": host.URL + "/certificate"})
		case "/finalize":
			var signed struct {
				Payload string `json:"payload"`
			}
			json.NewDecoder(r.Body).Decode(&signed)
			raw, _ := base64.RawURLEncoding.DecodeString(signed.Payload)
			var request struct {
				CSR string `json:"csr"`
			}
			json.Unmarshal(raw, &request)
			der, _ := base64.RawURLEncoding.DecodeString(request.CSR)
			csr, e := x509.ParseCertificateRequest(der)
			if e != nil || csr.CheckSignature() != nil {
				t.Error("invalid CSR", e)
				w.WriteHeader(400)
				return
			}
			ca, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Isolated CA"}, DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
			cert, e := x509.CreateCertificate(rand.Reader, template, template, csr.PublicKey, ca)
			if e != nil {
				t.Error(e)
			}
			certificate = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
			finalized = true
			send(200, map[string]string{"status": "valid", "certificate": host.URL + "/certificate"})
		case "/certificate":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			w.Write(certificate)
		default:
			t.Errorf("unexpected ACME path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer host.Close()
	issuer := &acmeIssuer{store: s, client: host.Client(), directory: func(string) string { return host.URL + "/directory" }}
	s.issuer = issuer
	j, e := s.QueueIssue(context.Background(), in, "protocol-request-key", event())
	if e != nil {
		t.Fatal(e)
	}
	s.work(context.Background())
	j, _ = s.Job(context.Background(), j.ID)
	if j.Status != "succeeded" {
		t.Fatal(j)
	}
	if len(s.challenges) != 0 {
		t.Fatal("HTTP challenges survived job cleanup")
	}
	request := httptest.NewRequest("GET", "http://example.test/.well-known/acme-challenge/"+token, nil)
	response := httptest.NewRecorder()
	s.HTTPChallenge(response, request)
	if response.Code != 404 {
		t.Fatal("completed challenge remains public")
	}
}
func TestCloudflareOwnedRecordsAndLostResponseRecovery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, e := s.SaveProvider(ctx, "", ProviderInput{Name: "CF", Kind: "cloudflare", ZoneID: strings.Repeat("a", 32), APIToken: "PRIVATE-CF-TOKEN"}, event())
	if e != nil {
		t.Fatal(e)
	}
	records := map[string]map[string]any{}
	counter := 0
	var endpoint *httptest.Server
	endpoint = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer PRIVATE-CF-TOKEN" {
			t.Error("wrong provider authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		result := any(map[string]any{})
		if r.Method == "POST" {
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			counter++
			id := fmt.Sprintf("%032x", counter)
			in["id"] = id
			records[id] = in
			result = in
		} else if r.Method == "GET" {
			out := []map[string]any{}
			for _, record := range records {
				out = append(out, record)
			}
			result = out
		} else if r.Method == "DELETE" {
			id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			delete(records, id)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}))
	defer endpoint.Close()
	a := &acmeIssuer{store: s, client: endpoint.Client(), cloudflareBase: endpoint.URL}
	cleanup, e := a.presentDNS(ctx, p.ID, "_acme-challenge.example.test", "public-challenge-value")
	if e != nil {
		t.Fatal(e)
	}
	if len(records) != 1 {
		t.Fatal(records)
	}
	for id := range records {
		if _, e = s.db.ExecContext(ctx, `UPDATE certificate_dns_challenges SET record_id=''`); e != nil {
			t.Fatal(e)
		}
		records[strings.Repeat("f", 32)] = map[string]any{"id": strings.Repeat("f", 32), "comment": "user-owned", "content": "public-challenge-value"}
		_ = id
	}
	if e = cleanup(); e != nil {
		t.Fatal(e)
	}
	if len(records) != 1 || records[strings.Repeat("f", 32)] == nil {
		t.Fatal("cleanup deleted unrelated TXT record", records)
	}
	var remaining int
	s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_dns_challenges`).Scan(&remaining)
	if remaining != 0 {
		t.Fatal("cleanup ledger survived", remaining)
	}
}
