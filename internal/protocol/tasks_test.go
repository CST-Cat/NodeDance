package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const testTaskNodeID = "b738a2d2-a255-4912-9e2e-26f974ac2529"

func TestTaskIntentUsesExistingCanonicalDigestContract(t *testing.T) {
	intent := TaskIntent{Action: TaskRestart, ContainerID: strings.Repeat("a", 64)}
	identity, err := TaskIdentity("task-1", testTaskNodeID, "restart-1", intent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := taskstate.RequestDigest(identity)
	if err != nil {
		t.Fatal(err)
	}
	legacyPayload := json.RawMessage(`{"action":"restart","container_id":"` + intent.ContainerID + `"}`)
	legacy, err := taskstate.RequestDigest(taskstate.Identity{
		TaskID: "task-1", NodeID: testTaskNodeID, IdempotencyKey: "restart-1", TargetID: intent.ContainerID,
		ResourceKey: "docker-container:" + intent.ContainerID, Action: "restart", Payload: legacyPayload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != legacy {
		t.Fatalf("shared canonical helper changed existing digest: got %x legacy %x", got, legacy)
	}
	canonical, err := CanonicalTaskIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(legacyPayload) {
		t.Fatalf("canonical payload changed: %s", canonical)
	}
}

func TestValidateTaskDispatchBindsEnvelopeNodeJournalTargetAndDigest(t *testing.T) {
	intent := TaskIntent{Action: TaskDelete, ContainerID: strings.Repeat("b", 64), DeleteConfirmed: true}
	digest, err := TaskRequestDigest("task-2", testTaskNodeID, "delete-2", intent)
	if err != nil {
		t.Fatal(err)
	}
	dispatch := TaskDispatch{TaskID: "task-2", NodeID: testTaskNodeID, JournalID: strings.Repeat("c", 64),
		TargetID: intent.ContainerID, IdempotencyKey: "delete-2", RequestDigest: DigestString(digest), Intent: intent}
	envelope := Envelope{Version: CurrentVersion, Type: TypeTaskDispatch, Generation: 8, RequestID: dispatch.TaskID}
	if err := ValidateTaskDispatch(envelope, dispatch, testTaskNodeID, dispatch.JournalID, 8); err != nil {
		t.Fatalf("valid dispatch rejected: %v", err)
	}
	tests := []struct {
		name       string
		envelope   Envelope
		dispatch   TaskDispatch
		node       string
		journal    string
		generation uint64
	}{
		{"stale-generation", Envelope{Version: CurrentVersion, Type: TypeTaskDispatch, Generation: 7, RequestID: "task-2"}, dispatch, testTaskNodeID, dispatch.JournalID, 8},
		{"request-id", Envelope{Version: CurrentVersion, Type: TypeTaskDispatch, Generation: 8, RequestID: "other"}, dispatch, testTaskNodeID, dispatch.JournalID, 8},
		{"wrong-node", envelope, dispatch, "other-node", dispatch.JournalID, 8},
		{"wrong-journal", envelope, dispatch, testTaskNodeID, strings.Repeat("d", 64), 8},
		{"wrong-target", envelope, func() TaskDispatch { copy := dispatch; copy.TargetID = strings.Repeat("e", 64); return copy }(), testTaskNodeID, dispatch.JournalID, 8},
		{"wrong-digest", envelope, func() TaskDispatch { copy := dispatch; copy.RequestDigest = strings.Repeat("0", 64); return copy }(), testTaskNodeID, dispatch.JournalID, 8},
		{"short-target", envelope, func() TaskDispatch {
			copy := dispatch
			copy.TargetID = "short"
			copy.Intent.ContainerID = "short"
			return copy
		}(), testTaskNodeID, dispatch.JournalID, 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateTaskDispatch(test.envelope, test.dispatch, test.node, test.journal, test.generation); !errors.Is(err, ErrInvalidTaskMessage) {
				t.Fatalf("invalid dispatch was accepted: %v", err)
			}
		})
	}
}

func TestTaskReportHasNoCoreDeliveryEvidenceAndRejectsSecrets(t *testing.T) {
	report := TaskReport{TaskID: "task-3", NodeID: testTaskNodeID, JournalID: strings.Repeat("f", 64),
		ReportRevision: 1,
		TargetID:       strings.Repeat("a", 64), IdempotencyKey: "key-3", RequestDigest: strings.Repeat("1", 64),
		Status: taskstate.Running, Evidence: TaskEvidence{ExecutionAttempted: true},
		Progress: TaskProgress{Phase: "executing"}}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "deliveryCommitted") || strings.Contains(string(encoded), "delivery_committed") {
		t.Fatalf("Agent report exposed Core-only evidence: %s", encoded)
	}
	envelope := Envelope{Version: CurrentVersion, Type: TypeTaskReport, Generation: 4, RequestID: report.TaskID}
	if err := ValidateTaskReport(envelope, report, testTaskNodeID, report.JournalID, 4); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	badRevision := report
	badRevision.ReportRevision = 0
	if err := ValidateTaskReport(envelope, badRevision, testTaskNodeID, report.JournalID, 4); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("report without revision was accepted: %v", err)
	}
	secret := "token_DO_NOT_PERSIST"
	bad := report
	bad.Result = TaskResult{Code: secret}
	if err := ValidateTaskReport(envelope, bad, testTaskNodeID, report.JournalID, 4); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("arbitrary nonterminal result was accepted: %v", err)
	}
	bad = report
	bad.Status = taskstate.Unknown
	bad.Progress.Phase = "reconciling"
	bad.Result = TaskResult{Code: "result_pending", ObservedState: secret}
	if err := ValidateTaskReport(envelope, bad, testTaskNodeID, report.JournalID, 4); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("unknown result carried arbitrary observation: %v", err)
	}
}

func TestTaskReportAckRequiresMatchingTaskGenerationAndRevision(t *testing.T) {
	ack := TaskReportAck{TaskID: "task-ack", ReportRevision: 17, Accepted: true}
	envelope := Envelope{Version: CurrentVersion, Type: TypeTaskReportAck, Generation: 7, RequestID: ack.TaskID}
	if err := ValidateTaskReportAck(envelope, ack, 7); err != nil {
		t.Fatalf("valid revision-bound acknowledgement rejected: %v", err)
	}
	bad := ack
	bad.ReportRevision = 0
	if err := ValidateTaskReportAck(envelope, bad, 7); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("acknowledgement without a revision was accepted: %v", err)
	}
	if err := ValidateTaskReportAck(envelope, ack, 8); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("stale-generation acknowledgement was accepted: %v", err)
	}
	wrongTask := envelope
	wrongTask.RequestID = "task-other"
	if err := ValidateTaskReportAck(wrongTask, ack, 7); !errors.Is(err, ErrInvalidTaskMessage) {
		t.Fatalf("acknowledgement for another task was accepted: %v", err)
	}
}

func TestValidateTaskIntentRejectsUnsupportedAndUnsafeFields(t *testing.T) {
	base := TaskIntent{Action: TaskRestart, ContainerID: strings.Repeat("1", 64)}
	tests := []TaskIntent{
		{Action: "exec", ContainerID: base.ContainerID},
		{Action: TaskRestart, ContainerID: base.ContainerID, NewName: "other"},
		{Action: TaskDelete, ContainerID: base.ContainerID},
		{Action: TaskRename, ContainerID: base.ContainerID, NewName: "../escape"},
		{Action: TaskRestart, ContainerID: "short"},
	}
	for _, intent := range tests {
		if err := ValidateTaskIntent(intent); !errors.Is(err, ErrInvalidTaskMessage) {
			t.Fatalf("unsafe intent accepted %+v: %v", intent, err)
		}
	}
}
