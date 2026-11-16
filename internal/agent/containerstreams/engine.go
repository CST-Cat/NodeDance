package containerstreams

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/moby/moby/client"
)

const defaultInspectTimeout = 10 * time.Second

var ErrInvalidContainerID = errors.New("container stream requires a full Docker container ID")

type Engine interface {
	Inspect(context.Context, string) (ContainerInfo, error)
	OpenLogs(context.Context, string, LogsOptions) (io.ReadCloser, error)
	OpenStats(context.Context, string) (io.ReadCloser, error)
}

type ContainerInfo struct {
	ID  string
	TTY bool
}

type SDKEngine struct {
	client         *client.Client
	inspectTimeout time.Duration
}

func NewEngine(cli *client.Client) (*SDKEngine, error) {
	if cli == nil {
		return nil, errors.New("shared Docker Engine client is required")
	}
	return &SDKEngine{client: cli, inspectTimeout: defaultInspectTimeout}, nil
}

func (e *SDKEngine) Inspect(ctx context.Context, id string) (ContainerInfo, error) {
	if ctx == nil {
		return ContainerInfo{}, errors.New("Docker inspect context is required")
	}
	if err := validateID(id); err != nil {
		return ContainerInfo{}, err
	}
	inspectCtx, cancel := context.WithTimeout(ctx, e.inspectTimeout)
	defer cancel()
	result, err := e.client.ContainerInspect(inspectCtx, id, client.ContainerInspectOptions{})
	if err != nil {
		return ContainerInfo{}, err
	}
	container := result.Container
	if container.ID == "" || container.Config == nil {
		return ContainerInfo{}, errors.New("Docker returned an incomplete container inspection")
	}
	return ContainerInfo{ID: container.ID, TTY: container.Config.Tty}, nil
}

func (e *SDKEngine) OpenLogs(ctx context.Context, id string, options LogsOptions) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, errors.New("Docker log request context is required")
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	var err error
	options, err = normalizeLogsOptions(options)
	if err != nil {
		return nil, err
	}
	result, err := e.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: options.ShowStdout,
		ShowStderr: options.ShowStderr,
		Since:      dockerTime(options.Since),
		Until:      dockerTime(options.Until),
		Timestamps: options.Timestamps,
		Follow:     options.Follow,
		Tail:       options.Tail,
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (e *SDKEngine) OpenStats(ctx context.Context, id string) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, errors.New("Docker stats request context is required")
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	result, err := e.client.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: true})
	if err != nil {
		return nil, err
	}
	return result.Body, nil
}

func dockerTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
