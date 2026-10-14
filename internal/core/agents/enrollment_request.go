package agents

import "github.com/CST-Cat/NodeDance/internal/protocol"

// EnrollmentRequest is the shared Core API request used by Web, CLI and TUI.
type EnrollmentRequest struct {
	Asset            *protocol.NodeAsset `json:"asset,omitempty"`
	DisplayName      string              `json:"displayName"`
	CoreURL          string              `json:"coreUrl,omitempty"`
	DeploymentMethod string              `json:"deploymentMethod,omitempty"`
}

const (
	DeploymentNative        = "native"
	DeploymentDocker        = "docker"
	DefaultDeploymentMethod = DeploymentNative
)

func ValidDeploymentMethod(method string) bool {
	return method == "" || method == DeploymentNative || method == DeploymentDocker
}
