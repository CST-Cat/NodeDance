package docker

import (
	"fmt"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// ToProtocolBatch converts the Agent's internal Docker model to the shared
// wire DTO. Call protocol.MarshalDockerBatch on the result before placing it
// in an Envelope so both schema validation and the payload byte limit apply.
func ToProtocolBatch(batch Batch) (protocol.DockerBatch, error) {
	out := protocol.DockerBatch{
		Sequence:      batch.Sequence,
		SnapshotID:    batch.SnapshotID,
		FullSnapshot:  batch.FullSnapshot,
		SnapshotIndex: batch.SnapshotIndex,
		SnapshotFinal: batch.SnapshotFinal,
		Changes:       make([]protocol.DockerChange, 0, len(batch.Changes)),
	}
	for i, change := range batch.Changes {
		converted := protocol.DockerChange{
			Sequence:    change.Sequence,
			Action:      protocol.DockerChangeAction(change.Action),
			ContainerID: change.ContainerID,
			Reason:      change.Reason,
			ObservedAt:  change.ObservedAt,
		}
		if change.Container != nil {
			container, err := toProtocolContainer(*change.Container)
			if err != nil {
				return protocol.DockerBatch{}, fmt.Errorf("Docker change %d: %w", i, err)
			}
			converted.Container = &container
		}
		out.Changes = append(out.Changes, converted)
	}
	if batch.Health != nil {
		health := protocol.DockerHealth{
			Sequence:        batch.Health.Sequence,
			Availability:    protocol.DockerAvailability(batch.Health.Availability),
			EventsConnected: batch.Health.EventsConnected,
			LastSuccessAt:   cloneTime(batch.Health.LastSuccessAt),
			LastSnapshotAt:  cloneTime(batch.Health.LastSnapshotAt),
			SnapshotFresh:   batch.Health.SnapshotFresh,
			ErrorKind:       batch.Health.ErrorKind,
			Reason:          batch.Health.Reason,
			ObservedAt:      batch.Health.ObservedAt,
		}
		out.Health = &health
	}
	return out, nil
}

func toProtocolContainer(value Container) (protocol.DockerContainer, error) {
	if value.Health != HealthUnknown && value.Health != HealthNone && value.Health != HealthStarting && value.Health != HealthHealthy && value.Health != HealthUnhealthy {
		return protocol.DockerContainer{}, fmt.Errorf("unsupported Docker health state %q", value.Health)
	}
	out := protocol.DockerContainer{
		ID:                    value.ID,
		Name:                  value.Name,
		Image:                 value.Image,
		ImageID:               value.ImageID,
		State:                 value.State,
		Running:               value.Running,
		Paused:                value.Paused,
		Restarting:            value.Restarting,
		Health:                protocol.DockerHealthState(value.Health),
		HealthcheckConfigured: value.HealthcheckConfigured,
		HealthReason:          value.HealthReason,
		UnavailableReason:     value.UnavailableReason,
		Stale:                 value.Stale,
		CreatedAt:             cloneTime(value.CreatedAt),
		StartedAt:             cloneTime(value.StartedAt),
		FinishedAt:            cloneTime(value.FinishedAt),
		ExitCode:              cloneInt(value.ExitCode),
		RestartCount:          value.RestartCount,
		HostNetwork:           value.HostNetwork,
		PublishAllPorts:       value.PublishAllPorts,
		ObservedAt:            value.ObservedAt,
		Ports:                 make([]protocol.DockerPort, 0, len(value.Ports)),
		Networks:              make([]protocol.DockerNetwork, 0, len(value.Networks)),
		Mounts:                make([]protocol.DockerMount, 0, len(value.Mounts)),
	}
	if value.Compose != nil {
		out.Compose = &protocol.DockerComposeIdentity{
			Project: value.Compose.Project, Service: value.Compose.Service,
			WorkingDir: value.Compose.WorkingDir, ConfigFiles: value.Compose.ConfigFiles,
			ContainerNumber: value.Compose.ContainerNumber, OneOff: value.Compose.OneOff,
			Version: value.Compose.Version,
		}
	}
	for _, port := range value.Ports {
		converted := protocol.DockerPort{
			ContainerPort: port.ContainerPort,
			Protocol:      port.Protocol,
			Exposed:       port.Exposed,
			Configured:    make([]protocol.DockerHostPort, 0, len(port.Configured)),
			Published:     make([]protocol.DockerHostPort, 0, len(port.Published)),
		}
		for _, binding := range port.Configured {
			converted.Configured = append(converted.Configured, protocol.DockerHostPort{IP: binding.IP, Port: binding.Port})
		}
		for _, binding := range port.Published {
			converted.Published = append(converted.Published, protocol.DockerHostPort{IP: binding.IP, Port: binding.Port})
		}
		out.Ports = append(out.Ports, converted)
	}
	for _, network := range value.Networks {
		out.Networks = append(out.Networks, protocol.DockerNetwork{
			Name: network.Name, ID: network.ID, IPv4: network.IPv4, IPv6: network.IPv6,
			Gateway: network.Gateway, IPv6Gateway: network.IPv6Gateway,
			Aliases: append([]string(nil), network.Aliases...),
		})
	}
	for _, mount := range value.Mounts {
		out.Mounts = append(out.Mounts, protocol.DockerMount{
			Type: mount.Type, Name: mount.Name, Source: mount.Source,
			Destination: mount.Destination, Driver: mount.Driver, Mode: mount.Mode,
			Propagation: mount.Propagation, ReadWrite: mount.ReadWrite,
		})
	}
	return out, nil
}
