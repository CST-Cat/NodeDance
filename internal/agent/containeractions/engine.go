package containeractions

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// SDKEngine is the real Docker Engine adapter. It permits only a local Unix
// socket, negotiates the API version, and hard-codes safe remove flags.
type SDKEngine struct {
	client *client.Client
}

func NewEngine(cli *client.Client) (*SDKEngine, error) {
	if cli == nil {
		return nil, errors.New("shared Docker Engine client is required")
	}
	return &SDKEngine{client: cli}, nil
}

func (e *SDKEngine) Inspect(ctx context.Context, id string) (Container, error) {
	result, err := e.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, err
	}
	value := result.Container
	if value.ID == "" || value.State == nil || value.Config == nil {
		return Container{}, errors.New("Docker Engine returned an incomplete container inspection")
	}
	composeManaged := false
	for label := range value.Config.Labels {
		if strings.HasPrefix(label, "com.docker.compose.") {
			composeManaged = true
			break
		}
	}
	return Container{
		ID:             value.ID,
		Name:           value.Name,
		Image:          value.Config.Image,
		Running:        value.State.Running,
		Paused:         value.State.Paused,
		Restarting:     value.State.Restarting,
		StartedAt:      value.State.StartedAt,
		RestartCount:   value.RestartCount,
		ComposeManaged: composeManaged,
		CreateTaskID:   value.Config.Labels[protocol.ContainerCreateTaskLabel],
		CreateDigest:   value.Config.Labels[protocol.ContainerCreateHashLabel],
	}, nil
}

func (e *SDKEngine) CreateContainer(ctx context.Context, spec protocol.ContainerCreateSpec, taskID, digest string) (Container, error) {
	if ctx == nil || protocol.ValidateContainerCreateSpec(spec) != nil || !validText(taskID, 128) || len(digest) != 64 {
		return Container{}, errors.New("invalid Docker container create request")
	}
	labels := map[string]string{protocol.ContainerCreateTaskLabel: taskID, protocol.ContainerCreateHashLabel: digest}
	config := &container.Config{Image: spec.Image, Cmd: append([]string(nil), spec.Command...), Env: append([]string(nil), spec.Environment...),
		ExposedPorts: make(network.PortSet), Labels: labels}
	defer func() {
		clear(config.Cmd)
		clear(config.Env)
		config.Image = ""
	}()
	hostConfig := &container.HostConfig{
		PortBindings:  make(network.PortMap),
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode(spec.RestartPolicy), MaximumRetryCount: spec.RestartRetries},
		Mounts:        make([]mount.Mount, 0, len(spec.Mounts)),
	}
	if spec.Network == "host" {
		hostConfig.NetworkMode = container.NetworkMode("host")
	}
	for _, portSpec := range spec.Ports {
		port, err := network.ParsePort(fmt.Sprintf("%d/%s", portSpec.ContainerPort, portSpec.Protocol))
		if err != nil {
			return Container{}, errors.New("invalid Docker container port")
		}
		config.ExposedPorts[port] = struct{}{}
		var hostIP netip.Addr
		if portSpec.HostIP != "" {
			hostIP, err = netip.ParseAddr(portSpec.HostIP)
			if err != nil {
				return Container{}, errors.New("invalid Docker host port address")
			}
		}
		hostConfig.PortBindings[port] = append(hostConfig.PortBindings[port], network.PortBinding{HostIP: hostIP, HostPort: portSpec.HostPort})
	}
	for _, mountSpec := range spec.Mounts {
		mountType := mount.TypeBind
		if mountSpec.Type == "volume" {
			mountType = mount.TypeVolume
		}
		hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{Type: mountType, Source: mountSpec.Source, Target: mountSpec.Target, ReadOnly: mountSpec.ReadOnly})
	}
	var networking *network.NetworkingConfig
	if spec.Network != "" && spec.Network != "host" {
		networking = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}}}
	}
	created, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: config, HostConfig: hostConfig, NetworkingConfig: networking, Name: spec.Name})
	if err != nil {
		return Container{}, errors.New("Docker container creation failed")
	}
	if !protocol.IsFullContainerID(created.ID) {
		return Container{}, errors.New("Docker returned an invalid container ID")
	}
	result, err := e.Inspect(ctx, created.ID)
	if err != nil {
		return Container{ID: created.ID}, errors.New("created container could not be inspected")
	}
	if result.ID != created.ID || result.CreateTaskID != taskID || result.CreateDigest != digest || result.Name == "" || result.Image == "" {
		return result, errors.New("created container did not match the submitted task")
	}
	return result, nil
}

func (e *SDKEngine) FindCreatedContainers(ctx context.Context, taskID, digest string) ([]Container, error) {
	if ctx == nil || !validText(taskID, 128) || len(digest) != 64 {
		return nil, errors.New("invalid Docker container create lookup")
	}
	filters := client.Filters{}.Add("label", protocol.ContainerCreateTaskLabel+"="+taskID).
		Add("label", protocol.ContainerCreateHashLabel+"="+digest)
	listed, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, errors.New("Docker container create reconciliation failed")
	}
	items := make([]Container, 0, len(listed.Items))
	for _, listedContainer := range listed.Items {
		if !protocol.IsFullContainerID(listedContainer.ID) {
			return nil, errors.New("Docker returned an invalid container ID during create reconciliation")
		}
		item, err := e.Inspect(ctx, listedContainer.ID)
		if err != nil || item.ID != listedContainer.ID || item.CreateTaskID != taskID || item.CreateDigest != digest {
			return nil, errors.New("Docker container create reconciliation returned an inconsistent resource")
		}
		items = append(items, item)
	}
	return items, nil
}

func (e *SDKEngine) Start(ctx context.Context, id string) error {
	_, err := e.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (e *SDKEngine) Stop(ctx context.Context, id string) error {
	_, err := e.client.ContainerStop(ctx, id, client.ContainerStopOptions{})
	return err
}

func (e *SDKEngine) Restart(ctx context.Context, id string) error {
	_, err := e.client.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
	return err
}

func (e *SDKEngine) Pause(ctx context.Context, id string) error {
	_, err := e.client.ContainerPause(ctx, id, client.ContainerPauseOptions{})
	return err
}

func (e *SDKEngine) Resume(ctx context.Context, id string) error {
	_, err := e.client.ContainerUnpause(ctx, id, client.ContainerUnpauseOptions{})
	return err
}

func (e *SDKEngine) Remove(ctx context.Context, id string) error {
	_, err := e.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{
		RemoveVolumes: false,
		Force:         false,
	})
	return err
}

func (e *SDKEngine) Rename(ctx context.Context, id, newName string) error {
	_, err := e.client.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: newName})
	return err
}
