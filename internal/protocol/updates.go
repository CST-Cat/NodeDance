package protocol

import "encoding/json"

const CapabilityAgentUpdates = "agent.updates.v1"

// CapabilityAgentUpdatesPreparedAck marks the update protocol that requires a
// Core-persisted prepared report acknowledgement before an Agent switches.
// It is intentionally distinct from the legacy capability: older Cores may
// dispatch updates without understanding the prepared ACK handoff.
const CapabilityAgentUpdatesPreparedAck = "agent.updates.prepared-ack.v1"

type AgentUpdateCommand struct {
	TaskID      string          `json:"taskId"`
	Manifest    json.RawMessage `json:"manifest"`
	ArtifactURL string          `json:"artifactUrl"`
	CoreVersion string          `json:"coreVersion"`
}

type AgentUpdateReport struct {
	TaskID  string `json:"taskId"`
	Status  string `json:"status"` // accepted, prepared, rejected, failed
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type AgentUpdatePreparedAck struct {
	TaskID string `json:"taskId"`
}
