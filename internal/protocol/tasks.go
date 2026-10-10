package protocol

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
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
	TypeTaskCancelRequest            = "task_cancel_request"
	TypeContainerRebuildPlanRequest  = "container_rebuild_plan_request"
	TypeContainerRebuildPlanResponse = "container_rebuild_plan_response"

	TaskSnapshotPageSize     = 32
	MaxTaskSnapshotTasks     = 10000
	MaxTaskSnapshotBytes     = 4 << 20
	MaxTaskPayloadBytes      = 768 << 10
	MaxComposeFileBytes      = 128 << 10
	MaxContainerCreateBytes  = 64 << 10
	ContainerCreateTaskLabel = "io.nodedance.create.task_id"
	ContainerCreateHashLabel = "io.nodedance.create.request_sha256"
)

var (
	ErrInvalidTaskMessage = errors.New("invalid task bridge message")
	fullContainerID       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	containerName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	composeContentHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	containerCreateHash   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	environmentKey        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// TaskAction and TaskIntent are the single canonical safe request representation
// shared by Core and Agent. Never add secrets, command text, or arbitrary JSON.
type TaskAction string

const (
	TaskStart           TaskAction = "start"
	TaskStop            TaskAction = "stop"
	TaskRestart         TaskAction = "restart"
	TaskPause           TaskAction = "pause"
	TaskResume          TaskAction = "resume"
	TaskDelete          TaskAction = "delete"
	TaskRename          TaskAction = "rename"
	TaskImagePull       TaskAction = "image_pull"
	TaskImageDelete     TaskAction = "image_delete"
	TaskRebuild         TaskAction = "rebuild"
	TaskRebuildCleanup  TaskAction = "rebuild_cleanup"
	TaskComposeStart    TaskAction = "compose_start"
	TaskComposeStop     TaskAction = "compose_stop"
	TaskComposeRestart  TaskAction = "compose_restart"
	TaskComposeDeploy   TaskAction = "compose_deploy"
	TaskComposeSave     TaskAction = "compose_config_save"
	TaskFileMkdir       TaskAction = "file_mkdir"
	TaskFileRename      TaskAction = "file_rename"
	TaskFileDelete      TaskAction = "file_delete"
	TaskFileSaveText    TaskAction = "file_save_text"
	TaskFileUpload      TaskAction = "file_upload"
	TaskAgentDeploy     TaskAction = "agent_deploy"
	TaskContainerCreate TaskAction = "container_create"
)

// ContainerCreateSpec is delivered only once in TaskDispatch. It is excluded
// from TaskIntent and both durable task journals; only its digest is persisted.
type ContainerCreateSpec struct {
	Image          string                 `json:"image"`
	Name           string                 `json:"name"`
	Command        []string               `json:"command,omitempty"`
	Environment    []string               `json:"environment,omitempty"`
	Ports          []ContainerCreatePort  `json:"ports,omitempty"`
	Mounts         []ContainerCreateMount `json:"mounts,omitempty"`
	Network        string                 `json:"network,omitempty"`
	RestartPolicy  string                 `json:"restartPolicy"`
	RestartRetries int                    `json:"restartRetries,omitempty"`
}

type ContainerCreatePort struct {
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
	HostIP        string `json:"hostIp,omitempty"`
	HostPort      string `json:"hostPort,omitempty"`
}

type ContainerCreateMount struct {
	Type     string `json:"type"`
	Source   string `json:"source,omitempty"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// ClearContainerCreateSpec best-effort clears the in-memory, one-use task
// payload after its transport or execution lifetime ends.
func ClearContainerCreateSpec(spec *ContainerCreateSpec) {
	if spec == nil {
		return
	}
	for index := range spec.Command {
		spec.Command[index] = ""
	}
	for index := range spec.Environment {
		spec.Environment[index] = ""
	}
	for index := range spec.Mounts {
		spec.Mounts[index].Source = ""
		spec.Mounts[index].Target = ""
	}
	clear(spec.Command)
	clear(spec.Environment)
	clear(spec.Ports)
	clear(spec.Mounts)
	spec.Image, spec.Name, spec.Network, spec.RestartPolicy = "", "", "", ""
}

// ComposeTaskSpec identifies a project from Docker's Compose labels. File
// content is kept out of the durable task intent and is sent only once in the
// task dispatch when saving a file.
type ComposeTaskSpec struct {
	Project       ComposeProjectRef `json:"project"`
	FileIndex     int               `json:"fileIndex,omitempty"`
	BaseSHA256    string            `json:"baseSha256,omitempty"`
	ContentSHA256 string            `json:"contentSha256,omitempty"`
}

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

// FileTaskSpec contains only bounded metadata for one filesystem operation.
// File contents are carried once in TaskDispatch or streamed through the
// existing file channel; they are never written to either task journal.
type FileTaskSpec struct {
	Path            string `json:"path"`
	NewPath         string `json:"newPath,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	Size            int64  `json:"size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

// AgentDeployTaskSpec identifies a Tailscale peer for Core-local provisioning.
// SSH credentials and one-time enrollment tokens are deliberately excluded.
type AgentDeployTaskSpec struct {
	PeerIdentity          string `json:"peer_identity"`
	PeerName              string `json:"peer_name"`
	FileRoot              string `json:"file_root,omitempty"`
	DisableFileRoot       bool   `json:"disable_file_root,omitempty"`
	CoreURL               string `json:"core_url"`
	FallbackURL           string `json:"fallback_url,omitempty"`
	AllowFallback         bool   `json:"allow_fallback,omitempty"`
	HostFingerprint       string `json:"host_fingerprint"`
	ConfirmChangedHostKey bool   `json:"confirm_changed_host_key,omitempty"`
	SSHUser               string `json:"ssh_user"`
	Authentication        string `json:"authentication"`
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
	Action          TaskAction           `json:"action"`
	ContainerID     string               `json:"container_id"`
	NewName         string               `json:"new_name,omitempty"`
	DeleteConfirmed bool                 `json:"delete_confirmed,omitempty"`
	ImageReference  string               `json:"image_reference,omitempty"`
	ImageID         string               `json:"image_id,omitempty"`
	Rebuild         *RebuildSpec         `json:"rebuild,omitempty"`
	Compose         *ComposeTaskSpec     `json:"compose,omitempty"`
	File            *FileTaskSpec        `json:"file,omitempty"`
	AgentDeploy     *AgentDeployTaskSpec `json:"agent_deploy,omitempty"`
	CreateSHA256    string               `json:"create_sha256,omitempty"`
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
	if intent.Action != TaskContainerCreate && intent.CreateSHA256 != "" {
		return ErrInvalidTaskMessage
	}
	if intent.Action != TaskAgentDeploy && intent.AgentDeploy != nil {
		return ErrInvalidTaskMessage
	}
	if IsFileTaskAction(intent.Action) {
		if !validFileTaskSpec(intent) {
			return fmt.Errorf("%w: file target or operation metadata is invalid", ErrInvalidTaskMessage)
		}
	} else if intent.Action == TaskContainerCreate {
		if !validContainerCreateIntent(intent) {
			return fmt.Errorf("%w: container create target or request digest is invalid", ErrInvalidTaskMessage)
		}
	} else if intent.Action == TaskAgentDeploy {
		if !validAgentDeployTaskSpec(intent) {
			return fmt.Errorf("%w: Agent deployment target or metadata is invalid", ErrInvalidTaskMessage)
		}
	} else if !IsFullContainerID(intent.ContainerID) {
		return fmt.Errorf("%w: target must be a full stable Docker resource digest", ErrInvalidTaskMessage)
	}
	switch intent.Action {
	case TaskStart, TaskStop, TaskRestart, TaskPause, TaskResume:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil {
			return ErrInvalidTaskMessage
		}
	case TaskDelete:
		if intent.NewName != "" || !intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil {
			return ErrInvalidTaskMessage
		}
	case TaskRename:
		if !containerName.MatchString(intent.NewName) || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil {
			return ErrInvalidTaskMessage
		}
	case TaskImagePull:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil || !validImageReference(intent.ImageReference) ||
			intent.ContainerID != ImageTargetKey("pull:"+intent.ImageReference) {
			return ErrInvalidTaskMessage
		}
	case TaskImageDelete:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil || !validImageID(intent.ImageID) ||
			intent.ContainerID != ImageTargetKey("delete:"+intent.ImageID) {
			return ErrInvalidTaskMessage
		}
	case TaskRebuild:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Compose != nil || intent.File != nil || intent.Rebuild == nil || intent.Rebuild.CleanupTaskID != "" || intent.Rebuild.ClearPortBindings && len(intent.Rebuild.PortBindings) != 0 {
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
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Compose != nil || intent.File != nil || intent.Rebuild == nil || intent.Rebuild.CleanupTaskID == "" || len(intent.Rebuild.CleanupTaskID) > 128 || intent.Rebuild.ClearPortBindings || len(intent.Rebuild.PortBindings) != 0 {
			return ErrInvalidTaskMessage
		}
		for _, r := range intent.Rebuild.CleanupTaskID {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._~-", r)) {
				return ErrInvalidTaskMessage
			}
		}
	case TaskComposeStart, TaskComposeStop, TaskComposeRestart, TaskComposeDeploy:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.File != nil || !validComposeTaskSpec(intent, false) {
			return ErrInvalidTaskMessage
		}
	case TaskComposeSave:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.File != nil || !validComposeTaskSpec(intent, true) {
			return ErrInvalidTaskMessage
		}
	case TaskFileMkdir, TaskFileRename, TaskFileDelete, TaskFileSaveText, TaskFileUpload:
		if intent.NewName != "" || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil {
			return ErrInvalidTaskMessage
		}
		if (intent.Action == TaskFileDelete) != intent.DeleteConfirmed {
			return ErrInvalidTaskMessage
		}
	case TaskAgentDeploy:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil {
			return ErrInvalidTaskMessage
		}
	case TaskContainerCreate:
		if intent.NewName != "" || intent.DeleteConfirmed || intent.ImageReference != "" || intent.ImageID != "" || intent.Rebuild != nil || intent.Compose != nil || intent.File != nil || intent.AgentDeploy != nil {
			return ErrInvalidTaskMessage
		}
	default:
		return ErrInvalidTaskMessage
	}
	return nil
}

func validContainerCreateIntent(intent TaskIntent) bool {
	return containerCreateHash.MatchString(intent.CreateSHA256) && intent.ContainerID == ContainerCreateTargetKey(intent.CreateSHA256)
}

func ContainerCreateTargetKey(digest string) string { return "container-create:" + digest }

func ContainerCreateSpecDigest(spec ContainerCreateSpec) (string, error) {
	if err := ValidateContainerCreateSpec(spec); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("encode container create request: %w", err)
	}
	defer clear(encoded)
	canonical, err := taskstate.CanonicalJSON(encoded)
	defer clear(canonical)
	if err != nil || len(canonical) > MaxContainerCreateBytes {
		return "", ErrInvalidTaskMessage
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateContainerCreateSpec(spec ContainerCreateSpec) error {
	if !validImageReference(spec.Image) || !containerName.MatchString(spec.Name) || len(spec.Command) > 128 || len(spec.Environment) > 256 || len(spec.Ports) > 128 || len(spec.Mounts) > 64 ||
		(spec.Network != "" && spec.Network != "host" && !containerName.MatchString(spec.Network)) ||
		(spec.RestartPolicy != "no" && spec.RestartPolicy != "always" && spec.RestartPolicy != "unless-stopped" && spec.RestartPolicy != "on-failure") ||
		spec.RestartRetries < 0 || spec.RestartRetries > 1000 || (spec.RestartPolicy != "on-failure" && spec.RestartRetries != 0) {
		return ErrInvalidTaskMessage
	}
	total := 0
	for _, argument := range spec.Command {
		if len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
			return ErrInvalidTaskMessage
		}
		total += len(argument)
	}
	seenEnvironment := make(map[string]struct{}, len(spec.Environment))
	for _, entry := range spec.Environment {
		if len(entry) > 8192 || strings.ContainsRune(entry, '\x00') {
			return ErrInvalidTaskMessage
		}
		key, _, ok := strings.Cut(entry, "=")
		if !ok || !environmentKey.MatchString(key) {
			return ErrInvalidTaskMessage
		}
		if _, exists := seenEnvironment[key]; exists {
			return ErrInvalidTaskMessage
		}
		seenEnvironment[key] = struct{}{}
		total += len(entry)
	}
	seenPorts := make(map[string]struct{}, len(spec.Ports))
	for _, port := range spec.Ports {
		if port.ContainerPort < 1 || port.ContainerPort > 65535 || (port.Protocol != "tcp" && port.Protocol != "udp") || spec.Network == "host" && (port.HostIP != "" || port.HostPort != "") {
			return ErrInvalidTaskMessage
		}
		if port.HostIP != "" {
			address, err := netip.ParseAddr(port.HostIP)
			if err != nil || address.Zone() != "" {
				return ErrInvalidTaskMessage
			}
		}
		if port.HostPort != "" {
			value, err := strconv.Atoi(port.HostPort)
			if err != nil || value < 1 || value > 65535 || strconv.Itoa(value) != port.HostPort {
				return ErrInvalidTaskMessage
			}
		}
		key := fmt.Sprintf("%d/%s@%s:%s", port.ContainerPort, port.Protocol, port.HostIP, port.HostPort)
		if _, exists := seenPorts[key]; exists {
			return ErrInvalidTaskMessage
		}
		seenPorts[key] = struct{}{}
	}
	seenTargets := make(map[string]struct{}, len(spec.Mounts))
	for _, mount := range spec.Mounts {
		if mount.Target == "" || len(mount.Target) > 4096 || !strings.HasPrefix(mount.Target, "/") || path.Clean(mount.Target) != mount.Target || mount.Target == "/" || strings.ContainsRune(mount.Target, '\x00') {
			return ErrInvalidTaskMessage
		}
		if _, exists := seenTargets[mount.Target]; exists {
			return ErrInvalidTaskMessage
		}
		seenTargets[mount.Target] = struct{}{}
		switch mount.Type {
		case "bind":
			if mount.Source == "" || len(mount.Source) > 4096 || !strings.HasPrefix(mount.Source, "/") || path.Clean(mount.Source) != mount.Source || strings.ContainsRune(mount.Source, '\x00') {
				return ErrInvalidTaskMessage
			}
		case "volume":
			if mount.Source != "" && !containerName.MatchString(mount.Source) {
				return ErrInvalidTaskMessage
			}
		default:
			return ErrInvalidTaskMessage
		}
		total += len(mount.Source) + len(mount.Target)
	}
	if total > MaxContainerCreateBytes {
		return ErrInvalidTaskMessage
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return ErrInvalidTaskMessage
	}
	defer clear(encoded)
	if len(encoded) > MaxContainerCreateBytes {
		return ErrInvalidTaskMessage
	}
	return nil
}

func validAgentDeployTaskSpec(intent TaskIntent) bool {
	if intent.AgentDeploy == nil || intent.AgentDeploy.PeerIdentity == "" || len(intent.AgentDeploy.PeerIdentity) > 200 ||
		strings.TrimSpace(intent.AgentDeploy.PeerIdentity) != intent.AgentDeploy.PeerIdentity || intent.AgentDeploy.PeerName == "" || len([]rune(intent.AgentDeploy.PeerName)) > 80 ||
		strings.TrimSpace(intent.AgentDeploy.PeerName) != intent.AgentDeploy.PeerName || len(intent.AgentDeploy.FileRoot) > 4096 ||
		(intent.AgentDeploy.DisableFileRoot && intent.AgentDeploy.FileRoot != "") || intent.AgentDeploy.CoreURL == "" || len(intent.AgentDeploy.CoreURL) > 2048 ||
		len(intent.AgentDeploy.FallbackURL) > 2048 || intent.AgentDeploy.HostFingerprint == "" || len(intent.AgentDeploy.HostFingerprint) > 256 ||
		intent.AgentDeploy.SSHUser == "" || len(intent.AgentDeploy.SSHUser) > 128 ||
		(intent.AgentDeploy.Authentication != "password" && intent.AgentDeploy.Authentication != "private_key") ||
		intent.ContainerID != "tailscale-peer:"+intent.AgentDeploy.PeerIdentity {
		return false
	}
	for _, value := range []string{intent.AgentDeploy.PeerIdentity, intent.AgentDeploy.CoreURL, intent.AgentDeploy.FallbackURL, intent.AgentDeploy.HostFingerprint, intent.AgentDeploy.SSHUser} {
		for _, r := range value {
			if r < 0x21 || r == 0x7f {
				return false
			}
		}
	}
	for _, value := range []string{intent.AgentDeploy.PeerName, intent.AgentDeploy.FileRoot} {
		for _, r := range value {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

func validFileTaskSpec(intent TaskIntent) bool {
	if intent.File == nil || !ValidVirtualFilePath(intent.File.Path) || intent.ContainerID != FileTargetKey(intent.File.Path) {
		return false
	}
	file := intent.File
	validVersion := func(value string) bool {
		return len(value) <= 128 && !strings.ContainsAny(value, "\x00\r\n")
	}
	switch intent.Action {
	case TaskFileMkdir, TaskFileDelete:
		return file.Path != "/" && file.NewPath == "" && file.ExpectedVersion == "" && file.Size == 0 && file.SHA256 == ""
	case TaskFileRename:
		return ValidVirtualFilePath(file.NewPath) && file.Path != "/" && file.NewPath != "/" && file.NewPath != file.Path &&
			file.ExpectedVersion == "" && file.Size == 0 && file.SHA256 == ""
	case TaskFileSaveText:
		return file.Path != "/" && file.NewPath == "" && validVersion(file.ExpectedVersion) && file.Size >= 0 && file.Size <= MaxTextFileBytes && validFileDigest(file.SHA256)
	case TaskFileUpload:
		return file.Path != "/" && file.NewPath == "" && validVersion(file.ExpectedVersion) && file.Size >= 0 && file.Size <= MaxFileSize && validFileDigest(file.SHA256)
	default:
		return false
	}
}

func ValidVirtualFilePath(value string) bool {
	if value == "" || len(value) > 4096 || value[0] != '/' || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(value) == value
}

func FileTargetKey(path string) string { return ImageTargetKey("file:" + path) }

func IsFileTaskAction(action TaskAction) bool {
	switch action {
	case TaskFileMkdir, TaskFileRename, TaskFileDelete, TaskFileSaveText, TaskFileUpload:
		return true
	default:
		return false
	}
}

func validComposeTaskSpec(intent TaskIntent, save bool) bool {
	if intent.Compose == nil || intent.Compose.Project.Key != intent.ContainerID || ValidateComposeProjectRef(intent.Compose.Project) != nil ||
		intent.Compose.FileIndex < 0 || intent.Compose.FileIndex >= len(intent.Compose.Project.ConfigFiles) {
		return false
	}
	if save {
		return composeContentHash.MatchString(intent.Compose.ContentSHA256) && composeContentHash.MatchString(intent.Compose.BaseSHA256)
	}
	return intent.Compose.FileIndex == 0 && intent.Compose.ContentSHA256 == "" && intent.Compose.BaseSHA256 == ""
}

// ImageTargetKey maps an image request to the fixed-width resource identifier
// used by the durable task ledger. The original reference or image ID remains
// a separate typed field; registry credentials are never part of this value.
func ImageTargetKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validImageReference(value string) bool {
	if len(value) == 0 || len(value) > 512 || strings.TrimSpace(value) != value || strings.ContainsAny(value, " \t\r\n") ||
		strings.Contains(value, "@sha256:") && len(value) < 72 {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
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
	resourceKey := "docker-container:" + intent.ContainerID
	if intent.Action == TaskImagePull || intent.Action == TaskImageDelete {
		resourceKey = "docker-image:" + intent.ContainerID
	} else if IsComposeTaskAction(intent.Action) {
		resourceKey = "docker-compose-project:" + intent.ContainerID
	} else if IsFileTaskAction(intent.Action) {
		resourceKey = "filesystem-path:" + intent.ContainerID
	} else if intent.Action == TaskAgentDeploy {
		resourceKey = "agent-deployment:" + intent.AgentDeploy.PeerIdentity
	} else if intent.Action == TaskContainerCreate {
		resourceKey = "docker-container-create:" + intent.CreateSHA256
	}
	return taskstate.Identity{
		TaskID: taskID, NodeID: nodeID, IdempotencyKey: idempotencyKey,
		TargetID: intent.ContainerID, ResourceKey: resourceKey,
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

func IsComposeTaskAction(action TaskAction) bool {
	switch action {
	case TaskComposeStart, TaskComposeStop, TaskComposeRestart, TaskComposeDeploy, TaskComposeSave:
		return true
	default:
		return false
	}
}

func mustDecodeSHA256(value string) [sha256.Size]byte {
	var digest [sha256.Size]byte
	raw, err := hex.DecodeString(value)
	if err == nil && len(raw) == len(digest) {
		copy(digest[:], raw)
	}
	return digest
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
	// RegistryAuth is a one-delivery credential, outside TaskIntent and its
	// digest, so Core and Agent task journals cannot persist it.
	RegistryAuth *RegistryCredentials `json:"registryAuth,omitempty"`
	// ComposeContent is a one-delivery config-save body; only its SHA-256 is
	// persisted in TaskIntent and Agent journal records.
	ComposeContent *ComposeContent `json:"composeContent,omitempty"`
	// FileContent is a one-delivery UTF-8 text body. Uploads use the existing
	// bounded file chunk channel and never place file bytes in a task journal.
	FileContent *FileContent `json:"fileContent,omitempty"`
	// ContainerCreate is a single-delivery specification. Only its digest is
	// persisted in the task intent and Agent journal.
	ContainerCreate *ContainerCreateSpec `json:"containerCreate,omitempty"`
}

type ComposeContent struct {
	SHA256  string `json:"sha256"`
	Content []byte `json:"content"`
}

type FileContent struct {
	SHA256  string `json:"sha256"`
	Content []byte `json:"content"`
}

type RegistryCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c RegistryCredentials) Valid() bool {
	return len(c.Username) > 0 && len(c.Username) <= 256 && len(c.Password) > 0 && len(c.Password) <= 4096 &&
		validCredentialText(c.Username) && validCredentialText(c.Password)
}

func validCredentialText(value string) bool {
	for _, r := range value {
		if r == 0 || r == '\r' || r == '\n' || r == 0x7f {
			return false
		}
	}
	return true
}

type TaskReconcileRequest struct {
	TaskID         string     `json:"taskId"`
	NodeID         string     `json:"nodeId"`
	JournalID      string     `json:"journalId"`
	TargetID       string     `json:"targetId"`
	IdempotencyKey string     `json:"idempotencyKey"`
	RequestDigest  string     `json:"requestDigest"`
	Intent         TaskIntent `json:"intent"`
}

// TaskCancelRequest only interrupts an active image pull. A canceled state is
// still reported by the Agent after the Engine call stops and the requested
// image state has been inspected.
type TaskCancelRequest struct {
	TaskID    string `json:"taskId"`
	JournalID string `json:"journalId"`
}

func ValidateTaskCancelRequest(envelope Envelope, request TaskCancelRequest, nodeID, journalID string, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Type != TypeTaskCancelRequest || envelope.Generation != generation ||
		generation == 0 || envelope.Sequence != 0 || envelope.RequestID == "" || envelope.RequestID != request.TaskID ||
		!validNodeIdentity(request.TaskID) || request.JournalID != journalID ||
		!validNodeIdentity(nodeID) || !validJournalIdentity(journalID) {
		return ErrInvalidTaskMessage
	}
	return nil
}

func validNodeIdentity(value string) bool {
	if value == "" || len(value) > taskstate.MaxIdentityBytes {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validJournalIdentity(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
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
		len(envelope.Payload) > MaxTaskPayloadBytes || dispatch.NodeID != nodeID || dispatch.JournalID != journalID || dispatch.TargetID != dispatch.Intent.ContainerID ||
		!validTaskIdentityFields(dispatch.TaskID, dispatch.NodeID, dispatch.JournalID, dispatch.TargetID, dispatch.IdempotencyKey) {
		return ErrInvalidTaskMessage
	}
	if dispatch.RegistryAuth != nil && (dispatch.Intent.Action != TaskImagePull || !dispatch.RegistryAuth.Valid()) {
		return fmt.Errorf("%w: registry credentials are invalid for this dispatch", ErrInvalidTaskMessage)
	}
	if dispatch.ComposeContent != nil {
		if dispatch.Intent.Action != TaskComposeSave || dispatch.Intent.Compose == nil ||
			dispatch.ComposeContent.SHA256 != dispatch.Intent.Compose.ContentSHA256 ||
			len(dispatch.ComposeContent.Content) > MaxComposeFileBytes || sha256.Sum256(dispatch.ComposeContent.Content) != mustDecodeSHA256(dispatch.ComposeContent.SHA256) {
			return fmt.Errorf("%w: Compose payload is invalid for this dispatch", ErrInvalidTaskMessage)
		}
	} else if dispatch.Intent.Action == TaskComposeSave {
		// The Core keeps file bodies only in memory. If that one-use payload was
		// lost, the Agent will persist a failed result without changing the file.
	}
	if dispatch.FileContent != nil {
		if dispatch.Intent.Action != TaskFileSaveText || dispatch.Intent.File == nil ||
			dispatch.FileContent.SHA256 != dispatch.Intent.File.SHA256 || len(dispatch.FileContent.Content) > MaxTextFileBytes ||
			sha256.Sum256(dispatch.FileContent.Content) != mustDecodeSHA256(dispatch.FileContent.SHA256) {
			return fmt.Errorf("%w: file text payload is invalid for this dispatch", ErrInvalidTaskMessage)
		}
	} else if dispatch.Intent.Action != TaskFileSaveText && IsFileTaskAction(dispatch.Intent.Action) {
		// Only the text editor uses a bounded one-delivery body. Other file
		// mutations contain all of their required metadata in the intent.
	} else if dispatch.Intent.Action == TaskFileSaveText {
		// Missing one-use content is handled as a failed task by the Agent.
	}
	if dispatch.ContainerCreate != nil {
		if dispatch.Intent.Action != TaskContainerCreate {
			return fmt.Errorf("%w: container create payload is invalid for this dispatch", ErrInvalidTaskMessage)
		}
		createDigest, err := ContainerCreateSpecDigest(*dispatch.ContainerCreate)
		if err != nil || createDigest != dispatch.Intent.CreateSHA256 {
			return fmt.Errorf("%w: container create payload digest does not match the task", ErrInvalidTaskMessage)
		}
	} else if dispatch.Intent.Action == TaskContainerCreate {
		// The Core keeps this specification only in memory. The Agent records a
		// failed task if the one-use payload is absent.
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
		request.NodeID != nodeID || request.JournalID != journalID || request.TargetID != request.Intent.ContainerID ||
		!validTaskIdentityFields(request.TaskID, request.NodeID, request.JournalID, request.TargetID, request.IdempotencyKey) {
		return ErrInvalidTaskMessage
	}
	digest, err := ParseDigest(request.RequestDigest)
	if err != nil {
		return err
	}
	computed, err := TaskRequestDigest(request.TaskID, request.NodeID, request.IdempotencyKey, request.Intent)
	if err != nil || subtle.ConstantTimeCompare(digest[:], computed[:]) != 1 {
		return ErrInvalidTaskMessage
	}
	return nil
}

func validTaskIdentityFields(taskID, nodeID, journalID, targetID, key string) bool {
	if taskID == "" || len(taskID) > taskstate.MaxIdentityBytes || nodeID == "" || len(nodeID) > taskstate.MaxIdentityBytes ||
		journalID == "" || len(journalID) != 64 || !(IsFullContainerID(targetID) || validContainerCreateTarget(targetID)) || key == "" || len(key) > taskstate.MaxIdempotencyKeyBytes {
		return false
	}
	decoded, err := hex.DecodeString(journalID)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == journalID
}

func validContainerCreateTarget(targetID string) bool {
	const prefix = "container-create:"
	return strings.HasPrefix(targetID, prefix) && containerCreateHash.MatchString(strings.TrimPrefix(targetID, prefix))
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
