package agent

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPChannelDoesNotForwardCredentialsThroughRedirect(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true); w.WriteHeader(204) }))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := agentHTTPClient(nil, protocol.DefaultAgentSettings())
	defer client.CloseIdleConnections()
	channel := newHTTPAgentChannel(ctx, client, origin.URL, "private-device-credential", true)
	defer channel.CloseNow()
	if err := channel.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`)); err == nil || reached.Load() {
		t.Fatal("redirect followed or credential forwarded")
	}
}
func TestHTTPChannelCloseCancelsInflightPoll(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	client := agentHTTPClient(nil, protocol.DefaultAgentSettings())
	defer client.CloseIdleConnections()
	channel := newHTTPAgentChannel(context.Background(), client, server.URL, "credential", true)
	channel.session = "test-session"
	done := make(chan error, 1)
	go func() { _, _, err := channel.Read(context.Background()); done <- err }()
	<-started
	channel.CloseNow()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("poll succeeded after close")
		}
	case <-time.After(time.Second):
		t.Fatal("closing HTTP channel left a poll running")
	}
}
