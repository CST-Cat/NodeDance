package tailscale

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestSSHHandshakeCancellationInterruptsSilentPeer(t *testing.T) {
	for _, action := range []string{"probe", "connect"} {
		t.Run(action, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan net.Conn, 1)
			go func() {
				conn, err := listener.Accept()
				if err == nil {
					accepted <- conn
				}
			}()
			host, rawPort, _ := net.SplitHostPort(listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			peer := Peer{Identity: "fixture", Class: "linux", Online: true, IPs: []string{host}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var err error
				if action == "probe" {
					_, err = probeHostKeyOnPort(ctx, peer, port)
				} else {
					_, err = connectSSHOnPort(ctx, peer, SSHCredentials{User: "root", Password: "fixture"}, "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", NewStateStore(t.TempDir()), false, port)
				}
				done <- err
			}()
			conn := <-accepted
			defer conn.Close()
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled handshake succeeded")
				}
			case <-time.After(time.Second):
				conn.Close()
				<-done
				t.Fatal("handshake ignored cancellation")
			}
		})
	}
}
