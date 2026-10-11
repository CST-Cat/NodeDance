// Package networkpolicy shares Agent DNS and IP preferences across transports
// and outbound probes without modifying the machine's resolver configuration.
package networkpolicy

import (
	"context"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"net"
	"time"
)

func Resolver(s protocol.AgentSettings) *net.Resolver {
	if s.DNS == "" {
		return net.DefaultResolver
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, s.DNS)
	}}
}
func Dial(ctx context.Context, network, address string, s protocol.AgentSettings) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Resolver: Resolver(s)}
	preferred := network
	if network == "tcp" {
		if s.PreferIPVersion == "4" {
			preferred = "tcp4"
		}
		if s.PreferIPVersion == "6" {
			preferred = "tcp6"
		}
	}
	attemptCtx := ctx
	cancel := func() {}
	if preferred != network {
		attemptCtx, cancel = context.WithTimeout(ctx, 3*time.Second)
	}
	c, err := d.DialContext(attemptCtx, preferred, address)
	cancel()
	if err != nil && preferred != network {
		return d.DialContext(ctx, network, address)
	}
	return c, err
}
