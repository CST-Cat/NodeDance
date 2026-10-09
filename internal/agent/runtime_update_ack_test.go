package agent

import (
	"encoding/json"
	"errors"
	"testing"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestPreparedUpdateAckIsPersistedBeforeAgentExits(t *testing.T) {
	const taskID = "5f29b72e-2613-4f44-8c88-3754fca3a124"
	stateDir := t.TempDir()
	if err := agentupdate.WriteJournal(stateDir, agentupdate.Journal{
		State: "staged", TaskID: taskID, OldTarget: "versions/old/nodedance-agent",
		CandidateTarget: "versions/candidate/nodedance-agent", Version: "candidate",
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(protocol.AgentUpdatePreparedAck{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	envelope := protocol.Envelope{Type: protocol.TypeAgentUpdatePreparedAck, RequestID: taskID, Payload: payload}

	if err := acceptPreparedUpdateAck(stateDir, "different-task", envelope); err == nil {
		t.Fatal("Agent accepted ACK for a report it did not send")
	}
	journal, err := agentupdate.ReadJournal(stateDir)
	if err != nil || journal.State != "staged" {
		t.Fatalf("journal after mismatched ACK = %#v, %v", journal, err)
	}

	if err := acceptPreparedUpdateAck(stateDir, taskID, envelope); !errors.Is(err, agentupdate.ErrPrepared) {
		t.Fatalf("accepted ACK result = %v, want ErrPrepared", err)
	}
	journal, err = agentupdate.ReadJournal(stateDir)
	if err != nil || journal.State != "prepared" {
		t.Fatalf("journal after accepted ACK = %#v, %v", journal, err)
	}
}

func TestStagedUpdateExplicitlyRejectsCoreWithoutPreparedAckCapability(t *testing.T) {
	const taskID = "5f29b72e-2613-4f44-8c88-3754fca3a124"
	stateDir := t.TempDir()
	if err := agentupdate.WriteJournal(stateDir, agentupdate.Journal{
		State: "staged", TaskID: taskID, OldTarget: "versions/old/nodedance-agent",
		CandidateTarget: "versions/candidate/nodedance-agent", Version: "candidate",
	}); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedUpdateWelcome(stateDir, []string{protocol.CapabilityAgentUpdates}); err == nil || err.Error() != "Core does not support agent.updates.prepared-ack.v1; staged update remains unapplied" {
		t.Fatalf("legacy Core welcome error = %v", err)
	}
	journal, err := agentupdate.ReadJournal(stateDir)
	if err != nil || journal.State != "staged" || journal.TaskID != taskID {
		t.Fatalf("staged update was silently altered: %#v, %v", journal, err)
	}
	if err := validatePreparedUpdateWelcome(stateDir, []string{protocol.CapabilityAgentUpdatesPreparedAck}); err != nil {
		t.Fatalf("prepared-ACK Core welcome rejected: %v", err)
	}
}
