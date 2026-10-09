package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailscaleDiscoveryAPIRequiresSessionAndReturnsVisiblePeers(t *testing.T) {
	core, err := New("tailscale-discovery-api-test", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	cli := `#!/bin/sh
[ "$1" = status ] && [ "$2" = --json ] || exit 2
printf '%s' '{"BackendState":"Running","Peer":{"nodekey:stable":{"PublicKey":"nodekey:stable","HostName":"edge-linux","OS":"linux","TailscaleIPs":["100.64.0.10"],"Online":true}}}'
`
	if err := os.WriteFile(filepath.Join(binDir, "tailscale"), []byte(cli), 0o700); err != nil {
		t.Fatal(err)
	}
	previousPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", binDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("PATH", previousPath) })

	request := func(authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/discovery/tailscale", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		}
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	if response := request(false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated discovery returned %d, want 401", response.Code)
	}
	response := request(true)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated discovery returned %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("private discovery Cache-Control=%q", response.Header().Get("Cache-Control"))
	}
	var body struct {
		Peers []struct {
			Identity string   `json:"identity"`
			Name     string   `json:"name"`
			Class    string   `json:"class"`
			IPs      []string `json:"ips"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Peers) != 1 || body.Peers[0].Identity != "nodekey:stable" || body.Peers[0].Name != "edge-linux" || body.Peers[0].Class != "linux" || strings.Join(body.Peers[0].IPs, ",") != "100.64.0.10" {
		t.Fatalf("unexpected authenticated Tailscale discovery response: %+v", body.Peers)
	}
}

func TestTailscaleDeploymentAPIRejectsMissingOriginOrCSRF(t *testing.T) {
	core, err := New("tailscale-deployment-csrf-test", Options{
		DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	for name, scenario := range map[string]struct{ origin, csrf bool }{
		"missing origin": {origin: false, csrf: true},
		"missing csrf":   {origin: true, csrf: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/deployments", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			if scenario.origin {
				r.Header.Set("Origin", "https://panel.test")
			}
			if scenario.csrf {
				r.Header.Set(csrfHeaderName, csrf)
				r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			}
			w := httptest.NewRecorder()
			core.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Fatalf("deployment request returned %d, want 403: %s", w.Code, w.Body.String())
			}
		})
	}
}
