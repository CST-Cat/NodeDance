// Package compose discovers Compose projects from Engine labels and runs only
// allowlisted Docker Compose operations. It never invokes a shell and never
// returns resolved Compose configuration (which may contain secrets).
package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	defaultOperationTimeout = 10 * time.Minute
	defaultVerifyTimeout    = 90 * time.Second
)

var (
	ErrProjectNotFound = errors.New("Compose project is no longer present in Docker")
	ErrConfigMissing   = errors.New("Compose project source files are unavailable")
	ErrComposeFailed   = errors.New("Docker Compose operation failed")
	ErrVerifyFailed    = errors.New("Docker Engine state did not satisfy the Compose operation")
	ErrOutcomeUnknown  = errors.New("Compose write outcome cannot be confirmed")
)

type Engine interface {
	ListAll(context.Context) ([]string, error)
	Inspect(context.Context, string) (agentdocker.Container, error)
}

type CommandRunner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

// ExecRunner uses exec.CommandContext with an argv slice. The Docker host is
// explicitly pinned to the Engine socket already used by the Agent SDK.
type ExecRunner struct {
	DockerPath string
	DockerHost string
}

func (r ExecRunner) Run(ctx context.Context, directory string, args []string, dockerHost string) ([]byte, error) {
	path := r.DockerPath
	if path == "" {
		path = "docker"
	}
	if dockerHost == "" {
		dockerHost = r.DockerHost
	}
	if dockerHost != "" && !strings.HasPrefix(dockerHost, "unix:///") {
		return nil, errors.New("Compose requires a local Docker Engine socket")
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Dir = directory
	command.Env = withoutEnv(os.Environ(), "DOCKER_HOST")
	if dockerHost != "" {
		command.Env = append(command.Env, "DOCKER_HOST="+dockerHost)
	}
	return command.CombinedOutput()
}

func withoutEnv(environment []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

type Options struct {
	DockerHost       string
	OperationTimeout time.Duration
	VerifyTimeout    time.Duration
	PollInterval     time.Duration
	Now              func() time.Time
}

type Manager struct {
	engine Engine
	runner CommandRunner
	opts   Options
}

func NewManager(engine Engine, runner CommandRunner, options Options) (*Manager, error) {
	if engine == nil || runner == nil {
		return nil, errors.New("Compose Engine and command runner are required")
	}
	if options.DockerHost != "" && !strings.HasPrefix(options.DockerHost, "unix:///") {
		return nil, errors.New("Compose requires a local Docker Engine socket")
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = defaultOperationTimeout
	}
	if options.OperationTimeout < time.Second || options.OperationTimeout > 30*time.Minute {
		return nil, errors.New("Compose operation timeout is outside the supported bounds")
	}
	if options.VerifyTimeout == 0 {
		options.VerifyTimeout = defaultVerifyTimeout
	}
	if options.VerifyTimeout < time.Second || options.VerifyTimeout > 5*time.Minute {
		return nil, errors.New("Compose verification timeout is outside the supported bounds")
	}
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
	}
	if options.PollInterval < 100*time.Millisecond || options.PollInterval > 10*time.Second {
		return nil, errors.New("Compose verification poll interval is outside the supported bounds")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{engine: engine, runner: runner, opts: options}, nil
}

func (m *Manager) List(ctx context.Context) ([]protocol.ComposeProject, error) {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Docker containers: %w", err)
	}
	sort.Strings(ids)
	type grouped struct {
		project  protocol.ComposeProject
		services map[string]*protocol.ComposeService
	}
	groups := make(map[string]*grouped)
	for _, id := range ids {
		container, inspectErr := m.engine.Inspect(ctx, id)
		if inspectErr != nil {
			return nil, fmt.Errorf("inspect Docker container: %w", inspectErr)
		}
		if container.Compose == nil || container.Compose.Project == "" || container.Compose.Service == "" {
			continue
		}
		ref, refErr := reference(container.Compose)
		if refErr != nil {
			// Keep the service visible as an unmanageable inventory entry only
			// when identity can still be formed. Malformed source labels cannot
			// authorize a write operation.
			continue
		}
		group := groups[ref.Key]
		if group == nil {
			project := protocol.ComposeProject{Ref: ref, ConfigAvailable: configExists(ref)}
			if !project.ConfigAvailable {
				project.ConfigReason = "config_missing"
			}
			group = &grouped{project: project, services: make(map[string]*protocol.ComposeService)}
			groups[ref.Key] = group
		}
		service := group.services[container.Compose.Service]
		if service == nil {
			service = &protocol.ComposeService{Name: container.Compose.Service, Instances: []protocol.ComposeServiceInstance{}}
			group.services[container.Compose.Service] = service
		}
		service.Instances = append(service.Instances, protocol.ComposeServiceInstance{
			ContainerID: container.ID, ContainerName: strings.TrimPrefix(container.Name, "/"),
			State: container.State, Health: string(container.Health),
		})
	}
	projects := make([]protocol.ComposeProject, 0, len(groups))
	for _, group := range groups {
		serviceNames := make([]string, 0, len(group.services))
		for name := range group.services {
			serviceNames = append(serviceNames, name)
		}
		sort.Strings(serviceNames)
		for _, name := range serviceNames {
			service := group.services[name]
			sort.Slice(service.Instances, func(i, j int) bool { return service.Instances[i].ContainerID < service.Instances[j].ContainerID })
			group.project.Services = append(group.project.Services, *service)
		}
		projects = append(projects, group.project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Ref.Name == projects[j].Ref.Name {
			return projects[i].Ref.WorkingDirectory < projects[j].Ref.WorkingDirectory
		}
		return projects[i].Ref.Name < projects[j].Ref.Name
	})
	return projects, nil
}

func reference(identity *agentdocker.ComposeIdentity) (protocol.ComposeProjectRef, error) {
	if identity == nil || identity.Project == "" || identity.WorkingDir == "" || identity.ConfigFiles == "" {
		return protocol.ComposeProjectRef{}, ErrConfigMissing
	}
	workingDirectory, err := filepath.Abs(identity.WorkingDir)
	if err != nil {
		return protocol.ComposeProjectRef{}, err
	}
	files := strings.Split(identity.ConfigFiles, ",")
	if len(files) == 0 || len(files) > protocol.MaxComposeConfigFiles {
		return protocol.ComposeProjectRef{}, errors.New("Compose config file label is invalid")
	}
	for index, file := range files {
		if strings.TrimSpace(file) != file || file == "" {
			return protocol.ComposeProjectRef{}, errors.New("Compose config file label is invalid")
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(workingDirectory, file)
		}
		files[index], err = filepath.Abs(file)
		if err != nil {
			return protocol.ComposeProjectRef{}, err
		}
	}
	ref := protocol.ComposeProjectRef{Name: identity.Project, WorkingDirectory: workingDirectory, ConfigFiles: files}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	return ref, protocol.ValidateComposeProjectRef(ref)
}

func configExists(ref protocol.ComposeProjectRef) bool {
	if info, err := os.Stat(ref.WorkingDirectory); err != nil || !info.IsDir() {
		return false
	}
	for _, file := range ref.ConfigFiles {
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func (m *Manager) Execute(ctx context.Context, request protocol.ComposeRequest) (protocol.ComposeResponse, error) {
	if err := protocol.ValidateComposeRequest(request); err != nil {
		return protocol.ComposeResponse{}, err
	}
	response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "failed"}
	if request.Action == protocol.ComposeList {
		projects, err := m.List(ctx)
		if err != nil {
			return response, err
		}
		response.Status, response.Verified, response.Projects = "succeeded", true, projects
		return response, nil
	}
	projects, err := m.List(ctx)
	if err != nil {
		return response, err
	}
	var selected *protocol.ComposeProject
	for index := range projects {
		if projects[index].Ref.Key == request.Project.Key {
			selected = &projects[index]
			break
		}
	}
	if selected == nil && (request.Action == protocol.ComposeUp || request.Action == protocol.ComposeValidate || request.Action == protocol.ComposeDown) && configExists(request.Project) {
		// Compose down removes the labels used for discovery. A previously
		// authorized, source-bound reference can still bring the same project
		// back up without guessing from a service name or directory basename.
		selected = &protocol.ComposeProject{Ref: request.Project, ConfigAvailable: true, Services: []protocol.ComposeService{}}
	}
	if selected == nil || !sameRef(selected.Ref, request.Project) {
		return response, ErrProjectNotFound
	}
	if !selected.ConfigAvailable {
		return response, ErrConfigMissing
	}
	for _, file := range request.EnvFiles {
		info, statErr := os.Stat(file)
		if statErr != nil || !info.Mode().IsRegular() {
			return response, ErrConfigMissing
		}
	}
	args := composePrefix(selected.Ref, request.EnvFiles, request.Profiles)
	if _, err := m.run(ctx, selected.Ref.WorkingDirectory, append(append([]string(nil), args...), "config", "--quiet")); err != nil {
		return response, ErrComposeFailed
	}
	if request.Action == protocol.ComposeValidate {
		response.Status, response.Verified, response.Project = "succeeded", true, selected
		return response, nil
	}
	var before []agentdocker.Container
	if request.Action != protocol.ComposeDown {
		before, err = m.observeProject(ctx, selected.Ref)
		if err != nil {
			return response, ErrProjectNotFound
		}
		if request.Action != protocol.ComposeUp && len(before) == 0 {
			return response, ErrProjectNotFound
		}
	}
	opCtx, cancel := context.WithTimeout(ctx, m.opts.OperationTimeout)
	defer cancel()
	commandArgs := append(append([]string(nil), args...), string(request.Action))
	if request.Action == protocol.ComposeUp {
		commandArgs = append(commandArgs, "--detach")
	}
	// Compose down deliberately receives no --volumes, --rmi, or --remove-orphans
	// option: project volumes and unrelated resources remain intact.
	if _, err := m.run(opCtx, selected.Ref.WorkingDirectory, commandArgs); err != nil {
		return response, ErrOutcomeUnknown
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, m.opts.VerifyTimeout)
	defer verifyCancel()
	if err := m.verify(verifyCtx, *selected, request.Action, request.EnvFiles, request.Profiles, before); err != nil {
		return response, ErrOutcomeUnknown
	}
	updated, err := m.List(ctx)
	if err != nil {
		return response, ErrOutcomeUnknown
	}
	for index := range updated {
		if updated[index].Ref.Key == request.Project.Key {
			selected = &updated[index]
			break
		}
	}
	response.Status, response.Verified, response.Project = "succeeded", true, selected
	return response, nil
}

func composePrefix(ref protocol.ComposeProjectRef, envFiles, profiles []string) []string {
	args := []string{"compose", "--project-name", ref.Name, "--project-directory", ref.WorkingDirectory}
	for _, file := range ref.ConfigFiles {
		args = append(args, "-f", file)
	}
	for _, file := range envFiles {
		args = append(args, "--env-file", file)
	}
	for _, profile := range profiles {
		args = append(args, "--profile", profile)
	}
	return args
}

func sameRef(left, right protocol.ComposeProjectRef) bool {
	if left.Key != right.Key || left.Name != right.Name || left.WorkingDirectory != right.WorkingDirectory || len(left.ConfigFiles) != len(right.ConfigFiles) {
		return false
	}
	for index := range left.ConfigFiles {
		if left.ConfigFiles[index] != right.ConfigFiles[index] {
			return false
		}
	}
	return true
}

func (m *Manager) run(ctx context.Context, directory string, args []string) ([]byte, error) {
	return m.runner.Run(ctx, directory, args, m.opts.DockerHost)
}

func (m *Manager) verify(ctx context.Context, project protocol.ComposeProject, action protocol.ComposeAction, envFiles, profiles []string, before []agentdocker.Container) error {
	desired := map[string]int{}
	if action == protocol.ComposeStart || action == protocol.ComposeRestart {
		for _, item := range before {
			if item.Compose == nil || item.Compose.Service == "" {
				return ErrVerifyFailed
			}
			desired[item.Compose.Service]++
		}
	} else if action == protocol.ComposeUp {
		args := append(composePrefix(project.Ref, envFiles, profiles), "config", "--format", "json")
		output, err := m.run(ctx, project.Ref.WorkingDirectory, args)
		if err != nil {
			return ErrComposeFailed
		}
		var model struct {
			Services map[string]struct {
				Deploy struct {
					Replicas *int `json:"replicas"`
				} `json:"deploy"`
			} `json:"services"`
		}
		if err := json.Unmarshal(output, &model); err != nil || len(model.Services) == 0 {
			return ErrVerifyFailed
		}
		for name, service := range model.Services {
			replicas := 1
			if service.Deploy.Replicas != nil {
				replicas = *service.Deploy.Replicas
			} else {
				// An earlier `docker compose up --scale` may have established a
				// count not represented in the source file. This implementation
				// does not expose a scale override, so preserve a known existing
				// count when verifying an otherwise unchanged `up`.
				observed := 0
				for _, item := range before {
					if item.Compose != nil && item.Compose.Service == name {
						observed++
					}
				}
				if observed > 0 {
					replicas = observed
				}
			}
			if replicas < 0 || replicas > 1000 {
				return ErrVerifyFailed
			}
			desired[name] = replicas
		}
	}
	deadline := time.NewTimer(m.opts.VerifyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(m.opts.PollInterval)
	defer ticker.Stop()
	for {
		observed, err := m.observeProject(ctx, project.Ref)
		if err == nil && verifyObserved(observed, desired, action) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrVerifyFailed
		case <-deadline.C:
			return ErrVerifyFailed
		case <-ticker.C:
		}
	}
}

func (m *Manager) observeProject(ctx context.Context, ref protocol.ComposeProjectRef) ([]agentdocker.Container, error) {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]agentdocker.Container, 0)
	for _, id := range ids {
		item, inspectErr := m.engine.Inspect(ctx, id)
		if inspectErr != nil {
			return nil, inspectErr
		}
		observedRef, refErr := reference(item.Compose)
		if refErr == nil && observedRef.Key == ref.Key {
			items = append(items, item)
		}
	}
	return items, nil
}

func verifyObserved(items []agentdocker.Container, desired map[string]int, action protocol.ComposeAction) bool {
	if action == protocol.ComposeDown {
		return len(items) == 0
	}
	if action == protocol.ComposeStop {
		for _, item := range items {
			if item.Running || item.Paused || item.Restarting {
				return false
			}
		}
		return len(items) > 0
	}
	seen := make(map[string]int, len(desired))
	for _, item := range items {
		if item.Compose == nil {
			return false
		}
		expected, inModel := desired[item.Compose.Service]
		if !inModel || expected == 0 {
			continue
		}
		if !item.Running || item.Paused || item.Restarting || (item.HealthcheckConfigured && item.Health != agentdocker.HealthHealthy) {
			return false
		}
		seen[item.Compose.Service]++
	}
	if len(items) == 0 {
		return false
	}
	for service, count := range desired {
		if seen[service] != count {
			return false
		}
	}
	return true
}
