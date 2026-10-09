package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/client"
)

// Engine is the small read-only surface used by discovery. Implementations
// must return all container IDs, including stopped containers.
type Engine interface {
	Ping(context.Context) error
	ListAll(context.Context) ([]string, error)
	Inspect(context.Context, string) (Container, error)
	OpenEvents(context.Context) (EventStream, error)
}

type EventStream struct {
	Events <-chan Event
	Errors <-chan error
	Close  func()
}

// SDKEngine uses the pinned Moby Go client. The default is the local Unix
// socket; remote TCP is intentionally not enabled by this Linux Agent module.
type SDKEngine struct {
	client *client.Client
}

// Docker's default stop grace period is 10 seconds. Leave room for the Engine
// to send the response headers after it stops a process that ignores SIGTERM.
// This bounds only response-header establishment; request contexts still own
// operation cancellation and long-lived stream shutdown.
const sdkResponseHeaderTimeout = 15 * time.Second

func NewSDKEngine(socket string) (*SDKEngine, error) {
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(socket, "unix:///") {
		return nil, fmt.Errorf("Docker Engine host must be a local unix socket")
	}
	// Events keeps its request context for the lifetime of the streaming body.
	// Bound only the response-header wait at the HTTP transport layer so an
	// event subscription can remain alive for the whole Discoverer run.
	httpClient := &http.Client{
		Transport:     &http.Transport{ResponseHeaderTimeout: sdkResponseHeaderTimeout},
		CheckRedirect: client.CheckRedirect,
	}
	cli, err := client.New(
		client.WithHTTPClient(httpClient),
		client.WithHost(socket),
		client.WithScheme("http"),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("create Docker Engine client: %w", err)
	}
	return &SDKEngine{client: cli}, nil
}

func (e *SDKEngine) Ping(ctx context.Context) error {
	_, err := e.client.Ping(ctx, client.PingOptions{})
	return err
}

func (e *SDKEngine) ListAll(ctx context.Context) ([]string, error) {
	result, err := e.client.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		if item.ID == "" {
			return nil, errors.New("Docker Engine returned a container without an ID")
		}
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

func (e *SDKEngine) Inspect(ctx context.Context, id string) (Container, error) {
	result, err := e.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, err
	}
	return normalizeInspect(result.Raw, time.Now().UTC())
}

func (e *SDKEngine) OpenEvents(ctx context.Context) (EventStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	result := e.client.Events(streamCtx, client.EventsListOptions{
		Filters: client.Filters{}.Add("type", "container"),
	})
	events := make(chan Event, 32)
	errorsOut := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errorsOut)
		for {
			select {
			case message, ok := <-result.Messages:
				if !ok {
					return
				}
				if message.Type != "container" || message.Actor.ID == "" {
					continue
				}
				observed := time.Now().UTC()
				if message.TimeNano > 0 {
					observed = time.Unix(0, message.TimeNano).UTC()
				} else if message.Time > 0 {
					observed = time.Unix(message.Time, 0).UTC()
				}
				item := Event{ContainerID: message.Actor.ID, Action: string(message.Action), EngineTime: observed}
				select {
				case events <- item:
				case <-streamCtx.Done():
					return
				}
			case err, ok := <-result.Err:
				if ok && err != nil && streamCtx.Err() == nil {
					select {
					case errorsOut <- err:
					default:
					}
				}
				return
			case <-streamCtx.Done():
				return
			}
		}
	}()
	return EventStream{Events: events, Errors: errorsOut, Close: cancel}, nil
}

func (e *SDKEngine) Close() error { return e.client.Close() }

// Client exposes the process-owned client for narrow feature adapters. The
// runtime remains responsible for its lifetime.
func (e *SDKEngine) Client() *client.Client {
	if e == nil {
		return nil
	}
	return e.client
}

type inspectResponse struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	ImageID string `json:"Image"`
	Created string `json:"Created"`
	Restart int    `json:"RestartCount"`
	State   *struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		ExitCode   int    `json:"ExitCode"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config *struct {
		Image        string                     `json:"Image"`
		ExposedPorts map[string]json.RawMessage `json:"ExposedPorts"`
		Labels       map[string]string          `json:"Labels"`
		Healthcheck  *struct {
			Test []string `json:"Test"`
		} `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig *struct {
		NetworkMode     string `json:"NetworkMode"`
		PublishAllPorts bool   `json:"PublishAllPorts"`
		PortBindings    map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings *struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]*struct {
			NetworkID         string   `json:"NetworkID"`
			IPAddress         string   `json:"IPAddress"`
			GlobalIPv6Address string   `json:"GlobalIPv6Address"`
			Gateway           string   `json:"Gateway"`
			IPv6Gateway       string   `json:"IPv6Gateway"`
			Aliases           []string `json:"Aliases"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Driver      string `json:"Driver"`
		Mode        string `json:"Mode"`
		Propagation string `json:"Propagation"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

func normalizeInspect(raw []byte, observedAt time.Time) (Container, error) {
	var decoded inspectResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Container{}, fmt.Errorf("decode Docker inspect response: %w", err)
	}
	if decoded.ID == "" {
		return Container{}, errors.New("Docker inspect response has no container ID")
	}
	out := Container{
		ID:           decoded.ID,
		Name:         canonicalName(decoded.Name),
		ImageID:      decoded.ImageID,
		CreatedAt:    parseDockerTime(decoded.Created),
		RestartCount: decoded.Restart,
		ObservedAt:   observedAt.UTC(),
		Health:       HealthUnknown,
		Ports:        []Port{},
		Networks:     []Network{},
		Mounts:       []Mount{},
	}
	if decoded.Config != nil {
		out.Image = decoded.Config.Image
		out.Compose = composeIdentity(decoded.Config.Labels)
		out.HealthcheckConfigured = healthcheckConfigured(decoded.Config.Healthcheck)
		if !out.HealthcheckConfigured {
			out.Health = HealthNone
		} else {
			out.HealthReason = "Docker has not reported a health-check result"
		}
		for key := range decoded.Config.ExposedPorts {
			port, err := ensurePort(&out.Ports, key)
			if err != nil {
				return Container{}, err
			}
			port.Exposed = true
		}
	}
	if decoded.HostConfig != nil {
		out.HostNetwork = strings.EqualFold(decoded.HostConfig.NetworkMode, "host")
		out.PublishAllPorts = decoded.HostConfig.PublishAllPorts
		for key, bindings := range decoded.HostConfig.PortBindings {
			port, err := ensurePort(&out.Ports, key)
			if err != nil {
				return Container{}, err
			}
			for _, binding := range bindings {
				hostPort, ok := parseDockerPort(binding.HostPort)
				if !ok {
					return Container{}, fmt.Errorf("Docker returned invalid configured host port %q", binding.HostPort)
				}
				port.Configured = append(port.Configured, HostPort{IP: normalizeAddress(binding.HostIP), Port: hostPort})
			}
		}
	}
	if decoded.State != nil {
		out.State = decoded.State.Status
		out.Running = decoded.State.Running
		out.Paused = decoded.State.Paused
		out.Restarting = decoded.State.Restarting
		out.StartedAt = parseDockerTime(decoded.State.StartedAt)
		out.FinishedAt = parseDockerTime(decoded.State.FinishedAt)
		exit := decoded.State.ExitCode
		if out.FinishedAt != nil || strings.EqualFold(decoded.State.Status, "exited") || strings.EqualFold(decoded.State.Status, "dead") {
			out.ExitCode = &exit
		}
		if out.HealthcheckConfigured {
			if decoded.State.Health != nil {
				out.Health = normalizeHealth(decoded.State.Health.Status)
				out.HealthReason = ""
			} else if strings.EqualFold(decoded.State.Status, "running") {
				out.Health = HealthStarting
				out.HealthReason = "Docker has not reported the first health-check result"
			}
		}
	} else if out.HealthcheckConfigured {
		out.HealthReason = "Docker has not reported container state"
	}
	if decoded.NetworkSettings != nil {
		for name, endpoint := range decoded.NetworkSettings.Networks {
			if endpoint == nil {
				continue
			}
			out.Networks = append(out.Networks, Network{
				Name:        name,
				ID:          endpoint.NetworkID,
				IPv4:        normalizeAddress(endpoint.IPAddress),
				IPv6:        normalizeAddress(endpoint.GlobalIPv6Address),
				Gateway:     normalizeAddress(endpoint.Gateway),
				IPv6Gateway: normalizeAddress(endpoint.IPv6Gateway),
				Aliases:     append([]string(nil), endpoint.Aliases...),
			})
		}
		if out.Running && !out.HostNetwork {
			for key, bindings := range decoded.NetworkSettings.Ports {
				port, err := ensurePort(&out.Ports, key)
				if err != nil {
					return Container{}, err
				}
				for _, binding := range bindings {
					actual, ok := parseDockerPort(binding.HostPort)
					if !ok || actual == "" {
						// An empty HostPort means Docker has not allocated/published a
						// host port yet; it must not be presented as an active mapping.
						continue
					}
					port.Published = append(port.Published, HostPort{IP: normalizeAddress(binding.HostIP), Port: actual})
				}
			}
		}
	}
	for _, mount := range decoded.Mounts {
		out.Mounts = append(out.Mounts, Mount{
			Type: mount.Type, Name: mount.Name, Source: mount.Source,
			Destination: mount.Destination, Driver: mount.Driver,
			Mode: mount.Mode, Propagation: mount.Propagation, ReadWrite: mount.RW,
		})
	}
	sortContainerData(&out)
	return out, nil
}

func composeIdentity(labels map[string]string) *ComposeIdentity {
	const prefix = "com.docker.compose."
	project := labels[prefix+"project"]
	service := labels[prefix+"service"]
	if project == "" && service == "" {
		return nil
	}
	return &ComposeIdentity{
		Project:         project,
		Service:         service,
		WorkingDir:      labels[prefix+"project.working_dir"],
		ConfigFiles:     labels[prefix+"project.config_files"],
		ContainerNumber: labels[prefix+"container-number"],
		OneOff:          strings.EqualFold(labels[prefix+"oneoff"], "true"),
		Version:         labels[prefix+"version"],
	}
}

func healthcheckConfigured(config *struct {
	Test []string `json:"Test"`
}) bool {
	if config == nil || len(config.Test) == 0 {
		return false
	}
	return !strings.EqualFold(config.Test[0], "NONE")
}

func normalizeHealth(value string) HealthState {
	switch strings.ToLower(value) {
	case "starting":
		return HealthStarting
	case "healthy":
		return HealthHealthy
	case "unhealthy":
		return HealthUnhealthy
	case "none":
		return HealthNone
	default:
		return HealthUnknown
	}
}

func ensurePort(ports *[]Port, key string) (*Port, error) {
	number, protocol, err := parsePortKey(key)
	if err != nil {
		return nil, err
	}
	for i := range *ports {
		if (*ports)[i].ContainerPort == number && (*ports)[i].Protocol == protocol {
			return &(*ports)[i], nil
		}
	}
	*ports = append(*ports, Port{ContainerPort: number, Protocol: protocol})
	return &(*ports)[len(*ports)-1], nil
}

func parsePortKey(value string) (uint16, string, error) {
	portText, protocol, found := strings.Cut(value, "/")
	if !found || protocol == "" {
		return 0, "", fmt.Errorf("Docker returned invalid port key %q", value)
	}
	portNumber, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portNumber == 0 {
		return 0, "", fmt.Errorf("Docker returned invalid port key %q", value)
	}
	protocol = strings.ToLower(protocol)
	if protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
		return 0, "", fmt.Errorf("Docker returned unsupported protocol in port key %q", value)
	}
	return uint16(portNumber), protocol, nil
}

func normalizeAddress(value string) string {
	if value == "" {
		return ""
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return value
	}
	return address.String()
}

func parseDockerTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func parseDockerPort(value string) (string, bool) {
	if value == "" {
		return "", true // Docker uses an empty value for a requested dynamic port.
	}
	n, err := strconv.ParseUint(value, 10, 16)
	if err == nil && n == 0 {
		return "", true
	}
	if err != nil || n > 65535 {
		return "", false
	}
	return strconv.FormatUint(n, 10), true
}

func canonicalIP(address netip.Addr) string {
	if !address.IsValid() {
		return ""
	}
	return address.String()
}
