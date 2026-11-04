package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/tcppunch"
	"github.com/coder/websocket"
)

func tcpSTUNServers() []string {
	value, ok := os.LookupEnv("NODEDANCE_TCP_STUN_SERVERS")
	if !ok {
		return []string{"stun:stun.nextcloud.com:443"}
	}
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) == "off" {
		return nil
	}
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
}

func punchEndpoints(peer *tcppunch.Peer) []string {
	var endpoints []string
	for _, address := range peer.Addresses() {
		endpoints = append(endpoints, "wss://"+address+protocol.FileRsyncDataRoute)
	}
	return endpoints
}

func punchAddresses(endpoints []string) []string {
	var addresses []string
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err == nil && protocol.ValidFilePeerEndpoint(endpoint) {
			addresses = append(addresses, parsed.Host)
		}
	}
	return addresses
}

func punchHandshake(certificate tls.Certificate, fingerprint string, receiver bool) func(context.Context, net.Conn) (net.Conn, error) {
	var selected atomic.Bool
	return func(ctx context.Context, raw net.Conn) (_ net.Conn, failure error) {
		stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
		defer stop()
		if deadline, ok := ctx.Deadline(); ok {
			_ = raw.SetDeadline(deadline)
		}
		config := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("unexpected TCP punch peer certificate")
			}
			digest := sha256.Sum256(state.PeerCertificates[0].Raw)
			if hex.EncodeToString(digest[:]) != fingerprint {
				return errors.New("TCP punch peer certificate does not match its task offer")
			}
			return nil
		}}
		var secured *tls.Conn
		if receiver {
			config.ClientAuth = tls.RequireAnyClientCert
			secured = tls.Server(raw, config)
		} else {
			secured = tls.Client(raw, config)
		}
		if err := secured.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		selection := []byte("NodeDance TCP punch\n")
		ack := []byte("OK\n")
		if receiver {
			actual := make([]byte, len(selection))
			if _, err := io.ReadFull(secured, actual); err != nil || !bytes.Equal(actual, selection) {
				return nil, errors.New("TCP punch stream was not selected")
			}
			if _, err := secured.Write(ack); err != nil {
				return nil, err
			}
		} else {
			if !selected.CompareAndSwap(false, true) {
				return nil, errors.New("another TCP punch stream was selected")
			}
			defer func() {
				if failure != nil {
					selected.Store(false)
				}
			}()
			if _, err := secured.Write(selection); err != nil {
				return nil, err
			}
			actual := make([]byte, len(ack))
			if _, err := io.ReadFull(secured, actual); err != nil || !bytes.Equal(actual, ack) {
				return nil, errors.New("TCP punch selection was not acknowledged")
			}
		}
		_ = secured.SetDeadline(time.Time{})
		return secured, nil
	}
}

type punchedListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
	used bool
}

func (l *punchedListener) Accept() (net.Conn, error) {
	if !l.used {
		l.used = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *punchedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *punchedListener) Addr() net.Addr { return l.conn.LocalAddr() }

func connectPunchedRsync(ctx context.Context, peer *tcppunch.Peer, endpoints []string, certificate tls.Certificate, fingerprint, taskID string, receiver bool) (net.Conn, error) {
	started := time.Now()
	deadline, _ := ctx.Deadline()
	log.Printf("Agent direct TCP punch task=%s receiver=%t stage=connect candidates=%d remaining=%s", taskID, receiver, len(endpoints), time.Until(deadline).Round(time.Millisecond))
	authenticate := punchHandshake(certificate, fingerprint, receiver)
	conn, err := peer.Connect(ctx, punchAddresses(endpoints), func(ctx context.Context, raw net.Conn) (net.Conn, error) {
		log.Printf("Agent direct TCP punch task=%s receiver=%t stage=tls peer=%s", taskID, receiver, raw.RemoteAddr())
		secured, err := authenticate(ctx, raw)
		if err != nil {
			log.Printf("Agent direct TCP punch task=%s receiver=%t stage=tls_rejected error=%v", taskID, receiver, err)
		}
		return secured, err
	})
	log.Printf("Agent direct TCP punch task=%s receiver=%t stage=connect_result elapsed=%s error=%v", taskID, receiver, time.Since(started).Round(time.Millisecond), err)
	return conn, err
}

func (b *agentFileBridge) receivePunchedRsync(ctx context.Context, id string, session *agentRsyncSession, ready protocol.FileRsyncResponse) (filetransport.Connection, func(), error) {
	peer, err := tcppunch.Discover(ctx, tcpSTUNServers())
	if err != nil {
		return nil, nil, err
	}
	defer peer.Close()
	certificate, fingerprint, err := rsyncCertificate()
	if err != nil {
		return nil, nil, err
	}
	ready.Endpoints, ready.Fingerprint = punchEndpoints(peer), fingerprint
	b.respondRsync(ctx, id, ready)
	var signal protocol.FileRsyncRequest
	select {
	case signal = <-session.signal:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	raw, err := connectPunchedRsync(ctx, peer, signal.Endpoints, certificate, signal.Fingerprint, session.request.Descriptor.TaskID, true)
	if err != nil {
		return nil, nil, err
	}
	listener := &punchedListener{conn: raw, done: make(chan struct{})}
	incoming := make(chan *websocket.Conn, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptRsyncStream(w, r, session, incoming)
	})}
	go func() { _ = server.Serve(listener) }()
	cleanup := func() {
		_ = server.Close()
		_ = raw.Close()
	}
	conn, err := waitIncoming(ctx, incoming)
	return conn, cleanup, err
}

func (b *agentFileBridge) sendPunchedRsync(ctx context.Context, id string, session *agentRsyncSession) (filetransport.Connection, func(), error) {
	peer, err := tcppunch.Discover(ctx, tcpSTUNServers())
	if err != nil {
		return nil, nil, err
	}
	defer peer.Close()
	certificate, fingerprint, err := rsyncCertificate()
	if err != nil {
		return nil, nil, err
	}
	request := session.request
	offer := protocol.FileRsyncResponse{Operation: "send", SessionID: request.SessionID, State: "offer", Endpoints: punchEndpoints(peer), Fingerprint: fingerprint}
	b.respondRsync(ctx, id, offer)
	raw, err := connectPunchedRsync(ctx, peer, request.Endpoints, certificate, request.Fingerprint, request.Descriptor.TaskID, false)
	if err != nil {
		return nil, nil, err
	}
	var consumed atomic.Bool
	transport := &http.Transport{DialTLSContext: func(context.Context, string, string) (net.Conn, error) {
		if !consumed.CompareAndSwap(false, true) {
			return nil, errors.New("TCP punch stream already consumed")
		}
		return raw, nil
	}}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, _, err := websocket.Dial(ctx, request.Endpoints[0], &websocket.DialOptions{HTTPClient: client, HTTPHeader: request.Grant.Headers(), CompressionMode: websocket.CompressionDisabled})
	cleanup := func() {
		_ = raw.Close()
		transport.CloseIdleConnections()
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return conn, cleanup, nil
}
