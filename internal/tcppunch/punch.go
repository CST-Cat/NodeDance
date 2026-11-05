package tcppunch

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/stun/v4"
)

type mapping struct {
	stream   net.Conn
	listener net.Listener
	local    *net.TCPAddr
	public   netip.AddrPort
}

type Peer struct {
	mappings []*mapping
	once     sync.Once
}

// Mapping is one observed NAT binding: Stream keeps the binding alive, Listener
// re-binds the same local source port for the simultaneous open, Public is the
// externally observed address.
type Mapping struct {
	Stream   net.Conn
	Listener net.Listener
	Local    *net.TCPAddr
	Public   netip.AddrPort
}

func NewPeer(mappings ...Mapping) *Peer {
	p := &Peer{}
	for _, m := range mappings {
		p.mappings = append(p.mappings, &mapping{stream: m.Stream, listener: m.Listener, local: m.Local, public: m.Public})
	}
	return p
}

func Discover(ctx context.Context, servers []string) (*Peer, error) {
	mappings, err := Observe(ctx, servers)
	if err != nil {
		return nil, err
	}
	return NewPeer(mappings...), nil
}

// Observe gathers TCP NAT mappings from STUN servers over TCP without building a Peer.
func Observe(ctx context.Context, servers []string) ([]Mapping, error) {
	if len(servers) == 0 || len(servers) > 4 {
		return nil, errors.New("TCP STUN requires between one and four servers")
	}
	addresses := make([]string, 0, len(servers))
	for _, server := range servers {
		u, err := stun.ParseURI(server)
		if err != nil || u.Scheme != stun.SchemeTypeSTUN {
			return nil, errors.New("TCP discovery only accepts STUN URLs")
		}
		addresses = append(addresses, net.JoinHostPort(u.Host, strconv.Itoa(u.Port)))
	}
	results := make(chan Mapping, 2)
	gather, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for _, network := range []string{"tcp4", "tcp6"} {
		var found atomic.Bool
		for _, address := range addresses {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m, err := discover(gather, network, address)
				if err == nil {
					if found.CompareAndSwap(false, true) {
						results <- m
					} else {
						_ = m.Listener.Close()
						_ = m.Stream.Close()
					}
				}
			}()
		}
	}
	wg.Wait()
	close(results)
	var mappings []Mapping
	for m := range results {
		mappings = append(mappings, m)
	}
	if ctx.Err() != nil || len(mappings) == 0 {
		closeMappings(mappings)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("no TCP STUN mapping available")
	}
	return mappings, nil
}

func closeMappings(mappings []Mapping) {
	for _, m := range mappings {
		_ = m.Listener.Close()
		_ = m.Stream.Close()
	}
}

func discover(ctx context.Context, network, address string) (_ Mapping, failure error) {
	dialer := net.Dialer{Control: ReuseSocketControl, LocalAddr: &net.TCPAddr{}}
	stream, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return Mapping{}, err
	}
	defer func() {
		if failure != nil {
			_ = stream.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() {
		_ = stream.Close()
	})
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	if err != nil {
		return Mapping{}, err
	}
	if _, err = io.Copy(stream, bytes.NewReader(request.Raw)); err != nil {
		return Mapping{}, err
	}
	header := make([]byte, 20)
	if _, err = io.ReadFull(stream, header); err != nil {
		return Mapping{}, err
	}
	size := int(binary.BigEndian.Uint16(header[2:4]))
	if size > 2048 || size%4 != 0 || !bytes.Equal(header[8:20], request.TransactionID[:]) {
		return Mapping{}, errors.New("invalid TCP STUN response")
	}
	message := &stun.Message{Raw: make([]byte, 20+size)}
	copy(message.Raw, header)
	if _, err = io.ReadFull(stream, message.Raw[20:]); err != nil {
		return Mapping{}, err
	}
	if err = message.Decode(); err != nil || message.Type != stun.BindingSuccess {
		return Mapping{}, errors.New("TCP STUN binding failed")
	}
	if message.Contains(stun.AttrFingerprint) && stun.Fingerprint.Check(message) != nil {
		return Mapping{}, errors.New("invalid TCP STUN fingerprint")
	}
	var mapped stun.XORMappedAddress
	if err = mapped.GetFrom(message); err != nil || mapped.Port <= 0 || mapped.Port > 65535 {
		return Mapping{}, errors.New("TCP STUN response has no usable mapping")
	}
	ip, ok := netip.AddrFromSlice(mapped.IP)
	local := stream.LocalAddr().(*net.TCPAddr)
	if !ok || ip.IsUnspecified() || ip.IsMulticast() || ip.Unmap().Is4() != (network == "tcp4") {
		return Mapping{}, errors.New("invalid TCP STUN mapped address")
	}
	listener, err := (&net.ListenConfig{Control: ReuseSocketControl}).Listen(ctx, network, local.String())
	if err != nil {
		return Mapping{}, err
	}
	_ = stream.SetDeadline(time.Time{})
	return Mapping{Stream: stream, Listener: listener, Local: local, Public: netip.AddrPortFrom(ip.Unmap(), uint16(mapped.Port))}, nil
}

func (p *Peer) Addresses() []string {
	seen := make(map[string]bool)
	var addresses []string
	for _, m := range p.mappings {
		for _, address := range []string{m.public.String(), m.local.String()} {
			if !seen[address] {
				addresses = append(addresses, address)
				seen[address] = true
			}
		}
	}
	return addresses
}

func (p *Peer) Close() {
	p.once.Do(func() {
		for _, m := range p.mappings {
			_ = m.listener.Close()
			_ = m.stream.Close()
		}
	})
}

func (p *Peer) Connect(ctx context.Context, addresses []string, authenticate func(context.Context, net.Conn) (net.Conn, error)) (net.Conn, error) {
	if len(addresses) == 0 || len(addresses) > 64 || authenticate == nil {
		return nil, errors.New("invalid TCP punch peer")
	}
	var peers []netip.AddrPort
	seen := make(map[netip.AddrPort]bool)
	for _, address := range addresses {
		peer, err := netip.ParseAddrPort(address)
		if err != nil || peer.Port() == 0 || peer.Addr().IsUnspecified() || peer.Addr().IsMulticast() || peer.Addr().IsLinkLocalUnicast() || peer.Addr().Zone() != "" {
			return nil, errors.New("invalid TCP punch address")
		}
		if !seen[peer] {
			peers = append(peers, peer)
			seen[peer] = true
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer p.Close()
	ready := make(chan net.Conn)
	var wg sync.WaitGroup
	slots := make(chan struct{}, 8)
	publish := func(raw net.Conn) {
		secured, err := authenticate(ctx, raw)
		if err == nil {
			select {
			case ready <- secured:
				return
			case <-ctx.Done():
			}
		}
		_ = raw.Close()
	}
	for _, m := range p.mappings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				raw, err := m.listener.Accept()
				if err != nil {
					return
				}
				select {
				case slots <- struct{}{}:
					wg.Add(1)
					go func() {
						defer wg.Done()
						defer func() { <-slots }()
						publish(raw)
					}()
				default:
					_ = raw.Close()
				}
			}
		}()
		for _, peer := range peers {
			if peer.Addr().Is4() != (m.local.IP.To4() != nil) {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				defer func() { <-slots }()
				network := "tcp6"
				if peer.Addr().Is4() {
					network = "tcp4"
				}
				dialer := net.Dialer{Control: ReuseSocketControl, LocalAddr: m.local}
				first := true
				for ctx.Err() == nil {
					if raw, err := dialer.DialContext(ctx, network, peer.String()); err == nil {
						publish(raw)
						return
					} else if first {
						log.Printf("TCP punch dial failed local=%v peer=%v error=%v", m.local, peer, err)
						first = false
					}
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
					}
				}
			}()
		}
	}
	var result net.Conn
	var err error
	select {
	case result = <-ready:
	case <-ctx.Done():
		err = ctx.Err()
	}
	cancel()
	p.Close()
	wg.Wait()
	return result, err
}
