package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
)

// Image is a bounded projection of Docker Engine image metadata. It omits
// labels and raw Inspect output because both can contain operator data.
type Image struct {
	ID         string   `json:"id"`
	Tags       []string `json:"tags"`
	Digests    []string `json:"digests"`
	Size       int64    `json:"size"`
	CreatedAt  int64    `json:"createdAt"`
	Containers int      `json:"containers"`
}

type PullProgress struct {
	Completed uint64
	Total     uint64
}

type Engine interface {
	List(context.Context) ([]Image, error)
	Inspect(context.Context, string) (Image, error)
	Pull(context.Context, string, string, func(PullProgress)) error
	ContainersUsing(context.Context, string) ([]string, error)
	Remove(context.Context, string) error
}

// SDKEngine uses one local Docker Engine client. It has no inbound listener and
// never persists registry credentials.
type SDKEngine struct {
	client *client.Client
}

func NewSDKEngine(socket string) (*SDKEngine, error) {
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(socket, "unix:///") {
		return nil, errors.New("Docker Engine host must be a local Unix socket")
	}
	cli, err := client.New(client.WithHost(socket), client.WithScheme("http"), client.WithAPIVersionNegotiation(), client.WithTimeout(10*time.Minute))
	if err != nil {
		return nil, errors.New("could not create Docker image client")
	}
	return &SDKEngine{client: cli}, nil
}

func (e *SDKEngine) List(ctx context.Context) ([]Image, error) {
	result, err := e.client.ImageList(ctx, client.ImageListOptions{All: true})
	if err != nil {
		return nil, errors.New("Docker image list failed")
	}
	refs := make(map[string]int)
	containers, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, errors.New("Docker container reference lookup failed")
	}
	for _, container := range containers.Items {
		refs[container.ImageID]++
	}
	images := make([]Image, 0, len(result.Items))
	for _, value := range result.Items {
		images = append(images, Image{ID: value.ID, Tags: cleanStrings(value.RepoTags), Digests: cleanStrings(value.RepoDigests),
			Size: value.Size, CreatedAt: value.Created, Containers: refs[value.ID]})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].ID < images[j].ID })
	return images, nil
}

func (e *SDKEngine) Inspect(ctx context.Context, id string) (Image, error) {
	value, err := e.client.ImageInspect(ctx, id)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return Image{}, err
		}
		return Image{}, errors.New("Docker image inspection failed")
	}
	refs, err := e.ContainersUsing(ctx, value.ID)
	if err != nil {
		return Image{}, err
	}
	created := int64(0)
	if value.Created != "" {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, value.Created); parseErr == nil {
			created = parsed.Unix()
		}
	}
	return Image{ID: value.ID, Tags: cleanStrings(value.RepoTags), Digests: cleanStrings(value.RepoDigests),
		Size: value.Size, CreatedAt: created, Containers: len(refs)}, nil
}

func (e *SDKEngine) Pull(ctx context.Context, ref, authHeader string, report func(PullProgress)) error {
	if _, err := reference.ParseNormalizedNamed(ref); err != nil {
		return errors.New("invalid image reference")
	}
	response, err := e.client.ImagePull(ctx, ref, client.ImagePullOptions{RegistryAuth: authHeader})
	if err != nil {
		if errdefs.IsUnauthorized(err) {
			return ErrUnauthorized
		}
		return errors.New("Docker image pull request failed")
	}
	defer response.Close()
	layerCurrent := make(map[string]int64)
	layerTotals := make(map[string]int64)
	for message, streamErr := range response.JSONMessages(ctx) {
		if streamErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Docker image pull stream was interrupted")
		}
		if message.Error != nil {
			if message.Error.Code == 401 || strings.Contains(strings.ToLower(message.Error.Message), "unauthorized") {
				return ErrUnauthorized
			}
			return errors.New("Docker registry rejected the image pull")
		}
		if message.Progress == nil || message.ID == "" || message.Progress.Current < 0 || message.Progress.Total < 0 {
			continue
		}
		layerCurrent[message.ID] = message.Progress.Current
		layerTotals[message.ID] = message.Progress.Total
		var currentBytes, totalBytes int64
		for id, value := range layerTotals {
			totalBytes += value
			currentBytes += layerCurrent[id]
		}
		if report != nil {
			report(PullProgress{Completed: uint64(max(0, currentBytes)), Total: uint64(max(0, totalBytes))})
		}
	}
	if err := response.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Docker image pull did not complete")
	}
	return nil
}

func (e *SDKEngine) ContainersUsing(ctx context.Context, imageID string) ([]string, error) {
	containers, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, errors.New("Docker container reference lookup failed")
	}
	ids := make([]string, 0)
	for _, container := range containers.Items {
		if container.ImageID == imageID {
			ids = append(ids, container.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *SDKEngine) Remove(ctx context.Context, imageID string) error {
	// Re-check immediately before Engine remove. Force and prune are explicitly
	// false; Docker itself remains the final race-safe reference guard.
	refs, err := e.ContainersUsing(ctx, imageID)
	if err != nil {
		return err
	}
	if len(refs) != 0 {
		return ErrImageInUse
	}
	_, err = e.client.ImageRemove(ctx, imageID, client.ImageRemoveOptions{Force: false, PruneChildren: false})
	if err != nil {
		if errdefs.IsConflict(err) {
			return ErrImageInUse
		}
		if errdefs.IsNotFound(err) {
			return err
		}
		return errors.New("Docker image removal failed")
	}
	return nil
}

func (e *SDKEngine) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && value != "<none>:<none>" {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

var (
	ErrUnauthorized  = errors.New("registry authentication failed")
	ErrImageInUse    = errors.New("image is referenced by a container")
	ErrImageNotFound = errors.New("image was not found")
)

// EncodeRegistryCredentials builds Docker's transient RegistryAuth header.
// The returned value must remain in the request's memory lifetime only.
func EncodeRegistryCredentials(username, password string) (string, error) {
	if len(username) == 0 || len(username) > 256 || len(password) == 0 || len(password) > 4096 {
		return "", errors.New("registry credentials are invalid")
	}
	data, err := json.Marshal(registry.AuthConfig{Username: username, Password: password})
	if err != nil {
		return "", errors.New("could not encode registry credentials")
	}
	defer clear(data)
	return base64.URLEncoding.EncodeToString(data), nil
}
