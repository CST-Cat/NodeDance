package geolocation

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func awaitCountry(t *testing.T, r *Resolver, ip string) Country {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		value := r.Lookup(ip)
		if value.Reason != "lookup_pending" {
			return value
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("lookup did not finish")
	return Country{}
}
func TestPublicIPBoundaries(t *testing.T) {
	for _, ip := range []string{"", "unknown", "127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "169.254.169.254", "198.51.100.5", "203.0.113.1", "192.0.2.1", "0.0.0.0", "255.255.255.255", "::1", "fe80::1%eth0", "fc00::1", "2001:db8::1", "https://example.com"} {
		if PublicIP(ip) != "" {
			t.Errorf("accepted non-public address %q", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "2606:4700:4700::1111"} {
		if PublicIP(ip) != ip {
			t.Errorf("rejected public address %s", ip)
		}
	}
}
func TestAsyncCountryCacheAndIsolation(t *testing.T) {
	r := New()
	release := make(chan struct{})
	var requests atomic.Int32
	r.client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.Host != "ipwho.is" || request.URL.Scheme != "https" || request.URL.Path != "/8.8.8.8" {
			t.Errorf("unexpected destination %s", request.URL)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			t.Error("credentials leaked")
		}
		<-release
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ip":"8.8.8.8","success":true,"country_code":"US","country":"United States"}`))}, nil
	})
	if r.Lookup("192.168.1.1").Reason != "public_ip_unavailable" {
		t.Fatal("private IP looked up")
	}
	if r.Lookup("8.8.8.8").Reason != "lookup_pending" {
		t.Fatal("first lookup must be asynchronous")
	}
	for i := 0; i < 50; i++ {
		r.Lookup("8.8.8.8")
	}
	close(release)
	result := awaitCountry(t, r, "8.8.8.8")
	if result.Code != "US" || result.Status != "known" || result.Source != "ipwho.is" {
		t.Fatalf("unexpected country %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatal("duplicate provider requests")
	}
}
func TestProviderFailureNeverGuessesCountry(t *testing.T) {
	for _, body := range []string{`{"ip":"1.1.1.1","success":true,"country_code":"US","country":"US"}`, `{"ip":"8.8.8.8","success":false}`, `{"ip":"8.8.8.8","success":true,"country_code":"../us","country":"US"}`, `not json`} {
		r := New()
		r.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		value := awaitCountry(t, r, "8.8.8.8")
		if value.Status != "unknown" || value.Code != "" {
			t.Fatalf("guessed country %+v", value)
		}
	}
}
