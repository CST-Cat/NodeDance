package containerrebuild

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// DockerEngine is the production adapter. Every removal keeps volumes and
// links; the caller supplies exact task-owned names and IDs.
type DockerEngine struct{ client *client.Client }

func NewDockerEngine(cli *client.Client) (*DockerEngine, error) {
	if cli == nil {
		return nil, errors.New("Docker SDK client is required")
	}
	return &DockerEngine{client: cli}, nil
}

func (e *DockerEngine) Inspect(ctx context.Context, id string) (Container, error) {
	result, err := e.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{Size: true})
	if err != nil {
		return Container{}, err
	}
	item := result.Container
	if item.ID == "" || item.State == nil || item.Config == nil || item.HostConfig == nil {
		return Container{}, errors.New("Docker returned incomplete container Inspect data")
	}
	compose := false
	for key := range item.Config.Labels {
		if strings.HasPrefix(key, "com.docker.compose.") {
			compose = true
			break
		}
	}
	networks := make(map[string]*network.EndpointSettings)
	publishedPorts := make(network.PortMap)
	if item.NetworkSettings != nil {
		for name, endpoint := range item.NetworkSettings.Networks {
			if endpoint != nil {
				networks[name] = endpoint
			}
		}
		for port, bindings := range item.NetworkSettings.Ports {
			if len(bindings) > 0 {
				publishedPorts[port] = append([]network.PortBinding(nil), bindings...)
			}
		}
	}
	writableSize := int64(0)
	if item.SizeRw != nil {
		writableSize = *item.SizeRw
	}
	health := "none"
	if item.State.Health != nil {
		health = string(item.State.Health.Status)
	}
	return Container{
		ID: item.ID, Name: item.Name, ImageID: item.Image, State: string(item.State.Status),
		Running: item.State.Running, Paused: item.State.Paused, Restarting: item.State.Restarting,
		AutoRemove: item.HostConfig.AutoRemove, HostNetwork: string(item.HostConfig.NetworkMode) == "host", Compose: compose,
		WritableSize: writableSize, Health: health, Config: item.Config, HostConfig: item.HostConfig,
		Networks: networks, PublishedPorts: publishedPorts, Mounts: append([]container.MountPoint(nil), item.Mounts...),
	}, nil
}

func (e *DockerEngine) Info(ctx context.Context) (DockerInfo, error) {
	result, err := e.client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return DockerInfo{}, err
	}
	if result.Info.DockerRootDir == "" {
		return DockerInfo{}, errors.New("Docker did not report its storage root")
	}
	return DockerInfo{RootDirectory: result.Info.DockerRootDir}, nil
}

func (e *DockerEngine) ImageInspect(ctx context.Context, reference string) (string, map[string]string, error) {
	result, err := e.client.ImageInspect(ctx, reference)
	if err != nil {
		return "", nil, err
	}
	return result.ID, cloneStrings(result.Config.Labels), nil
}

func (e *DockerEngine) Commit(ctx context.Context, id, reference string, labels map[string]string) (string, error) {
	before, err := e.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || before.Container.Config == nil {
		return "", errors.New("container could not be inspected before writable-layer snapshot")
	}
	config, err := cloneJSON(*before.Container.Config)
	if err != nil {
		return "", errors.New("container config could not be copied for writable-layer snapshot")
	}
	config.Labels = cloneStrings(config.Labels)
	for key, value := range labels {
		config.Labels[key] = value
	}
	result, err := e.client.ContainerCommit(ctx, id, client.ContainerCommitOptions{
		Reference: reference, Comment: "NodeDance controlled rebuild writable-layer snapshot", Config: &config,
	})
	if err != nil {
		return "", err
	}
	return result.ID, nil
}

func (e *DockerEngine) Stop(ctx context.Context, id string, timeoutSeconds int) error {
	if timeoutSeconds < 1 || timeoutSeconds > maxStopTimeout {
		return errors.New("container stop timeout is outside bounds")
	}
	timeout := int(timeoutSeconds)
	_, err := e.client.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

func (e *DockerEngine) UpdateRestartPolicy(ctx context.Context, id string, policy container.RestartPolicy) error {
	_, err := e.client.ContainerUpdate(ctx, id, client.ContainerUpdateOptions{RestartPolicy: &policy})
	return err
}

func (e *DockerEngine) Rename(ctx context.Context, id, name string) error {
	_, err := e.client.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name})
	return err
}

func (e *DockerEngine) DisconnectNetwork(ctx context.Context, networkID, id string) error {
	_, err := e.client.NetworkDisconnect(ctx, networkID, client.NetworkDisconnectOptions{Container: id, Force: false})
	return err
}

func (e *DockerEngine) ConnectNetwork(ctx context.Context, networkID, id string, endpoint *network.EndpointSettings) error {
	if endpoint == nil {
		return errors.New("network endpoint config is unavailable")
	}
	copyEndpoint, err := cloneJSON(*endpoint)
	if err != nil {
		return err
	}
	clearEndpointRuntimeFields(&copyEndpoint)
	_, err = e.client.NetworkConnect(ctx, networkID, client.NetworkConnectOptions{Container: id, EndpointConfig: &copyEndpoint})
	return err
}

func (e *DockerEngine) Create(ctx context.Context, name string, config *container.Config, host *container.HostConfig, endpoints map[string]*network.EndpointSettings) (string, error) {
	if config == nil || host == nil {
		return "", ErrUnsupportedConfiguration
	}
	result, err := e.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: config, HostConfig: host, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: endpoints}, Name: name,
	})
	if err != nil {
		return "", err
	}
	return result.ID, nil
}

func (e *DockerEngine) Start(ctx context.Context, id string) error {
	_, err := e.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (e *DockerEngine) Remove(ctx context.Context, id string) error {
	_, err := e.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: false, RemoveLinks: false, Force: false})
	return err
}

func (e *DockerEngine) RemoveImage(ctx context.Context, reference string) error {
	_, err := e.client.ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: false, PruneChildren: false})
	return err
}

func (e *DockerEngine) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}

func IsNotFound(err error) bool { return err != nil && errdefs.IsNotFound(err) }

func (e *DockerEngine) String() string { return fmt.Sprintf("DockerEngine(%p)", e.client) }
