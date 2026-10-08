// Package protocol contains the versioned enrollment, identity, and control
// wire contract shared by the Core and outbound Agents.
package protocol

import "encoding/json"

const (
	// CurrentVersion is incremented when an incompatible wire change is made.
	CurrentVersion = 1
	// MinimumVersion is the oldest wire version accepted by Core.
	MinimumVersion = 1
	// MaxMessageBytes bounds every Agent WebSocket frame before JSON decoding.
	MaxMessageBytes = 1 << 20
	// HeartbeatInterval is the Agent's default heartbeat period.
	HeartbeatIntervalSeconds = 5
	OfflineAfterSeconds      = 30
)

const (
	TypeHello          = "hello"
	TypeWelcome        = "welcome"
	TypeHeartbeat      = "heartbeat"
	TypeHeartbeatAck   = "heartbeat_ack"
	TypeRotateRequest  = "rotate_request"
	TypeRotatePrepare  = "rotate_prepare"
	TypeRotateAccepted = "rotate_accepted"
	TypeProtocolError  = "error"
)

// Envelope is the only top-level JSON shape accepted on the Agent channel.
// Generation is assigned by Core after a successful authenticated hello; the
// Agent echoes it, while Core independently checks its connection-local value.
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
	Capabilities []string `json:"capabilities,omitempty"`
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
