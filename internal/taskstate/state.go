// Package taskstate defines the task contract shared by Core and Agent.
// It has no persistence, transport, or executor dependencies.
package taskstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	TimedOut  Status = "timed_out"
	Canceled  Status = "canceled"
	Unknown   Status = "unknown"
)

const (
	MaxIdentityBytes       = 256
	MaxActionBytes         = 64
	MaxIdempotencyKeyBytes = 200
	MaxRequestBytes        = 64 << 10
	MaxNumberBytes         = 128
	MaxNumberDigits        = 96
	MaxAbsExponent         = 10000
)

var (
	ErrInvalidRequest        = errors.New("invalid task request")
	ErrInvalidStatus         = errors.New("invalid task status transition")
	ErrCancellationRequested = errors.New("task cancellation was explicitly requested")
)

// Identity contains only stable identifiers and the request body used to
// calculate the deduplication digest. Payload is never stored by the journal.
// Callers must not put credentials or other secrets in a persisted summary.
type Identity struct {
	TaskID         string
	NodeID         string
	IdempotencyKey string
	TargetID       string
	ResourceKey    string
	Action         string
	Payload        json.RawMessage
}

// RequestDigest validates and canonically hashes a request. JSON object keys
// are sorted by encoding/json, duplicate keys are rejected, and numeric tokens
// preserve their valid original JSON spelling (for example, 1 and 1.0 have
// different digests). Numeric token size, digit count, and exponent are
// bounded. The idempotency key and task ID are excluded: they identify the
// ledger entry, while the remaining request content determines whether a
// replay is equal.
func RequestDigest(identity Identity) ([sha256.Size]byte, error) {
	var zero [sha256.Size]byte
	if !validIdentifier(identity.TaskID) || !validIdentifier(identity.NodeID) ||
		!validIdempotencyKey(identity.IdempotencyKey) || !validIdentifier(identity.TargetID) ||
		!validIdentifier(identity.ResourceKey) || !validAction(identity.Action) {
		return zero, fmt.Errorf("%w: invalid identity field", ErrInvalidRequest)
	}
	if identity.Action == "container_create" && !validContainerCreateIdentity(identity) {
		return zero, fmt.Errorf("%w: invalid container create identity", ErrInvalidRequest)
	}
	payload, err := CanonicalJSON(identity.Payload)
	if err != nil {
		return zero, fmt.Errorf("%w: payload: %v", ErrInvalidRequest, err)
	}
	envelope := struct {
		Version     int             `json:"version"`
		NodeID      string          `json:"node_id"`
		TargetID    string          `json:"target_id"`
		ResourceKey string          `json:"resource_key"`
		Action      string          `json:"action"`
		Payload     json.RawMessage `json:"payload"`
	}{
		Version: 1, NodeID: identity.NodeID, TargetID: identity.TargetID,
		ResourceKey: identity.ResourceKey, Action: identity.Action, Payload: payload,
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return zero, fmt.Errorf("%w: encode request identity: %v", ErrInvalidRequest, err)
	}
	return sha256.Sum256(canonical), nil
}

func validContainerCreateIdentity(identity Identity) bool {
	const targetPrefix = "container-create:"
	const resourcePrefix = "docker-container-create:"
	if !strings.HasPrefix(identity.TargetID, targetPrefix) || !strings.HasPrefix(identity.ResourceKey, resourcePrefix) {
		return false
	}
	digest := strings.TrimPrefix(identity.TargetID, targetPrefix)
	if len(digest) != sha256.Size*2 || identity.ResourceKey != resourcePrefix+digest {
		return false
	}
	for _, value := range digest {
		if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
			return false
		}
	}
	var payload struct {
		CreateSHA256 string `json:"create_sha256"`
	}
	if err := json.Unmarshal(identity.Payload, &payload); err != nil || payload.CreateSHA256 != digest {
		return false
	}
	return true
}

// CanonicalJSON returns a deterministic JSON encoding of a bounded request
// body. Duplicate object keys are rejected rather than silently choosing one.
// Numeric spellings are preserved exactly instead of converting through
// float64, so large integer and decimal values do not lose precision.
func CanonicalJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxRequestBytes || !utf8.Valid(raw) {
		return nil, errors.New("JSON body is empty, oversized, or invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON data: %w", err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > 128 {
		return nil, errors.New("JSON nesting exceeds 128 levels")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				child, err := decodeJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				object[key] = child
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated JSON object")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				child, err := decodeJSONValue(decoder, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated JSON array")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	case json.Number:
		if err := validateNumber(string(value)); err != nil {
			return nil, err
		}
		return value, nil
	case string, bool, nil:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", token)
	}
}

func validateNumber(value string) error {
	if len(value) == 0 || len(value) > MaxNumberBytes {
		return errors.New("JSON number is oversized")
	}
	mantissa := value
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		exponent := mantissa[index+1:]
		mantissa = mantissa[:index]
		if len(exponent) == 0 || len(exponent) > 6 {
			return errors.New("JSON number exponent is oversized")
		}
		n, err := strconv.Atoi(exponent)
		if err != nil || n > MaxAbsExponent || n < -MaxAbsExponent {
			return errors.New("JSON number exponent is outside the supported range")
		}
	}
	digits := 0
	for _, r := range mantissa {
		if r >= '0' && r <= '9' {
			digits++
		}
	}
	if digits == 0 || digits > MaxNumberDigits {
		return errors.New("JSON number has an unsupported digit count")
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > MaxIdentityBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validAction(value string) bool {
	if value == "" || len(value) > MaxActionBytes {
		return false
	}
	for i, r := range value {
		if !((r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && (r == '_' || r == '-'))) {
			return false
		}
	}
	return true
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > MaxIdempotencyKeyBytes {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
			return false
		}
	}
	return true
}

type Evidence struct {
	ExecutionAttempted    bool
	ExecutionCompleted    bool
	FailureConfirmed      bool
	PostconditionVerified bool
	ProcessTerminated     bool
	ActualResultConfirmed bool
	CancellationConfirmed bool
	// DeliveryCommitted means Core durably committed an outbound delivery
	// attempt before writing to the Agent connection. It does not claim that
	// the Agent received the task or entered its executor.
	DeliveryCommitted bool
}

func (e Evidence) validate(status Status, from Status) error {
	switch status {
	case Running:
		if from != Queued {
			return errors.New("only a queued task can begin execution")
		}
		if !e.ExecutionAttempted {
			return errors.New("running requires durable execution intent")
		}
	case Succeeded:
		if !e.ExecutionAttempted || !e.ExecutionCompleted || !e.PostconditionVerified {
			return errors.New("succeeded requires completed execution and verified postcondition")
		}
	case Failed:
		if !e.FailureConfirmed || !e.ActualResultConfirmed ||
			(from != Queued && (!e.ExecutionAttempted || !e.ExecutionCompleted)) ||
			(e.ExecutionAttempted && !e.ExecutionCompleted) {
			return errors.New("failed requires a confirmed failure and actual result")
		}
	case TimedOut:
		if !e.ExecutionAttempted || !e.ProcessTerminated || !e.ActualResultConfirmed {
			return errors.New("timed_out requires terminated execution and confirmed actual result")
		}
	case Canceled:
		if !e.CancellationConfirmed || !e.ActualResultConfirmed ||
			(from != Queued && (!e.ExecutionAttempted || !e.ProcessTerminated)) ||
			(e.ExecutionAttempted && !e.ProcessTerminated) {
			return errors.New("canceled requires confirmed cancellation and actual result")
		}
	case Unknown:
		switch from {
		case Running:
			// The Agent durably recorded execution before entering its executor.
		case Queued:
			if !e.DeliveryCommitted {
				return errors.New("a queued task requires committed Core delivery before it can become unknown")
			}
		default:
			return errors.New("only a running or durably delivered queued task can become unknown")
		}
	case Queued:
		return errors.New("tasks cannot transition back to queued")
	default:
		return fmt.Errorf("unsupported status %q", status)
	}
	return nil
}

func CanTransition(from, to Status, evidence Evidence) error {
	if !knownStatus(from) || !knownStatus(to) || isTerminal(from) {
		return ErrInvalidStatus
	}
	switch from {
	case Queued:
		if to != Running && to != Failed && to != Canceled && to != Unknown {
			return ErrInvalidStatus
		}
	case Running:
		if to != Succeeded && to != Failed && to != TimedOut && to != Canceled && to != Unknown {
			return ErrInvalidStatus
		}
	case Unknown:
		if to != Succeeded && to != Failed && to != TimedOut && to != Canceled {
			return ErrInvalidStatus
		}
	}
	if err := evidence.validate(to, from); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidStatus, err)
	}
	return nil
}

func knownStatus(status Status) bool {
	switch status {
	case Queued, Running, Succeeded, Failed, TimedOut, Canceled, Unknown:
		return true
	default:
		return false
	}
}

func isTerminal(status Status) bool {
	return status == Succeeded || status == Failed || status == TimedOut || status == Canceled
}

func IsTerminal(status Status) bool { return isTerminal(status) }

func HoldsResource(status Status) bool {
	return status == Queued || status == Running || status == Unknown
}
