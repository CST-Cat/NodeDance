package protocol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	CapabilityTaskBridge = "agent.task-bridge.v1"

	TypeTaskJournalHello    = "task_journal_hello"
	TypeTaskJournalStatus   = "task_journal_status"
	TypeTaskSnapshotRequest = "task_snapshot_request"
	TypeTaskSnapshotPage    = "task_snapshot_page"
	TypeTaskDispatch        = "task_dispatch"
	TypeTaskReport          = "task_report"
	TypeTaskReportAck       = "task_report_ack"
	TypeTaskReconcile       = "task_reconcile_request"

	TaskSnapshotPageSize = 32
	MaxTaskSnapshotTasks = 10000
	MaxTaskSnapshotBytes = 4 << 20
	MaxTaskPayloadBytes  = 64 << 10
)

var (
	ErrInvalidTaskMessage = errors.New("invalid task bridge message")
	fullContainerID       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	containerName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// TaskAction and TaskIntent are the single canonical safe request representation
// shared by Core and Agent. Never add secrets, command text, or arbitrary JSON.
type TaskAction string

const (
	TaskStart   TaskAction = "start"
	TaskStop    TaskAction = "stop"
	TaskRestart TaskAction = "restart"
	TaskPause   TaskAction = "pause"
	TaskResume  TaskAction = "resume"
	TaskDelete  TaskAction = "delete"
	TaskRename  TaskAction = "rename"
)

type TaskIntent struct {
	Action          TaskAction `json:"action"`
	ContainerID     string     `json:"container_id"`
	NewName         string     `json:"new_name,omitempty"`
	DeleteConfirmed bool       `json:"delete_confirmed,omitempty"`
}

// CanonicalTaskIntent returns the exact canonical JSON used in the task
// identity digest. The typed DTO prevents callers from smuggling extra fields.
func CanonicalTaskIntent(intent TaskIntent) ([]byte, error) {
	if err := ValidateTaskIntent(intent); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, fmt.Errorf("encode safe task intent: %w", err)
	}
	canonical, err := taskstate.CanonicalJSON(encoded)
	if err != nil {
		return nil, fmt.Errorf("canonicalize safe task intent: %w", err)
	}
	return canonical, nil
}

func ValidateTaskIntent(intent TaskIntent) error {
	if !IsFullContainerID(intent.ContainerID) {
		return fmt.Errorf("%w: target must be the full Docker container ID", ErrInvalidTaskMessage)
	}
	switch intent.Action {
	case TaskStart, TaskStop, TaskRestart, TaskPause, TaskResume:
		if intent.NewName != "" || intent.DeleteConfirmed {
			return ErrInvalidTaskMessage
		}
	case TaskDelete:
		if intent.NewName != "" || !intent.DeleteConfirmed {
			return ErrInvalidTaskMessage
		}
	case TaskRename:
		if !containerName.MatchString(intent.NewName) || intent.DeleteConfirmed {
			return ErrInvalidTaskMessage
		}
	default:
		return ErrInvalidTaskMessage
	}
	return nil
}

// TaskIdentity constructs the shared identity digest input. TaskID and
// IdempotencyKey identify the ledger row and are deliberately checked outside
// the request digest; NodeID, full target, resource, action, and intent are
// covered by the digest exactly as taskstate specifies.
func TaskIdentity(taskID, nodeID, idempotencyKey string, intent TaskIntent) (taskstate.Identity, error) {
	payload, err := CanonicalTaskIntent(intent)
	if err != nil {
		return taskstate.Identity{}, err
	}
	return taskstate.Identity{
		TaskID: taskID, NodeID: nodeID, IdempotencyKey: idempotencyKey,
		TargetID: intent.ContainerID, ResourceKey: "docker-container:" + intent.ContainerID,
		Action: string(intent.Action), Payload: payload,
	}, nil
}

func TaskRequestDigest(taskID, nodeID, idempotencyKey string, intent TaskIntent) ([sha256.Size]byte, error) {
	identity, err := TaskIdentity(taskID, nodeID, idempotencyKey, intent)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("digest safe task intent: %w", err)
	}
	return digest, nil
}

func IsFullContainerID(id string) bool { return fullContainerID.MatchString(id) }

func DigestString(digest [sha256.Size]byte) string { return hex.EncodeToString(digest[:]) }

func ParseDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != value {
		return digest, fmt.Errorf("%w: request digest must be 64 lowercase hex characters", ErrInvalidTaskMessage)
	}
	copy(digest[:], decoded)
	return digest, nil
}

type TaskJournalHello struct {
	NodeID    string `json:"nodeId"`
	JournalID string `json:"journalId"`
}

type TaskJournalStatus struct {
	Accepted       bool   `json:"accepted"`
	JournalID      string `json:"journalId"`
	ReviewRequired bool   `json:"reviewRequired"`
	Capacity       int    `json:"capacity"`
}

type TaskSnapshotRequest struct {
	SnapshotID string `json:"snapshotId"`
	JournalID  string `json:"journalId"`
}

type TaskSnapshotPage struct {
	SnapshotID string       `json:"snapshotId"`
	JournalID  string       `json:"journalId"`
	Page       uint32       `json:"page"`
	Final      bool         `json:"final"`
	Reports    []TaskReport `json:"reports"`
}

type TaskDispatch struct {
	TaskID         string     `json:"taskId"`
	NodeID         string     `json:"nodeId"`
	JournalID      string     `json:"journalId"`
	TargetID       string     `json:"targetId"`
	IdempotencyKey string     `json:"idempotencyKey"`
	RequestDigest  string     `json:"requestDigest"`
	Intent         TaskIntent `json:"intent"`
}

type TaskReconcileRequest struct {
	TaskID         string `json:"taskId"`
	NodeID         string `json:"nodeId"`
	JournalID      string `json:"journalId"`
	TargetID       string `json:"targetId"`
	IdempotencyKey string `json:"idempotencyKey"`
	RequestDigest  string `json:"requestDigest"`
}

type TaskEvidence struct {
	ExecutionAttempted    bool `json:"executionAttempted,omitempty"`
	ExecutionCompleted    bool `json:"executionCompleted,omitempty"`
	FailureConfirmed      bool `json:"failureConfirmed,omitempty"`
	PostconditionVerified bool `json:"postconditionVerified,omitempty"`
	ProcessTerminated     bool `json:"processTerminated,omitempty"`
	ActualResultConfirmed bool `json:"actualResultConfirmed,omitempty"`
	CancellationConfirmed bool `json:"cancellationConfirmed,omitempty"`
}

func (e TaskEvidence) TaskStateEvidence() taskstate.Evidence {
	return taskstate.Evidence{
		ExecutionAttempted: e.ExecutionAttempted, ExecutionCompleted: e.ExecutionCompleted,
		FailureConfirmed: e.FailureConfirmed, PostconditionVerified: e.PostconditionVerified,
		ProcessTerminated: e.ProcessTerminated, ActualResultConfirmed: e.ActualResultConfirmed,
		CancellationConfirmed: e.CancellationConfirmed,
	}
}

type TaskProgress struct {
	Phase     string `json:"phase"`
	Completed uint64 `json:"completed,omitempty"`
	Total     uint64 `json:"total,omitempty"`
}

type TaskResult struct {
	Code             string `json:"code,omitempty"`
	ObservedState    string `json:"observedState,omitempty"`
	ResourceRevision string `json:"resourceRevision,omitempty"`
}

type TaskReport struct {
	TaskID         string           `json:"taskId"`
	ReportRevision uint64           `json:"reportRevision"`
	NodeID         string           `json:"nodeId"`
	JournalID      string           `json:"journalId"`
	TargetID       string           `json:"targetId"`
	IdempotencyKey string           `json:"idempotencyKey"`
	RequestDigest  string           `json:"requestDigest"`
	Status         taskstate.Status `json:"status"`
	Evidence       TaskEvidence     `json:"evidence"`
	Progress       TaskProgress     `json:"progress"`
	Result         TaskResult       `json:"result"`
}

type TaskReportAck struct {
	TaskID         string `json:"taskId"`
	ReportRevision uint64 `json:"reportRevision"`
	Accepted       bool   `json:"accepted"`
	Duplicate      bool   `json:"duplicate,omitempty"`
}

func ValidateTaskDispatch(envelope Envelope, dispatch TaskDispatch, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskDispatch || envelope.Generation != generation ||
		envelope.RequestID == "" || envelope.RequestID != dispatch.TaskID || generation == 0 ||
		dispatch.NodeID != nodeID || dispatch.JournalID != journalID || dispatch.TargetID != dispatch.Intent.ContainerID ||
		!validTaskIdentityFields(dispatch.TaskID, dispatch.NodeID, dispatch.JournalID, dispatch.TargetID, dispatch.IdempotencyKey) {
		return ErrInvalidTaskMessage
	}
	digest, err := ParseDigest(dispatch.RequestDigest)
	if err != nil {
		return err
	}
	computed, err := TaskRequestDigest(dispatch.TaskID, dispatch.NodeID, dispatch.IdempotencyKey, dispatch.Intent)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(digest[:], computed[:]) != 1 {
		return fmt.Errorf("%w: request digest mismatch", ErrInvalidTaskMessage)
	}
	return nil
}

func ValidateTaskReport(envelope Envelope, report TaskReport, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskReport || envelope.Generation != generation ||
		envelope.RequestID == "" || envelope.RequestID != report.TaskID || generation == 0 ||
		report.NodeID != nodeID || report.JournalID != journalID ||
		report.ReportRevision == 0 ||
		!validTaskIdentityFields(report.TaskID, report.NodeID, report.JournalID, report.TargetID, report.IdempotencyKey) {
		return ErrInvalidTaskMessage
	}
	if _, err := ParseDigest(report.RequestDigest); err != nil {
		return err
	}
	if !validTaskStatus(report.Status) || len(report.Progress.Phase) > 32 || report.Progress.Completed > report.Progress.Total && report.Progress.Total != 0 ||
		!validResult(report.Status, report.Result) {
		return ErrInvalidTaskMessage
	}
	return nil
}

// ValidateTaskReportAck binds a report acknowledgement to the active
// connection generation and exact report revision. A task ID alone is not
// sufficient because a queued acknowledgement can arrive after a terminal
// report for the same task.
func ValidateTaskReportAck(envelope Envelope, ack TaskReportAck, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskReportAck || envelope.Generation != generation ||
		generation == 0 || envelope.RequestID == "" || envelope.RequestID != ack.TaskID ||
		ack.TaskID == "" || len(ack.TaskID) > taskstate.MaxIdentityBytes || ack.ReportRevision == 0 {
		return ErrInvalidTaskMessage
	}
	return nil
}

func ValidateTaskReconcile(envelope Envelope, request TaskReconcileRequest, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskReconcile || envelope.Generation != generation ||
		envelope.RequestID == "" || envelope.RequestID != request.TaskID || generation == 0 ||
		request.NodeID != nodeID || request.JournalID != journalID ||
		!validTaskIdentityFields(request.TaskID, request.NodeID, request.JournalID, request.TargetID, request.IdempotencyKey) {
		return ErrInvalidTaskMessage
	}
	_, err := ParseDigest(request.RequestDigest)
	return err
}

func validTaskIdentityFields(taskID, nodeID, journalID, targetID, key string) bool {
	if taskID == "" || len(taskID) > taskstate.MaxIdentityBytes || nodeID == "" || len(nodeID) > taskstate.MaxIdentityBytes ||
		journalID == "" || len(journalID) != 64 || !IsFullContainerID(targetID) || key == "" || len(key) > taskstate.MaxIdempotencyKeyBytes {
		return false
	}
	decoded, err := hex.DecodeString(journalID)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == journalID
}

func validTaskStatus(status taskstate.Status) bool {
	switch status {
	case taskstate.Queued, taskstate.Running, taskstate.Succeeded, taskstate.Failed, taskstate.TimedOut, taskstate.Canceled, taskstate.Unknown:
		return true
	default:
		return false
	}
}

func validResult(status taskstate.Status, result TaskResult) bool {
	if !safeTaskToken(result.ObservedState, 64) || !safeTaskToken(result.ResourceRevision, 128) {
		return false
	}
	switch status {
	case taskstate.Queued, taskstate.Running:
		return result == (TaskResult{})
	case taskstate.Unknown:
		return result == (TaskResult{}) || result == (TaskResult{Code: "result_pending"})
	case taskstate.Succeeded:
		return result.Code == "verified" && result.ObservedState != ""
	case taskstate.Failed:
		return result.Code == "failed" && result.ObservedState != ""
	case taskstate.TimedOut:
		return result.Code == "timed_out" && result.ObservedState != ""
	case taskstate.Canceled:
		return result.Code == "canceled" && result.ObservedState != ""
	default:
		return false
	}
}

func safeTaskToken(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == ':' || r == '-') {
			return false
		}
	}
	return true
}
