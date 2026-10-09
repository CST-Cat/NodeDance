package tailscale

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type CreateEnrollment func(context.Context, string) (Enrollment, error)
type WaitForAgent func(context.Context, string) error
type WaitForDocker func(context.Context, string) (bool, error)

type Dependencies struct {
	Discovery  Discovery
	Artifacts  ArtifactLoader
	Transport  SSHTransport
	Create     CreateEnrollment
	WaitOnline WaitForAgent
	WaitDocker WaitForDocker
}

type DeployRequest struct {
	PeerIdentity      string         `json:"peerIdentity"`
	DisplayName       string         `json:"displayName"`
	CoreURL           string         `json:"coreUrl"`
	FallbackURL       string         `json:"fallbackUrl,omitempty"`
	AllowFallback     bool           `json:"allowFallback"`
	HostFingerprint   string         `json:"hostFingerprint"`
	ConfirmHostKey    bool           `json:"confirmHostKey"`
	ConfirmChangedKey bool           `json:"confirmChangedHostKey"`
	Credentials       SSHCredentials `json:"credentials"`
}

type DeploymentResult struct {
	NodeID       string `json:"nodeId,omitempty"`
	PeerIdentity string `json:"peerIdentity"`
	DockerState  string `json:"dockerState"`
}

type Service struct {
	State *StateStore
	Dependencies
}

func (s *Service) ListPeers(ctx context.Context) ([]Peer, error) {
	if s.State == nil {
		return nil, errors.New("Tailscale deployment state is not initialized")
	}
	peers, err := s.Discovery.List(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.State.Read()
	if err != nil {
		return nil, err
	}
	for index := range peers {
		if nodeID := state.Managed[peers[index].Identity]; nodeID != "" {
			peers[index].Managed = true
			peers[index].NodeID = nodeID
		}
	}
	return peers, nil
}

func (s *Service) Probe(ctx context.Context, identity string) (string, error) {
	peer, err := s.findPeer(ctx, identity)
	if err != nil {
		return "", err
	}
	transport := s.Transport
	if transport == nil {
		transport = RealSSHTransport{}
	}
	return transport.Probe(ctx, peer)
}

func (s *Service) findPeer(ctx context.Context, identity string) (Peer, error) {
	if strings.TrimSpace(identity) == "" || len(identity) > 512 {
		return Peer{}, errors.New("Tailscale peer identity is invalid")
	}
	peers, err := s.Discovery.List(ctx)
	if err != nil {
		return Peer{}, err
	}
	for _, peer := range peers {
		if peer.Identity == identity {
			return peer, nil
		}
	}
	return Peer{}, errors.New("selected Tailscale peer is no longer visible; refresh discovery")
}

func (s *Service) Deploy(ctx context.Context, request DeployRequest) (DeploymentResult, error) {
	if s.State == nil {
		return DeploymentResult{}, errors.New("Tailscale deployment state is not initialized")
	}
	if s.Artifacts == nil {
		return DeploymentResult{}, errors.New("signed Agent artifacts are not configured on this Core")
	}
	if !request.ConfirmHostKey {
		return DeploymentResult{}, errors.New("confirm that the SSH host-key fingerprint matches the selected node")
	}
	name := strings.TrimSpace(request.DisplayName)
	if name == "" || len([]rune(name)) > 80 {
		return DeploymentResult{}, errors.New("node display name must contain 1 to 80 characters")
	}
	peer, err := s.findPeer(ctx, request.PeerIdentity)
	if err != nil {
		return DeploymentResult{}, err
	}
	if _, err := firstOnlineIP(peer); err != nil {
		return DeploymentResult{}, err
	}
	transport := s.Transport
	if transport == nil {
		transport = RealSSHTransport{}
	}
	remote, err := transport.Connect(ctx, peer, request.Credentials, request.HostFingerprint, s.State, request.ConfirmChangedKey)
	if err != nil {
		return DeploymentResult{}, err
	}
	defer remote.Close()
	preflight, err := InspectRemote(ctx, remote)
	if err != nil {
		return DeploymentResult{}, err
	}
	if preflight.Docker.Present && preflight.Docker.Access != "group" && preflight.Docker.Access != "world" && preflight.Docker.Access != "owner" {
		return DeploymentResult{}, errors.New("Docker socket is present but its current permissions cannot safely grant the Agent service user access; deployment was stopped before enrollment or service start")
	}
	artifact, err := s.Artifacts.Load(preflight.OS, preflight.Arch)
	if err != nil {
		return DeploymentResult{}, err
	}
	selectedURL, err := SelectReachableCoreURL(ctx, remote, request.CoreURL, request.FallbackURL, request.AllowFallback)
	if err != nil {
		return DeploymentResult{}, err
	}
	enrollment := Enrollment{}
	if preflight.ExistingConfig {
		if preflight.NodeID == "" {
			return DeploymentResult{}, errors.New("an existing Agent config was found but its NodeDance identity could not be read; refusing to replace or re-enroll it")
		}
		enrollment.NodeID = preflight.NodeID
	} else {
		if s.Create == nil {
			return DeploymentResult{}, errors.New("Agent enrollment service is unavailable")
		}
		enrollment, err = s.Create(ctx, name)
		if err != nil {
			return DeploymentResult{}, fmt.Errorf("create one-time Agent enrollment: %w", err)
		}
	}
	if err := InstallRemote(ctx, remote, preflight, artifact, selectedURL, enrollment); err != nil {
		return DeploymentResult{}, err
	}
	if s.WaitOnline != nil {
		waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.WaitOnline(waitCtx, enrollment.NodeID)
		cancel()
		if err != nil {
			return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity}, fmt.Errorf("%w: Agent installation completed but a live Core lease was not confirmed; inspect systemd and Agent status before retrying: %v", ErrOutcomeUnknown, err)
		}
	}
	dockerState := "absent"
	if preflight.Docker.Present {
		dockerState = "configured_unverified"
		if s.WaitDocker != nil {
			waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			available, dockerErr := s.WaitDocker(waitCtx, enrollment.NodeID)
			cancel()
			if dockerErr != nil {
				return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity, DockerState: "unknown"}, fmt.Errorf("%w: Agent online, but Core did not receive a confirmed Docker Engine state: %v", ErrOutcomeUnknown, dockerErr)
			}
			if available {
				dockerState = "connected"
			} else {
				dockerState = "unavailable"
			}
		}
	}
	if err := s.State.Associate(peer.Identity, enrollment.NodeID); err != nil {
		return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity, DockerState: dockerState}, fmt.Errorf("Agent is online, but the discovered-peer association could not be persisted: %w", err)
	}
	return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity, DockerState: dockerState}, nil
}

func newTaskID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}

type DeploymentTask struct {
	ID           string     `json:"taskId"`
	PeerIdentity string     `json:"peerIdentity"`
	PeerName     string     `json:"peerName"`
	Status       string     `json:"status"`
	Phase        string     `json:"phase"`
	Message      string     `json:"message,omitempty"`
	NodeID       string     `json:"nodeId,omitempty"`
	DockerState  string     `json:"dockerState,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// Coordinator is process-local for live SSH work and persists only task
// metadata, host-key pins, and peer-to-node associations. Credentials are
// captured only by the active goroutine and are never serialized.
type Coordinator struct {
	mu          sync.Mutex
	service     *Service
	state       *StateStore
	tasks       map[string]DeploymentTask
	activePeers map[string]bool
}

var coordinatorRegistry sync.Map

func SharedCoordinator(dataDir string, service *Service) (*Coordinator, error) {
	if dataDir == "" || service == nil {
		return nil, errors.New("deployment manager requires a data directory and service")
	}
	if existing, ok := coordinatorRegistry.Load(dataDir); ok {
		coordinator := existing.(*Coordinator)
		coordinator.mu.Lock()
		service.State = coordinator.state
		coordinator.service = service
		coordinator.mu.Unlock()
		return coordinator, nil
	}
	state := service.State
	if state == nil {
		state = NewStateStore(dataDir)
		service.State = state
	}
	loaded, err := state.Read()
	if err != nil {
		return nil, err
	}
	tasks := loaded.Tasks
	if tasks == nil {
		tasks = map[string]DeploymentTask{}
	}
	for id, task := range tasks {
		if task.Status == "queued" || task.Status == "running" {
			task.Status, task.Phase, task.Message = "unknown", "result_pending", "Core restarted before deployment outcome was confirmed; inspect the node before retrying."
			task.UpdatedAt = time.Now().UTC()
			tasks[id] = task
		}
	}
	coordinator := &Coordinator{service: service, state: state, tasks: tasks, activePeers: map[string]bool{}}
	if err := coordinator.persistLocked(loaded); err != nil {
		return nil, err
	}
	actual, loadedExisting := coordinatorRegistry.LoadOrStore(dataDir, coordinator)
	if loadedExisting {
		return actual.(*Coordinator), nil
	}
	return coordinator, nil
}

func (c *Coordinator) persistLocked(_ State) error {
	return c.state.Update(func(state *State) error { state.Tasks = c.tasks; return nil })
}

func (c *Coordinator) Start(ctx context.Context, request DeployRequest) (DeploymentTask, error) {
	if !request.ConfirmHostKey {
		return DeploymentTask{}, errors.New("confirm the SSH host-key fingerprint before starting deployment")
	}
	if _, err := request.Credentials.authMethods(); err != nil {
		return DeploymentTask{}, err
	}
	if _, err := validateCoreURL(request.CoreURL); err != nil {
		return DeploymentTask{}, err
	}
	if request.FallbackURL != "" {
		if _, err := validateCoreURL(request.FallbackURL); err != nil {
			return DeploymentTask{}, fmt.Errorf("backup Core URL rejected: %w", err)
		}
	}
	id, err := newTaskID()
	if err != nil {
		return DeploymentTask{}, err
	}
	peer, err := c.service.findPeer(ctx, request.PeerIdentity)
	if err != nil {
		return DeploymentTask{}, err
	}
	if peer.Class != "linux" {
		return DeploymentTask{}, errors.New("SSH deployment supports Linux peers only")
	}
	if _, err := firstOnlineIP(peer); err != nil {
		return DeploymentTask{}, err
	}
	if err := c.state.CheckPin(peer.Identity, request.HostFingerprint, request.ConfirmChangedKey); err != nil {
		return DeploymentTask{}, err
	}
	now := time.Now().UTC()
	task := DeploymentTask{ID: id, PeerIdentity: peer.Identity, PeerName: peer.Name, Status: "queued", Phase: "queued", Message: "Deployment accepted; remote preflight has not started.", CreatedAt: now, UpdatedAt: now}
	c.mu.Lock()
	if c.activePeers[peer.Identity] {
		c.mu.Unlock()
		return DeploymentTask{}, errors.New("a deployment is already active for this Tailscale peer")
	}
	c.activePeers[peer.Identity] = true
	c.tasks[id] = task
	state, readErr := c.state.Read()
	if readErr == nil {
		readErr = c.persistLocked(state)
	}
	service := c.service
	c.mu.Unlock()
	if readErr != nil {
		c.mu.Lock()
		delete(c.activePeers, peer.Identity)
		delete(c.tasks, id)
		c.mu.Unlock()
		return DeploymentTask{}, readErr
	}
	go func() {
		workCtx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		c.updateTask(id, "running", "preflight", "Checking the SSH host, OS, architecture, signature, and Core HTTPS reachability.", "")
		result, deployErr := service.Deploy(workCtx, request)
		if deployErr != nil {
			if errors.Is(deployErr, ErrOutcomeUnknown) {
				c.updateTask(id, "unknown", "result_pending", safeDeploymentError(deployErr, request), result.NodeID)
			} else {
				c.updateTask(id, "failed", "failed", safeDeploymentError(deployErr, request), result.NodeID)
			}
		} else {
			message := "Agent is online; Docker availability was not reported."
			status, phase := "succeeded", "complete"
			switch result.DockerState {
			case "connected":
				message = "Agent is online, Docker Engine access is confirmed, and the Tailscale peer is associated with its NodeDance node."
			case "unavailable":
				status, phase = "failed", "docker_unavailable"
				message = "Agent is online, but its Docker Engine connection is unavailable; container management is not ready and deployment is incomplete. Inspect Docker socket permissions and Engine health before retrying."
			case "absent":
				message = "Agent is online; this host has no Docker socket, so container management is unavailable."
			case "configured_unverified":
				status, phase = "unknown", "docker_verification_pending"
				message = "Agent is online and Docker socket access was configured, but the Engine connection was not confirmed; deployment is not considered complete. Check the node and refresh task status before retrying."
			}
			c.updateTaskWithDocker(id, status, phase, message, result.NodeID, result.DockerState)
		}
		clearDeploymentSecrets(&request)
		c.mu.Lock()
		delete(c.activePeers, peer.Identity)
		c.mu.Unlock()
	}()
	return task, nil
}

func (c *Coordinator) updateTask(id, status, phase, message, nodeID string) {
	c.updateTaskWithDocker(id, status, phase, message, nodeID, "")
}

func (c *Coordinator) updateTaskWithDocker(id, status, phase, message, nodeID, dockerState string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	task, ok := c.tasks[id]
	if !ok {
		return
	}
	task.Status, task.Phase, task.Message, task.NodeID, task.UpdatedAt = status, phase, message, nodeID, time.Now().UTC()
	if dockerState != "" {
		task.DockerState = dockerState
	}
	if status == "succeeded" || status == "failed" || status == "unknown" {
		finished := task.UpdatedAt
		task.FinishedAt = &finished
	}
	c.tasks[id] = task
	state, err := c.state.Read()
	if err == nil {
		_ = c.persistLocked(state)
	}
}

func (c *Coordinator) Task(id string) (DeploymentTask, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	task, ok := c.tasks[id]
	return task, ok
}

func safeDeploymentError(err error, request DeployRequest) string {
	message := err.Error()
	for _, secret := range []string{request.Credentials.Password, request.Credentials.PrivateKey, request.Credentials.Passphrase} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}

func clearDeploymentSecrets(request *DeployRequest) {
	if request == nil {
		return
	}
	request.Credentials.Password, request.Credentials.PrivateKey, request.Credentials.Passphrase = "", "", ""
}
