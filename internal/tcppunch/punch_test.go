package tcppunch

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/tcppunch/testutil"
)

func TestDiscoveryKeepsMappingPortForListenerAndClosesResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := Discover(ctx, []string{testutil.STUNServer(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if len(p.mappings) != 1 || p.mappings[0].public.Port() != uint16(p.mappings[0].local.Port) || p.mappings[0].listener.Addr().String() != p.mappings[0].local.String() {
		t.Fatal("listener did not preserve the discovered source port")
	}
	p.Close()
	if _, err := p.mappings[0].listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("listener survived cleanup", err)
	}
	if _, err := p.mappings[0].stream.Write([]byte("closed")); !errors.Is(err, net.ErrClosed) {
		t.Fatal("STUN mapping survived cleanup", err)
	}
}

func TestDiscoveryCancellationClosesSilentSTUNConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		b := make([]byte, 2048)
		for {
			if _, err := conn.Read(b); err != nil {
				close(closed)
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if p, err := Discover(ctx, []string{"stun:" + listener.Addr().String()}); err == nil {
		p.Close()
		t.Fatal("silent STUN discovery succeeded")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("canceled discovery leaked its TCP connection")
	}
}

func TestDiscoveryRejectsRelayAndUnboundedServerLists(t *testing.T) {
	for _, servers := range [][]string{{"turn:127.0.0.1:3478"}, {"stun:127.0.0.1:1", "stun:127.0.0.1:2", "stun:127.0.0.1:3", "stun:127.0.0.1:4", "stun:127.0.0.1:5"}} {
		if p, err := Discover(context.Background(), servers); err == nil {
			p.Close()
			t.Fatal("invalid discovery configuration accepted")
		}
	}
}

func TestDiscoveryRejectsUnboundOrOversizedSTUNResponses(t *testing.T) {
	for _, mode := range []string{"transaction", "size"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				header := make([]byte, 20)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				binary.BigEndian.PutUint16(header[:2], 0x101)
				if mode == "transaction" {
					header[8] ^= 0xff
				} else {
					binary.BigEndian.PutUint16(header[2:4], 4096)
				}
				_, _ = conn.Write(header)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if p, err := Discover(ctx, []string{"stun:" + listener.Addr().String()}); err == nil {
				p.Close()
				t.Fatal("invalid STUN response produced a mapping")
			}
		})
	}
}

func TestCanceledPunchClosesMappingAndListener(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := Discover(ctx, []string{testutil.STUNServer(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	attempt, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	_, err = p.Connect(attempt, []string{p.mappings[0].local.String()}, func(ctx context.Context, conn net.Conn) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("canceled punch did not report its deadline", err)
	}
	if _, err := p.mappings[0].listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("canceled punch left a listener open", err)
	}
}
