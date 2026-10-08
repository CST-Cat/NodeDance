// Package containerstreams provides bounded, cancellation-aware Docker log and
// on-demand statistics streams. It has no Agent runtime, transport, or browser
// session integration.
package containerstreams

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

const (
	defaultResponseHeaderTimeout = 8 * time.Second
	defaultInspectTimeout        = 10 * time.Second
)

var ErrInvalidContainerID = errors.New("container stream requires a full Docker container ID")

// Engine is the narrow, read-only Docker API used by the stream components.
// Every returned body belongs to the caller and must be closed.
type Engine interface {
	Inspect(context.Context, string) (ContainerInfo, error)
	OpenLogs(context.Context, string, LogsOptions) (io.ReadCloser, error)
	OpenStats(context.Context, string) (io.ReadCloser, error)
}

type ContainerInfo struct {
	ID  string
	TTY bool
}

// SDKEngine wraps the Moby SDK with a response-header deadline and no whole
// response timeout. Log follow and stats bodies remain attached until their
// caller cancels or closes them.
type SDKEngine struct {
	client         *client.Client
	inspectTimeout time.Duration
}

func NewSDKEngine(socket string) (*SDKEngine, error) {
	return newSDKEngine(socket, defaultResponseHeaderTimeout, defaultInspectTimeout)
}

func newSDKEngine(socket string, responseHeaderTimeout, inspectTimeout time.Duration) (*SDKEngine, error) {
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(socket, "unix:///") {
		return nil, errors.New("Docker Engine host must be a local Unix socket")
	}
	if responseHeaderTimeout <= 0 || responseHeaderTimeout > time.Minute {
		return nil, errors.New("Docker response-header timeout is outside the supported bounds")
	}
	if inspectTimeout <= 0 || inspectTimeout > time.Minute {
		return nil, errors.New("Docker inspect timeout is outside the supported bounds")
	}
	httpClient := &http.Client{
		Transport:     &http.Transport{ResponseHeaderTimeout: responseHeaderTimeout},
		CheckRedirect: client.CheckRedirect,
		// Deliberately leave http.Client.Timeout at zero. A total timeout would
		// terminate otherwise healthy follow-log and stats streams.
	}
	cli, err := client.New(
		client.WithHTTPClient(httpClient),
		client.WithHost(socket),
		client.WithScheme("http"),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create Docker stream client: %w", err)
	}
	return &SDKEngine{client: cli, inspectTimeout: inspectTimeout}, nil
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

func (e *SDKEngine) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}

func dockerTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
