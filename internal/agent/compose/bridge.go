package compose

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type EnvelopeWriter interface {
	Send(context.Context, protocol.Envelope) error
}

// Bridge exposes Compose inventory only. Mutating Compose operations are
// disabled until they use the Core's common task state and result path.
type Bridge struct{ manager *Manager }

func NewBridge(manager *Manager) (*Bridge, error) {
	if manager == nil {
		return nil, errors.New("Compose manager is required")
	}
	return &Bridge{manager: manager}, nil
}

func (b *Bridge) Run(ctx context.Context, writer EnvelopeWriter, commands <-chan protocol.Envelope, generation uint64) error {
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
			if json.Unmarshal(envelope.Payload, &request) != nil || request.OperationID != envelope.RequestID ||
				protocol.ValidateComposeRequest(request) != nil {
				return errors.New("Core sent an unsupported Compose operation")
			}
			projects, err := b.manager.List(ctx)
			if err != nil {
				return errors.New("Compose inventory could not be read")
			}
			response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "succeeded", Verified: true, Projects: projects}
			if err := protocol.ValidateComposeResponse(response, request); err != nil {
				return errors.New("Compose inventory response is invalid")
			}
			payload, err := json.Marshal(response)
			if err != nil || len(payload) > protocol.MaxMessageBytes-1024 {
				return errors.New("Compose inventory response exceeds its bound")
			}
			if err := writer.Send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeResponse,
				Generation: generation, RequestID: request.OperationID, Payload: payload}); err != nil {
				return err
			}
		}
	}
}
