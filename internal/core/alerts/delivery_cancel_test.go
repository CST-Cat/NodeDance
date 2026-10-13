package alerts

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestSMTPCancellationInterruptsGreeting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	portNumber, _ := strconv.Atoi(port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- (&Sender{Timeout: time.Minute}).sendSMTP(ctx, ChannelConfig{SMTPHost: host, SMTPPort: portNumber, SMTPSecurityMode: SMTPSecuritySTARTTLS}, "", "{}")
	}()
	conn := <-accepted
	defer conn.Close()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled SMTP send succeeded")
		}
	case <-time.After(time.Second):
		conn.Close()
		<-done
		t.Fatal("SMTP greeting ignored cancellation")
	}
}

func TestWebhookHonorsSenderTimeoutWithCustomClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (&Sender{HTTPClient: server.Client(), Timeout: 30 * time.Millisecond}).Send(ctx, Channel{Kind: ChannelWebhook, Config: ChannelConfig{WebhookURL: server.URL}}, "", "{}")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("timed out webhook succeeded")
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("custom client bypassed Sender timeout")
	}
}
