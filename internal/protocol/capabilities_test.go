package protocol

import "testing"

func TestIndependentBuildNegotiation(t *testing.T) {
	offered := []string{CapabilityMetrics, "agent.future.v9", CapabilityMetrics, CapabilityFiles}
	accepted := NegotiateCapabilities(offered)
	if len(accepted) != 2 || accepted[0] != CapabilityMetrics || accepted[1] != CapabilityFiles {
		t.Fatalf("negotiated %v", accepted)
	}
	if !ValidNegotiation(offered, accepted) {
		t.Fatal("valid subset rejected")
	}
	for _, bad := range [][]string{{CapabilityDocker}, {CapabilityMetrics, CapabilityMetrics}} {
		if ValidNegotiation(offered, bad) {
			t.Fatalf("unsafe negotiation accepted: %v", bad)
		}
	}
	if !ValidNegotiation(offered, nil) {
		t.Fatal("Core can disable optional capabilities")
	}
	if len(Describe().Capabilities) != len(SupportedCapabilities()) {
		t.Fatal("descriptor differs from capability contract")
	}
}
