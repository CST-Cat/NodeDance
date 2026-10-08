package server

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	current, ok := s.authenticateRequest(w, r, false)
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/ws/v1/dashboard" {
		// The Agent channel belongs to S02 and cannot inherit browser-session
		// authority. Unknown browser channels stay unavailable as well.
		http.NotFound(w, r)
		return
	}
	if !s.validOrigin(r) {
		http.Error(w, "request rejected", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "connection closed")
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	readDone := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				readDone <- err
				return
			}
		}
	}()
	ticker := time.NewTicker(s.websocketCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-readDone:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !s.sessionStillValid(current.ID) {
				_ = conn.Close(websocket.StatusPolicyViolation, "session expired or revoked")
				return
			}
		}
	}
}
