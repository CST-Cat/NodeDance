package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/sshconnection"
)

func (s *Server) agentHost(ctx context.Context, nodeID string, req protocol.HostRequest) (protocol.HostResponse, error) {
	c := s.agentConnectionForNode(nodeID)
	if c == nil || !c.capabilityEnabled(protocol.CapabilityHost) {
		return protocol.HostResponse{}, errors.New("Agent host control unavailable")
	}
	id, err := newContainerStreamRequestID()
	if err != nil {
		return protocol.HostResponse{}, err
	}
	waiter := make(chan protocol.HostResponse, 1)
	c.hostMu.Lock()
	if c.hostWaiters == nil {
		c.hostWaiters = map[string]chan protocol.HostResponse{}
	}
	if len(c.hostWaiters) >= 16 {
		c.hostMu.Unlock()
		return protocol.HostResponse{}, errors.New("host request limit reached")
	}
	c.hostWaiters[id] = waiter
	c.hostMu.Unlock()
	defer func() { c.hostMu.Lock(); delete(c.hostWaiters, id); c.hostMu.Unlock() }()
	data, _ := json.Marshal(req)
	if !s.enqueueContainerStream(c, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHostRequest, Generation: c.generation, RequestID: id, Payload: data}) {
		return protocol.HostResponse{}, errors.New("Agent channel busy")
	}
	select {
	case response := <-waiter:
		return response, nil
	case <-ctx.Done():
		return protocol.HostResponse{}, ctx.Err()
	case <-c.ctx.Done():
		return protocol.HostResponse{}, errors.New("Agent disconnected; retry with the same idempotency key")
	}
}
func (s *Server) handleHostResponse(c *agentConnection, e protocol.Envelope) error {
	if !c.capabilityNegotiated(protocol.CapabilityHost) || e.Generation != c.generation || e.Sequence != 0 || e.RequestID == "" || len(e.Payload) > protocol.MaxMessageBytes {
		return errors.New("invalid host response")
	}
	var response protocol.HostResponse
	if decodeAgentPayload(e.Payload, &response) != nil || response.Status < 200 || response.Status > 599 {
		return errors.New("invalid host response payload")
	}
	c.hostMu.Lock()
	waiter := c.hostWaiters[e.RequestID]
	c.hostMu.Unlock()
	if waiter != nil {
		select {
		case waiter <- response:
		default:
		}
	}
	return nil
}
func (s *Server) handleHostAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") || len(parts) < 2 || (parts[1] != "agent-settings" && parts[1] != "diagnostics" && parts[1] != "commands") {
		return false
	}
	if !validUUID(parts[0]) || len(parts) > 3 {
		http.NotFound(w, r)
		return true
	}
	req := protocol.HostRequest{}
	switch parts[1] {
	case "agent-settings":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return true
		}
		if r.Method == "GET" {
			req.Action = "settings.get"
		} else if r.Method == "PUT" {
			req.Action = "settings.put"
			var settings protocol.AgentSettings
			if !decodeJSON(w, r, &settings) {
				return true
			}
			if err := protocol.ValidateAgentSettings(settings); err != nil {
				http.Error(w, err.Error(), 400)
				return true
			}
			req.Settings = &settings
		} else {
			http.Error(w, "method not allowed", 405)
			return true
		}
	case "diagnostics":
		if len(parts) != 2 || r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		req.Action = "diagnostics"
	case "commands":
		if len(parts) == 2 {
			if r.Method == "GET" {
				req.Action = "commands.list"
			} else if r.Method == "POST" {
				req.Action = "commands.submit"
				var c protocol.CommandSpec
				if !decodeJSON(w, r, &c) {
					return true
				}
				if err := protocol.ValidateCommand(c); err != nil {
					http.Error(w, err.Error(), 400)
					return true
				}
				req.Command = &c
				req.IdempotencyKey = r.Header.Get("Idempotency-Key")
				if len(req.IdempotencyKey) < 8 || len(req.IdempotencyKey) > 128 {
					http.Error(w, "Idempotency-Key required", 400)
					return true
				}
			} else {
				http.Error(w, "method not allowed", 405)
				return true
			}
		} else if parts[2] == "cleanup" && r.Method == "POST" {
			req.Action = "commands.cleanup"
		} else {
			req.JobID = parts[2]
			if r.Method == "GET" {
				req.Action = "commands.get"
			} else if r.Method == "DELETE" {
				req.Action = "commands.cancel"
			} else {
				http.Error(w, "method not allowed", 405)
				return true
			}
		}
	}
	if r.Method != "GET" {
		if err := audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: req.Action, Outcome: "accepted", ActorID: requestActor(r), Target: audit.Target{Kind: audit.TargetNode, ID: parts[0]}, RemoteAddr: current.RemoteAddr}); err != nil {
			http.Error(w, "cannot record host request", 500)
			return true
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	result, err := s.agentHost(ctx, parts[0], req)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return true
	}
	if result.Error != "" {
		http.Error(w, result.Error, result.Status)
		return true
	}
	if req.Action == "diagnostics" && result.SSH != nil {
		resolved, e := s.resolveNodeSSH(ctx, parts[0], *result.SSH)
		if e != nil {
			http.Error(w, "invalid SSH discovery response", 502)
			return true
		}
		result.SSH = &resolved
	}
	writeJSON(w, result.Status, result)
	return true
}

func (s *Server) resolveNodeSSH(ctx context.Context, node string, info sshconnection.Info) (sshconnection.Info, error) {
	preferences, e := (dashboard.Repository{DB: s.store.DB, Now: s.now}).List(ctx, node)
	if e != nil {
		return sshconnection.Info{}, e
	}
	var host, user string
	var port int
	for _, pref := range preferences {
		if pref.TargetKind == "node" && pref.Identity == dashboard.NodeIdentity(node) {
			host, user, port = pref.SSHHost, pref.SSHUser, pref.SSHPort
		}
	}
	return sshconnection.Resolve(info, host, user, port)
}
