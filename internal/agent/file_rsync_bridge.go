package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agentrsync "github.com/CST-Cat/NodeDance/internal/agent/rsync"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/CST-Cat/NodeDance/internal/peertransport"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	"golang.org/x/net/netutil"
)

type agentRsyncSession struct {
	request  protocol.FileRsyncRequest
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	used     bool
	received bool
	signaled bool
	signal   chan protocol.FileRsyncRequest
}

// sweepRsyncSessions reaps attempts that can never serve another stream: a
// prepare session whose one-use grant expired without ever accepting, and a
// send session whose grant expired without ever receiving its start signal.
// Core aborts every attempt in the ordinary flow, so this only bounds the
// table when Core dies before it can abort; in-flight streams are left alone.
func (b *agentFileBridge) sweepRsyncSessions() {
	now := time.Now()
	b.mu.Lock()
	for key, session := range b.rsyncSessions {
		if session.request.Grant.ExpiresAt.After(now) {
			continue
		}
		session.mu.Lock()
		var spent bool
		if strings.HasPrefix(key, "prepare/") {
			spent = session.used || session.received
		} else {
			spent = session.signaled
		}
		session.mu.Unlock()
		if spent {
			continue
		}
		session.cancel()
		delete(b.rsyncSessions, key)
	}
	b.mu.Unlock()
}

func (b *agentFileBridge) respondRsync(ctx context.Context, id string, response protocol.FileRsyncResponse) {
	_ = b.writer.send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRsyncResponse, Generation: b.generation, RequestID: id, Payload: encodePayload(response)})
}

func (b *agentFileBridge) handleRsync(ctx context.Context, id string, request protocol.FileRsyncRequest) {
	response := protocol.FileRsyncResponse{Operation: request.Operation, SessionID: request.SessionID, State: "error", Code: "rsync_failed"}
	if b.nodeID != request.Descriptor.TargetNodeID && b.nodeID != request.Descriptor.File.Transfer.SourceNodeID {
		b.respondRsync(ctx, id, response)
		return
	}
	switch request.Operation {
	case "connect", "start":
		key := "prepare/"
		if request.Operation == "start" {
			if request.PeerMode != "tcp_punch" || b.nodeID != request.Descriptor.File.Transfer.SourceNodeID {
				break
			}
			key = "send/"
		}
		b.mu.Lock()
		session := b.rsyncSessions[key+request.SessionID]
		b.mu.Unlock()
		if session == nil || !reflect.DeepEqual(session.request.Descriptor, request.Descriptor) || session.request.PeerMode != request.PeerMode || session.request.Grant != request.Grant {
			break
		}
		session.mu.Lock()
		if !session.signaled && session.ctx.Err() == nil {
			session.signaled = true
			session.signal <- request
			response.State, response.Code = "signaled", ""
		}
		session.mu.Unlock()

	case "abort":
		b.mu.Lock()
		var sessions []*agentRsyncSession
		for key, session := range b.rsyncSessions {
			if session.request.SessionID == request.SessionID && reflect.DeepEqual(session.request.Descriptor, request.Descriptor) {
				session.cancel()
				sessions = append(sessions, session)
				delete(b.rsyncSessions, key)
			}
		}
		b.mu.Unlock()
		for _, session := range sessions {
			<-session.done
		}
		response.State, response.Code = "aborted", ""
	case "progress":
		if b.nodeID == request.Descriptor.TargetNodeID && b.tasks != nil {
			b.tasks.TransferProgress(request.Descriptor.TaskID, request.Completed, taskjournal.PhaseExecuting)
		}
		return
	case "commit":
		b.mu.Lock()
		session := b.rsyncSessions["prepare/"+request.SessionID]
		b.mu.Unlock()
		if session == nil || b.tasks == nil || b.nodeID != request.Descriptor.TargetNodeID || !reflect.DeepEqual(session.request.Descriptor, request.Descriptor) {
			break
		}
		session.mu.Lock()
		received := session.received
		session.mu.Unlock()
		if !received {
			break
		}
		if err := b.tasks.CommitRsync(ctx, request.Descriptor, request.Transport); err != nil {
			response.Code = fileErrorCode(err)
			break
		}
		response.State, response.Code = "committed", ""
	case "prepare", "send":
		key := request.Operation + "/" + request.SessionID
		b.mu.Lock()
		if b.closed || b.rsyncSessions[key] != nil || len(b.rsyncSessions) >= 2*maxAgentFileTransfers {
			b.mu.Unlock()
			break
		}
		sessionCtx, cancel := context.WithCancel(ctx)
		session := &agentRsyncSession{request: request, ctx: sessionCtx, cancel: cancel, done: make(chan struct{}), signal: make(chan protocol.FileRsyncRequest, 1)}
		b.rsyncSessions[key] = session
		b.mu.Unlock()
		defer close(session.done)
		if request.Operation == "prepare" {
			b.prepareRsync(id, session)
		} else {
			b.sendRsync(id, session)
		}
		return
	}
	b.respondRsync(ctx, id, response)
}

func (b *agentFileBridge) prepareRsync(id string, session *agentRsyncSession) {
	request := session.request
	response := protocol.FileRsyncResponse{Operation: request.Operation, SessionID: request.SessionID, State: "unavailable", Code: "rsync_unavailable"}
	if b.tasks == nil || b.nodeID != request.Descriptor.TargetNodeID {
		b.respondRsync(session.ctx, id, response)
		return
	}
	binary, cleanupBinary, err := agentrsync.Available(filepath.Dir(b.configPath))
	if err != nil {
		b.respondRsync(session.ctx, id, response)
		return
	}
	defer cleanupBinary()
	ctx, stage, release, err := b.tasks.AcquireRsync(session.ctx, request.Descriptor)
	if err != nil {
		response.Code = fileErrorCode(err)
		b.respondRsync(session.ctx, id, response)
		return
	}
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(release) }
	defer finish()
	template, err := agentrsync.ProbeReceiver(ctx, binary, filepath.Dir(b.configPath), protocol.FileBandwidthBytes(request.Descriptor.File.Transfer.BandwidthLimit))
	if err == nil {
		err = agentrsync.ProbeConfinement(ctx, binary, stage.Directory, stage.Payload, request.Descriptor.File.Size)
	}
	if err != nil {
		finish()
		b.respondRsync(session.ctx, id, response)
		return
	}
	var conn filetransport.Connection
	var cleanup func()
	if request.Transport == "direct" {
		conn, cleanup, err = b.receiveDirect(ctx, id, session)
	} else {
		response.State, response.Code = "ready", ""
		b.respondRsync(session.ctx, id, response)
		conn, cleanup, err = b.dialCoreFileStream(ctx, protocol.FileRsyncDataRoute, request.Grant)
	}

	if cleanup != nil {
		defer cleanup()
	}
	failureCode := "rsync_failed"
	if request.Transport == "direct" && err != nil {
		failureCode = directRsyncError(request.PeerMode, err)
	}
	if err == nil {
		stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
		err = agentrsync.Receive(ctx, binary, stage.Directory, stage.Payload, request.Descriptor.File.Size, template, conn)
		stop()
		if err == nil {
			err = stage.Payload.Sync()
		}
	}
	finish()
	response.State, response.Code = "received", ""
	if err != nil {
		response.State, response.Code = "error", failureCode
	} else {
		session.mu.Lock()
		session.received = true
		session.mu.Unlock()
	}
	b.respondRsync(session.ctx, id, response)
}

func (b *agentFileBridge) sendRsync(id string, session *agentRsyncSession) {
	request := session.request
	response := protocol.FileRsyncResponse{Operation: request.Operation, SessionID: request.SessionID, State: "unavailable", Code: "rsync_unavailable"}
	if b.nodeID != request.Descriptor.File.Transfer.SourceNodeID {
		b.respondRsync(session.ctx, id, response)
		return
	}
	binary, cleanupBinary, err := agentrsync.Available(filepath.Dir(b.configPath))
	if err != nil {
		b.respondRsync(session.ctx, id, response)
		return
	}
	defer cleanupBinary()
	source, entry, err := b.openTransfer(session.ctx, request.Descriptor.File.Transfer.SourcePath, request.Descriptor.File.Transfer.Directory)
	if err != nil || entry.Size != request.Descriptor.File.Size || entry.Version != request.Descriptor.File.Transfer.SourceVersion {
		response.State, response.Code = "error", "source_changed"
		b.respondRsync(session.ctx, id, response)
		if source != nil {
			_ = source.Close()
		}
		return
	}
	defer source.Close()
	var conn filetransport.Connection
	var cleanup func()
	if request.Transport == "direct" {
		conn, cleanup, err = b.sendDirect(id, session)
		if err != nil {
			response.State, response.Code = "error", directRsyncError(request.PeerMode, err)
			b.respondRsync(session.ctx, id, response)
			return
		}
	} else {
		conn, cleanup, err = b.dialCoreFileStream(session.ctx, protocol.FileRsyncDataRoute, request.Grant)
	}

	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		err = agentrsync.Send(session.ctx, binary, filepath.Dir(b.configPath), source, protocol.FileBandwidthBytes(request.Descriptor.File.Transfer.BandwidthLimit), conn, func(completed uint64) {
			if completed <= uint64(request.Descriptor.File.Size) {
				b.respondRsync(session.ctx, id, protocol.FileRsyncResponse{Operation: "send", SessionID: request.SessionID, State: "progress", Completed: completed})
			}
		})
	}
	response.State, response.Code = "sent", ""
	if err != nil {
		response.State, response.Code = "error", "rsync_failed"
	}
	b.respondRsync(session.ctx, id, response)
}

func rsyncCertificate() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certificate := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	data, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	digest := sha256.Sum256(data)
	return tls.Certificate{Certificate: [][]byte{data}, PrivateKey: key}, hex.EncodeToString(digest[:]), nil
}

func rsyncEndpoints(listener net.Listener) []string {
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	interfaces, _ := net.Interfaces()
	seen := map[string]bool{}
	var result []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			result = append(result, "wss://"+net.JoinHostPort(ip.String(), port)+protocol.FileRsyncDataRoute)
		}
	}
	sort.Strings(result)
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}

type rsyncListener struct {
	listener    net.Listener
	server      *http.Server
	fingerprint string
	pending     map[string]*rsyncIncoming
}

type rsyncIncoming struct {
	session  *agentRsyncSession
	incoming chan *websocket.Conn
}

func (b *agentFileBridge) listenRsync(ctx context.Context, session *agentRsyncSession) (<-chan *websocket.Conn, protocol.FileRsyncResponse, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	port := session.request.ListenPort
	if b.directListeners == nil {
		b.directListeners = make(map[int]*rsyncListener)
	}
	shared := b.directListeners[port]
	if port == 0 {
		shared = nil
	}
	if shared == nil {
		certificate, fingerprint, err := rsyncCertificate()
		if err != nil {
			return nil, protocol.FileRsyncResponse{}, nil, err
		}
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return nil, protocol.FileRsyncResponse{}, nil, err
		}
		shared = &rsyncListener{listener: listener, fingerprint: fingerprint, pending: make(map[string]*rsyncIncoming)}
		shared.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
		current := shared
		shared.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b.mu.Lock()
			pending := current.pending[r.Header.Get("X-Nodedance-Stream")]
			b.mu.Unlock()
			if pending == nil {
				http.Error(w, "unknown file task", http.StatusUnauthorized)
				return
			}
			acceptRsyncStream(w, r, pending.session, pending.incoming)
		})
		tlsListener := tls.NewListener(netutil.LimitListener(listener, 8), &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
		go func() { _ = current.server.Serve(tlsListener) }()
		if port != 0 {
			b.directListeners[port] = shared
		}
	}
	if shared.pending[session.request.SessionID] != nil {
		return nil, protocol.FileRsyncResponse{}, nil, errors.New("duplicate direct session")
	}
	pending := &rsyncIncoming{session: session, incoming: make(chan *websocket.Conn, 1)}
	shared.pending[session.request.SessionID] = pending
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(shared.pending, session.request.SessionID)
			empty := len(shared.pending) == 0
			if empty && port != 0 {
				delete(b.directListeners, port)
			}
			if empty {
				_ = shared.server.Close()
				_ = shared.listener.Close()
			}
			b.mu.Unlock()
		})
	}
	stop := context.AfterFunc(ctx, cleanup)
	return pending.incoming, protocol.FileRsyncResponse{Endpoints: rsyncEndpoints(shared.listener), Fingerprint: shared.fingerprint}, func() { stop(); cleanup() }, nil
}

func acceptRsyncStream(w http.ResponseWriter, r *http.Request, active *agentRsyncSession, incoming chan<- *websocket.Conn) {
	grant := active.request.Grant
	scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	actual, expected := sha256.Sum256([]byte(secret)), sha256.Sum256([]byte(grant.Secret))
	if r.Method != http.MethodGet || r.URL.Path != protocol.FileRsyncDataRoute || r.URL.RawQuery != "" || !strings.EqualFold(scheme, "Task") ||
		!grant.MatchesHeaders(r.Header) || !grant.ExpiresAt.After(time.Now()) || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
		http.Error(w, "file task credential rejected", http.StatusUnauthorized)
		return
	}
	active.mu.Lock()
	if active.used || active.ctx.Err() != nil {
		active.mu.Unlock()
		http.Error(w, "file task credential consumed", http.StatusUnauthorized)
		return
	}
	active.used = true
	active.mu.Unlock()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	select {
	case incoming <- conn:
	case <-active.ctx.Done():
		return
	}
	select {
	case <-active.done:
	case <-active.ctx.Done():
	}
}

func dialDirectRsync(ctx context.Context, request protocol.FileRsyncRequest) (*websocket.Conn, func(), error) {
	config := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) != 1 {
			return errors.New("unexpected rsync peer certificate")
		}
		digest := sha256.Sum256(state.PeerCertificates[0].Raw)
		if hex.EncodeToString(digest[:]) != request.Fingerprint {
			return errors.New("rsync peer certificate does not match the task offer")
		}
		return nil
	}}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	results := make(chan string, len(request.Endpoints))
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, endpoint := range request.Endpoints {
		wg.Add(1)
		go func(endpoint string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-probeCtx.Done():
				return
			}
			defer func() { <-slots }()
			parsed, _ := url.Parse(endpoint)
			dialCtx, stop := context.WithTimeout(probeCtx, time.Second)
			defer stop()
			connection, err := (&tls.Dialer{Config: config}).DialContext(dialCtx, "tcp", parsed.Host)
			if err == nil {
				_ = connection.Close()
				results <- endpoint
			}
		}(endpoint)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var selected string
	select {
	case selected = <-results:
	case <-done:
		select {
		case selected = <-results:
		default:
		}
	case <-probeCtx.Done():
	}
	cancel()
	<-done
	if selected == "" {
		return nil, nil, errors.New("no reachable rsync peer endpoint")
	}
	transport := &http.Transport{TLSClientConfig: config}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	dialCtx, stop := context.WithDeadline(ctx, request.Grant.ExpiresAt)
	defer stop()
	conn, _, err := websocket.Dial(dialCtx, selected, &websocket.DialOptions{HTTPClient: client, HTTPHeader: request.Grant.Headers(), CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, err
	}
	cleanup := func() { _ = conn.CloseNow(); transport.CloseIdleConnections() }
	return conn, cleanup, nil
}

func directSTUNServers() []string {
	value, ok := os.LookupEnv("NODEDANCE_STUN_SERVERS")
	if !ok {
		return []string{"stun:stun.cloudflare.com:3478", "stun:stun.l.google.com:19302"}
	}
	if strings.TrimSpace(value) == "" || value == "off" {
		return nil
	}
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' })
}

func waitIncoming(ctx context.Context, incoming <-chan *websocket.Conn) (*websocket.Conn, error) {
	select {
	case conn := <-incoming:
		return conn, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *agentFileBridge) receiveDirect(ctx context.Context, id string, session *agentRsyncSession) (filetransport.Connection, func(), error) {
	setup, cancel := context.WithDeadline(ctx, minTime(session.request.Grant.ExpiresAt, time.Now().Add(20*time.Second)))
	defer cancel()
	ready := protocol.FileRsyncResponse{Operation: "prepare", SessionID: session.request.SessionID, State: "ready"}
	if session.request.PeerMode == "tcp_punch" {
		return b.receivePunchedRsync(setup, id, session, ready)
	}
	if session.request.PeerMode == "tcp" {
		incoming, offer, cleanup, err := b.listenRsync(ctx, session)
		if err != nil {
			return nil, nil, err
		}
		ready.Endpoints, ready.Fingerprint = offer.Endpoints, offer.Fingerprint
		b.respondRsync(ctx, id, ready)
		conn, err := waitIncoming(setup, incoming)
		return conn, cleanup, err
	}
	var peer *peertransport.Peer
	if session.request.PeerMode == "ice" {
		var err error
		peer, err = peertransport.New(ctx, directSTUNServers())
		if err != nil {
			return nil, nil, err
		}
		ready.SDP, err = peer.Offer(setup)
		if err != nil {
			peer.Close()
			return nil, nil, err
		}
	}
	b.respondRsync(ctx, id, ready)
	var signal protocol.FileRsyncRequest
	select {
	case signal = <-session.signal:
	case <-setup.Done():
		if peer != nil {
			peer.Close()
		}
		return nil, nil, setup.Err()
	}
	if peer == nil {
		conn, cleanup, err := dialDirectRsync(setup, signal)
		return conn, cleanup, err
	}
	cleanup := func() { peer.Close() }
	if err := peer.Accept(signal.SDP); err != nil {
		return nil, cleanup, err
	}
	conn, err := peer.Wait(setup)
	if err == nil {
		err = authenticatePeer(setup, conn, session.request.Grant, true)
	}
	return conn, cleanup, err
}

func (b *agentFileBridge) sendDirect(id string, session *agentRsyncSession) (filetransport.Connection, func(), error) {
	request := session.request
	if request.PeerMode == "tcp" {
		return dialDirectRsync(session.ctx, request)
	}
	setup, cancel := context.WithDeadline(session.ctx, minTime(request.Grant.ExpiresAt, time.Now().Add(20*time.Second)))
	defer cancel()
	if request.PeerMode == "tcp_punch" {
		return b.sendPunchedRsync(setup, id, session)
	}
	offer := protocol.FileRsyncResponse{Operation: "send", SessionID: request.SessionID, State: "offer"}
	if request.PeerMode == "reverse" {
		incoming, endpoint, cleanup, err := b.listenRsync(session.ctx, session)
		if err != nil {
			return nil, nil, err
		}
		offer.Endpoints, offer.Fingerprint = endpoint.Endpoints, endpoint.Fingerprint
		b.respondRsync(session.ctx, id, offer)
		conn, err := waitIncoming(setup, incoming)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		return conn, cleanup, err
	}
	peer, err := peertransport.New(session.ctx, directSTUNServers())
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { peer.Close() }
	offer.SDP, err = peer.Answer(setup, request.SDP)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	b.respondRsync(session.ctx, id, offer)
	conn, err := peer.Wait(setup)
	if err == nil {
		err = authenticatePeer(setup, conn, request.Grant, false)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return conn, cleanup, nil
}

func authenticatePeer(ctx context.Context, conn filetransport.Connection, grant protocol.FileStreamGrant, receiver bool) error {
	conn.SetReadLimit(8 << 10)
	if receiver {
		kind, data, err := conn.Read(ctx)
		var actual protocol.FileStreamGrant
		if err != nil {
			return err
		}
		expectedDigest := protocol.FileStreamDigest(grant)
		if kind != websocket.MessageText || json.Unmarshal(data, &actual) != nil || !grant.ExpiresAt.After(time.Now()) || subtle.ConstantTimeCompare([]byte(protocol.FileStreamDigest(actual)), []byte(expectedDigest)) != 1 {
			return errors.New("peer task credential rejected")
		}
		return conn.Write(ctx, websocket.MessageText, []byte("OK"))
	}
	data, _ := json.Marshal(grant)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		return err
	}
	kind, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	if kind != websocket.MessageText || string(data) != "OK" {
		return errors.New("peer task authorization failed")
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func directRsyncError(mode string, err error) string {
	if errors.Is(err, syscall.EADDRINUSE) {
		return "direct_listen_in_use"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "direct_" + mode + "_timeout"
	}
	return "direct_" + mode + "_unavailable"
}
