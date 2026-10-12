package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSlowJSONBodyHasDeadlineAndReleasesCapacity(t *testing.T) {
	s, err := New("audit", Options{DataDir: t.TempDir(), Development: true, JSONReadTimeout: 40 * time.Millisecond, MaxConcurrentRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	host := httptest.NewServer(s)
	defer host.Close()
	response, err := http.Get(host.URL + "/api/v1/auth/csrf")
	if err != nil {
		t.Fatal(err)
	}
	var csrf struct {
		Token string `json:"token"`
	}
	json.NewDecoder(response.Body).Decode(&csrf)
	response.Body.Close()
	cookies := []string{}
	for _, c := range response.Cookies() {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	conn, err := net.Dial("tcp", strings.TrimPrefix(host.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprintf(conn, "POST /api/v1/auth/setup HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nX-CSRF-Token: %s\r\nCookie: %s\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{", strings.TrimPrefix(host.URL, "http://"), host.URL, csrf.Token, strings.Join(cookies, "; "))
	result, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal("body deadline did not release connection", err)
	}
	result.Body.Close()
	if result.StatusCode != http.StatusRequestTimeout {
		t.Fatal("unexpected slow-body status", result.StatusCode)
	}
	if len(s.requestSlots) != 0 {
		t.Fatal("timed-out request retained capacity")
	}
}

func TestRequestCapacityRejectsAndHealthRemainsAvailable(t *testing.T) {
	s, err := New("audit", Options{DataDir: t.TempDir(), Development: true, MaxConcurrentRequests: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.requestSlots <- struct{}{}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/auth/setup/status", nil))
	if w.Code != 503 || w.Header().Get("Retry-After") == "" {
		t.Fatal("capacity not enforced", w.Code)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/health", nil))
	if w.Code != 200 {
		t.Fatal("health blocked by saturation")
	}
	<-s.requestSlots
}
