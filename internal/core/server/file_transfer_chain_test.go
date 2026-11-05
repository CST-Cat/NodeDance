package server

import (
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestDirectAttemptChain(t *testing.T) {
	full := []string{"tcp", "reverse", "tcp_punch", "ice"}
	punchIce := []string{"tcp_punch", "ice"}
	asset := func(lineType string, listenPort int) protocol.NodeAsset {
		return protocol.NodeAsset{LineType: lineType, DirectListenPort: listenPort}
	}
	for _, tt := range []struct {
		name           string
		source, target protocol.NodeAsset
		want           []string
	}{
		{name: "unlabeled_runs_full", source: asset("", 0), target: asset("", 0), want: full},
		{name: "unlabeled_target_runs_full", source: asset(protocol.LineTypeKVM, 0), target: asset("", 0), want: full},
		{name: "kvm_kvm_full", source: asset(protocol.LineTypeKVM, 0), target: asset(protocol.LineTypeKVM, 0), want: full},
		{name: "kvm_nat_without_port", source: asset(protocol.LineTypeKVM, 0), target: asset(protocol.LineTypeNAT, 0), want: punchIce},
		{name: "nat_kvm_without_port", source: asset(protocol.LineTypeNAT, 0), target: asset(protocol.LineTypeKVM, 0), want: punchIce},
		{name: "kvm_nat_with_declared_port", source: asset(protocol.LineTypeKVM, 0), target: asset(protocol.LineTypeNAT, 2222), want: full},
		{name: "nat_nat_without_port", source: asset(protocol.LineTypeNAT, 0), target: asset(protocol.LineTypeNAT, 0), want: punchIce},
		{name: "kvm_homelab", source: asset(protocol.LineTypeKVM, 0), target: asset(protocol.LineTypeHomelab, 0), want: punchIce},
		{name: "homelab_kvm", source: asset(protocol.LineTypeHomelab, 0), target: asset(protocol.LineTypeKVM, 0), want: punchIce},
		{name: "homelab_homelab", source: asset(protocol.LineTypeHomelab, 0), target: asset(protocol.LineTypeHomelab, 0), want: punchIce},
		{name: "nat_homelab", source: asset(protocol.LineTypeNAT, 0), target: asset(protocol.LineTypeHomelab, 0), want: punchIce},
		{name: "nat_homelab_ignores_declared_port", source: asset(protocol.LineTypeNAT, 3333), target: asset(protocol.LineTypeHomelab, 0), want: punchIce},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := directAttemptChain(tt.source, tt.target)
			if len(got) != len(tt.want) {
				t.Fatalf("chain = %v, want %v", got, tt.want)
			}
			for index, mode := range tt.want {
				if got[index] != mode {
					t.Fatalf("chain = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
