package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	CapabilityCompose       = "agent.compose.v1"
	CapabilityComposeEditor = "agent.compose-editor.v1"
	TypeComposeRequest      = "compose_request"
	TypeComposeResponse     = "compose_response"
	MaxComposeConfigFiles   = 16
	MaxComposeEnvFiles      = 16
	MaxComposeProfiles      = 64
)

var composeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var composeProfile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
var composeOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type ComposeAction string

const (
	ComposeList        ComposeAction = "list"
	ComposeValidate    ComposeAction = "validate"
	ComposeUp          ComposeAction = "up"
	ComposeStart       ComposeAction = "start"
	ComposeStop        ComposeAction = "stop"
	ComposeRestart     ComposeAction = "restart"
	ComposeDown        ComposeAction = "down"
	ComposeEditRead    ComposeAction = "edit_read"
	ComposeEditPreview ComposeAction = "edit_preview"
	ComposeEditApply   ComposeAction = "edit_apply"
	ComposeEditStatus  ComposeAction = "edit_status"

	MaxComposeEditFiles = 32
	MaxComposeEditBytes = 256 << 10
	MaxComposeDiffBytes = 256 << 10
)

// ComposeProjectRef is derived from Docker's standard Compose labels. Config
// files retain their original order; environment values and raw labels are
// never sent to Core.
type ComposeProjectRef struct {
	Key              string   `json:"key"`
	Name             string   `json:"name"`
	WorkingDirectory string   `json:"workingDirectory"`
	ConfigFiles      []string `json:"configFiles"`
}

type ComposeRequest struct {
	OperationID string              `json:"operationId"`
	Action      ComposeAction       `json:"action"`
	Project     ComposeProjectRef   `json:"project"`
	EnvFiles    []string            `json:"envFiles,omitempty"`
	Profiles    []string            `json:"profiles,omitempty"`
	Editor      *ComposeEditorInput `json:"editor,omitempty"`
}

// ComposeEditorInput carries one administrator-approved source snapshot to the
// Agent. Source text is never written into the Core operation ledger; the Core
// stores only a digest and status for these requests.
type ComposeEditorInput struct {
	TargetOperationID string              `json:"targetOperationId,omitempty"`
	Files             []ComposeSourceFile `json:"files,omitempty"`
	ExpectedVersions  map[string]string   `json:"expectedVersions,omitempty"`
	PortEdits         []ComposePortEdit   `json:"portEdits,omitempty"`
}

type ComposeSourceFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Version string `json:"version,omitempty"`
}

type ComposePortEdit struct {
	File         string `json:"file"`
	Service      string `json:"service"`
	Target       uint16 `json:"target"`
	Protocol     string `json:"protocol"`
	OldHostIP    string `json:"oldHostIP,omitempty"`
	OldPublished uint16 `json:"oldPublished,omitempty"`
	NewHostIP    string `json:"newHostIP,omitempty"`
	NewPublished uint16 `json:"newPublished"`
}

type ComposeEditorResult struct {
	Files             []ComposeSourceFile `json:"files,omitempty"`
	Diff              string              `json:"diff,omitempty"`
	ResolvedConfig    string              `json:"resolvedConfig,omitempty"`
	AffectedServices  []string            `json:"affectedServices,omitempty"`
	Impact            []string            `json:"impact,omitempty"`
	DataBackup        bool                `json:"dataBackup"`
	RollbackConfirmed bool                `json:"rollbackConfirmed,omitempty"`
}

type ComposeServiceInstance struct {
	ContainerID   string `json:"containerId"`
	ContainerName string `json:"containerName"`
	State         string `json:"state"`
	Health        string `json:"health"`
}

type ComposeService struct {
	Name      string                   `json:"name"`
	Instances []ComposeServiceInstance `json:"instances"`
}

type ComposeProject struct {
	Ref             ComposeProjectRef `json:"ref"`
	ConfigAvailable bool              `json:"configAvailable"`
	ConfigReason    string            `json:"configReason,omitempty"`
	DataStale       bool              `json:"dataStale,omitempty"`
	Services        []ComposeService  `json:"services"`
}

type ComposeResponse struct {
	OperationID string               `json:"operationId"`
	Status      string               `json:"status"`
	ErrorCode   string               `json:"errorCode,omitempty"`
	Verified    bool                 `json:"verified"`
	Projects    []ComposeProject     `json:"projects,omitempty"`
	Project     *ComposeProject      `json:"project,omitempty"`
	Editor      *ComposeEditorResult `json:"editor,omitempty"`
}

// ComposeProjectKey binds a project name to its working directory and ordered
// source list. Identical service/project names in different paths therefore
// cannot select each other's configuration.
func ComposeProjectKey(name, workingDirectory string, configFiles []string) string {
	parts := []string{name, filepath.Clean(workingDirectory)}
	for _, file := range configFiles {
		parts = append(parts, filepath.Clean(file))
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func ValidateComposeProjectRef(ref ComposeProjectRef) error {
	if !composeName.MatchString(ref.Name) || !filepath.IsAbs(ref.WorkingDirectory) || len(ref.ConfigFiles) == 0 || len(ref.ConfigFiles) > MaxComposeConfigFiles {
		return errors.New("invalid Compose project reference")
	}
	for _, file := range ref.ConfigFiles {
		if !filepath.IsAbs(file) || strings.TrimSpace(file) != file || strings.IndexByte(file, 0) >= 0 {
			return errors.New("invalid Compose config path")
		}
	}
	if ref.Key == "" || ref.Key != ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles) {
		return errors.New("Compose project identity does not match its source context")
	}
	return nil
}

func ValidateComposeRequest(request ComposeRequest) error {
	if !composeOperationID.MatchString(request.OperationID) || len(request.EnvFiles) > MaxComposeEnvFiles || len(request.Profiles) > MaxComposeProfiles {
		return errors.New("invalid Compose request")
	}
	switch request.Action {
	case ComposeList:
		if request.Project.Name != "" || request.Project.Key != "" || len(request.Project.ConfigFiles) != 0 || request.Project.WorkingDirectory != "" || len(request.EnvFiles) != 0 || len(request.Profiles) != 0 {
			return errors.New("list request cannot target a project")
		}
	case ComposeValidate, ComposeUp, ComposeStart, ComposeStop, ComposeRestart, ComposeDown,
		ComposeEditRead, ComposeEditPreview, ComposeEditApply, ComposeEditStatus:
		if ValidateComposeProjectRef(request.Project) != nil {
			return errors.New("invalid Compose project reference")
		}
	default:
		return errors.New("unsupported Compose action")
	}
	for _, file := range request.EnvFiles {
		if !filepath.IsAbs(file) || strings.TrimSpace(file) != file || strings.IndexByte(file, 0) >= 0 {
			return errors.New("invalid Compose environment file")
		}
	}
	for _, profile := range request.Profiles {
		if !composeProfile.MatchString(profile) {
			return errors.New("invalid Compose profile")
		}
	}
	if request.Action == ComposeEditRead || request.Action == ComposeEditPreview || request.Action == ComposeEditApply || request.Action == ComposeEditStatus {
		if request.Editor == nil || validateComposeEditorInput(request) != nil {
			return errors.New("invalid Compose editor input")
		}
	} else if request.Editor != nil {
		return errors.New("Compose editor input is only valid for editor actions")
	}
	return nil
}

func validateComposeEditorInput(request ComposeRequest) error {
	input := request.Editor
	if len(input.Files) > MaxComposeEditFiles || len(input.ExpectedVersions) > MaxComposeEditFiles || len(input.PortEdits) > 64 {
		return errors.New("Compose editor input exceeds limits")
	}
	allowed := make(map[string]struct{}, len(request.Project.ConfigFiles)+len(request.EnvFiles))
	for _, path := range request.Project.ConfigFiles {
		allowed[path] = struct{}{}
	}
	for _, path := range request.EnvFiles {
		allowed[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(input.Files))
	total := 0
	for _, file := range input.Files {
		if _, ok := allowed[file.Path]; !ok || !filepath.IsAbs(file.Path) || len(file.Content) > MaxComposeEditBytes {
			return errors.New("Compose editor source file is invalid or too large")
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return errors.New("duplicate Compose editor source file")
		}
		seen[file.Path] = struct{}{}
		total += len(file.Content)
		if total > MaxComposeEditBytes {
			return errors.New("Compose editor source exceeds total size limit")
		}
	}
	if request.Action == ComposeEditStatus {
		if !composeOperationID.MatchString(input.TargetOperationID) || len(input.Files) != 0 || len(input.ExpectedVersions) != 0 || len(input.PortEdits) != 0 {
			return errors.New("invalid Compose editor result query")
		}
		return nil
	}
	if input.TargetOperationID != "" {
		return errors.New("target operation ID is only valid for a status query")
	}
	if request.Action == ComposeEditRead && (len(input.Files) != 0 || len(input.ExpectedVersions) != 0 || len(input.PortEdits) != 0) {
		return errors.New("Compose editor read cannot include edits")
	}
	if request.Action == ComposeEditPreview || request.Action == ComposeEditApply {
		if len(input.Files) == 0 && len(input.PortEdits) == 0 {
			return errors.New("Compose editor change set is empty")
		}
		for path, version := range input.ExpectedVersions {
			if _, ok := allowed[path]; !ok || !filepath.IsAbs(path) || len(version) != 64 {
				return errors.New("invalid Compose source version")
			}
			if _, err := hex.DecodeString(version); err != nil {
				return errors.New("invalid Compose source version")
			}
		}
		for path := range allowed {
			if _, ok := input.ExpectedVersions[path]; !ok {
				return errors.New("source versions are required for every Compose context file")
			}
		}
	}
	for _, edit := range input.PortEdits {
		if _, ok := allowed[edit.File]; !ok || !containsPath(request.Project.ConfigFiles, edit.File) || edit.Service == "" || len(edit.Service) > 255 || edit.Target == 0 || (edit.Protocol != "tcp" && edit.Protocol != "udp") || edit.NewPublished == 0 {
			return errors.New("invalid structured Compose port edit")
		}
		if len(edit.OldHostIP) > 64 || len(edit.NewHostIP) > 64 {
			return errors.New("invalid Compose host IP")
		}
	}
	return nil
}

func containsPath(paths []string, path string) bool {
	for _, candidate := range paths {
		if candidate == path {
			return true
		}
	}
	return false
}

func ValidateComposeResponse(response ComposeResponse, request ComposeRequest) error {
	if response.OperationID == "" || response.OperationID != request.OperationID || len(response.Projects) > 4096 || len(response.ErrorCode) > 64 {
		return errors.New("invalid Compose response identity or bounds")
	}
	switch response.Status {
	case "succeeded":
		if !response.Verified || response.ErrorCode != "" {
			return errors.New("Compose success requires verified state")
		}
	case "failed", "unknown", "timed_out":
		if response.Verified || response.ErrorCode == "" || !composeErrorCode.MatchString(response.ErrorCode) {
			return errors.New("invalid Compose failure result")
		}
	default:
		return errors.New("invalid Compose response status")
	}
	if request.Action == ComposeList {
		if response.Project != nil {
			return errors.New("Compose list response contains a single project")
		}
	} else if len(response.Projects) != 0 {
		return errors.New("Compose operation response contains a project list")
	}
	if request.Action == ComposeEditRead || request.Action == ComposeEditPreview || request.Action == ComposeEditApply || request.Action == ComposeEditStatus {
		if response.Editor == nil || response.Project != nil {
			return errors.New("Compose editor response is missing editor result")
		}
		if len(response.Editor.Files) > MaxComposeEditFiles || len(response.Editor.Diff) > MaxComposeDiffBytes || len(response.Editor.ResolvedConfig) > MaxComposeEditBytes || len(response.Editor.AffectedServices) > 4096 || len(response.Editor.Impact) > 64 {
			return errors.New("Compose editor response exceeds bounds")
		}
	} else if response.Editor != nil {
		return errors.New("unexpected Compose editor response")
	}
	if response.Project != nil {
		if err := validateComposeProject(*response.Project); err != nil {
			return err
		}
	}
	for _, project := range response.Projects {
		if err := validateComposeProject(project); err != nil {
			return err
		}
	}
	return nil
}

func validateComposeProject(project ComposeProject) error {
	if err := ValidateComposeProjectRef(project.Ref); err != nil {
		return err
	}
	if project.ConfigReason != "" && project.ConfigReason != "config_missing" {
		return errors.New("invalid Compose configuration status")
	}
	if project.ConfigAvailable && project.ConfigReason != "" || !project.ConfigAvailable && project.ConfigReason != "config_missing" {
		return errors.New("inconsistent Compose configuration status")
	}
	if len(project.Services) > 4096 {
		return errors.New("too many Compose services")
	}
	services := make(map[string]struct{}, len(project.Services))
	for _, service := range project.Services {
		if service.Name == "" || len(service.Name) > 255 || len(service.Instances) > 1000 {
			return errors.New("invalid Compose service")
		}
		if _, exists := services[service.Name]; exists {
			return errors.New("duplicate Compose service")
		}
		services[service.Name] = struct{}{}
		instances := make(map[string]struct{}, len(service.Instances))
		for _, instance := range service.Instances {
			if !IsFullContainerID(instance.ContainerID) || instance.ContainerName == "" || len(instance.ContainerName) > 255 || len(instance.State) > 32 || len(instance.Health) > 32 {
				return errors.New("invalid Compose service instance")
			}
			if _, exists := instances[instance.ContainerID]; exists {
				return errors.New("duplicate Compose instance")
			}
			instances[instance.ContainerID] = struct{}{}
		}
	}
	return nil
}

var composeErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
