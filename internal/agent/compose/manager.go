// Package compose discovers Compose inventory from Docker Engine labels and
// executes Compose changes through the shared Core Task and Agent TaskJournal.
package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var ErrConfigMissing = errors.New("Compose project source files are unavailable")

type Engine interface {
	ListAll(context.Context) ([]string, error)
	Inspect(context.Context, string) (agentdocker.Container, error)
}

type Manager struct {
	engine     Engine
	dockerPath string
	createMu   sync.Mutex
}

func NewManager(engine Engine) (*Manager, error) {
	if engine == nil {
		return nil, errors.New("Compose Engine is required")
	}
	dockerPath, err := osexec.LookPath("docker")
	if err != nil {
		return nil, errors.New("Docker Compose CLI is unavailable")
	}
	return &Manager{engine: engine, dockerPath: dockerPath}, nil
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
	root, err := openSafeWorkingRoot(ref.WorkingDirectory)
	if err != nil {
		return false
	}
	defer root.Close()
	for _, file := range ref.ConfigFiles {
		clean := filepath.Clean(file)
		relative, relErr := filepath.Rel(ref.WorkingDirectory, clean)
		resolved, resolveErr := filepath.EvalSymlinks(clean)
		info, statErr := root.Lstat(relative)
		if relErr != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
			resolveErr != nil || resolved != clean || statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}
