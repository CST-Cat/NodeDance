package protocol

type Descriptor struct {
	Version                  int      `json:"version"`
	Capabilities             []string `json:"capabilities"`
	MaxMessageBytes          int      `json:"maxMessageBytes"`
	HeartbeatIntervalSeconds int      `json:"heartbeatIntervalSeconds"`
	OfflineAfterSeconds      int      `json:"offlineAfterSeconds"`
}

func Describe() Descriptor {
	return Descriptor{Version: CurrentVersion, Capabilities: SupportedCapabilities(), MaxMessageBytes: MaxMessageBytes, HeartbeatIntervalSeconds: HeartbeatIntervalSeconds, OfflineAfterSeconds: OfflineAfterSeconds}
}
func SupportedCapabilities() []string {
	return []string{"agent.heartbeat.v1", "agent.rotation.v1", "agent.os-permissions.v1", CapabilityMetrics, CapabilityHost, CapabilityDocker, CapabilityTaskBridge, CapabilityContainerStreams, CapabilityCompose, CapabilityImages, CapabilityTerminal, CapabilityFiles, CapabilityProbes, CapabilityUpdate}
}
func KnownCapability(capability string) bool {
	for _, known := range SupportedCapabilities() {
		if capability == known {
			return true
		}
	}
	return false
}

func NegotiateCapabilities(offered []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, capability := range offered {
		if KnownCapability(capability) && !seen[capability] {
			result = append(result, capability)
			seen[capability] = true
		}
	}
	return result
}

func ValidNegotiation(offered, accepted []string) bool {
	offer := map[string]bool{}
	for _, c := range offered {
		offer[c] = true
	}
	seen := map[string]bool{}
	for _, c := range accepted {
		if !offer[c] || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}
