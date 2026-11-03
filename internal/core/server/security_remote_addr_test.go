package server

import (
	"net"
	"net/http/httptest"
	"testing"
)

func effectiveAddrFor(t *testing.T, s *Server, remoteAddr, forwardedFor string) string {
	t.Helper()
	request := httptest.NewRequest("GET", "http://core/", nil)
	request.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return s.effectiveRemoteAddr(request)
}

func firstNonLoopbackInterfaceIP(t *testing.T) net.IP {
	t.Helper()
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("interface addresses: %v", err)
	}
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
			return ip
		}
	}
	return nil
}

func TestEffectiveRemoteAddrTrustsLoopbackForwarder(t *testing.T) {
	s := &Server{}
	if got := effectiveAddrFor(t, s, "127.0.0.1:40000", "203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("effective remote addr = %q, want forwarded client", got)
	}
}

func TestEffectiveRemoteAddrTrustsOwnInterfaceForwarder(t *testing.T) {
	self := firstNonLoopbackInterfaceIP(t)
	if self == nil {
		t.Skip("no non-loopback interface address available")
	}
	s := &Server{}
	remoteAddr := net.JoinHostPort(self.String(), "40000")
	if got := effectiveAddrFor(t, s, remoteAddr, "203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("effective remote addr = %q, want forwarded client", got)
	}
}

func TestEffectiveRemoteAddrTrustsMappedLoopbackForwarder(t *testing.T) {
	s := &Server{}
	if got := effectiveAddrFor(t, s, "[::ffff:127.0.0.1]:40000", "203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("effective remote addr = %q, want forwarded client", got)
	}
}

func TestEffectiveRemoteAddrIgnoresForwardedForRemotePeer(t *testing.T) {
	s := &Server{}
	if got := effectiveAddrFor(t, s, "198.51.100.9:40000", "203.0.113.7"); got != "198.51.100.9" {
		t.Fatalf("effective remote addr = %q, want direct peer", got)
	}
}

func TestEffectiveRemoteAddrWalksChainToLastUntrusted(t *testing.T) {
	s := &Server{}
	if got := effectiveAddrFor(t, s, "127.0.0.1:40000", "203.0.113.7, 198.51.100.9"); got != "198.51.100.9" {
		t.Fatalf("effective remote addr = %q, want last untrusted hop", got)
	}
}

func TestEffectiveRemoteAddrHonorsConfiguredProxy(t *testing.T) {
	_, network, err := net.ParseCIDR("198.51.100.0/24")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{trusted: []*net.IPNet{network}}
	if got := effectiveAddrFor(t, s, "198.51.100.9:40000", "203.0.113.7"); got != "203.0.113.7" {
		t.Fatalf("effective remote addr = %q, want forwarded client", got)
	}
}

func TestEffectiveRemoteAddrSelfPeerWithoutForwardedHeader(t *testing.T) {
	s := &Server{}
	if got := effectiveAddrFor(t, s, "127.0.0.1:40000", ""); got != "127.0.0.1" {
		t.Fatalf("effective remote addr = %q, want the peer itself", got)
	}
}

func TestIsSelfAddress(t *testing.T) {
	if !isSelfAddress(net.ParseIP("127.0.0.1")) {
		t.Fatal("loopback must be a self address")
	}
	if !isSelfAddress(net.ParseIP("::ffff:127.0.0.1")) {
		t.Fatal("mapped loopback must be a self address")
	}
	if isSelfAddress(net.ParseIP("203.0.113.7")) {
		t.Fatal("documentation address must not be a self address")
	}
	if isSelfAddress(nil) {
		t.Fatal("nil must not be a self address")
	}
}
