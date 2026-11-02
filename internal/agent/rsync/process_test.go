package rsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMain(m *testing.M) {
	if handled, err := Internal(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func rsyncFixture(t *testing.T, content []byte) (string, string, *os.File, *os.File, *os.File) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("real rsync transfer requires a root Agent")
	}
	binary, err := Available()
	if err != nil {
		t.Skipf("real rsync is unavailable: %v", err)
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(state, "stage")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "source"), content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(filepath.Join(state, "source"))
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.OpenFile(filepath.Join(stage, "payload"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { source.Close(); payload.Close(); directory.Close() })
	return binary, state, source, directory, payload
}

func runTransfer(t *testing.T, ctx context.Context, binary, state string, source, directory, payload *os.File, size, bandwidth int64) (error, error) {
	t.Helper()
	template, err := ProbeReceiver(ctx, binary, state, bandwidth)
	if err != nil {
		t.Fatalf("runtime receiver probe: %v", err)
	}
	if err := ProbeConfinement(ctx, binary, directory, payload, size); err != nil {
		t.Fatalf("runtime confinement probe: %v", err)
	}
	received := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			received <- err
			return
		}
		defer conn.CloseNow()
		stop := context.AfterFunc(ctx, func() { conn.CloseNow() })
		defer stop()
		received <- Receive(ctx, binary, directory, payload, size, template, conn)
	}))
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client(), CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	sentErr := Send(ctx, binary, state, source, bandwidth, conn, nil)
	select {
	case receivedErr := <-received:
		return sentErr, receivedErr
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not reap its process")
		return nil, nil
	}
}

func TestRuntimeTemplateAndRealDuplexTransfer(t *testing.T) {
	content := bytes.Repeat([]byte("rsync data\x00with arbitrary binary bytes\xff"), 32000)
	binary, state, source, directory, payload := rsyncFixture(t, content)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	template, err := ProbeReceiver(ctx, binary, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateServerArgs(template, template); err != nil {
		t.Fatal(err)
	}
	for _, altered := range [][]string{append(append([]string{}, template...), "--delete"), append([]string{"--sender"}, template[1:]...), append(append([]string{}, template[:len(template)-1]...), "../escape")} {
		if ValidateServerArgs(altered, template) == nil {
			t.Fatalf("accepted altered receiver arguments: %v", altered)
		}
	}
	sent, received := runTransfer(t, ctx, binary, state, source, directory, payload, int64(len(content)), 0)
	if sent != nil || received != nil {
		t.Fatalf("duplex transfer: sender=%v receiver=%v", sent, received)
	}
	actual, err := os.ReadFile(payload.Name())
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(content) {
		t.Fatalf("payload digest mismatch: %v", err)
	}
}

func TestInterruptedTransferRetainsPartialAndReruns(t *testing.T) {
	content := make([]byte, 2<<20)
	for index := range content {
		content[index] = byte(index*17 + index/251)
	}
	binary, state, source, directory, payload := rsyncFixture(t, content)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	attemptCtx, interrupt := context.WithCancel(ctx)
	partialSeen := make(chan int64, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-attemptCtx.Done():
				return
			case <-ticker.C:
				if info, err := payload.Stat(); err == nil && info.Size() >= 64<<10 && info.Size() < int64(len(content)) {
					partialSeen <- info.Size()
					interrupt()
					return
				}
			}
		}
	}()
	sent, received := runTransfer(t, attemptCtx, binary, state, source, directory, payload, int64(len(content)), 128<<10)
	if sent == nil || received == nil {
		t.Fatalf("interruption reported success: sender=%v receiver=%v", sent, received)
	}
	select {
	case <-partialSeen:
	default:
		t.Fatal("interruption did not leave a real partial payload")
	}
	info, err := payload.Stat()
	if err != nil || info.Size() == 0 || info.Size() >= int64(len(content)) {
		t.Fatalf("partial payload was not retained: %v %v", info, err)
	}
	sent, received = runTransfer(t, ctx, binary, state, source, directory, payload, int64(len(content)), 0)
	if sent != nil || received != nil {
		t.Fatalf("fresh-session rerun: sender=%v receiver=%v", sent, received)
	}
	actual, err := os.ReadFile(payload.Name())
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("rerun payload differs: %v", err)
	}
}

func TestReceiverRejectsAlteredWireArguments(t *testing.T) {
	binary, state, _, directory, payload := rsyncFixture(t, nil)
	partial := []byte("private partial")
	if _, err := payload.WriteAt(partial, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	template, err := ProbeReceiver(ctx, binary, state, 0)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			received <- err
			return
		}
		defer conn.CloseNow()
		received <- Receive(ctx, binary, directory, payload, int64(len(partial)), template, conn)
	}))
	defer server.Close()
	for _, altered := range [][]string{
		append(append([]string{}, template...), "--delete"),
		append([]string{"--server", "--sender"}, template[1:]...),
		append(append([]string{}, template[:len(template)-1]...), "../escape"),
	} {
		conn, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https"), &websocket.DialOptions{HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(altered)
		if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
		_, _, err = conn.Read(ctx)
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("altered arguments reached the receiver: %v", err)
		}
		_ = conn.CloseNow()
		select {
		case err := <-received:
			if err == nil {
				t.Fatal("receiver accepted an altered command")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	actual, err := os.ReadFile(payload.Name())
	if err != nil || !bytes.Equal(actual, partial) {
		t.Fatal("rejected handshake changed the private partial", err)
	}
	if _, err := os.Stat(filepath.Join(directory.Name(), "..", "escape")); !os.IsNotExist(err) {
		t.Fatal("rejected handshake escaped the stage", err)
	}
}

func TestAvailableRejectsWritableExecutableDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("rsync executable policy requires the root Agent")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "rsync"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	if _, err := Available(); err == nil || !strings.Contains(err.Error(), "executable path is not protected") {
		t.Fatal("a replaceable root-owned executable was accepted", err)
	}
}
