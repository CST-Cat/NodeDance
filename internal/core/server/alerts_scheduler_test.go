package server

import (
	"reflect"
	"testing"
	"time"

	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestAlertProbeConfigsGroupByNodeWithoutChangingSamplesOrOrder(t *testing.T) {
	fallback := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	checkedAt := "2026-10-09T11:59:00.123456789Z"
	// Probe Store.List returns ORDER BY node_id,name,id. Grouping must preserve
	// each node's subsequence so alert samples remain deterministic.
	configs := []coreprobes.Config{
		{ID: "probe-a-1", NodeID: "node-a", Name: "alpha", Status: protocol.ProbeResultHealthy, LastCheckedAt: checkedAt},
		{ID: "probe-a-2", NodeID: "node-a", Name: "zeta", Status: protocol.ProbeResultUnhealthy, LastCheckedAt: checkedAt},
		{ID: "probe-a-3", NodeID: "node-a", Name: "zeta", Status: protocol.ProbeResultUnknown, LastCheckedAt: "not-a-time"},
		{ID: "probe-b-1", NodeID: "node-b", Name: "beta", Status: protocol.ProbeResultUnhealthy, LastCheckedAt: checkedAt},
	}

	grouped := groupAlertProbeConfigsByNode(configs)
	if got, want := len(grouped), 2; got != want {
		t.Fatalf("group count=%d, want %d", got, want)
	}

	nodeASamples := alertProbeSamples("node-a", "Node A", grouped["node-a"], fallback)
	nodeBSamples := alertProbeSamples("node-b", "Node B", grouped["node-b"], fallback)
	assertProbeSamples := func(nodeID, nodeName string, got []corealerts.Sample, wantIDs []string, wantStates []string, wantKnown []bool) {
		t.Helper()
		if len(got) != len(wantIDs) {
			t.Fatalf("node %s sample count=%d, want %d: %+v", nodeID, len(got), len(wantIDs), got)
		}
		for i, sample := range got {
			if sample.NodeID != nodeID || sample.SubjectID != wantIDs[i] || sample.NodeName != nodeName {
				t.Errorf("node %s sample[%d] was assigned to the wrong resource: %+v", nodeID, i, sample)
			}
			if sample.Kind != corealerts.KindProbeState || sample.State != wantStates[i] || sample.Known != wantKnown[i] {
				t.Errorf("node %s sample[%d] status mismatch: %+v", nodeID, i, sample)
			}
		}
	}
	assertProbeSamples("node-a", "Node A", nodeASamples,
		[]string{"probe-a-1", "probe-a-2", "probe-a-3"},
		[]string{protocol.ProbeResultHealthy, protocol.ProbeResultUnhealthy, protocol.ProbeResultUnknown},
		[]bool{true, true, false})
	assertProbeSamples("node-b", "Node B", nodeBSamples,
		[]string{"probe-b-1"}, []string{protocol.ProbeResultUnhealthy}, []bool{true})

	wantObserved, err := time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !nodeASamples[0].ObservedAt.Equal(wantObserved) || !nodeASamples[2].ObservedAt.Equal(fallback) {
		t.Fatalf("probe observed-at semantics changed: checked=%s fallback=%s", nodeASamples[0].ObservedAt, nodeASamples[2].ObservedAt)
	}

	// Repeating the grouping and projection must not depend on map iteration.
	if again := alertProbeSamples("node-a", "Node A", groupAlertProbeConfigsByNode(configs)["node-a"], fallback); !reflect.DeepEqual(nodeASamples, again) {
		t.Fatalf("node A probe samples are not stable:\nfirst: %+v\nagain: %+v", nodeASamples, again)
	}
}
