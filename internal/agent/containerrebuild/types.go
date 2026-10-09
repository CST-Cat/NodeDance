// Package containerrebuild plans and executes conservative rebuilds of
// non-Compose Docker containers. Named, anonymous, and bind-mounted data is
// always preserved by reference; Docker image snapshots cover only the
// container writable layer and are never represented as volume backups.
package containerrebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

var (
	ErrUnsupportedConfiguration = errors.New("container configuration cannot be safely rebuilt")
	ErrInsufficientSpace        = errors.New("Docker storage has insufficient free space for a writable-layer snapshot")
	ErrSnapshotFailed           = errors.New("container writable-layer snapshot failed")
	ErrRecoveryRequired         = errors.New("container rebuild requires recovery")
	ErrRecordConflict           = errors.New("container rebuild record conflicts with Docker resources")
	ErrHealthCheckFailed        = errors.New("rebuilt container failed health verification")
	ErrPortVerificationFailed   = errors.New("rebuilt container port bindings could not be verified")
	ErrCleanupConfirmation      = errors.New("rollback cleanup confirmation does not match the target")
)

const (
	markerTaskLabel        = "io.nodedance.rebuild.task_id"
	markerOwnerLabel       = "io.nodedance.rebuild.owner"
	minimumSnapshotReserve = uint64(64 << 20)
)

// Container contains a single fresh Engine Inspect. Configuration fields are
// kept only in Agent memory; plans and API responses use the redacted Plan DTO.
type Container struct {
	ID             string
	Name           string
	ImageID        string
	State          string
	Running        bool
	Paused         bool
	Restarting     bool
	AutoRemove     bool
	HostNetwork    bool
	Compose        bool
	WritableSize   int64
	Health         string
	Config         *container.Config
	HostConfig     *container.HostConfig
	Networks       map[string]*network.EndpointSettings
	PublishedPorts network.PortMap
	Mounts         []container.MountPoint
}

type DockerInfo struct {
	RootDirectory string
}

type Engine interface {
	Inspect(context.Context, string) (Container, error)
	Info(context.Context) (DockerInfo, error)
	ImageInspect(context.Context, string) (id string, labels map[string]string, err error)
	Commit(context.Context, string, string, map[string]string) (string, error)
	Stop(context.Context, string, int) error
	UpdateRestartPolicy(context.Context, string, container.RestartPolicy) error
	Rename(context.Context, string, string) error
	DisconnectNetwork(context.Context, string, string) error
	ConnectNetwork(context.Context, string, string, *network.EndpointSettings) error
	Create(context.Context, string, *container.Config, *container.HostConfig, map[string]*network.EndpointSettings) (string, error)
	Start(context.Context, string) error
	Remove(context.Context, string) error
	RemoveImage(context.Context, string) error
}

type TaskJournal interface {
	Enqueue(context.Context, taskstate.Identity) (taskjournal.EnqueueResult, error)
	BeginExecution(context.Context, string) error
	MarkUnknown(context.Context, string) error
	Finish(context.Context, string, taskstate.Status, taskstate.Evidence, taskjournal.Result) error
	Get(context.Context, string) (taskjournal.Snapshot, error)
	UpdateProgress(context.Context, string, taskjournal.Progress) error
}

// Plan is safe for an authenticated administrator. It intentionally omits
// environment, command, secret labels, and bind source paths.
type Plan = protocol.ContainerRebuildPlan

type Request struct {
	TaskID         string
	NodeID         string
	IdempotencyKey string
	Action         protocol.TaskAction
	ContainerID    string
	Spec           *protocol.RebuildSpec
	ConfirmationID string
}

type Result struct {
	Status taskstate.Status
	Task   taskjournal.Snapshot
}

func BuildPlan(current Container, spec protocol.RebuildSpec) (Plan, error) {
	if current.ID == "" || current.Config == nil || current.HostConfig == nil || current.State == "dead" ||
		current.State == "removing" || current.Paused || current.Restarting {
		return Plan{}, ErrUnsupportedConfiguration
	}
	if current.Compose || current.HostNetwork || current.AutoRemove || current.HostConfig.AutoRemove ||
		current.HostConfig.PublishAllPorts || len(current.HostConfig.VolumesFrom) > 0 || len(current.HostConfig.Links) > 0 ||
		current.HostConfig.ContainerIDFile != "" {
		return Plan{}, ErrUnsupportedConfiguration
	}
	mode := string(current.HostConfig.NetworkMode)
	_, namedNetworkMode := current.Networks[mode]
	if mode != "" && mode != "default" && mode != "bridge" && !namedNetworkMode {
		return Plan{}, ErrUnsupportedConfiguration
	}
	for label := range current.Config.Labels {
		if strings.HasPrefix(label, "com.docker.compose.") {
			return Plan{}, ErrUnsupportedConfiguration
		}
	}
	policy, err := json.Marshal(current.HostConfig.RestartPolicy)
	if err != nil {
		return Plan{}, ErrUnsupportedConfiguration
	}
	// Validate reconstructed mount references while the old instance is
	// untouched. An incomplete Inspect must fail during planning, before stop.
	if _, _, _, err := createConfig(current, spec, "plan-validation", "", "", string(policy), current.Networks); err != nil {
		return Plan{}, ErrUnsupportedConfiguration
	}
	if err := protocol.ValidateTaskIntent(protocol.TaskIntent{
		Action: protocol.TaskRebuild, ContainerID: current.ID, Rebuild: &spec,
	}); err != nil {
		return Plan{}, ErrUnsupportedConfiguration
	}
	portsBefore := portSummary(current.PublishedPorts)
	if len(portsBefore) == 0 {
		portsBefore = portSummary(current.HostConfig.PortBindings)
	}
	portsAfter := append([]string(nil), portsBefore...)
	if spec.ClearPortBindings {
		portsAfter = []string{}
	} else if len(spec.PortBindings) > 0 {
		portsAfter = make([]string, 0, len(spec.PortBindings))
		for _, binding := range spec.PortBindings {
			port, err := network.ParsePort(binding.ContainerPort)
			if err != nil {
				return Plan{}, ErrUnsupportedConfiguration
			}
			if _, exists := current.Config.ExposedPorts[port]; !exists {
				return Plan{}, fmt.Errorf("%w: requested port is not exposed by the current container", ErrUnsupportedConfiguration)
			}
			if binding.HostIP != "" {
				if _, err := netip.ParseAddr(binding.HostIP); err != nil {
					return Plan{}, ErrUnsupportedConfiguration
				}
			}
			portsAfter = append(portsAfter, fmt.Sprintf("%s <- %s:%s", binding.ContainerPort, binding.HostIP, binding.HostPort))
		}
		sort.Strings(portsAfter)
	}
	mounts := make([]protocol.ContainerRebuildMount, 0, len(current.Mounts))
	for _, item := range current.Mounts {
		mounts = append(mounts, protocol.ContainerRebuildMount{Type: string(item.Type), Destination: item.Destination,
			ReadWrite: item.RW, VolumeID: item.Name})
	}
	return Plan{
		ContainerID: current.ID, Name: strings.TrimPrefix(current.Name, "/"), ImageID: current.ImageID,
		WasRunning: current.Running, WritableLayerSize: current.WritableSize,
		SnapshotRequired: current.WritableSize > 0, PortsBefore: portsBefore, PortsAfter: portsAfter,
		Preserved: []string{"image configuration", "environment and command", "user and working directory", "restart policy", "resource limits", "named volumes", "anonymous volumes", "bind mounts", "network aliases"},
		Changed:   rebuildChanges(spec), Downtime: "The current container stops while its name and network addresses move to the replacement.",
		Risks: []string{"A Docker image snapshot includes the writable container layer only; mounted volume and bind-mount data are not backed up.",
			"The old container remains stopped as a rollback resource until an administrator explicitly cleans it up.",
			"If the writable-layer snapshot is the replacement's active image, cleanup retains that image to preserve the replacement's data.",
			"The replacement uses the current image ID; image updates are not part of this rebuild request."},
		Mounts: mounts,
	}, nil
}

func rebuildChanges(spec protocol.RebuildSpec) []string {
	if spec.ClearPortBindings {
		return []string{"published ports will be removed"}
	}
	if len(spec.PortBindings) > 0 {
		return []string{"published ports will change"}
	}
	return []string{"container instance will be recreated with its current published ports"}
}

func portSummary(bindings network.PortMap) []string {
	values := make([]string, 0)
	for port, hosts := range bindings {
		for _, host := range hosts {
			values = append(values, fmt.Sprintf("%s <- %s:%s", port, hostIPText(host.HostIP), host.HostPort))
		}
	}
	sort.Strings(values)
	return values
}

func buildPortMap(current network.PortMap, exposed network.PortSet, spec protocol.RebuildSpec) (network.PortMap, error) {
	if spec.ClearPortBindings {
		return network.PortMap{}, nil
	}
	if len(spec.PortBindings) == 0 {
		return current, nil
	}
	result := make(network.PortMap, len(spec.PortBindings))
	for _, binding := range spec.PortBindings {
		port, err := network.ParsePort(binding.ContainerPort)
		if err != nil {
			return nil, ErrUnsupportedConfiguration
		}
		if _, ok := exposed[port]; !ok {
			return nil, ErrUnsupportedConfiguration
		}
		if binding.HostIP != "" {
			if _, err := netip.ParseAddr(binding.HostIP); err != nil {
				return nil, ErrUnsupportedConfiguration
			}
		}
		var hostIP netip.Addr
		if binding.HostIP != "" {
			hostIP, err = netip.ParseAddr(binding.HostIP)
			if err != nil {
				return nil, ErrUnsupportedConfiguration
			}
		}
		result[port] = append(result[port], network.PortBinding{HostIP: hostIP, HostPort: binding.HostPort})
	}
	return result, nil
}

func hostIPText(value netip.Addr) string {
	if !value.IsValid() {
		return ""
	}
	return value.String()
}

func preserveMounts(config *container.Config, host *container.HostConfig, mounts []container.MountPoint) error {
	if config == nil || host == nil {
		return ErrUnsupportedConfiguration
	}
	// Reuse the exact Engine-assigned volume ID for both named and anonymous
	// volumes. A Config.Volumes destination without a prior mount remains
	// anonymous and will be allocated only if the original was never mounted.
	byTarget := make(map[string]container.MountPoint, len(mounts))
	for _, item := range mounts {
		if item.Destination == "" || item.Type == "" {
			return ErrUnsupportedConfiguration
		}
		byTarget[item.Destination] = item
	}
	for index := range host.Mounts {
		current := &host.Mounts[index]
		point, ok := byTarget[current.Target]
		if !ok {
			continue
		}
		if current.Type == mount.TypeVolume {
			if point.Name == "" {
				return ErrUnsupportedConfiguration
			}
			current.Source = point.Name
		}
		delete(byTarget, current.Target)
	}
	// Docker may return bind and volume declarations in HostConfig.Binds.
	// Preserve them and replace anonymous volume sources with the exact Engine
	// volume name so a new anonymous volume is never silently allocated.
	for index, bind := range host.Binds {
		parts := strings.Split(bind, ":")
		if len(parts) < 2 {
			return ErrUnsupportedConfiguration
		}
		target := parts[1]
		point, ok := byTarget[target]
		if !ok {
			continue
		}
		if point.Type == mount.TypeVolume {
			if point.Name == "" {
				return ErrUnsupportedConfiguration
			}
			parts[0] = point.Name
			host.Binds[index] = strings.Join(parts, ":")
		}
		delete(byTarget, target)
	}
	// Volumes exposed by Config but not present in Engine Mounts are only
	// created by Docker on first start. All actual bind/volume mounts must have
	// matched a known HostConfig source above.
	for destination, point := range byTarget {
		if _, configured := config.Volumes[destination]; !configured || point.Type == mount.TypeVolume || point.Type == mount.TypeBind {
			return ErrUnsupportedConfiguration
		}
	}
	return nil
}
