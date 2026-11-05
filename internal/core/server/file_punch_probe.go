package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

const (
	filePunchProbeLifetime = 2 * time.Minute
	filePunchProbeLimit    = 4
)

func (s *Server) handleAgentPunchProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	credential, ok := authorizationCredential(r.Header.Get("Authorization"), "Bearer")
	if !ok || !validDeviceCredential(credential) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not permitted on the Agent channel", http.StatusForbidden)
		return
	}
	digest := auth.DigestToken(credential)
	identity, err := s.agents.IdentityByCredential(r.Context(), digest)
	if errors.Is(err, agents.ErrUnauthorized) || errors.Is(err, agents.ErrRevoked) {
		http.Error(w, "Agent credential rejected", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "Agent authentication failed", http.StatusInternalServerError)
		return
	}
	observed, observable := s.punchObservation(r)
	if !s.beginPunchProbe(identity.NodeID) {
		http.Error(w, "punch probe limit reached", http.StatusTooManyRequests)
		return
	}
	defer s.endPunchProbe(identity.NodeID)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(protocol.MaxFilePunchMessage)
	observation := protocol.FilePunchObservation{RemoteAddr: observed}
	if !observable {
		observation = protocol.FilePunchObservation{Error: protocol.FilePunchUnobservable}
	}
	log.Printf("Agent punch probe node=%s peer=%s observable=%t", identity.NodeID, observed, observable)
	data, _ := json.Marshal(observation)
	writeCtx, cancelWrite := context.WithTimeout(r.Context(), 5*time.Second)
	err = conn.Write(writeCtx, websocket.MessageText, data)
	cancelWrite()
	if err != nil {
		return
	}
	holdCtx, cancelHold := context.WithTimeout(r.Context(), filePunchProbeLifetime)
	defer cancelHold()
	for {
		if _, _, err := conn.Read(holdCtx); err != nil {
			return
		}
	}
}

func remotePort(remoteAddr string) string {
	if _, port, err := net.SplitHostPort(remoteAddr); err == nil {
		return port
	}
	return ""
}

// punchObservation reports the raw TCP peer endpoint Core observes for this request.
// A trusted proxy or loopback peer means Core did not terminate the Agent's TCP
// connection, so the observed port is not the Agent's NAT mapping.
func (s *Server) punchObservation(r *http.Request) (string, bool) {
	ip := remoteIP(r)
	if ip == nil {
		return "", false
	}
	if s.isTrustedProxy(ip) {
		return "", false
	}
	return net.JoinHostPort(ip.String(), remotePort(r.RemoteAddr)), true
}

func (s *Server) beginPunchProbe(nodeID string) bool {
	s.punchProbeMu.Lock()
	defer s.punchProbeMu.Unlock()
	if s.punchProbes[nodeID] >= filePunchProbeLimit {
		return false
	}
	s.punchProbes[nodeID]++
	return true
}

func (s *Server) endPunchProbe(nodeID string) {
	s.punchProbeMu.Lock()
	defer s.punchProbeMu.Unlock()
	if s.punchProbes[nodeID] <= 1 {
		delete(s.punchProbes, nodeID)
		return
	}
	s.punchProbes[nodeID]--
}
