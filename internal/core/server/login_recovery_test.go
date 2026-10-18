package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestLoginWithExistingSessionRotatesOnlyAfterPasswordVerification(t *testing.T) {
	_, newClient := newAccountFixture(t)
	client := newClient()
	client.login("admin", "administrator-password")
	base, _ := url.Parse(client.base)
	oldCookies := client.client.Jar.Cookies(base)
	client.call("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": "incorrect-password"}, 401)
	client.call("GET", "/api/v1/auth/me", nil, 200)
	client.login("admin", "administrator-password")
	client.call("GET", "/api/v1/auth/me", nil, 200)
	stale := newClient()
	stale.client.Jar.SetCookies(base, oldCookies)
	stale.call("GET", "/api/v1/auth/me", nil, 401)
}

func TestLoginRecoveryStillRejectsInvalidOriginAndCSRF(t *testing.T) {
	_, newClient := newAccountFixture(t)
	for _, authenticated := range []bool{false, true} {
		client := newClient()
		if authenticated {
			client.login("admin", "administrator-password")
		}
		var csrf struct{ Token string }
		json.Unmarshal(client.call("GET", "/api/v1/auth/csrf", nil, 200), &csrf)
		for _, scenario := range []struct{ origin, token string }{
			{"https://attacker.invalid", csrf.Token},
			{client.base, "forged"},
			{client.base, ""},
		} {
			body := []byte(`{"username":"admin","password":"administrator-password"}`)
			req, _ := http.NewRequest("POST", client.base+"/api/v1/auth/login", bytes.NewReader(body))
			req.Header.Set("Origin", scenario.origin)
			req.Header.Set("X-CSRF-Token", scenario.token)
			response, err := client.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatalf("authenticated=%v: got %d", authenticated, response.StatusCode)
			}
		}
	}
}
