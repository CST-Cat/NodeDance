package protocol

import "encoding/json"

const CapabilityAgentUpdates = "agent.updates.v1"

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
