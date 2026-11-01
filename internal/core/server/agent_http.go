package server

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type httpAgentChannel struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agentID   string
	nodeID    string
	in        chan []byte
	out       chan []byte
	pollMu    sync.Mutex
	sendMu    sync.Mutex
	pending   []byte
	readSeq   uint64
	writeSeq  uint64
	writeHash [32]byte
}

func (c *httpAgentChannel) SetReadLimit(int64)                       {}
func (c *httpAgentChannel) Close(websocket.StatusCode, string) error { return c.CloseNow() }
func (c *httpAgentChannel) CloseNow() error                          { c.cancel(); return nil }
func (c *httpAgentChannel) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case b := <-c.in:
		return websocket.MessageText, b, nil
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case <-c.ctx.Done():
		return 0, nil, errors.New("HTTP Agent channel closed")
	}
}
func (c *httpAgentChannel) Write(ctx context.Context, _ websocket.MessageType, b []byte) error {
	copy := append([]byte{}, b...)
	select {
	case c.out <- copy:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return errors.New("HTTP Agent channel closed")
	}
}
func readHTTPAgentFrame(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body := io.Reader(http.MaxBytesReader(w, r.Body, protocol.MaxMessageBytes))
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, e := gzip.NewReader(body)
		if e != nil {
			return nil, e
		}
		defer gz.Close()
		body = gz
	} else if r.Header.Get("Content-Encoding") != "" {
		return nil, errors.New("unsupported encoding")
	}
	b, e := io.ReadAll(io.LimitReader(body, protocol.MaxMessageBytes+1))
	if e != nil || len(b) > protocol.MaxMessageBytes {
		return nil, errors.New("invalid Agent frame size")
	}
	if _, e := decodeAgentEnvelope(b); e != nil {
		return nil, e
	}
	return b, nil
}
func (s *Server) handleHTTPAgentChannel(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origins are not permitted", 403)
		return
	}
	credential, ok := authorizationCredential(r.Header.Get("Authorization"), "Bearer")
	if !ok || !validDeviceCredential(credential) {
		http.Error(w, "Agent credential rejected", 401)
		return
	}
	digest := auth.DigestToken(credential)
	identity, err := s.agents.IdentityByCredential(r.Context(), digest)
	if err != nil {
		http.Error(w, "Agent credential rejected", 401)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/channel")
	if path == "" {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return
		}
		b, e := readHTTPAgentFrame(w, r)
		if e != nil {
			http.Error(w, "invalid Agent hello", 400)
			return
		}
		envelope, _ := decodeAgentEnvelope(b)
		if envelope.Type != protocol.TypeHello {
			http.Error(w, "hello required", 400)
			return
		}
		id, e := newContainerStreamRequestID()
		if e != nil {
			http.Error(w, "channel unavailable", 500)
			return
		}
		if !s.beginAgentHandler() {
			http.Error(w, "Core is shutting down", 503)
			return
		}
		ctx, cancel := context.WithCancel(s.agentContext)
		c := &httpAgentChannel{ctx: ctx, cancel: cancel, agentID: identity.AgentID, nodeID: identity.NodeID, in: make(chan []byte, 32), out: make(chan []byte, 32)}
		s.httpChannelsMu.Lock()
		if s.httpChannels == nil {
			s.httpChannels = map[string]*httpAgentChannel{}
		}
		if len(s.httpChannels) >= 1024 {
			s.httpChannelsMu.Unlock()
			cancel()
			s.agentWait.Done()
			http.Error(w, "channel capacity reached", 429)
			return
		}
		s.httpChannels[id] = c
		s.httpChannelsMu.Unlock()
		c.in <- b
		go func() {
			defer s.agentWait.Done()
			defer cancel()
			defer func() { s.httpChannelsMu.Lock(); delete(s.httpChannels, id); s.httpChannelsMu.Unlock() }()
			s.serveAgentChannel(ctx, c, identity, digest)
		}()
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 201, map[string]string{"channelId": id})
		return
	}
	id := strings.TrimPrefix(path, "/")
	if strings.Contains(id, "/") || !validUUID(id) {
		http.NotFound(w, r)
		return
	}
	s.httpChannelsMu.Lock()
	c := s.httpChannels[id]
	s.httpChannelsMu.Unlock()
	if c == nil || c.agentID != identity.AgentID || c.nodeID != identity.NodeID {
		http.Error(w, "Agent channel unavailable", 410)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case "POST":
		b, e := readHTTPAgentFrame(w, r)
		if e != nil {
			http.Error(w, "invalid Agent frame", 400)
			return
		}
		seq, e := strconv.ParseUint(r.Header.Get("X-Channel-Sequence"), 10, 64)
		if e != nil || seq == 0 {
			http.Error(w, "sequence required", 400)
			return
		}
		hash := sha256.Sum256(b)
		c.sendMu.Lock()
		defer c.sendMu.Unlock()
		if seq == c.writeSeq && hash == c.writeHash {
			w.WriteHeader(204)
			return
		}
		if seq != c.writeSeq+1 {
			http.Error(w, "channel sequence mismatch", 409)
			return
		}
		select {
		case c.in <- b:
			c.writeSeq = seq
			c.writeHash = hash
			w.WriteHeader(204)
		case <-c.ctx.Done():
			http.Error(w, "channel closed", 410)
		default:
			http.Error(w, "channel busy", 429)
		}
	case "GET":
		if !c.pollMu.TryLock() {
			http.Error(w, "concurrent polling is not allowed", 409)
			return
		}
		defer c.pollMu.Unlock()
		ack, _ := strconv.ParseUint(r.Header.Get("X-Channel-Ack"), 10, 64)
		if c.pending != nil {
			if ack == c.readSeq {
				clear(c.pending)
				c.pending = nil
			} else if ack != c.readSeq-1 {
				http.Error(w, "channel acknowledgement mismatch", 409)
				return
			}
		}
		if c.pending == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			defer cancel()
			select {
			case c.pending = <-c.out:
				c.readSeq++
			case <-ctx.Done():
				w.WriteHeader(204)
				return
			case <-c.ctx.Done():
				http.Error(w, "channel closed", 410)
				return
			}
		}
		w.Header().Set("X-Channel-Sequence", strconv.FormatUint(c.readSeq, 10))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(c.pending)
	case "DELETE":
		c.cancel()
		w.WriteHeader(204)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
