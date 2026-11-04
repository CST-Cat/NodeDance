package server

import (
	"errors"
	"strings"
	"testing"
)

func TestICECandidateDiagnosticsClassifyUDPWithoutLeakingSDP(t *testing.T) {
	sdp := "v=0\r\na=ice-ufrag:PRIVATE-UFRAG\r\na=ice-pwd:PRIVATE-PASSWORD\r\na=fingerprint:sha-256 PRIVATE-FINGERPRINT\r\n" +
		"a=candidate:1 1 udp 123 10.0.0.2 40000 typ host\r\n" +
		"a=candidate:2 1 udp 123 100.64.0.2 40001 typ host\r\n" +
		"a=candidate:3 1 udp 123 127.0.0.1 40002 typ host\r\n" +
		"a=candidate:4 1 udp 123 203.0.113.7 40003 typ host\r\n" +
		"a=candidate:5 1 udp 123 8.8.8.8 40004 typ host\r\n" +
		"a=candidate:6 1 udp 123 2606:4700::1111 40005 typ host\r\n" +
		"a=candidate:7 1 udp 123 1.2.3.4 50000 typ srflx raddr 10.0.0.2 rport 40000\r\n" +
		"a=candidate:8 1 udp 123 1.2.3.5 50001 typ relay\r\n" +
		"a=candidate:9 1 tcp 123 1.2.3.6 50002 typ host\r\n" +
		"a=candidate:10 1 udp 123 malformed 40000 typ host\r\n" +
		"a=candidate:11 1 udp 123 1.2.3.7 0 typ host\r\n" +
		"a=candidate:12 1 udp\r\n"
	want := "host=6 public_host=2 srflx=1 relay=1"
	if got := iceOfferSummary(sdp); got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
	cause := errors.New("Agent rsync attempt failed: direct_ice_timeout")
	err := iceFailureDetails(cause, "connectivity_or_dtls", sdp, sdp)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "source{"+want+"}") || !strings.Contains(err.Error(), "target{"+want+"}") {
		t.Fatal("lost connection failure context", err)
	}
	for _, secret := range []string{"PRIVATE-UFRAG", "PRIVATE-PASSWORD", "PRIVATE-FINGERPRINT", "10.0.0.2", "8.8.8.8", "2606:4700::1111"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("diagnostics expose peer credentials or addresses")
		}
	}
}

func TestICECandidateDiagnosticsDistinguishMissingOfferFromNoReflexiveCandidates(t *testing.T) {
	if got := iceOfferSummary(""); got != "not_advertised" {
		t.Fatal(got)
	}
	if got := iceOfferSummary("v=0\na=candidate:1 1 udp 123 192.168.0.2 12345 typ host\n"); got != "host=1 public_host=0 srflx=0 relay=0" {
		t.Fatal(got)
	}
	cause := rsyncCommitError{cause: errors.New("commit result unknown")}
	err := iceFailureDetails(cause, "connectivity_or_dtls", "", "")
	var committing rsyncCommitError
	if !errors.As(err, &committing) {
		t.Fatal("lost the no-replay commit error identity")
	}
}
