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
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentrsync "github.com/CST-Cat/NodeDance/internal/agent/rsync"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
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
		session := &agentRsyncSession{request: request, ctx: sessionCtx, cancel: cancel, done: make(chan struct{})}
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
	template, err := agentrsync.ProbeReceiver(ctx, binary, filepath.Dir(b.configPath), request.Descriptor.File.Transfer.BandwidthLimit)
	if err == nil {
		err = agentrsync.ProbeConfinement(ctx, binary, stage.Directory, stage.Payload, request.Descriptor.File.Size)
	}
	if err != nil {
		finish()
		b.respondRsync(session.ctx, id, response)
		return
	}
	var conn *websocket.Conn
	var cleanup func()
	if request.Transport == "direct" {
		var offer protocol.FileRsyncResponse
		var incoming <-chan *websocket.Conn
		incoming, offer, cleanup, err = listenRsync(ctx, session)
		if err == nil {
			offer.Operation, offer.SessionID, offer.State = request.Operation, request.SessionID, "ready"
			b.respondRsync(session.ctx, id, offer)
			timer := time.NewTimer(time.Until(request.Grant.ExpiresAt))
			select {
			case conn = <-incoming:
			case <-ctx.Done():
				err = ctx.Err()
			case <-timer.C:
				err = errors.New("direct rsync credential expired before connection")
			}
			timer.Stop()
		}
	} else {
		response.State, response.Code = "ready", ""
		b.respondRsync(session.ctx, id, response)
		conn, cleanup, err = b.dialCoreFileStream(ctx, protocol.FileRsyncDataRoute, request.Grant)
	}
	if cleanup != nil {
		defer cleanup()
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
		response.State, response.Code = "error", "rsync_failed"
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
	source, entry, err := b.service.OpenDownload(request.Descriptor.File.Transfer.SourcePath)
	if err != nil || entry.Size != request.Descriptor.File.Size || entry.Version != request.Descriptor.File.Transfer.SourceVersion {
		response.State, response.Code = "error", "source_changed"
		b.respondRsync(session.ctx, id, response)
		if source != nil {
			_ = source.Close()
		}
		return
	}
	defer source.Close()
	var conn *websocket.Conn
	var cleanup func()
	if request.Transport == "direct" {
		conn, cleanup, err = dialDirectRsync(session.ctx, request)
		if err != nil {
			response.Code = "direct_unavailable"
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
		err = agentrsync.Send(session.ctx, binary, filepath.Dir(b.configPath), source, request.Descriptor.File.Transfer.BandwidthLimit, conn, func(completed uint64) {
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

func listenRsync(ctx context.Context, session *agentRsyncSession) (<-chan *websocket.Conn, protocol.FileRsyncResponse, func(), error) {
	certificate, fingerprint, err := rsyncCertificate()
	if err != nil {
		return nil, protocol.FileRsyncResponse{}, nil, err
	}
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(session.request.ListenPort))
	if err != nil {
		return nil, protocol.FileRsyncResponse{}, nil, err
	}
	offer := protocol.FileRsyncResponse{Endpoints: rsyncEndpoints(listener), Fingerprint: fingerprint}
	incoming := make(chan *websocket.Conn, 1)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0), BaseContext: func(net.Listener) context.Context { return ctx }}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grant := session.request.Grant
		scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		actual, expected := sha256.Sum256([]byte(secret)), sha256.Sum256([]byte(grant.Secret))
		if r.Method != http.MethodGet || r.URL.Path != protocol.FileRsyncDataRoute || r.URL.RawQuery != "" || !strings.EqualFold(scheme, "Task") ||
			!grant.MatchesHeaders(r.Header) || !grant.ExpiresAt.After(time.Now()) || subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			http.Error(w, "file task credential rejected", http.StatusUnauthorized)
			return
		}
		session.mu.Lock()
		if session.used || ctx.Err() != nil {
			session.mu.Unlock()
			http.Error(w, "file task credential consumed", http.StatusUnauthorized)
			return
		}
		session.used = true
		session.mu.Unlock()
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		select {
		case incoming <- conn:
		case <-ctx.Done():
			_ = conn.CloseNow()
			return
		}
		select {
		case <-session.done:
		case <-ctx.Done():
		}
		_ = conn.CloseNow()
	})
	tlsListener := tls.NewListener(netutil.LimitListener(listener, 8), &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13})
	go func() { _ = server.Serve(tlsListener) }()
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	cleanup := func() { stop(); _ = server.Close(); _ = listener.Close() }
	return incoming, offer, cleanup, nil
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
