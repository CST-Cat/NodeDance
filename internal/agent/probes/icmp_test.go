package probes

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestICMPLoopbackIPv4AndIPv6(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw ICMP is root-only")
	}
	e := NewExecutor()
	for _, target := range []string{"icmp://127.0.0.1", "icmp://[::1]"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := e.checkICMP(ctx, target)
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
}

func TestDNSTimeoutIsNotICMPPacketLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := &net.DNSError{Err: "DNS deadline exceeded", Name: "invalid.test", IsTimeout: true}
	if got := classifyError(ctx, err); got != "dns" {
		t.Fatalf("DNS timeout was counted as %s", got)
	}
}
