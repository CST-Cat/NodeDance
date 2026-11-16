package protocol

import "encoding/json"

const (
	CurrentVersion           = 1
	MinimumVersion           = 1
	MaxMessageBytes          = 16 << 20
	HeartbeatIntervalSeconds = 5
	OfflineAfterSeconds      = 30
)

const (
	CapabilityUpdate   = "agent.update.v1"
	TypeUpdateRequest  = "update_request"
	TypeUpdateResponse = "update_response"
	TypeHello          = "hello"
	TypeWelcome        = "welcome"
	TypeHeartbeat      = "heartbeat"
	TypeHeartbeatAck   = "heartbeat_ack"
	TypeRotateRequest  = "rotate_request"
	TypeRotatePrepare  = "rotate_prepare"
	TypeRotateAccepted = "rotate_accepted"
	TypeProtocolError  = "error"

	TypeTerminalFrame  = "terminal_frame"
	CapabilityTerminal = "agent.terminal.v1"
)

type Envelope struct {
	Version    int             `json:"version"`
	Type       string          `json:"type"`
	Generation uint64          `json:"generation,omitempty"`
	Sequence   uint64          `json:"sequence,omitempty"`
	RequestID  string          `json:"requestId,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type Hello struct {
	AgentID      string             `json:"agentId"`
	NodeID       string             `json:"nodeId"`
	AgentVersion string             `json:"agentVersion"`
	Capabilities []string           `json:"capabilities"`
	Permissions  RuntimePermissions `json:"permissions"`
}

type RuntimePermissions struct {
	OS                  string `json:"os"`
	Architecture        string `json:"architecture"`
	EffectiveUID        int    `json:"effectiveUid"`
	EffectiveGID        int    `json:"effectiveGid"`
	SupplementaryGroups []int  `json:"supplementaryGroups"`
}

type Welcome struct {
	AgentID                string   `json:"agentId"`
	NodeID                 string   `json:"nodeId"`
	Generation             uint64   `json:"generation"`
	HeartbeatIntervalSecs  int      `json:"heartbeatIntervalSeconds"`
	OfflineAfterSecs       int      `json:"offlineAfterSeconds"`
	Capabilities           []string `json:"capabilities"`
	RotationRequestedID    string   `json:"rotationRequestedId,omitempty"`
	CredentialRotationDone bool     `json:"credentialRotationCommitted,omitempty"`
}

type Heartbeat struct {
	Capabilities []string `json:"capabilities"`
}

type HeartbeatAck struct {
	AcceptedAt int64 `json:"acceptedAt"`
}

type RotateRequest struct {
	RotationID string `json:"rotationId"`
}

type RotatePrepare struct {
	RotationID    string `json:"rotationId"`
	NewCredential string `json:"newCredential"`
}

type RotateAccepted struct {
	RotationID string `json:"rotationId"`
}

type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
