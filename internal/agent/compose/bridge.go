package compose

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxComposeOperations = 2

var errComposeBridgeBusy = errors.New("Compose operation limit reached")

type EnvelopeWriter interface {
	Send(context.Context, protocol.Envelope) error
}

type Bridge struct {
	manager  *Manager
	active   chan struct{}
	mu       sync.Mutex
	projects map[string]*sync.Mutex
	requests map[string]struct{}
	wg       sync.WaitGroup
	closed   bool
}

func NewBridge(manager *Manager) (*Bridge, error) {
	if manager == nil {
		return nil, errors.New("Compose manager is required")
	}
	return &Bridge{manager: manager, active: make(chan struct{}, maxComposeOperations), projects: make(map[string]*sync.Mutex), requests: make(map[string]struct{})}, nil
}

func (b *Bridge) Run(ctx context.Context, writer EnvelopeWriter, commands <-chan protocol.Envelope, generation uint64) (returnErr error) {
	defer func() {
		b.mu.Lock()
		b.closed = true
		b.mu.Unlock()
		b.wg.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case envelope, ok := <-commands:
			if !ok {
				return errors.New("Compose command channel closed")
			}
			if envelope.Type != protocol.TypeComposeRequest || envelope.Generation != generation || envelope.RequestID == "" {
				return errors.New("Core sent an invalid Compose control envelope")
			}
			var request protocol.ComposeRequest
			if json.Unmarshal(envelope.Payload, &request) != nil || request.OperationID != envelope.RequestID || protocol.ValidateComposeRequest(request) != nil {
				return errors.New("Core Compose request failed validation")
			}
			if err := b.start(ctx, writer, generation, request); err != nil {
				if errors.Is(err, errComposeBridgeBusy) {
					response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "failed", ErrorCode: "agent_busy"}
					payload, marshalErr := json.Marshal(response)
					if marshalErr != nil {
						return marshalErr
					}
					if err := writer.Send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeResponse,
						Generation: generation, RequestID: request.OperationID, Payload: payload}); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
	}
}

func (b *Bridge) start(parent context.Context, writer EnvelopeWriter, generation uint64, request protocol.ComposeRequest) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("Compose bridge is closed")
	}
	if _, exists := b.requests[request.OperationID]; exists {
		b.mu.Unlock()
		return errors.New("Core reused an active Compose operation ID")
	}
	if len(b.requests) >= maxComposeOperations {
		b.mu.Unlock()
		return errComposeBridgeBusy
	}
	b.requests[request.OperationID] = struct{}{}
	projectLock := b.projects[request.Project.Key]
	if projectLock == nil {
		projectLock = &sync.Mutex{}
		b.projects[request.Project.Key] = projectLock
	}
	b.wg.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		defer func() {
			b.mu.Lock()
			delete(b.requests, request.OperationID)
			if len(b.requests) == 0 {
				b.projects = make(map[string]*sync.Mutex)
			}
			b.mu.Unlock()
		}()
		select {
		case b.active <- struct{}{}:
			defer func() { <-b.active }()
		case <-parent.Done():
			return
		}
		projectLock.Lock()
		defer projectLock.Unlock()
		response, err := b.manager.Execute(parent, request)
		if err != nil {
			status := "failed"
			if errors.Is(err, ErrOutcomeUnknown) {
				status = "unknown"
			}
			response = protocol.ComposeResponse{OperationID: request.OperationID, Status: status, ErrorCode: errorCode(err)}
		}
		if protocol.ValidateComposeResponse(response, request) != nil {
			return
		}
		payload, marshalErr := json.Marshal(response)
		if marshalErr != nil || len(payload) > protocol.MaxMessageBytes-1024 {
			return
		}
		_ = writer.Send(parent, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeResponse,
			Generation: generation, RequestID: request.OperationID, Payload: payload})
	}()
	return nil
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		return "project_not_found"
	case errors.Is(err, ErrConfigMissing):
		return "config_missing"
	case errors.Is(err, ErrComposeFailed):
		return "compose_failed"
	case errors.Is(err, ErrVerifyFailed):
		return "verification_failed"
	case errors.Is(err, ErrOutcomeUnknown):
		return "result_pending"
	default:
		return "engine_error"
	}
}
