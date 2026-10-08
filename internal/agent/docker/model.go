// Package docker discovers and normalizes the local Docker Engine inventory.
// It is read-only: user-authorized Engine mutations are implemented by S05.
package docker

import (
	"context"
	"sort"
	"strings"
	"time"
)

// HealthState is Docker's application health state. It is deliberately separate
// from State: a running process does not imply a healthy application.
type HealthState string

const (
	HealthUnknown   HealthState = "unknown"
	HealthNone      HealthState = "none"
	HealthStarting  HealthState = "starting"
	HealthHealthy   HealthState = "healthy"
	HealthUnhealthy HealthState = "unhealthy"
)

// Container is the read-only, normalized view used by the Agent and Core.
// Times are pointers because Docker uses an empty/zero timestamp for a
// container that has never started or has no exit time.
type Container struct {
	ID                    string           `json:"id"`
	Name                  string           `json:"name"`
	Image                 string           `json:"image"`
	ImageID               string           `json:"image_id"`
	State                 string           `json:"state"`
	Running               bool             `json:"running"`
	Paused                bool             `json:"paused"`
	Restarting            bool             `json:"restarting"`
	Health                HealthState      `json:"health"`
	HealthcheckConfigured bool             `json:"healthcheck_configured"`
	HealthReason          string           `json:"health_reason,omitempty"`
	UnavailableReason     string           `json:"unavailable_reason,omitempty"`
	Stale                 bool             `json:"stale"`
	CreatedAt             *time.Time       `json:"created_at,omitempty"`
	StartedAt             *time.Time       `json:"started_at,omitempty"`
	FinishedAt            *time.Time       `json:"finished_at,omitempty"`
	ExitCode              *int             `json:"exit_code,omitempty"`
	RestartCount          int              `json:"restart_count"`
	HostNetwork           bool             `json:"host_network"`
	PublishAllPorts       bool             `json:"publish_all_ports"`
	Compose               *ComposeIdentity `json:"compose,omitempty"`
	Ports                 []Port           `json:"ports"`
	Networks              []Network        `json:"networks"`
	Mounts                []Mount          `json:"mounts"`
	ObservedAt            time.Time        `json:"observed_at"`
}

// Port keeps image/container exposure, the requested host binding, and the
// Engine's actual published binding distinct. Empty Published means Docker has
// not confirmed a listening host port; EXPOSE alone can never appear published.
type Port struct {
	ContainerPort uint16     `json:"container_port"`
	Protocol      string     `json:"protocol"`
	Exposed       bool       `json:"exposed"`
	Configured    []HostPort `json:"configured,omitempty"`
	Published     []HostPort `json:"published,omitempty"`
}

type HostPort struct {
	IP   string `json:"ip,omitempty"`
	Port string `json:"port,omitempty"`
}

type Network struct {
	Name        string   `json:"name"`
	ID          string   `json:"id,omitempty"`
	IPv4        string   `json:"ipv4,omitempty"`
	IPv6        string   `json:"ipv6,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	IPv6Gateway string   `json:"ipv6_gateway,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

type Mount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	Driver      string `json:"driver,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Propagation string `json:"propagation,omitempty"`
	ReadWrite   bool   `json:"read_write"`
}

// ComposeIdentity contains only the standard Compose labels needed to keep a
// service associated with its project and source files. Arbitrary labels,
// environment values, and inspect payloads never leave the Agent.
type ComposeIdentity struct {
	Project         string `json:"project"`
	Service         string `json:"service"`
	WorkingDir      string `json:"working_dir,omitempty"`
	ConfigFiles     string `json:"config_files,omitempty"`
	ContainerNumber string `json:"container_number,omitempty"`
	OneOff          bool   `json:"one_off"`
	Version         string `json:"version,omitempty"`
}

type ChangeAction string

const (
	ChangeUpsert ChangeAction = "upsert"
	ChangeDelete ChangeAction = "delete"
	ChangeStale  ChangeAction = "stale"
)

type Change struct {
	Sequence    uint64       `json:"sequence"`
	Action      ChangeAction `json:"action"`
	Container   *Container   `json:"container,omitempty"`
	ContainerID string       `json:"container_id"`
	Reason      string       `json:"reason,omitempty"`
	ObservedAt  time.Time    `json:"observed_at"`
}

// Event is the small subset of a Docker event required for reconciliation.
// Event attributes are never treated as authoritative container state.
type Event struct {
	ContainerID string
	Action      string
	EngineTime  time.Time
}

type EngineAvailability string

const (
	EngineAvailable   EngineAvailability = "available"
	EngineUnavailable EngineAvailability = "unavailable"
)

type Health struct {
	Sequence        uint64             `json:"sequence"`
	Availability    EngineAvailability `json:"availability"`
	EventsConnected bool               `json:"events_connected"`
	LastSuccessAt   *time.Time         `json:"last_success_at,omitempty"`
	LastSnapshotAt  *time.Time         `json:"last_snapshot_at,omitempty"`
	SnapshotFresh   bool               `json:"snapshot_fresh"`
	ErrorKind       string             `json:"error_kind,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	ObservedAt      time.Time          `json:"observed_at"`
}

// Batch is sent asynchronously to an Observer. FullSnapshot batches are
// bounded to MaxBatchChanges entries. The receiver stages them by SnapshotID
// and only replaces its active inventory after SnapshotFinal is true.
type Batch struct {
	Sequence      uint64   `json:"sequence"`
	SnapshotID    uint64   `json:"snapshot_id,omitempty"`
	FullSnapshot  bool     `json:"full_snapshot,omitempty"`
	SnapshotIndex int      `json:"snapshot_index,omitempty"`
	SnapshotFinal bool     `json:"snapshot_final,omitempty"`
	Changes       []Change `json:"changes,omitempty"`
	Health        *Health  `json:"health,omitempty"`
}

// Observer receives bounded updates on a dedicated worker. Implementations
// must honor ctx. Slow or failing observers cannot block Engine events or the
// Agent's independent heartbeat loop; queue overflow causes a fresh snapshot.
type Observer interface {
	ApplyDockerBatch(ctx context.Context, batch Batch) error
}

func cloneContainer(c Container) Container {
	out := c
	out.CreatedAt = cloneTime(c.CreatedAt)
	out.StartedAt = cloneTime(c.StartedAt)
	out.FinishedAt = cloneTime(c.FinishedAt)
	out.ExitCode = cloneInt(c.ExitCode)
	out.Ports = append([]Port(nil), c.Ports...)
	for i := range out.Ports {
		out.Ports[i].Configured = append([]HostPort(nil), c.Ports[i].Configured...)
		out.Ports[i].Published = append([]HostPort(nil), c.Ports[i].Published...)
	}
	out.Networks = append([]Network(nil), c.Networks...)
	for i := range out.Networks {
		out.Networks[i].Aliases = append([]string(nil), c.Networks[i].Aliases...)
	}
	out.Mounts = append([]Mount(nil), c.Mounts...)
	if c.Compose != nil {
		copy := *c.Compose
		out.Compose = &copy
	}
	return out
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func canonicalName(value string) string {
	return strings.TrimPrefix(value, "/")
}

func sortContainerData(c *Container) {
	sort.Slice(c.Ports, func(i, j int) bool {
		if c.Ports[i].ContainerPort != c.Ports[j].ContainerPort {
			return c.Ports[i].ContainerPort < c.Ports[j].ContainerPort
		}
		return c.Ports[i].Protocol < c.Ports[j].Protocol
	})
	for i := range c.Ports {
		sort.Slice(c.Ports[i].Configured, func(a, b int) bool {
			if c.Ports[i].Configured[a].IP != c.Ports[i].Configured[b].IP {
				return c.Ports[i].Configured[a].IP < c.Ports[i].Configured[b].IP
			}
			return c.Ports[i].Configured[a].Port < c.Ports[i].Configured[b].Port
		})
		sort.Slice(c.Ports[i].Published, func(a, b int) bool {
			if c.Ports[i].Published[a].IP != c.Ports[i].Published[b].IP {
				return c.Ports[i].Published[a].IP < c.Ports[i].Published[b].IP
			}
			return c.Ports[i].Published[a].Port < c.Ports[i].Published[b].Port
		})
	}
	sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Name < c.Networks[j].Name })
	for i := range c.Networks {
		sort.Strings(c.Networks[i].Aliases)
	}
	sort.Slice(c.Mounts, func(i, j int) bool {
		if c.Mounts[i].Destination != c.Mounts[j].Destination {
			return c.Mounts[i].Destination < c.Mounts[j].Destination
		}
		return c.Mounts[i].Source < c.Mounts[j].Source
	})
}
