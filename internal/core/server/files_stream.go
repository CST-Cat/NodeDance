package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type streamRoute struct {
	source *agentConnection
	target *agentConnection
}

func (s *Server) registerStreamRoute(streamID string, source, target *agentConnection) {
	s.streamRoutesMu.Lock()
	defer s.streamRoutesMu.Unlock()
	if s.streamRoutes == nil {
		s.streamRoutes = make(map[string]*streamRoute)
	}
	s.streamRoutes[streamID] = &streamRoute{source: source, target: target}
}

func (s *Server) unregisterStreamRoute(streamID string) {
	s.streamRoutesMu.Lock()
	defer s.streamRoutesMu.Unlock()
	delete(s.streamRoutes, streamID)
}

func validateAgentStreamFrame(connection *agentConnection, envelope protocol.Envelope) (protocol.StreamFrame, error) {
	if connection == nil || len(envelope.Payload) == 0 || len(envelope.Payload) > protocol.MaxFileControlBytes {
		return protocol.StreamFrame{}, errors.New("Agent stream frame is unavailable or too large")
	}
	var frame protocol.StreamFrame
	if err := decodeAgentPayload(envelope.Payload, &frame); err != nil {
		return protocol.StreamFrame{}, err
	}
	if err := protocol.ValidateStreamFrame(envelope, connection.generation, frame); err != nil {
		return protocol.StreamFrame{}, err
	}
	return frame, nil
}

func (s *Server) routeStreamFrame(connection *agentConnection, envelope protocol.Envelope) error {
	frame, err := validateAgentStreamFrame(connection, envelope)
	if err != nil {
		return err
	}
	streamID := frame.StreamID
	var peer *agentConnection
	s.streamRoutesMu.Lock()
	switch {
	case strings.HasSuffix(streamID, "/s"):
		route := s.streamRoutes[strings.TrimSuffix(streamID, "/s")]
		if route == nil || connection != route.source {
			s.streamRoutesMu.Unlock()
			return nil
		}
		peer = route.target
		frame.StreamID = strings.TrimSuffix(streamID, "/s") + "/t"
	case strings.HasSuffix(streamID, "/t"):
		route := s.streamRoutes[strings.TrimSuffix(streamID, "/t")]
		if route == nil || connection != route.target {
			s.streamRoutesMu.Unlock()
			return nil
		}
		peer = route.source
		frame.StreamID = strings.TrimSuffix(streamID, "/t") + "/s"
	default:
		s.streamRoutesMu.Unlock()
		return nil
	}
	s.streamRoutesMu.Unlock()
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	outbound := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeStreamData,
		Generation: peer.generation, RequestID: frame.StreamID, Sequence: envelope.Sequence, Payload: payload}
	if err := protocol.ValidateStreamFrame(outbound, peer.generation, frame); err != nil {
		return err
	}
	select {
	case peer.commands <- outbound:
		return nil
	case <-peer.ctx.Done():
		return nil
	case <-connection.ctx.Done():
		return errors.New("stream source disconnected")
	}
}

func (s *Server) waitStreamResponse(ctx context.Context, connection *agentConnection, transfer *coreFileTransfer) (protocol.Envelope, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
			err := errors.New("browser Session was revoked or expired")
			s.closeCoreFileTransfer(connection, transfer, true, err)
			return protocol.Envelope{}, err
		}
		select {
		case envelope := <-transfer.updates:
			if !s.dashboardSessionStillValid(ctx, transfer.sessionID) {
				err := errors.New("browser Session was revoked or expired")
				s.closeCoreFileTransfer(connection, transfer, true, err)
				return protocol.Envelope{}, err
			}
			return envelope, nil
		case <-transfer.done:
			if err := transfer.error(); err != nil {
				return protocol.Envelope{}, err
			}
			return protocol.Envelope{}, errors.New("file transfer ended")
		case <-ctx.Done():
			err := ctx.Err()
			s.closeCoreFileTransfer(connection, transfer, true, err)
			return protocol.Envelope{}, err
		case <-connection.ctx.Done():
			return protocol.Envelope{}, errors.New("Agent disconnected")
		case <-ticker.C:
		}
	}
}
