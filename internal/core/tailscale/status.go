// Package tailscale reads the local Tailscale CLI's administrator-visible peer
// list. It deliberately has no API keys or remote-control surface of its own.
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
)

var (
	ErrUnavailable = errors.New("Tailscale CLI is unavailable")
	ErrNotLoggedIn = errors.New("Tailscale is not logged in or has no visible peers")
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type Peer struct {
	Identity string   `json:"identity"`
	Name     string   `json:"name"`
	DNSName  string   `json:"dnsName,omitempty"`
	OS       string   `json:"os"`
	Class    string   `json:"class"`
	Online   bool     `json:"online"`
	IPs      []string `json:"ips"`
	Managed  bool     `json:"managed"`
	NodeID   string   `json:"nodeId,omitempty"`
}

type statusEnvelope struct {
	BackendState string                     `json:"BackendState"`
	Self         json.RawMessage            `json:"Self"`
	Peer         map[string]json.RawMessage `json:"Peer"`
	Peers        []json.RawMessage          `json:"Peers"`
}

type rawPeer struct {
	ID           json.RawMessage `json:"ID"`
	PublicKey    string          `json:"PublicKey"`
	HostName     string          `json:"HostName"`
	DNSName      string          `json:"DNSName"`
	OS           string          `json:"OS"`
	TailscaleIPs []string        `json:"TailscaleIPs"`
	Online       bool            `json:"Online"`
	Active       bool            `json:"Active"`
}

type Discovery struct{ Runner Runner }

func (d Discovery) List(ctx context.Context) ([]Peer, error) {
	runner := d.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	output, err := runner.Run(ctx, "tailscale", "status", "--json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrUnavailable
		}
		return nil, fmt.Errorf("read local Tailscale status: %w: %s", ErrNotLoggedIn, strings.TrimSpace(string(output)))
	}
	var status statusEnvelope
	if err := json.Unmarshal(output, &status); err != nil {
		return nil, fmt.Errorf("parse Tailscale status JSON: %w", err)
	}
	if status.BackendState != "" && !strings.EqualFold(status.BackendState, "Running") {
		return nil, ErrNotLoggedIn
	}
	peers := make([]Peer, 0, len(status.Peer)+len(status.Peers))
	appendPeer := func(key string, raw json.RawMessage) error {
		var p rawPeer
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		identity := strings.TrimSpace(p.PublicKey)
		if identity == "" {
			identity = strings.TrimSpace(key)
		}
		if identity == "" {
			identity = rawID(p.ID)
		}
		if identity == "" {
			return errors.New("Tailscale peer has no stable identity")
		}
		ips := make([]string, 0, len(p.TailscaleIPs))
		for _, candidate := range p.TailscaleIPs {
			if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil {
				ips = append(ips, ip.String())
			}
		}
		if len(ips) == 0 {
			return nil
		}
		sort.Strings(ips)
		class := "unsupported"
		if strings.EqualFold(p.OS, "linux") {
			class = "linux"
		}
		if strings.TrimSpace(p.OS) == "" {
			class = "unknown"
		}
		name := strings.TrimSpace(p.HostName)
		if name == "" {
			name = strings.TrimSuffix(strings.TrimSpace(p.DNSName), ".")
		}
		peers = append(peers, Peer{Identity: identity, Name: name, DNSName: strings.TrimSuffix(strings.TrimSpace(p.DNSName), "."), OS: strings.ToLower(strings.TrimSpace(p.OS)), Class: class, Online: p.Online, IPs: ips})
		return nil
	}
	for key, raw := range status.Peer {
		if err := appendPeer(key, raw); err != nil {
			return nil, fmt.Errorf("parse Tailscale peer: %w", err)
		}
	}
	for _, raw := range status.Peers {
		if err := appendPeer("", raw); err != nil {
			return nil, fmt.Errorf("parse Tailscale peer: %w", err)
		}
	}
	if len(peers) == 0 {
		return nil, ErrNotLoggedIn
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Identity < peers[j].Identity })
	return peers, nil
}

func rawID(data json.RawMessage) string {
	var text string
	if json.Unmarshal(data, &text) == nil {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(string(data))
}
