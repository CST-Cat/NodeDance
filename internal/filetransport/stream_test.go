package filetransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// streamPair connects two file streams over an in-memory websocket and also
// returns the raw connections so tests can drive Forward directly.
func streamPair(t *testing.T) (client *Stream, server *Stream, clientRaw, serverRaw *websocket.Conn) {
	t.Helper()
	ctx := context.Background()
	serverReady := make(chan *websocket.Conn, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		serverReady <- conn
	}))
	t.Cleanup(httpServer.Close)
	raw, _, err := websocket.Dial(ctx, strings.Replace(httpServer.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatalf("dial test stream: %v", err)
	}
	t.Cleanup(func() { raw.CloseNow() })
	select {
	case serverConn := <-serverReady:
		return New(ctx, raw), New(ctx, serverConn), raw, serverConn
	case <-time.After(5 * time.Second):
		t.Fatal("test server stream never connected")
		return nil, nil, nil, nil
	}
}

func TestStreamCarriesBinaryAndSignalsEOF(t *testing.T) {
	client, server, _, _ := streamPair(t)
	payload := []byte("rsync data chunk")
	if _, err := client.Write(payload); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}
	buffer := make([]byte, len(payload))
	count, err := io.ReadFull(server, buffer)
	if err != nil || count != len(payload) || string(buffer) != string(payload) {
		t.Fatalf("server read: %d %q %v", count, buffer, err)
	}
	if _, err := server.Read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF sentinel missing: %v", err)
	}
	// A writer may not send payload after its own EOF.
	if _, err := client.Write([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write after CloseWrite: %v", err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatalf("repeated CloseWrite must stay idempotent: %v", err)
	}
}

func TestStreamRejectsForeignTextMessages(t *testing.T) {
	ctx := context.Background()
	client, server, clientRaw, _ := streamPair(t)
	if err := clientRaw.Write(ctx, websocket.MessageText, []byte("NOT-EOF")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Read(make([]byte, 16)); err == nil || err.Error() != "invalid file stream EOF" {
		t.Fatalf("a forged text message must abort the stream: %v", err)
	}
	_ = client
}

func TestForwardPreservesMessageKinds(t *testing.T) {
	ctx := context.Background()
	client, relaySource, clientRaw, relaySourceRaw := streamPair(t)
	_, receiver, relayTargetRaw, _ := streamPair(t)
	go func() {
		_ = Forward(ctx, relaySourceRaw, relayTargetRaw)
	}()
	if err := clientRaw.Write(ctx, websocket.MessageBinary, []byte("binary")); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 6)
	if _, err := io.ReadFull(receiver, payload); err != nil || string(payload) != "binary" {
		t.Fatalf("forwarded binary payload: %q %v", payload, err)
	}
	if _, err := receiver.Read(make([]byte, 4)); !errors.Is(err, io.EOF) {
		t.Fatalf("forwarded EOF sentinel: %v", err)
	}
	_ = relaySource
}
