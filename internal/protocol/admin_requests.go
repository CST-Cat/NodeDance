package protocol

// Administrator request DTOs are shared by Core decoders and the published
// operation contract. Request presence is explicit, independent of JSON output.

type ContainerActionRequest struct {
	Action               TaskAction   `json:"action" request:"required"`
	NewName              string       `json:"newName,omitempty"`
	DeleteConfirmed      bool         `json:"deleteConfirmed,omitempty"`
	DeleteConfirmationID string       `json:"deleteConfirmationId,omitempty"`
	Rebuild              *RebuildSpec `json:"rebuild,omitempty"`
	ConfirmationID       string       `json:"confirmationId,omitempty"`
}

type ComposeActionRequest struct {
	DeleteConfirmationKey string `json:"deleteConfirmationKey,omitempty"`
	Action                string `json:"action" request:"required"`
	FileIndex             int    `json:"fileIndex,omitempty"`
	Content               string `json:"content,omitempty"`
	BaseSHA256            string `json:"baseSha256,omitempty"`
}

type ComposeValidateRequest struct {
	Content string `json:"content" request:"required"`
}

type ComposeCreateRequest struct {
	Name             string `json:"name" request:"required"`
	WorkingDirectory string `json:"workingDirectory" request:"required"`
	Content          string `json:"content" request:"required"`
}

type FileActionRequest struct {
	Format           string   `json:"format,omitempty"`
	Paths            []string `json:"paths,omitempty"`
	Action           string   `json:"action" request:"required"`
	Path             string   `json:"path" request:"required"`
	NewPath          string   `json:"newPath,omitempty"`
	ExpectedVersion  string   `json:"expectedVersion,omitempty"`
	DeleteConfirmed  bool     `json:"deleteConfirmed,omitempty"`
	ConfirmationPath string   `json:"confirmationPath,omitempty"`
	Content          string   `json:"content,omitempty"`
}

type ImagePullRequest struct {
	ImageReference string `json:"imageReference" request:"required"`
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
}

type ImageDeleteRequest struct {
	DeleteConfirmed bool   `json:"deleteConfirmed" request:"required"`
	ConfirmationID  string `json:"confirmationId" request:"required"`
}

type TerminalCreateRequest struct {
	TargetKind  string `json:"targetKind" request:"required"`
	ContainerID string `json:"containerId,omitempty"`
}

type ProbeConfigRequest struct {
	NodeID             string `json:"nodeId" request:"required"`
	Name               string `json:"name" request:"required"`
	Kind               string `json:"kind" request:"required"`
	Target             string `json:"target" request:"required"`
	ExpectedHTTPStatus *int   `json:"expectedHttpStatus,omitempty"`
	IntervalSeconds    int    `json:"intervalSeconds" request:"required"`
	TimeoutSeconds     int    `json:"timeoutSeconds" request:"required"`
	Enabled            *bool  `json:"enabled,omitempty"`
	Revision           int64  `json:"revision,omitempty"`
}

type AuthSetupRequest struct {
	Credential  string `json:"credential" request:"required"`
	Password    string `json:"password,omitempty"`
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}
type AuthLoginRequest struct {
	Username string `json:"username" request:"required"`
	Password string `json:"password" request:"required"`
}
type PasswordChangeRequest struct {
	CurrentPassword string `json:"currentPassword" request:"required"`
	NewPassword     string `json:"newPassword" request:"required"`
}
type AppearanceRequest struct {
	DisplayName     string `json:"displayName" request:"required"`
	Theme           string `json:"theme" request:"required"`
	BackgroundColor string `json:"backgroundColor" request:"required"`
}
type RebuildPlanRequest struct {
	Spec RebuildSpec `json:"spec" request:"required"`
}
type HostKeyRequest struct {
	PeerIdentity string `json:"peerIdentity" request:"required"`
}
type ResolveDeploymentRequest struct {
	Outcome       string `json:"outcome" request:"required"`
	ObservedState string `json:"observedState" request:"required"`
	Confirmed     bool   `json:"confirmed" request:"required"`
}
