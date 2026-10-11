package agent

import (
	"context"
	"crypto/tls"
	"github.com/CST-Cat/NodeDance/internal/agent/networkpolicy"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"time"
)

func agentCompression(enabled bool) websocket.CompressionMode {
	if enabled {
		return websocket.CompressionContextTakeover
	}
	return websocket.CompressionDisabled
}
func agentTransport(tlsConfig *tls.Config, s protocol.AgentSettings) *http.Transport {
	return &http.Transport{TLSClientConfig: tlsConfig, Proxy: http.ProxyFromEnvironment, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return networkpolicy.Dial(ctx, network, address, s)
	}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxConnsPerHost: 8}
}

func agentHTTPClient(tlsConfig *tls.Config, s protocol.AgentSettings) *http.Client {
	return &http.Client{Transport: agentTransport(tlsConfig, s), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
