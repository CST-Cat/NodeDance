package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type peerTestWriter chan protocol.FileRsyncResponse

func (w peerTestWriter) send(ctx context.Context, e protocol.Envelope) error {
	var response protocol.FileRsyncResponse
	if err := json.Unmarshal(e.Payload, &response); err != nil {
		return err
	}
	select {
	case w <- response:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type peerTestResult struct {
	conn    filetransport.Connection
	cleanup func()
	err     error
}

func TestAgentNegotiatesReverseAndICEDataStreams(t *testing.T) {
	t.Setenv("NODEDANCE_STUN_SERVERS", "off")
	for _, mode := range []string{"reverse", "ice", "ice_bad_credential"} {
		t.Run(mode, func(t *testing.T) {
			peerMode := mode
			if peerMode == "ice_bad_credential" {
				peerMode = "ice"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			descriptor := protocol.FileTransferDescriptor{TaskID: "task", TargetNodeID: "target", File: protocol.FileTaskSpec{Transfer: &protocol.FileTransferSpec{SourceNodeID: "source"}}}
			grant := protocol.FileStreamGrant{SessionID: "session", Secret: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute)}
			request := protocol.FileRsyncRequest{Operation: "prepare", SessionID: "session", Descriptor: descriptor, Grant: grant, Transport: "direct", PeerMode: peerMode}
			receiver := &agentRsyncSession{request: request, ctx: ctx, done: make(chan struct{}), signal: make(chan protocol.FileRsyncRequest, 1)}
			defer close(receiver.done)
			target := &agentFileBridge{nodeID: "target", writer: make(peerTestWriter, 4), rsyncSessions: map[string]*agentRsyncSession{"prepare/session": receiver}}
			targetResult := make(chan peerTestResult, 1)
			go func() {
				conn, cleanup, err := target.receiveDirect(ctx, "prepare", receiver)
				if err != nil && cleanup != nil {
					cleanup()
					cleanup = nil
				}
				targetResult <- peerTestResult{conn, cleanup, err}
			}()
			var ready protocol.FileRsyncResponse
			select {
			case ready = <-target.writer.(peerTestWriter):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			request.Operation, request.Endpoints, request.Fingerprint, request.SDP = "send", ready.Endpoints, ready.Fingerprint, ready.SDP
			if mode == "ice_bad_credential" {
				request.Grant.Secret = strings.Repeat("b", 64)
			}
			sender := &agentRsyncSession{request: request, ctx: ctx, done: make(chan struct{})}
			defer close(sender.done)
			source := &agentFileBridge{writer: make(peerTestWriter, 4)}
			sourceResult := make(chan peerTestResult, 1)
			go func() {
				conn, cleanup, err := source.sendDirect("send", sender)
				sourceResult <- peerTestResult{conn, cleanup, err}
			}()
			var offer protocol.FileRsyncResponse
			select {
			case offer = <-source.writer.(peerTestWriter):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			signal := request
			signal.Grant = grant
			signal.Operation, signal.Endpoints, signal.Fingerprint, signal.SDP = "connect", offer.Endpoints, offer.Fingerprint, offer.SDP
			target.handleRsync(ctx, "connect", signal)
			if reply := <-target.writer.(peerTestWriter); reply.State != "signaled" {
				t.Fatal(reply)
			}
			target.handleRsync(ctx, "replay", signal)
			if reply := <-target.writer.(peerTestWriter); reply.State != "error" {
				t.Fatal("accepted repeated negotiation", reply)
			}
			var a, b peerTestResult
			select {
			case a = <-sourceResult:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if a.cleanup != nil {
				defer a.cleanup()
			}
			select {
			case b = <-targetResult:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if b.cleanup != nil {
				defer b.cleanup()
			}
			if mode == "ice_bad_credential" {
				if a.err == nil || b.err == nil {
					t.Fatal("accepted another task credential", a.err, b.err)
				}
				return
			}
			if a.err != nil || b.err != nil {
				t.Fatal(a.err, b.err)
			}
			data := bytes.Repeat([]byte("authenticated rsync stream\x00"), 16000)
			sa, sb := filetransport.New(ctx, a.conn), filetransport.New(ctx, b.conn)
			done := make(chan error, 1)
			go func() {
				_, err := io.Copy(sa, bytes.NewReader(data))
				if err == nil {
					err = sa.CloseWrite()
				}
				done <- err
			}()
			actual, err := io.ReadAll(sb)
			if err != nil || !bytes.Equal(data, actual) {
				t.Fatal("direct stream differs", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectFixedPortSupportsConcurrentTaskListeners(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	bridge := &agentFileBridge{}
	for i := range 2 {
		request := protocol.FileRsyncRequest{SessionID: strconv.Itoa(i), ListenPort: port, Grant: protocol.FileStreamGrant{SessionID: strconv.Itoa(i), Secret: strings.Repeat(strconv.Itoa(i), 64), ExpiresAt: time.Now().Add(time.Minute)}}
		session := &agentRsyncSession{request: request, ctx: ctx, done: make(chan struct{})}
		defer close(session.done)
		incoming, offer, cleanup, err := bridge.listenRsync(ctx, session)
		if err != nil {
			t.Fatal("concurrent fixed-port listener", err)
		}
		defer cleanup()
		request.Endpoints, request.Fingerprint = offer.Endpoints, offer.Fingerprint
		conn, closeConn, err := dialDirectRsync(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		defer closeConn()
		var remote filetransport.Connection
		select {
		case remote = <-incoming:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		if err = conn.Write(ctx, 1, []byte(request.SessionID)); err != nil {
			t.Fatal(err)
		}
		_, data, err := remote.Read(ctx)
		if err != nil || string(data) != request.SessionID {
			t.Fatal("listener routed to another task", err)
		}
		if replay, cleanupReplay, err := dialDirectRsync(ctx, request); err == nil {
			cleanupReplay()
			replay.CloseNow()
			t.Fatal("consumed listener credential was reused")
		}
	}
}
