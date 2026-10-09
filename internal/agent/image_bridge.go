package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentimages "github.com/CST-Cat/NodeDance/internal/agent/images"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func runAgentImageSession(ctx context.Context, writer *socketEnvelopeWriter, engine *agentimages.SDKEngine, generation uint64, incoming <-chan protocol.Envelope) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case envelope := <-incoming:
			var request protocol.ImageListRequest
			if err := decodeSocketPayload(envelope.Payload, &request); err != nil || protocol.ValidateImageListRequest(envelope, request, generation) != nil {
				return errors.New("Core image list request is invalid")
			}
			queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			images, err := engine.List(queryCtx)
			cancel()
			if err != nil {
				return sendAgentImageError(ctx, writer, generation, envelope.RequestID, request.Page, "engine_unavailable")
			}
			if request.Filter != "" {
				needle := strings.ToLower(request.Filter)
				filtered := images[:0]
				for _, image := range images {
					if strings.Contains(strings.ToLower(image.ID), needle) || containsImageText(image.Tags, needle) || containsImageText(image.Digests, needle) {
						filtered = append(filtered, image)
					}
				}
				images = filtered
			}
			total := len(images)
			start := uint64(request.Page) * uint64(request.PageSize)
			if start > uint64(total) {
				start = uint64(total)
			}
			end := min(uint64(total), start+uint64(request.PageSize))
			response := protocol.ImageListResponse{Total: uint32(total), Page: request.Page, Images: make([]protocol.ImageSummary, 0, end-start)}
			for _, image := range images[start:end] {
				response.Images = append(response.Images, protocol.ImageSummary{ID: image.ID, Tags: image.Tags, Digests: image.Digests,
					Size: image.Size, CreatedAt: image.CreatedAt, Containers: image.Containers})
			}
			if err := sendAgentImageResponse(ctx, writer, generation, envelope.RequestID, response); err != nil {
				return err
			}
		}
	}
}

func containsImageText(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func sendAgentImageResponse(ctx context.Context, writer *socketEnvelopeWriter, generation uint64, requestID string, response protocol.ImageListResponse) error {
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeImageListResponse,
		Generation: generation, RequestID: requestID, Payload: encodePayload(response)}
	if protocol.ValidateImageListResponse(envelope, response, generation) != nil {
		return errors.New("Agent image list response is invalid")
	}
	data, err := json.Marshal(envelope)
	if err != nil || len(data) > protocol.MaxMessageBytes {
		return errors.New("Agent image list response exceeds the transport bound")
	}
	defer clear(data)
	return writer.send(ctx, envelope)
}

func sendAgentImageError(ctx context.Context, writer *socketEnvelopeWriter, generation uint64, requestID string, page uint32, code string) error {
	response := protocol.ImageListResponse{Page: page, ErrorCode: code, Images: []protocol.ImageSummary{}}
	return sendAgentImageResponse(ctx, writer, generation, requestID, response)
}
