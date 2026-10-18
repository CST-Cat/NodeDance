package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
)

// The update controller owns privileged deployment transactions. Core only
// authenticates requests and routes the same contract locally or to an Agent.
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/updates/")
	method := http.MethodGet
	if action == "apply" || action == "rollback" {
		method = http.MethodPost
	}
	if action != "check" && action != "apply" && action != "status" && action != "rollback" {
		http.NotFound(w, r)
		return
	}
	if r.Method != method {
		http.Error(w, "method not allowed", 405)
		return
	}
	request := update.Request{Component: r.URL.Query().Get("component"), NodeID: r.URL.Query().Get("nodeId"), Version: r.URL.Query().Get("version"), JobID: r.URL.Query().Get("jobId")}
	if method == http.MethodPost {
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err := update.Decode(raw, &request); err != nil || readErr != nil {
			http.Error(w, "invalid update request", 400)
			return
		}
	}
	if err := update.Validate(request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if request.Component == "core" && request.NodeID != "" || request.Component == "agent" && request.NodeID == "" {
		http.Error(w, "agent updates require nodeId; Core updates must omit it", 400)
		return
	}
	if method == http.MethodPost {
		if !request.Confirm || len(r.Header.Get("Idempotency-Key")) < 8 || len(r.Header.Get("Idempotency-Key")) > 128 {
			http.Error(w, "confirmation and Idempotency-Key are required", 400)
			return
		}
		if err := audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: "update_" + action, Outcome: "accepted", ActorID: requestActor(r)}); err != nil {
			http.Error(w, "cannot record update request", 500)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rpc := update.RPC{Action: action, Request: request, IdempotencyKey: r.Header.Get("Idempotency-Key")}
	var response update.Response
	var err error
	if request.Component == "core" {
		response, err = s.invokeUpdate(ctx, rpc)
	} else {
		response, err = s.agentUpdate(ctx, request.NodeID, rpc)
	}
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	if response.Error != "" {
		http.Error(w, response.Error, response.Status)
		return
	}
	if response.Status < 200 || response.Status > 299 || !json.Valid(response.Data) {
		http.Error(w, "invalid update controller response", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.Status)
	_, _ = w.Write(response.Data)
}
func (s *Server) agentUpdate(ctx context.Context, nodeID string, rpc update.RPC) (update.Response, error) {
	connection := s.agentConnectionForNode(nodeID)
	if connection == nil {
		return update.Response{}, errors.New("Agent is offline")
	}
	id, err := newContainerStreamRequestID()
	if err != nil {
		return update.Response{}, err
	}
	waiter := make(chan update.Response, 1)
	connection.updateMu.Lock()
	if connection.updateWaiters == nil {
		connection.updateWaiters = make(map[string]chan update.Response)
	}
	if len(connection.updateWaiters) >= 8 {
		connection.updateMu.Unlock()
		return update.Response{}, errors.New("Agent update request limit reached")
	}
	connection.updateWaiters[id] = waiter
	connection.updateMu.Unlock()
	defer func() { connection.updateMu.Lock(); delete(connection.updateWaiters, id); connection.updateMu.Unlock() }()
	payload, _ := json.Marshal(rpc)
	if !s.enqueueContainerStream(connection, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeUpdateRequest, Generation: connection.generation, RequestID: id, Payload: payload}) {
		return update.Response{}, errors.New("Agent update channel is busy")
	}
	select {
	case result := <-waiter:
		return result, nil
	case <-ctx.Done():
		return update.Response{}, ctx.Err()
	case <-connection.ctx.Done():
		return update.Response{}, errors.New("Agent disconnected; retry status or submission with the same idempotency key")
	}
}
func (s *Server) handleUpdateResponse(c *agentConnection, e protocol.Envelope) error {
	if e.Generation != c.generation || e.Sequence != 0 || e.RequestID == "" {
		return errors.New("invalid update response envelope")
	}
	var response update.Response
	if err := decodeAgentPayloadLimit(e.Payload, &response, protocol.MaxMessageBytes); err != nil || response.Status < 200 || response.Status > 599 {
		return errors.New("invalid update response")
	}
	c.updateMu.Lock()
	waiter := c.updateWaiters[e.RequestID]
	c.updateMu.Unlock()
	if waiter != nil {
		select {
		case waiter <- response:
		default:
		}
	}
	return nil
}

func (s *Server) invokeUpdate(ctx context.Context, rpc update.RPC) (update.Response, error) {
	if s.updateInvoke != nil {
		return s.updateInvoke(ctx, rpc)
	}
	return update.Invoke(ctx, "core", s.dataDir, rpc)
}
