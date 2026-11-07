package agent

import (
	"net"
	"net/url"
	"strconv"
	"testing"
)

// FuzzParseServerURLContainment asserts every URL ParseServerURL accepts is an
// authority-only origin whose scheme keeps the HTTPS (or development loopback
// HTTP) rule, and that the normalized result is idempotent.
func FuzzParseServerURLContainment(f *testing.F) {
	for _, seed := range []string{
		"https://core.example", "https://core.example:8443", "https://core.example/",
		"http://127.0.0.1:8180", "http://[::1]:8180", "http://localhost:8180",
		"https://core.example/path", "https://user@core.example", "https://core.example?x=1",
		"https://core.example#frag", "ftp://core.example", "https://100.111.176.125:8180",
		"http://0x7f000001", "https://[fe80::1]", " https://core.example ", "https://",
		"https://core.example:0", "https://core.example:99999",
	} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, value string, development bool) {
		result, err := ParseServerURL(value, development)
		if err != nil {
			return
		}
		parsed, parseErr := url.Parse(result)
		if parseErr != nil {
			t.Fatalf("accepted %q produced unparseable %q: %v", value, result, parseErr)
		}
		if parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
			t.Fatalf("accepted %q produced non-authority result %q", value, result)
		}
		switch parsed.Scheme {
		case "https":
		case "http":
			ip := net.ParseIP(parsed.Hostname())
			if !development || ip == nil || !ip.IsLoopback() {
				t.Fatalf("accepted %q produced insecure result %q", value, result)
			}
		default:
			t.Fatalf("accepted %q produced scheme %q", value, parsed.Scheme)
		}
		if port := parsed.Port(); port != "" {
			if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
				t.Fatalf("accepted %q produced invalid port %q", value, result)
			}
		}
		again, againErr := ParseServerURL(result, development)
		if againErr != nil || again != result {
			t.Fatalf("result %q is not idempotent: %q %v", result, again, againErr)
		}
	})
}
