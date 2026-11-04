package peertransport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/pion/logging"
	"github.com/pion/stun/v4"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
)

func natPeers(t *testing.T, symmetric, blocked bool) (*Peer, *Peer) {
	t.Helper()
	logger := logging.NewDefaultLoggerFactory()
	wan, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "1.2.3.0/24", LoggerFactory: logger})
	if err != nil {
		t.Fatal(err)
	}
	sn, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{"1.2.3.100"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = wan.AddNet(sn); err != nil {
		t.Fatal(err)
	}
	server, err := sn.ListenPacket("udp4", "1.2.3.100:3478")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := server.ReadFrom(buf)
			if err != nil {
				return
			}
			m := &stun.Message{Raw: append([]byte(nil), buf[:n]...)}
			if m.Decode() != nil || m.Type != stun.BindingRequest {
				continue
			}
			a := addr.(*net.UDPAddr)
			response, err := stun.Build(m, stun.BindingSuccess, &stun.XORMappedAddress{IP: a.IP, Port: a.Port})
			if err == nil {
				_, _ = server.WriteTo(response.Raw, addr)
			}
		}
	}()
	peers := make([]*Peer, 2)
	for i := range peers {
		mapping := vnet.EndpointIndependent
		if symmetric {
			mapping = vnet.EndpointAddrPortDependent
		}
		lan, err := vnet.NewRouter(&vnet.RouterConfig{
			CIDR: fmt.Sprintf("10.%d.0.0/24", i), StaticIPs: []string{fmt.Sprintf("1.2.3.%d", i+1)}, LoggerFactory: logger,
			NATType: &vnet.NATType{MappingBehavior: mapping, FilteringBehavior: vnet.EndpointAddrPortDependent},
		})
		if err != nil {
			t.Fatal(err)
		}
		n, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{fmt.Sprintf("10.%d.0.2", i)}})
		if err != nil {
			t.Fatal(err)
		}
		if err = lan.AddNet(n); err != nil {
			t.Fatal(err)
		}
		if err = wan.AddRouter(lan); err != nil {
			t.Fatal(err)
		}
		if blocked {
			lan.AddChunkFilter(func(c vnet.Chunk) bool {
				return strings.Contains(c.SourceAddr().String(), "1.2.3.100:") || strings.Contains(c.DestinationAddr().String(), "1.2.3.100:")
			})
		}
		settings := webrtc.SettingEngine{}
		settings.SetNet(n)
		peers[i], err = newPeer(context.Background(), []string{"stun:1.2.3.100:3478"}, settings)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(peers[i].Close)
	}
	if err := wan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wan.Stop() })
	return peers[0], peers[1]
}

func negotiate(t *testing.T, a, b *Peer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	offer, err := a.Offer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(offer, " typ srflx") {
		t.Fatal("NAT test did not discover a public mapping")
	}
	answer, err := b.Answer(ctx, offer)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Accept(answer); err != nil {
		t.Fatal(err)
	}
}

func TestDirectDataAcrossTwoPortRestrictedNATs(t *testing.T) {
	a, b := natPeers(t, false, false)
	negotiate(t, a, b)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ca, err := a.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := b.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := a.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
	if err != nil || pair == nil || pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
		t.Fatal("not a direct candidate pair", err)
	}
	data := bytes.Repeat([]byte("rsync\x00NAT\xff"), 200000)
	sa, sb := filetransport.New(ctx, ca), filetransport.New(ctx, cb)
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(sa, bytes.NewReader(data))
		if err == nil {
			err = sa.CloseWrite()
		}
		done <- err
	}()
	actual, err := io.ReadAll(sb)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatal("reliable stream lost data", len(actual), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err = sb.Write([]byte("verified")); err != nil {
		t.Fatal(err)
	}
	if err = sb.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	reply, err := io.ReadAll(sa)
	if err != nil || string(reply) != "verified" {
		t.Fatal("reverse stream", err)
	}
}

func TestUnreachableNATsFailWithoutRelay(t *testing.T) {
	for _, mode := range []string{"symmetric", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			a, b := natPeers(t, mode == "symmetric", mode == "blocked")
			negotiate(t, a, b)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if conn, err := a.Wait(ctx); err == nil {
				conn.CloseNow()
				t.Fatal("unexpected direct connection")
			}
		})
	}
}

func TestDirectRejectsRelayConfiguration(t *testing.T) {
	if p, err := New(context.Background(), []string{"turn:1.2.3.4:3478"}); err == nil {
		p.Close()
		t.Fatal("TURN accepted in direct mode")
	}
}
