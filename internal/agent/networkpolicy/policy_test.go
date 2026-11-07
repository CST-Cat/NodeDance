package networkpolicy

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestResolverFallsBackToDefault(t *testing.T) {
	if Resolver(protocol.AgentSettings{}) != net.DefaultResolver {
		t.Fatal("empty DNS setting must keep the default resolver")
	}
	custom := Resolver(protocol.AgentSettings{DNS: "127.0.0.1:53"})
	if custom == net.DefaultResolver {
		t.Fatal("custom DNS setting was ignored")
	}
	if !custom.PreferGo {
		t.Fatal("custom resolver must resolve in-process")
	}
}

func TestDialPrefersConfiguredIPVersion(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("loopback TCP listener unavailable:", err)
	}
	defer listener.Close()
	settings := protocol.AgentSettings{PreferIPVersion: "4"}
	conn, err := Dial(context.Background(), "tcp", listener.Addr().String(), settings)
	if err != nil {
		t.Fatalf("dial with IPv4 preference: %v", err)
	}
	if local := conn.LocalAddr().(*net.TCPAddr); local.IP.To4() == nil {
		t.Fatalf("IPv4 preference was not applied: %v", local)
	}
	conn.Close()

	broken := protocol.AgentSettings{PreferIPVersion: "4", DNS: "127.0.0.1:1"}
	deadline, cancelDeadline := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDeadline()
	if _, err := Dial(deadline, "tcp", listener.Addr().String(), broken); err != nil {
		t.Fatalf("unreachable custom DNS must not prevent the fallback dial: %v", err)
	}
}
