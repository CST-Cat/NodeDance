package protocol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

const (
	CapabilityTaskBridge = "agent.task-bridge.v1"

	TypeTaskJournalHello             = "task_journal_hello"
	TypeTaskJournalStatus            = "task_journal_status"
	TypeTaskSnapshotRequest          = "task_snapshot_request"
	TypeTaskSnapshotPage             = "task_snapshot_page"
	TypeTaskDispatch                 = "task_dispatch"
	TypeTaskReport                   = "task_report"
	TypeTaskReportAck                = "task_report_ack"
	TypeTaskReconcile                = "task_reconcile_request"
	TypeContainerRebuildPlanRequest  = "container_rebuild_plan_request"
	TypeContainerRebuildPlanResponse = "container_rebuild_plan_response"

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
	TaskStart          TaskAction = "start"
	TaskStop           TaskAction = "stop"
	TaskRestart        TaskAction = "restart"
	TaskPause          TaskAction = "pause"
	TaskResume         TaskAction = "resume"
	TaskDelete         TaskAction = "delete"
	TaskRename         TaskAction = "rename"
	TaskRebuild        TaskAction = "rebuild"
	TaskRebuildCleanup TaskAction = "rebuild_cleanup"
)

// RebuildPortBinding is the only mutable part of a controlled container
// rebuild in this stage. Every other supported Docker setting is copied from
// a fresh Engine Inspect. An empty PortBindings slice means preserve existing
// bindings; ClearPortBindings explicitly requests no published host ports.
type RebuildPortBinding struct {
	ContainerPort string `json:"containerPort"`
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort,omitempty"`
}

type RebuildSpec struct {
	PortBindings      []RebuildPortBinding `json:"portBindings,omitempty"`
	ClearPortBindings bool                 `json:"clearPortBindings,omitempty"`
	CleanupTaskID     string               `json:"cleanupTaskId,omitempty"`
}

type ContainerRebuildPlanRequest struct {
	ContainerID string      `json:"containerId"`
	Spec        RebuildSpec `json:"spec"`
}

type ContainerRebuildPlanResponse struct {
	Plan      *ContainerRebuildPlan `json:"plan,omitempty"`
	ErrorCode string                `json:"errorCode,omitempty"`
}

type ContainerRebuildPlan struct {
	ContainerID       string                  `json:"containerId"`
	Name              string                  `json:"name"`
	ImageID           string                  `json:"imageId"`
	WasRunning        bool                    `json:"wasRunning"`
	WritableLayerSize int64                   `json:"writableLayerBytes"`
	SnapshotRequired  bool                    `json:"snapshotRequired"`
	PortsBefore       []string                `json:"portsBefore"`
	PortsAfter        []string                `json:"portsAfter"`
	Preserved         []string                `json:"preserved"`
	Changed           []string                `json:"changed"`
	Downtime          string                  `json:"downtime"`
	Risks             []string                `json:"risks"`
	Mounts            []ContainerRebuildMount `json:"mounts"`
}

type ContainerRebuildMount struct {
	Type        string `json:"type"`
	Destination string `json:"destination"`
	ReadWrite   bool   `json:"readWrite"`
	VolumeID    string `json:"volumeId,omitempty"`
}

type TaskIntent struct {
	Action          TaskAction   `json:"action"`
	ContainerID     string       `json:"container_id"`
	NewName         string       `json:"new_name,omitempty"`
	DeleteConfirmed bool         `json:"delete_confirmed,omitempty"`
	Rebuild         *RebuildSpec `json:"rebuild,omitempty"`
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
		if intent.NewName != "" || intent.DeleteConfirmed || intent.Rebuild != nil {
			return ErrInvalidTaskMessage
		}
	case TaskDelete:
		if intent.NewName != "" || !intent.DeleteConfirmed || intent.Rebuild != nil {
			return ErrInvalidTaskMessage
		}
	case TaskRename:
		if !containerName.MatchString(intent.NewName) || intent.DeleteConfirmed || intent.Rebuild != nil {
			return ErrInvalidTaskMessage
		}
	case TaskRebuild:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.Rebuild == nil || intent.Rebuild.CleanupTaskID != "" || intent.Rebuild.ClearPortBindings && len(intent.Rebuild.PortBindings) != 0 {
			return ErrInvalidTaskMessage
		}
		if len(intent.Rebuild.PortBindings) > 128 {
			return ErrInvalidTaskMessage
		}
		seen := make(map[string]struct{}, len(intent.Rebuild.PortBindings))
		for _, binding := range intent.Rebuild.PortBindings {
			if !validRebuildPortBinding(binding) {
				return ErrInvalidTaskMessage
			}
			key := binding.ContainerPort + "|" + binding.HostIP + "|" + binding.HostPort
			if _, exists := seen[key]; exists {
				return ErrInvalidTaskMessage
			}
			seen[key] = struct{}{}
		}
	case TaskRebuildCleanup:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.Rebuild == nil || intent.Rebuild.CleanupTaskID == "" || len(intent.Rebuild.CleanupTaskID) > 128 || intent.Rebuild.ClearPortBindings || len(intent.Rebuild.PortBindings) != 0 {
			return ErrInvalidTaskMessage
		}
		for _, r := range intent.Rebuild.CleanupTaskID {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
				return ErrInvalidTaskMessage
			}
		}
	default:
		return ErrInvalidTaskMessage
	}
	return nil
}

func ValidateContainerRebuildPlanRequest(envelope Envelope, request ContainerRebuildPlanRequest, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeContainerRebuildPlanRequest || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || envelope.RequestID == "" || len(envelope.RequestID) > 128 ||
		!IsFullContainerID(request.ContainerID) || len(envelope.Payload) > MaxTaskPayloadBytes {
		return ErrInvalidTaskMessage
	}
	return ValidateTaskIntent(TaskIntent{Action: TaskRebuild, ContainerID: request.ContainerID, Rebuild: &request.Spec})
}

func ValidateContainerRebuildPlanResponse(envelope Envelope, response ContainerRebuildPlanResponse, requestID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeContainerRebuildPlanResponse || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || envelope.RequestID != requestID || requestID == "" ||
		len(envelope.Payload) > MaxTaskPayloadBytes || (response.Plan == nil) == (response.ErrorCode == "") {
		return ErrInvalidTaskMessage
	}
	if response.ErrorCode != "" {
		if !safeTaskToken(response.ErrorCode, 64) {
			return ErrInvalidTaskMessage
		}
		return nil
	}
	plan := response.Plan
	if !IsFullContainerID(plan.ContainerID) || plan.Name == "" || len(plan.Name) > 255 || plan.ImageID == "" || len(plan.ImageID) > 256 ||
		plan.WritableLayerSize < 0 || len(plan.PortsBefore) > 4096 || len(plan.PortsAfter) > 4096 || len(plan.Preserved) > 64 || len(plan.Changed) > 64 || len(plan.Risks) > 64 || len(plan.Mounts) > 1024 ||
		plan.SnapshotRequired != (plan.WritableLayerSize > 0) || len(plan.Downtime) > 512 {
		return ErrInvalidTaskMessage
	}
	for _, mount := range plan.Mounts {
		if mount.Type == "" || len(mount.Type) > 32 || mount.Destination == "" || len(mount.Destination) > 4096 || len(mount.VolumeID) > 256 {
			return ErrInvalidTaskMessage
		}
	}
	for _, values := range [][]string{plan.PortsBefore, plan.PortsAfter, plan.Preserved, plan.Changed, plan.Risks} {
		for _, value := range values {
			if len(value) == 0 || len(value) > 512 || strings.ContainsAny(value, "\x00\r\n") {
				return ErrInvalidTaskMessage
			}
		}
	}
	return nil
}

func validRebuildPortBinding(binding RebuildPortBinding) bool {
	if len(binding.ContainerPort) > 16 || len(binding.HostIP) > 64 || len(binding.HostPort) > 5 || binding.ContainerPort == "" {
		return false
	}
	parts := strings.Split(binding.ContainerPort, "/")
	if len(parts) != 2 || (parts[1] != "tcp" && parts[1] != "udp") {
		return false
	}
	containerPort, err := strconv.Atoi(parts[0])
	if err != nil || containerPort < 1 || containerPort > 65535 {
		return false
	}
	if binding.HostPort != "" {
		hostPort, err := strconv.Atoi(binding.HostPort)
		if err != nil || hostPort < 1 || hostPort > 65535 {
			return false
		}
	}
	if binding.HostIP != "" {
		if _, err := netip.ParseAddr(binding.HostIP); err != nil {
			return false
		}
	}
	return true
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
	NodeID        string `json:"nodeId"`
	JournalID     string `json:"journalId"`
	Capacity      int    `json:"capacity"`
	CapacityLimit int    `json:"capacityLimit"`
}

type TaskJournalStatus struct {
	Accepted       bool   `json:"accepted"`
	JournalID      string `json:"journalId"`
	ReviewRequired bool   `json:"reviewRequired"`
	Capacity       int    `json:"capacity"`
	// SnapshotID and SnapshotAccepted form the durable Core acknowledgement
	// for one complete, reconciled Agent journal snapshot. They are omitted in
	// the initial journal acceptance response.
	SnapshotID       string `json:"snapshotId,omitempty"`
	SnapshotAccepted bool   `json:"snapshotAccepted,omitempty"`
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

func ValidateTaskJournalHello(envelope Envelope, hello TaskJournalHello, nodeID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskJournalHello || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || envelope.RequestID != "" || hello.NodeID != nodeID ||
		!validJournalID(hello.JournalID) || hello.Capacity < 0 || hello.Capacity > 4096 ||
		hello.CapacityLimit < 1 || hello.CapacityLimit > 4096 || hello.Capacity > hello.CapacityLimit {
		return ErrInvalidTaskMessage
	}
	return nil
}

func ValidateTaskJournalStatus(envelope Envelope, status TaskJournalStatus, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskJournalStatus || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || status.Capacity < 0 || status.Capacity > 4096 ||
		!validJournalID(status.JournalID) || status.ReviewRequired && status.Accepted ||
		status.SnapshotAccepted && (!status.Accepted || status.ReviewRequired || !validSnapshotID(status.SnapshotID)) ||
		!status.SnapshotAccepted && status.SnapshotID != "" {
		return ErrInvalidTaskMessage
	}
	if status.SnapshotAccepted && envelope.RequestID != status.SnapshotID || !status.SnapshotAccepted && envelope.RequestID != "" {
		return ErrInvalidTaskMessage
	}
	return nil
}

func ValidateTaskSnapshotRequest(envelope Envelope, request TaskSnapshotRequest, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskSnapshotRequest || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || envelope.RequestID != request.SnapshotID ||
		request.JournalID != journalID || !validJournalID(request.JournalID) || !validSnapshotID(request.SnapshotID) || nodeID == "" {
		return ErrInvalidTaskMessage
	}
	return nil
}

func ValidateTaskSnapshotPage(envelope Envelope, page TaskSnapshotPage, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskSnapshotPage || envelope.Generation != generation ||
		generation == 0 || envelope.RequestID != page.SnapshotID || envelope.Sequence != uint64(page.Page)+1 ||
		page.JournalID != journalID || !validJournalID(page.JournalID) || !validSnapshotID(page.SnapshotID) ||
		len(page.Reports) > TaskSnapshotPageSize {
		return ErrInvalidTaskMessage
	}
	return nil
}

func validJournalID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func validSnapshotID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
			return false
		}
	}
	return true
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
