package containeractions

import (
	"context"
	"errors"
	"strings"

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
		Running:        value.State.Running,
		Paused:         value.State.Paused,
		Restarting:     value.State.Restarting,
		StartedAt:      value.State.StartedAt,
		RestartCount:   value.RestartCount,
		ComposeManaged: composeManaged,
	}, nil
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
