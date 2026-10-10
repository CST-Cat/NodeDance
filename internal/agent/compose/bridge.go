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

// Bridge handles read-only inventory and config preflight requests. Compose
// file writes and project lifecycle mutations use the shared Core Task and
// Agent TaskJournal path.
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
			action := request.Action
			if action == "" {
				action = "inventory"
			}
			response := protocol.ComposeResponse{OperationID: request.OperationID}
			switch action {
			case "inventory":
				projects, err := b.manager.List(ctx)
				if err != nil {
					response.Status, response.ErrorCode = "failed", "inventory_unavailable"
				} else {
					response.Status, response.Verified, response.Projects = "succeeded", true, projects
				}
			case "config_read":
				document, err := b.manager.ReadConfig(ctx, *request.Project, request.FileIndex)
				if err != nil {
					response.Status, response.ErrorCode = "failed", composeErrorCode(err)
				} else {
					response.Status, response.Verified, response.Content = "succeeded", true, document.Text
					response.ContentSHA256 = document.SHA256
				}
			case "config_validate":
				if err := b.manager.ValidateConfig(ctx, *request.Project, request.FileIndex, request.Content); err != nil {
					response.Status, response.ErrorCode = "failed", composeErrorCode(err)
				} else {
					response.Status, response.Verified = "succeeded", true
				}
			default:
				return errors.New("Core sent an unsupported Compose operation")
			}
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

func composeErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrConfigChanged):
		return "config_changed"
	case errors.Is(err, ErrConfigInvalid):
		return "config_invalid"
	case errors.Is(err, ErrConfigUnsafe):
		return "config_unsafe"
	case errors.Is(err, ErrProjectUnavailable):
		return "project_unavailable"
	default:
		return "compose_unavailable"
	}
}
