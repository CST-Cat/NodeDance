package protocol

import (
	"errors"
	"fmt"
	"strings"
)

const (
	CapabilityImages      = "agent.images.v1"
	TypeImageListRequest  = "image_list_request"
	TypeImageListResponse = "image_list_response"
	MaxImagePageSize      = 50
)

// ImageListRequest is a read-only Agent Engine query. Page bounds keep a
// large host's inventory from consuming the shared heartbeat socket.
type ImageListRequest struct {
	Filter   string `json:"filter,omitempty"`
	Page     uint32 `json:"page"`
	PageSize uint32 `json:"pageSize"`
}

type ImageSummary struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Digests    []string `json:"digests"`
	Size       int64    `json:"size"`
	CreatedAt  int64    `json:"createdAt"`
	Containers int      `json:"containers"`
}

type ImageListResponse struct {
	Images    []ImageSummary `json:"images"`
	Total     uint32         `json:"total"`
	Page      uint32         `json:"page"`
	ErrorCode string         `json:"errorCode,omitempty"`
}

func ValidateImageListRequest(envelope Envelope, request ImageListRequest, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeImageListRequest || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || !validImageRequestID(envelope.RequestID) ||
		len(request.Filter) > 256 || request.PageSize == 0 || request.PageSize > MaxImagePageSize {
		return ErrInvalidImageMessage
	}
	for _, r := range request.Filter {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidImageMessage
		}
	}
	return nil
}

func ValidateImageListResponse(envelope Envelope, response ImageListResponse, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeImageListResponse || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || !validImageRequestID(envelope.RequestID) ||
		response.Page > 1_000_000 || len(response.Images) > MaxImagePageSize || response.Total > 10_000_000 {
		return ErrInvalidImageMessage
	}
	if response.ErrorCode != "" && response.ErrorCode != "engine_unavailable" && response.ErrorCode != "capability_unavailable" && response.ErrorCode != "agent_busy" {
		return ErrInvalidImageMessage
	}
	for index, image := range response.Images {
		if !validImageID(image.ID) || image.Size < 0 || image.Containers < 0 || len(image.Tags) > 128 || len(image.Digests) > 128 {
			return fmt.Errorf("%w: image %d has invalid metadata", ErrInvalidImageMessage, index)
		}
		for _, value := range append(append([]string(nil), image.Tags...), image.Digests...) {
			if len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
				return ErrInvalidImageMessage
			}
		}
	}
	return nil
}

func validImageRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
			return false
		}
	}
	return true
}

func validImageID(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

var ErrInvalidImageMessage = errors.New("invalid image bridge message")
