package tailscale

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
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
	FileRoot          string         `json:"fileRoot,omitempty"`
	DisableFileRoot   bool           `json:"disableFileRoot,omitempty"`
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

func (s *Service) deploy(ctx context.Context, request DeployRequest, beforeInstall func(Peer, string) error) (DeploymentResult, error) {
	if beforeInstall == nil {
		return DeploymentResult{}, errors.New("Agent deployment requires the Core task store before remote installation")
	}
	if s.State == nil {
		return DeploymentResult{}, errors.New("Tailscale deployment state is not initialized")
	}
	if s.Artifacts == nil {
		return DeploymentResult{}, errors.New("signed Agent artifacts are not configured on this Core")
	}
	if err := validateFileRootRequest(request.FileRoot); err != nil {
		return DeploymentResult{}, err
	}
	if request.DisableFileRoot && request.FileRoot != "" {
		return DeploymentResult{}, errors.New("fileRoot and disableFileRoot cannot be used together")
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
	artifact, err := s.Artifacts.Load(preflight.OS, preflight.Arch)
	if err != nil {
		return DeploymentResult{}, err
	}
	selectedURL, err := SelectReachableCoreURL(ctx, remote, request.CoreURL, request.FallbackURL, request.AllowFallback)
	if err != nil {
		return DeploymentResult{}, err
	}
	if request.FileRoot != "" {
		if err := preflightRemoteFileRoot(ctx, remote, preflight, artifact, request.FileRoot); err != nil {
			return DeploymentResult{}, fmt.Errorf("target file-root preflight failed before Agent enrollment: %w", err)
		}
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
	if err := beforeInstall(peer, enrollment.NodeID); err != nil {
		return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity}, err
	}
	if err := InstallRemoteWithFileRootOptions(ctx, remote, preflight, artifact, selectedURL, enrollment, request.FileRoot, request.DisableFileRoot); err != nil {
		return DeploymentResult{NodeID: enrollment.NodeID, PeerIdentity: peer.Identity}, fmt.Errorf("%w: remote Agent installation result must be inspected before retrying: %v", ErrOutcomeUnknown, err)
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

// Coordinator is process-local for live SSH work. Task identity, idempotency,
// state transitions, and audit records live only in the Core task store.
// Credentials are captured only by the active goroutine and are never stored.
type Coordinator struct {
	mu          sync.Mutex
	service     *Service
	state       *StateStore
	tasks       *coretasks.Store
	activePeers map[string]chan struct{}
}

var coordinatorRegistry sync.Map

func SharedCoordinator(dataDir string, service *Service, tasks *coretasks.Store) (*Coordinator, error) {
	if dataDir == "" || service == nil || tasks == nil {
		return nil, errors.New("deployment manager requires a data directory, service, and Core task store")
	}
	if existing, ok := coordinatorRegistry.Load(dataDir); ok {
		coordinator := existing.(*Coordinator)
		coordinator.mu.Lock()
		service.State = coordinator.state
		coordinator.service = service
		coordinator.tasks = tasks
		coordinator.mu.Unlock()
		return coordinator, nil
	}
	state := service.State
	if state == nil {
		state = NewStateStore(dataDir)
		service.State = state
	}
	coordinator := &Coordinator{service: service, state: state, tasks: tasks, activePeers: map[string]chan struct{}{}}
	actual, loadedExisting := coordinatorRegistry.LoadOrStore(dataDir, coordinator)
	if loadedExisting {
		shared := actual.(*Coordinator)
		shared.mu.Lock()
		service.State = shared.state
		shared.service = service
		shared.tasks = tasks
		shared.mu.Unlock()
		return shared, nil
	}
	return coordinator, nil
}

var errDeploymentAlreadyAccepted = errors.New("deployment request was already accepted")

type deploymentAcceptance struct {
	task    coretasks.Task
	created bool
	err     error
}

func (c *Coordinator) Start(ctx context.Context, request DeployRequest, idempotencyKey string, actorID sql.NullInt64, remoteAddr string) (DeploymentTask, error) {
	if ctx == nil {
		return DeploymentTask{}, coretasks.ErrInvalidRequest
	}
	service, state, tasks := c.snapshot()
	if service == nil || state == nil || tasks == nil {
		return DeploymentTask{}, errors.New("Core task store is not initialized")
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	authentication := "password"
	if request.Credentials.PrivateKey != "" {
		authentication = "private_key"
	}
	intent := protocol.TaskIntent{
		Action:      protocol.TaskAgentDeploy,
		ContainerID: "tailscale-peer:" + request.PeerIdentity,
		AgentDeploy: &protocol.AgentDeployTaskSpec{
			PeerIdentity: request.PeerIdentity, PeerName: request.DisplayName,
			FileRoot: request.FileRoot, DisableFileRoot: request.DisableFileRoot,
			CoreURL: request.CoreURL, FallbackURL: request.FallbackURL, AllowFallback: request.AllowFallback,
			HostFingerprint: request.HostFingerprint, ConfirmChangedHostKey: request.ConfirmChangedKey,
			SSHUser: request.Credentials.User, Authentication: authentication,
		},
	}
	prior, found, err := tasks.FindLocalByIdempotency(ctx, idempotencyKey, intent)
	if err != nil {
		return DeploymentTask{}, err
	}
	if found {
		return deploymentTaskView(prior), nil
	}
	if err := tasks.CheckLocalResourceAvailable(ctx, intent); err != nil {
		return DeploymentTask{}, err
	}
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
	peer, err := service.findPeer(ctx, request.PeerIdentity)
	if err != nil {
		return DeploymentTask{}, err
	}
	if peer.Class != "linux" {
		return DeploymentTask{}, errors.New("SSH deployment supports Linux peers only")
	}
	if _, err := firstOnlineIP(peer); err != nil {
		return DeploymentTask{}, err
	}
	if err := state.CheckPin(peer.Identity, request.HostFingerprint, request.ConfirmChangedKey); err != nil {
		return DeploymentTask{}, err
	}
	c.mu.Lock()
	if active := c.activePeers[peer.Identity]; active != nil {
		c.mu.Unlock()
		// A concurrent retry may have crossed the initial lookup just before
		// the first executor committed its durable task. Recheck the authority
		// store before reporting the peer as busy.
		prior, found, lookupErr := tasks.FindLocalByIdempotency(ctx, idempotencyKey, intent)
		if lookupErr != nil {
			return DeploymentTask{}, lookupErr
		}
		if found {
			return deploymentTaskView(prior), nil
		}
		return DeploymentTask{}, coretasks.ErrResourceBusy
	}
	active := make(chan struct{})
	c.activePeers[peer.Identity] = active
	c.mu.Unlock()
	acceptance := make(chan deploymentAcceptance, 1)
	done := make(chan error, 1)
	workCtx, cancelWork := context.WithTimeout(context.Background(), 12*time.Minute)
	go func() {
		defer cancelWork()
		defer func() {
			c.mu.Lock()
			if c.activePeers[peer.Identity] == active {
				delete(c.activePeers, peer.Identity)
				close(active)
			}
			c.mu.Unlock()
		}()
		var localTask coretasks.Task
		accepted := false
		result, deployErr := service.deploy(workCtx, request, func(deployPeer Peer, nodeID string) error {
			queued, enqueueErr := tasks.EnqueueAndStartLocal(workCtx, coretasks.EnqueueRequest{
				NodeID: nodeID, IdempotencyKey: idempotencyKey, Intent: intent,
				ActorID: actorID, RemoteAddr: remoteAddr,
			})
			if enqueueErr != nil {
				acceptance <- deploymentAcceptance{err: enqueueErr}
				return enqueueErr
			}
			if !queued.Created {
				acceptance <- deploymentAcceptance{task: queued.Task}
				return errDeploymentAlreadyAccepted
			}
			localTask = queued.Task
			accepted = true
			acceptance <- deploymentAcceptance{task: localTask, created: true}
			_ = deployPeer
			return nil
		})
		clearDeploymentSecrets(&request)
		if errors.Is(deployErr, errDeploymentAlreadyAccepted) {
			done <- nil
			return
		}
		if accepted {
			if err := persistDeploymentOutcome(tasks, localTask, result, deployErr, actorID, remoteAddr); err != nil {
				log.Printf("NodeDance Core-local Agent deployment task recovery remains pending for task %s: %v", localTask.TaskID, err)
			}
		}
		done <- deployErr
	}()
	select {
	case result := <-acceptance:
		if result.err != nil {
			return DeploymentTask{}, result.err
		}
		return deploymentTaskView(result.task), nil
	case err := <-done:
		if err == nil {
			return DeploymentTask{}, errors.New("deployment ended before its Core task was accepted")
		}
		return DeploymentTask{}, err
	case <-ctx.Done():
		// If acceptance raced with the HTTP cancellation, return the durable
		// task and let its executor continue independently. Otherwise cancel
		// preflight before any remote install; the worker owns peer-lock cleanup.
		select {
		case result := <-acceptance:
			if result.err == nil {
				return deploymentTaskView(result.task), nil
			}
			cancelWork()
			return DeploymentTask{}, result.err
		default:
		}
		cancelWork()
		return DeploymentTask{}, ctx.Err()
	}
}

func persistDeploymentOutcome(tasks *coretasks.Store, task coretasks.Task, result DeploymentResult, deployErr error, actorID sql.NullInt64, remoteAddr string) error {
	status, observed := taskstate.Unknown, "result_pending"
	if deployErr == nil {
		if result.DockerState == "configured_unverified" {
			observed = result.DockerState
		} else {
			status, observed = taskstate.Succeeded, result.DockerState
		}
	}
	completionCtx, cancelCompletion := context.WithTimeout(context.Background(), 10*time.Second)
	_, completeErr := tasks.CompleteLocal(completionCtx, task.NodeID, task.TaskID, status, observed, actorID, remoteAddr)
	cancelCompletion()
	if completeErr == nil {
		return nil
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 10*time.Second)
	_, recoveryErr := tasks.MarkLocalUnknown(recoveryCtx, task.NodeID, task.TaskID, actorID, remoteAddr)
	cancelRecovery()
	if recoveryErr != nil {
		return fmt.Errorf("complete Core task: %v; record unknown outcome: %w", completeErr, recoveryErr)
	}
	return nil
}

func (c *Coordinator) Task(ctx context.Context, id string) (DeploymentTask, bool) {
	tasks := c.taskStore()
	if tasks == nil {
		return DeploymentTask{}, false
	}
	task, err := tasks.GetByID(ctx, id)
	if err != nil || task.Intent.Action != coretasks.ActionAgentDeploy {
		return DeploymentTask{}, false
	}
	return deploymentTaskView(task), true
}

func (c *Coordinator) AssociatePeer(identity, nodeID string) error {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == nil {
		return errors.New("Tailscale deployment state is not initialized")
	}
	return state.Associate(identity, nodeID)
}

func (c *Coordinator) DisassociatePeer(identity, nodeID string) error {
	c.mu.Lock()
	state := c.state
	c.mu.Unlock()
	if state == nil {
		return errors.New("Tailscale deployment state is not initialized")
	}
	return state.Disassociate(identity, nodeID)
}

func (c *Coordinator) UnresolvedTasks(ctx context.Context) ([]DeploymentTask, error) {
	tasks := c.taskStore()
	if tasks == nil {
		return nil, errors.New("Core task store is not initialized")
	}
	pending, err := tasks.ListUnresolvedLocalAgentDeployments(ctx, coretasks.MaxPageSize)
	if err != nil {
		return nil, err
	}
	views := make([]DeploymentTask, 0, len(pending))
	for _, task := range pending {
		views = append(views, deploymentTaskView(task))
	}
	return views, nil
}

func (c *Coordinator) taskStore() *coretasks.Store {
	_, _, tasks := c.snapshot()
	return tasks
}

func (c *Coordinator) snapshot() (*Service, *StateStore, *coretasks.Store) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.service, c.state, c.tasks
}

func deploymentTaskView(task coretasks.Task) DeploymentTask {
	peerIdentity, peerName := "", ""
	if task.Intent.AgentDeploy != nil {
		peerIdentity, peerName = task.Intent.AgentDeploy.PeerIdentity, task.Intent.AgentDeploy.PeerName
	}
	phase := string(task.Progress.Phase)
	if phase == "" {
		phase = "accepted"
	}
	message := "Deployment accepted; remote installation has not started."
	dockerState := task.Result.ObservedState
	switch task.Status {
	case taskstate.Running:
		message = "Agent installation and registration are in progress."
	case taskstate.Succeeded:
		switch dockerState {
		case "connected":
			message = "Agent is online, Docker Engine access is confirmed, and the Tailscale peer is associated with its NodeDance node."
		case "unavailable":
			message = "Agent is online and host monitoring is active, but Docker Engine access is unavailable."
		case "absent":
			message = "Agent is online; this host has no Docker socket, so container management is unavailable."
		default:
			message = "Agent installation completed and the Agent is online."
		}
	case taskstate.Failed:
		message = "Agent deployment failed before a remote outcome became uncertain."
	case taskstate.Unknown, taskstate.TimedOut:
		phase = "result_pending"
		message = "Agent deployment outcome could not be confirmed; inspect the node before retrying."
	case taskstate.Canceled:
		message = "Agent deployment did not start before Core restart."
	}
	return DeploymentTask{ID: task.TaskID, PeerIdentity: peerIdentity, PeerName: peerName,
		Status: string(task.Status), Phase: phase, Message: message, NodeID: task.NodeID,
		DockerState: dockerState, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, FinishedAt: task.FinishedAt}
}

func clearDeploymentSecrets(request *DeployRequest) {
	if request == nil {
		return
	}
	request.Credentials.Password, request.Credentials.PrivateKey, request.Credentials.Passphrase = "", "", ""
}
