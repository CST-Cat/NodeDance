package peertransport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/datachannel"
	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

const maxMessage = 64 << 10
const label = "nodedance-rsync"

type Peer struct {
	pc       *webrtc.PeerConnection
	ready    chan datachannel.ReadWriteCloserDeadliner
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	stop     func() bool
	accepted atomic.Bool
}

func New(ctx context.Context, servers []string) (*Peer, error) {
	return newPeer(ctx, servers, webrtc.SettingEngine{})
}

func newPeer(ctx context.Context, servers []string, settings webrtc.SettingEngine) (*Peer, error) {
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	settings.DetachDataChannels()
	settings.EnableDataChannelBlockWrite(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	settings.SetSTUNGatherTimeout(3 * time.Second)
	settings.SetICETimeouts(5*time.Second, 20*time.Second, 2*time.Second)
	settings.SetSCTPMaxReceiveBufferSize(1 << 20)
	settings.SetSCTPMaxMessageSize(maxMessage)
	config := webrtc.Configuration{}
	for _, server := range servers {
		if !strings.HasPrefix(server, "stun:") {
			return nil, errors.New("only STUN servers are allowed for direct transfers")
		}
		config.ICEServers = append(config.ICEServers, webrtc.ICEServer{URLs: []string{server}})
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Peer{pc: pc, ready: make(chan datachannel.ReadWriteCloserDeadliner, 1), ctx: ctx, cancel: cancel}
	p.stop = context.AfterFunc(ctx, func() { _ = pc.Close() })
	pc.OnDataChannel(p.attach)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			cancel()
		}
	})
	return p, nil
}

func (p *Peer) attach(dc *webrtc.DataChannel) {
	if dc.Label() != label || dc.Protocol() != label || !dc.Ordered() || dc.MaxRetransmits() != nil || dc.MaxPacketLifeTime() != nil || !p.accepted.CompareAndSwap(false, true) {
		go p.Close()
		return
	}
	dc.OnOpen(func() {
		pair, err := p.pc.SCTP().Transport().ICETransport().GetSelectedCandidatePair()
		if err != nil || pair == nil || pair.Local.Typ == webrtc.ICECandidateTypeRelay || pair.Remote.Typ == webrtc.ICECandidateTypeRelay {
			go p.Close()
			return
		}
		raw, err := dc.DetachWithDeadline()
		if err != nil {
			go p.Close()
			return
		}
		select {
		case p.ready <- raw:
		case <-p.ctx.Done():
			_ = raw.Close()
		}
	})
}

func (p *Peer) description(ctx context.Context, d webrtc.SessionDescription) (string, error) {
	done := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(d); err != nil {
		return "", err
	}
	select {
	case <-done:
		sdp := p.pc.LocalDescription().SDP
		if len(sdp) > maxMessage {
			return "", errors.New("peer description exceeds limit")
		}
		return sdp, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-p.ctx.Done():
		return "", errors.New("peer closed while gathering candidates")
	}
}

func (p *Peer) Offer(ctx context.Context) (string, error) {
	protocol := label
	dc, err := p.pc.CreateDataChannel(label, &webrtc.DataChannelInit{Protocol: &protocol})
	if err != nil {
		return "", err
	}
	p.attach(dc)
	offer, err := p.pc.CreateOffer(nil)
	if err != nil {
		return "", err
	}
	return p.description(ctx, offer)
}

func (p *Peer) Answer(ctx context.Context, sdp string) (string, error) {
	if err := p.remote(sdp, webrtc.SDPTypeOffer); err != nil {
		return "", err
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}
	return p.description(ctx, answer)
}

func (p *Peer) Accept(sdp string) error { return p.remote(sdp, webrtc.SDPTypeAnswer) }

func (p *Peer) remote(sdp string, kind webrtc.SDPType) error {
	if sdp == "" || len(sdp) > maxMessage {
		return errors.New("invalid peer description size")
	}
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: kind, SDP: sdp})
}

func (p *Peer) Wait(ctx context.Context) (*Connection, error) {
	select {
	case raw := <-p.ready:
		return &Connection{raw: raw, peer: p, limit: atomic.Int64{}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, errors.New("ICE connectivity or DTLS negotiation failed")
	}
}

func (p *Peer) Close() { p.once.Do(func() { p.cancel(); p.stop(); _ = p.pc.Close() }) }

type Connection struct {
	raw     datachannel.ReadWriteCloserDeadliner
	peer    *Peer
	limit   atomic.Int64
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func deadline(ctx context.Context, set func(time.Time) error) func() {
	if d, ok := ctx.Deadline(); ok {
		_ = set(d)
	} else {
		_ = set(time.Time{})
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = set(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = set(time.Time{})
	}
}

func (c *Connection) SetReadLimit(n int64)                     { c.limit.Store(n) }
func (c *Connection) CloseNow() error                          { c.peer.Close(); return nil }
func (c *Connection) Close(websocket.StatusCode, string) error { return c.CloseNow() }

func (c *Connection) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	finish := deadline(ctx, c.raw.SetReadDeadline)
	defer finish()
	b := make([]byte, maxMessage)
	n, text, err := c.raw.ReadDataChannel(b)
	kind := websocket.MessageBinary
	if text {
		kind = websocket.MessageText
	}
	if err == nil && c.limit.Load() > 0 && int64(n) > c.limit.Load() {
		err = errors.New("peer message exceeds read limit")
	}
	return kind, b[:n], err
}

func (c *Connection) Reader(ctx context.Context) (websocket.MessageType, io.Reader, error) {
	kind, b, err := c.Read(ctx)
	return kind, bytes.NewReader(b), err
}

func (c *Connection) Write(ctx context.Context, kind websocket.MessageType, b []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if kind != websocket.MessageBinary && kind != websocket.MessageText {
		return errors.New("invalid peer message type")
	}
	if kind == websocket.MessageText && len(b) > maxMessage {
		return errors.New("peer control message exceeds limit")
	}
	finish := deadline(ctx, c.raw.SetWriteDeadline)
	defer finish()
	for {
		count := len(b)
		if kind == websocket.MessageBinary && count > 16<<10 {
			count = 16 << 10
		}
		n, err := c.raw.WriteDataChannel(b[:count], kind == websocket.MessageText)
		if err != nil {
			return err
		}
		if n != count {
			return io.ErrShortWrite
		}
		b = b[count:]
		if len(b) == 0 {
			return nil
		}
	}
}
