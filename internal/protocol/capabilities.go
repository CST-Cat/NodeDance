package protocol

// Descriptor describes the wire contract independently of Core and Agent builds.
// Build versions never participate in negotiation: peers accept the current wire
// version and enable only the intersection of their offered capabilities.
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
	return []string{"agent.heartbeat.v1", "agent.rotation.v1", "agent.os-permissions.v1", CapabilityMetrics, CapabilityDocker, CapabilityTaskBridge, CapabilityContainerStreams, CapabilityCompose, CapabilityImages, CapabilityTerminal, CapabilityFiles, CapabilityProbes, CapabilityUpdate}
}
func KnownCapability(capability string) bool {
	for _, known := range SupportedCapabilities() {
		if capability == known {
			return true
		}
	}
	return false
}

// NegotiateCapabilities ignores unimplemented optional capabilities. This is
// forward extensibility of the current contract, not a retired protocol adapter.
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

// ValidNegotiation prevents Core from enabling a capability the Agent did not
// offer (including locally disabled file/Docker features).
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
