package metrics

import "testing"

func TestClassifyInterfaceAvoidsLoopbackVirtualBridgeAndDuplicateLinks(t *testing.T) {
	tests := []struct {
		name       string
		facts      interfaceFacts
		factsOK    bool
		link       linkMetadata
		linkErr    error
		masterKind string
		included   bool
		reason     string
		known      bool
	}{
		{name: "lo", facts: interfaceFacts{up: true, loopback: true}, factsOK: true, included: false, reason: "loopback", known: true},
		{name: "veth42", facts: interfaceFacts{up: true}, factsOK: true, included: false, reason: "virtual_interface", known: true},
		{name: "br-123", facts: interfaceFacts{up: true}, factsOK: true, included: false, reason: "virtual_interface", known: true},
		{name: "eth0", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 2, iflink: 2}, included: true, known: true},
		{name: "bond0", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 7, iflink: 7, kind: "bond"}, included: true, known: true},
		{name: "eth1", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 3, iflink: 3, master: "bond0"}, masterKind: "bond", included: false, reason: "enslaved_interface", known: true},
		{name: "ens4", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 4, iflink: 4, master: "br0"}, masterKind: "bridge", included: true, known: true},
		{name: "eth0.20", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 20, iflink: 2}, included: false, reason: "lower_or_virtual_interface", known: true},
		{name: "macvlan0", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 21, iflink: 21, lowerLinks: []string{"eth0"}}, included: false, reason: "upper_or_virtual_interface", known: true},
		{name: "enp1s0", facts: interfaceFacts{up: true}, factsOK: true, link: linkMetadata{ifindex: 2, iflink: 2, upperLinks: []string{"macvlan0"}}, included: true, known: true},
		{name: "eth9", facts: interfaceFacts{up: true}, factsOK: true, linkErr: errInvalidData, included: false, reason: "topology_unavailable", known: false},
		{name: "eth8", facts: interfaceFacts{}, factsOK: false, link: linkMetadata{}, included: false, reason: "interface_metadata_unavailable", known: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			included, reason, known := classifyInterface(test.name, test.facts, test.factsOK, test.link, test.linkErr, test.masterKind)
			if included != test.included || reason != test.reason || known != test.known {
				t.Fatalf("classification = (%v, %q, %v), want (%v, %q, %v)", included, reason, known, test.included, test.reason, test.known)
			}
		})
	}
}
