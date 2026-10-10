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
	CapabilityCompose     = "agent.compose.v1"
	TypeComposeRequest    = "compose_request"
	TypeComposeResponse   = "compose_response"
	MaxComposeConfigFiles = 16
)

var composeName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var composeOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

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
	OperationID string             `json:"operationId"`
	Action      string             `json:"action,omitempty"`
	Project     *ComposeProjectRef `json:"project,omitempty"`
	FileIndex   int                `json:"fileIndex,omitempty"`
	Content     string             `json:"content,omitempty"`
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
	OperationID   string           `json:"operationId"`
	Status        string           `json:"status"`
	ErrorCode     string           `json:"errorCode,omitempty"`
	Verified      bool             `json:"verified"`
	Projects      []ComposeProject `json:"projects,omitempty"`
	Content       string           `json:"content,omitempty"`
	ContentSHA256 string           `json:"contentSha256,omitempty"`
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
	if !composeName.MatchString(ref.Name) || !filepath.IsAbs(ref.WorkingDirectory) || filepath.Clean(ref.WorkingDirectory) != ref.WorkingDirectory ||
		strings.TrimSpace(ref.WorkingDirectory) != ref.WorkingDirectory || strings.IndexByte(ref.WorkingDirectory, 0) >= 0 ||
		len(ref.ConfigFiles) == 0 || len(ref.ConfigFiles) > MaxComposeConfigFiles {
		return errors.New("invalid Compose project reference")
	}
	for _, file := range ref.ConfigFiles {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file || strings.TrimSpace(file) != file || strings.IndexByte(file, 0) >= 0 {
			return errors.New("invalid Compose config path")
		}
	}
	if ref.Key == "" || ref.Key != ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles) {
		return errors.New("Compose project identity does not match its source context")
	}
	return nil
}

func ValidateComposeRequest(request ComposeRequest) error {
	if !composeOperationID.MatchString(request.OperationID) {
		return errors.New("invalid Compose request")
	}
	action := request.Action
	if action == "" {
		action = "inventory"
	}
	switch action {
	case "inventory":
		if request.Project != nil || request.FileIndex != 0 || request.Content != "" {
			return errors.New("inventory request has unexpected fields")
		}
	case "config_read":
		if !validComposeEditorRequest(request) || request.Content != "" {
			return errors.New("invalid Compose config read request")
		}
	case "config_validate":
		if !validComposeEditorRequest(request) || len(request.Content) > MaxComposeFileBytes {
			return errors.New("invalid Compose config validation request")
		}
	default:
		return errors.New("unsupported Compose request action")
	}
	return nil
}

func validComposeEditorRequest(request ComposeRequest) bool {
	return request.Project != nil && ValidateComposeProjectRef(*request.Project) == nil &&
		request.FileIndex >= 0 && request.FileIndex < len(request.Project.ConfigFiles)
}

func ValidateComposeResponse(response ComposeResponse, request ComposeRequest) error {
	if response.OperationID == "" || response.OperationID != request.OperationID || len(response.Projects) > 4096 || len(response.ErrorCode) > 64 ||
		len(response.Content) > MaxComposeFileBytes || len(response.ContentSHA256) > 64 {
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
	for _, project := range response.Projects {
		if err := validateComposeProject(project); err != nil {
			return err
		}
	}
	action := request.Action
	if action == "" {
		action = "inventory"
	}
	switch action {
	case "inventory":
		if response.Content != "" || response.ContentSHA256 != "" {
			return errors.New("inventory response contains config data")
		}
	case "config_read":
		if len(response.Projects) != 0 || response.Status == "succeeded" && !composeContentHash.MatchString(response.ContentSHA256) {
			return errors.New("invalid Compose config read result")
		}
	case "config_validate":
		if len(response.Projects) != 0 || response.Content != "" || response.ContentSHA256 != "" {
			return errors.New("invalid Compose config validation result")
		}
	}
	if response.Content != "" || response.ContentSHA256 != "" {
		if request.Action != "config_read" || response.Status != "succeeded" || !composeContentHash.MatchString(response.ContentSHA256) {
			return errors.New("invalid Compose config response")
		}
		digest := sha256.Sum256([]byte(response.Content))
		if hex.EncodeToString(digest[:]) != response.ContentSHA256 {
			return errors.New("Compose config response digest mismatch")
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
