package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// TypeDocker is sent only by Agents that negotiated CapabilityDocker.
	TypeDocker       = "docker"
	CapabilityDocker = "agent.docker.v1"

	// MaxDockerPayloadBytes matches the Core Agent payload decoder bound.
	MaxDockerPayloadBytes = 64 << 10
	MaxDockerBatchChanges = 64

	maxDockerIDBytes       = 128
	maxDockerNameBytes     = 255
	maxDockerTextBytes     = 4096
	maxDockerReasonBytes   = 512
	maxDockerErrorKindSize = 64
	maxDockerPorts         = 4096
	maxDockerNetworks      = 256
	maxDockerMounts        = 1024
	maxDockerAliases       = 64
)

type DockerAvailability string

const (
	DockerAvailabilityUnknown     DockerAvailability = "unknown"
	DockerAvailabilityAvailable   DockerAvailability = "available"
	DockerAvailabilityUnavailable DockerAvailability = "unavailable"
)

type DockerHealthState string

const (
	DockerHealthUnknown   DockerHealthState = "unknown"
	DockerHealthNone      DockerHealthState = "none"
	DockerHealthStarting  DockerHealthState = "starting"
	DockerHealthHealthy   DockerHealthState = "healthy"
	DockerHealthUnhealthy DockerHealthState = "unhealthy"
)

type DockerChangeAction string

const (
	DockerChangeUpsert DockerChangeAction = "upsert"
	DockerChangeDelete DockerChangeAction = "delete"
	DockerChangeStale  DockerChangeAction = "stale"
)

// DockerBatch is the shared, size-bounded payload for Agent Docker inventory
// updates. Envelope identity, generation, and transport sequence are supplied
// by the authenticated Core-Agent connection and never repeated as identity
// claims in this payload. Sequence is the Agent discovery cache watermark;
// all chunks of one full snapshot intentionally share it.
type DockerBatch struct {
	Sequence      uint64         `json:"sequence"`
	SnapshotID    uint64         `json:"snapshotId,omitempty"`
	FullSnapshot  bool           `json:"fullSnapshot,omitempty"`
	SnapshotIndex int            `json:"snapshotIndex,omitempty"`
	SnapshotFinal bool           `json:"snapshotFinal,omitempty"`
	Changes       []DockerChange `json:"changes,omitempty"`
	Health        *DockerHealth  `json:"health,omitempty"`
}

// DockerChange is one normalized resource update. It contains neither raw
// Engine Inspect JSON, environment variables, nor arbitrary Docker labels.
type DockerChange struct {
	Sequence    uint64             `json:"sequence"`
	Action      DockerChangeAction `json:"action"`
	Container   *DockerContainer   `json:"container,omitempty"`
	ContainerID string             `json:"containerId"`
	Reason      string             `json:"reason,omitempty"`
	ObservedAt  time.Time          `json:"observedAt"`
}

type DockerContainer struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Image                 string                 `json:"image"`
	ImageID               string                 `json:"imageId"`
	State                 string                 `json:"state"`
	Running               bool                   `json:"running"`
	Paused                bool                   `json:"paused"`
	Restarting            bool                   `json:"restarting"`
	Health                DockerHealthState      `json:"health"`
	HealthcheckConfigured bool                   `json:"healthcheckConfigured"`
	HealthReason          string                 `json:"healthReason,omitempty"`
	UnavailableReason     string                 `json:"unavailableReason,omitempty"`
	Stale                 bool                   `json:"stale"`
	CreatedAt             *time.Time             `json:"createdAt,omitempty"`
	StartedAt             *time.Time             `json:"startedAt,omitempty"`
	FinishedAt            *time.Time             `json:"finishedAt,omitempty"`
	ExitCode              *int                   `json:"exitCode,omitempty"`
	RestartCount          int                    `json:"restartCount"`
	HostNetwork           bool                   `json:"hostNetwork"`
	PublishAllPorts       bool                   `json:"publishAllPorts"`
	Compose               *DockerComposeIdentity `json:"compose,omitempty"`
	Ports                 []DockerPort           `json:"ports"`
	Networks              []DockerNetwork        `json:"networks"`
	Mounts                []DockerMount          `json:"mounts"`
	ObservedAt            time.Time              `json:"observedAt"`
}

type DockerComposeIdentity struct {
	Project         string `json:"project"`
	Service         string `json:"service"`
	WorkingDir      string `json:"workingDir,omitempty"`
	ConfigFiles     string `json:"configFiles,omitempty"`
	ContainerNumber string `json:"containerNumber,omitempty"`
	OneOff          bool   `json:"oneOff"`
	Version         string `json:"version,omitempty"`
}

type DockerPort struct {
	ContainerPort uint16           `json:"containerPort"`
	Protocol      string           `json:"protocol"`
	Exposed       bool             `json:"exposed"`
	Configured    []DockerHostPort `json:"configured"`
	Published     []DockerHostPort `json:"published"`
}

type DockerHostPort struct {
	IP   string `json:"ip,omitempty"`
	Port string `json:"port,omitempty"`
}

type DockerNetwork struct {
	Name        string   `json:"name"`
	ID          string   `json:"id,omitempty"`
	IPv4        string   `json:"ipv4,omitempty"`
	IPv6        string   `json:"ipv6,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	IPv6Gateway string   `json:"ipv6Gateway,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
}

type DockerMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	Driver      string `json:"driver,omitempty"`
	Mode        string `json:"mode,omitempty"`
	Propagation string `json:"propagation,omitempty"`
	ReadWrite   bool   `json:"readWrite"`
}

type DockerHealth struct {
	Sequence        uint64             `json:"sequence"`
	Availability    DockerAvailability `json:"availability"`
	EventsConnected bool               `json:"eventsConnected"`
	LastSuccessAt   *time.Time         `json:"lastSuccessAt,omitempty"`
	LastSnapshotAt  *time.Time         `json:"lastSnapshotAt,omitempty"`
	SnapshotFresh   bool               `json:"snapshotFresh"`
	ErrorKind       string             `json:"errorKind,omitempty"`
	Reason          string             `json:"reason,omitempty"`
	ObservedAt      time.Time          `json:"observedAt"`
}

// MarshalDockerBatch validates the shared payload and enforces the same
// 64 KiB limit used by the Core's Agent payload decoder.
func MarshalDockerBatch(batch DockerBatch) ([]byte, error) {
	if err := validateDockerBatch(batch); err != nil {
		return nil, err
	}
	data, err := json.Marshal(batch)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxDockerPayloadBytes {
		return nil, fmt.Errorf("Docker payload is %d bytes; limit is %d", len(data), MaxDockerPayloadBytes)
	}
	return data, nil
}

// UnmarshalDockerBatch strictly decodes one bounded payload. Unknown fields
// and trailing JSON are rejected so the Core and Agent use the same schema.
func UnmarshalDockerBatch(data []byte) (DockerBatch, error) {
	var batch DockerBatch
	if len(data) == 0 || len(data) > MaxDockerPayloadBytes {
		return batch, fmt.Errorf("Docker payload length %d is invalid", len(data))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&batch); err != nil {
		return DockerBatch{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return DockerBatch{}, errors.New("trailing Docker payload data")
	} else if !errors.Is(err, io.EOF) {
		return DockerBatch{}, fmt.Errorf("trailing Docker payload data: %w", err)
	}
	if err := validateDockerBatch(batch); err != nil {
		return DockerBatch{}, err
	}
	return batch, nil
}

func validateDockerBatch(batch DockerBatch) error {
	if batch.Sequence == 0 {
		return errors.New("Docker batch sequence is required")
	}
	if len(batch.Changes) > MaxDockerBatchChanges {
		return fmt.Errorf("Docker batch has %d changes; limit is %d", len(batch.Changes), MaxDockerBatchChanges)
	}
	if batch.FullSnapshot {
		if batch.SnapshotIndex < 0 {
			return errors.New("Docker full snapshot identity or index is invalid")
		}
		if batch.SnapshotIndex == 0 && batch.Health == nil {
			return errors.New("first Docker snapshot chunk must include health")
		}
		if batch.SnapshotID == 0 && (batch.SnapshotIndex != 0 || batch.Health == nil ||
			(batch.Health.Availability == DockerAvailabilityAvailable && batch.Health.SnapshotFresh)) {
			return errors.New("zero Docker snapshot ID is only valid for a non-authoritative first chunk")
		}
		if batch.SnapshotIndex > 0 && batch.Health != nil {
			return errors.New("Docker snapshot health is only allowed in the first chunk")
		}
		if batch.SnapshotIndex > 0 && len(batch.Changes) == 0 {
			return errors.New("empty non-initial Docker snapshot chunk is invalid")
		}
		if len(batch.Changes) == 0 && !batch.SnapshotFinal {
			return errors.New("empty Docker snapshot must be a final single chunk")
		}
	} else if batch.SnapshotID != 0 || batch.SnapshotIndex != 0 || batch.SnapshotFinal {
		return errors.New("incremental Docker batch contains snapshot metadata")
	}
	if len(batch.Changes) == 0 && batch.Health == nil {
		return errors.New("Docker batch has neither changes nor health")
	}
	seenContainers := make(map[string]struct{}, len(batch.Changes))
	for index := range batch.Changes {
		change := &batch.Changes[index]
		if change.Sequence == 0 || change.Sequence != batch.Sequence {
			return fmt.Errorf("Docker change %d sequence does not match its batch", index)
		}
		if !validDockerID(change.ContainerID) || change.ObservedAt.IsZero() {
			return fmt.Errorf("Docker change %d has invalid identity or observation time", index)
		}
		if _, duplicate := seenContainers[change.ContainerID]; duplicate {
			return fmt.Errorf("Docker batch repeats container %q", change.ContainerID)
		}
		seenContainers[change.ContainerID] = struct{}{}
		switch change.Action {
		case DockerChangeUpsert:
			if change.Container == nil || change.Container.ID != change.ContainerID {
				return fmt.Errorf("Docker change %d upsert identity does not match its container", index)
			}
			if err := validateDockerContainer(*change.Container); err != nil {
				return fmt.Errorf("Docker change %d container: %w", index, err)
			}
		case DockerChangeDelete:
			if change.Container != nil || change.Reason != "" {
				return fmt.Errorf("Docker delete change %d has unexpected container or reason", index)
			}
		case DockerChangeStale:
			if change.Container != nil || !validDockerText(change.Reason, maxDockerReasonBytes, false) {
				return fmt.Errorf("Docker stale change %d has invalid reason or container", index)
			}
		default:
			return fmt.Errorf("Docker change %d has unsupported action %q", index, change.Action)
		}
		if batch.FullSnapshot && change.Action != DockerChangeUpsert {
			return errors.New("Docker full snapshot can contain only upserts")
		}
	}
	if batch.Health != nil {
		if batch.Health.Sequence != batch.Sequence {
			return errors.New("Docker health sequence does not match its batch")
		}
		if err := validateDockerHealth(*batch.Health); err != nil {
			return err
		}
	}
	return nil
}

func validateDockerContainer(container DockerContainer) error {
	if !validDockerID(container.ID) ||
		!validDockerText(container.Name, maxDockerNameBytes, true) ||
		!validDockerText(container.Image, maxDockerTextBytes, true) ||
		!validDockerText(container.ImageID, maxDockerTextBytes, true) ||
		!validDockerText(container.State, 64, false) || container.ObservedAt.IsZero() || container.RestartCount < 0 {
		return errors.New("invalid Docker container identity, state, observation time, or restart count")
	}
	if !validDockerHealthState(container.Health) ||
		!validDockerText(container.HealthReason, maxDockerReasonBytes, true) ||
		!validDockerText(container.UnavailableReason, maxDockerReasonBytes, true) {
		return errors.New("invalid Docker container health state or reason")
	}
	if len(container.Ports) > maxDockerPorts || len(container.Networks) > maxDockerNetworks || len(container.Mounts) > maxDockerMounts {
		return errors.New("Docker container has too many ports, networks, or mounts")
	}
	for index, port := range container.Ports {
		if port.ContainerPort == 0 || !validDockerProtocol(port.Protocol) || len(port.Configured) > maxDockerNetworks || len(port.Published) > maxDockerNetworks {
			return fmt.Errorf("Docker port %d is invalid", index)
		}
		for _, binding := range port.Configured {
			if !validDockerText(binding.IP, 128, true) || !validDockerHostPort(binding.Port, false) {
				return fmt.Errorf("Docker port %d has an invalid configured host binding", index)
			}
		}
		for _, binding := range port.Published {
			if !validDockerText(binding.IP, 128, true) || !validDockerHostPort(binding.Port, true) {
				return fmt.Errorf("Docker port %d has an invalid host binding", index)
			}
		}
	}
	for index, network := range container.Networks {
		if !validDockerText(network.Name, maxDockerNameBytes, false) ||
			!validDockerText(network.ID, maxDockerTextBytes, true) ||
			!validDockerText(network.IPv4, 128, true) || !validDockerText(network.IPv6, 128, true) ||
			!validDockerText(network.Gateway, 128, true) || !validDockerText(network.IPv6Gateway, 128, true) || len(network.Aliases) > maxDockerAliases {
			return fmt.Errorf("Docker network %d is invalid", index)
		}
		for _, alias := range network.Aliases {
			if !validDockerText(alias, maxDockerNameBytes, false) {
				return fmt.Errorf("Docker network %d contains an invalid alias", index)
			}
		}
	}
	for index, mount := range container.Mounts {
		if !validDockerText(mount.Type, 64, false) ||
			!validDockerText(mount.Name, maxDockerTextBytes, true) ||
			!validDockerText(mount.Source, maxDockerTextBytes, true) ||
			!validDockerText(mount.Destination, maxDockerTextBytes, false) ||
			!validDockerText(mount.Driver, 128, true) ||
			!validDockerText(mount.Mode, 128, true) ||
			!validDockerText(mount.Propagation, 128, true) {
			return fmt.Errorf("Docker mount %d is invalid", index)
		}
	}
	if compose := container.Compose; compose != nil {
		if !validDockerText(compose.Project, maxDockerNameBytes, true) ||
			!validDockerText(compose.Service, maxDockerNameBytes, true) ||
			(compose.Project == "" && compose.Service == "") ||
			!validDockerText(compose.WorkingDir, maxDockerTextBytes, true) ||
			!validDockerText(compose.ConfigFiles, maxDockerTextBytes, true) ||
			!validDockerText(compose.ContainerNumber, 32, true) ||
			!validDockerText(compose.Version, 128, true) {
			return errors.New("invalid selected Compose identity")
		}
	}
	return nil
}

func validateDockerHealth(health DockerHealth) error {
	if health.Sequence == 0 || health.ObservedAt.IsZero() {
		return errors.New("Docker health sequence and observation time are required")
	}
	switch health.Availability {
	case DockerAvailabilityUnknown, DockerAvailabilityAvailable, DockerAvailabilityUnavailable:
	default:
		return fmt.Errorf("unsupported Docker availability %q", health.Availability)
	}
	if !validDockerText(health.ErrorKind, maxDockerErrorKindSize, true) ||
		!validDockerText(health.Reason, maxDockerReasonBytes, true) {
		return errors.New("Docker health reason is invalid")
	}
	return nil
}

func validDockerHealthState(value DockerHealthState) bool {
	switch value {
	case DockerHealthUnknown, DockerHealthNone, DockerHealthStarting, DockerHealthHealthy, DockerHealthUnhealthy:
		return true
	default:
		return false
	}
}

func validDockerProtocol(value string) bool {
	switch value {
	case "tcp", "udp", "sctp":
		return true
	default:
		return false
	}
}

func validDockerHostPort(value string, published bool) bool {
	if value == "" {
		return !published
	}
	port, err := strconv.ParseUint(value, 10, 16)
	return err == nil && port != 0
}

func validDockerText(value string, maxBytes int, emptyAllowed bool) bool {
	if (value == "" && !emptyAllowed) || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDockerID(value string) bool {
	return strings.TrimSpace(value) == value && validDockerText(value, maxDockerIDBytes, false)
}
