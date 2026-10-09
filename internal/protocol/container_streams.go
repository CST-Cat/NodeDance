package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"
)

const (
	CapabilityContainerStreams = "agent.container-streams.v1"

	TypeContainerStreamOpen      = "container_stream_open"
	TypeContainerStreamClose     = "container_stream_close"
	TypeContainerStreamReady     = "container_stream_ready"
	TypeContainerLog             = "container_log"
	TypeContainerStats           = "container_stats"
	TypeContainerStreamError     = "container_stream_error"
	TypeContainerStreamEnd       = "container_stream_end"
	TypeContainerStreamHeartbeat = "container_stream_heartbeat"

	StreamLogs  = "logs"
	StreamStats = "stats"

	MaxContainerStreamPayloadBytes = 64 << 10
	MaxContainerLogFrameBytes      = 32 << 10
	MaxContainerStreamTail         = 1_000_000
)

var streamRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// ContainerStreamOpen is sent only by Core after authorizing a browser
// Session, node, and full container identity. Agent independently validates
// every field before opening an Engine request.
type ContainerStreamOpen struct {
	Kind        string `json:"kind"`
	ContainerID string `json:"containerId"`
	Tail        string `json:"tail,omitempty"`
	Follow      bool   `json:"follow,omitempty"`
	Timestamps  bool   `json:"timestamps,omitempty"`
	ShowStdout  bool   `json:"showStdout,omitempty"`
	ShowStderr  bool   `json:"showStderr,omitempty"`
}

type ContainerStreamClose struct{}

type ContainerStreamReady struct {
	Kind        string `json:"kind"`
	ContainerID string `json:"containerId"`
}

type ContainerStreamLog struct {
	ContainerID string `json:"containerId"`
	Channel     string `json:"channel"`
	Data        []byte `json:"data"`
}

type ContainerStreamStats struct {
	Snapshot ContainerStatsSnapshot `json:"snapshot"`
}

type ContainerStatsSnapshot struct {
	ContainerID string                                      `json:"container_id"`
	ObservedAt  *time.Time                                  `json:"observed_at,omitempty"`
	CPUPercent  ContainerStreamMetric[float64]              `json:"cpu_percent"`
	Memory      ContainerStreamMetric[ContainerMemoryUsage] `json:"memory"`
	Network     ContainerStreamMetric[ContainerNetworkRate] `json:"network"`
	BlockIO     ContainerStreamMetric[ContainerBlockIORate] `json:"block_io"`
}

type ContainerStreamMetric[T any] struct {
	State     string     `json:"state"`
	Value     *T         `json:"value,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	SampledAt *time.Time `json:"sampled_at,omitempty"`
}

type ContainerMemoryUsage struct {
	UsedBytes   uint64  `json:"used_bytes"`
	CacheBytes  uint64  `json:"cache_bytes"`
	LimitBytes  uint64  `json:"limit_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

type ContainerNetworkRate struct {
	ReceivedBytesPerSecond float64 `json:"received_bytes_per_second"`
	SentBytesPerSecond     float64 `json:"sent_bytes_per_second"`
}

type ContainerBlockIORate struct {
	ReadBytesPerSecond  float64 `json:"read_bytes_per_second"`
	WriteBytesPerSecond float64 `json:"write_bytes_per_second"`
}

type ContainerStreamError struct {
	Code string `json:"code"`
}

// ValidateContainerStreamOpen rejects resource identifiers and options that
// could broaden a read beyond the requested Docker resource or create an
// unbounded historical response.
func ValidateContainerStreamOpen(request ContainerStreamOpen) error {
	if !IsFullContainerID(request.ContainerID) {
		return errors.New("container stream requires the full container ID")
	}
	switch request.Kind {
	case StreamLogs:
		if request.Tail == "" {
			request.Tail = "200"
		}
		if request.Tail != "all" {
			tail, err := strconv.ParseUint(request.Tail, 10, 32)
			if err != nil || tail > MaxContainerStreamTail || fmt.Sprint(tail) != request.Tail {
				return errors.New("container log tail must be all or a canonical integer within the limit")
			}
		}
	case StreamStats:
		if request.Tail != "" || request.Follow || request.Timestamps || request.ShowStdout || request.ShowStderr {
			return errors.New("container stats request contains log options")
		}
	default:
		return errors.New("container stream kind is unsupported")
	}
	return nil
}

func ValidateContainerStreamEnvelope(envelope Envelope, generation uint64) error {
	if envelope.Version != CurrentVersion || envelope.Generation != generation || generation == 0 || !streamRequestID.MatchString(envelope.RequestID) {
		return errors.New("container stream envelope identity is invalid")
	}
	if len(envelope.Payload) == 0 || len(envelope.Payload) > MaxContainerStreamPayloadBytes {
		return errors.New("container stream payload size is invalid")
	}
	switch envelope.Type {
	case TypeContainerStreamOpen:
		if envelope.Sequence != 0 {
			return errors.New("container stream open sequence is invalid")
		}
		var request ContainerStreamOpen
		if err := strictStreamJSON(envelope.Payload, &request); err != nil {
			return err
		}
		return ValidateContainerStreamOpen(request)
	case TypeContainerStreamClose:
		if envelope.Sequence != 0 {
			return errors.New("container stream close sequence is invalid")
		}
		var request ContainerStreamClose
		return strictStreamJSON(envelope.Payload, &request)
	case TypeContainerStreamReady:
		if envelope.Sequence == 0 {
			return errors.New("container stream ready sequence is invalid")
		}
		var ready ContainerStreamReady
		if err := strictStreamJSON(envelope.Payload, &ready); err != nil {
			return err
		}
		if !IsFullContainerID(ready.ContainerID) || (ready.Kind != StreamLogs && ready.Kind != StreamStats) {
			return errors.New("container stream ready payload is invalid")
		}
	case TypeContainerLog:
		if envelope.Sequence == 0 {
			return errors.New("container log sequence is invalid")
		}
		var frame ContainerStreamLog
		if err := strictStreamJSON(envelope.Payload, &frame); err != nil {
			return err
		}
		if !IsFullContainerID(frame.ContainerID) || (frame.Channel != "stdout" && frame.Channel != "stderr") || len(frame.Data) == 0 || len(frame.Data) > MaxContainerLogFrameBytes {
			return errors.New("container log frame is invalid")
		}
	case TypeContainerStats:
		if envelope.Sequence == 0 {
			return errors.New("container stats sequence is invalid")
		}
		var frame ContainerStreamStats
		if err := strictStreamJSON(envelope.Payload, &frame); err != nil {
			return err
		}
		if !IsFullContainerID(frame.Snapshot.ContainerID) {
			return errors.New("container stats frame is invalid")
		}
		if err := validateStreamSnapshot(frame.Snapshot); err != nil {
			return err
		}
	case TypeContainerStreamError:
		if envelope.Sequence == 0 {
			return errors.New("container stream error sequence is invalid")
		}
		var streamError ContainerStreamError
		if err := strictStreamJSON(envelope.Payload, &streamError); err != nil {
			return err
		}
		if !validContainerStreamError(streamError.Code) {
			return errors.New("container stream error code is invalid")
		}
	case TypeContainerStreamEnd:
		if envelope.Sequence == 0 {
			return errors.New("container stream end sequence is invalid")
		}
		var end ContainerStreamClose
		return strictStreamJSON(envelope.Payload, &end)
	case TypeContainerStreamHeartbeat:
		if envelope.Sequence == 0 {
			return errors.New("container stream heartbeat sequence is invalid")
		}
		var heartbeat ContainerStreamClose
		return strictStreamJSON(envelope.Payload, &heartbeat)
	default:
		return errors.New("container stream message type is unsupported")
	}
	return nil
}

func strictStreamJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("container stream JSON has trailing data")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("container stream JSON has trailing data")
	}
	return nil
}

func validateStreamSnapshot(snapshot ContainerStatsSnapshot) error {
	for _, state := range []string{snapshot.CPUPercent.State, snapshot.Memory.State, snapshot.Network.State, snapshot.BlockIO.State} {
		if state != "ok" && state != "unknown" {
			return errors.New("container stats metric state is invalid")
		}
	}
	if snapshot.CPUPercent.Value != nil && (*snapshot.CPUPercent.Value < 0 || *snapshot.CPUPercent.Value > 1_000_000) {
		return errors.New("container CPU metric is invalid")
	}
	if snapshot.Memory.Value != nil && (snapshot.Memory.Value.UsedPercent < 0 || snapshot.Memory.Value.UsedPercent > 100) {
		return errors.New("container memory metric is invalid")
	}
	if snapshot.Network.Value != nil && (snapshot.Network.Value.ReceivedBytesPerSecond < 0 || snapshot.Network.Value.SentBytesPerSecond < 0) {
		return errors.New("container network metric is invalid")
	}
	if snapshot.BlockIO.Value != nil && (snapshot.BlockIO.Value.ReadBytesPerSecond < 0 || snapshot.BlockIO.Value.WriteBytesPerSecond < 0) {
		return errors.New("container block I/O metric is invalid")
	}
	return nil
}

func validContainerStreamError(code string) bool {
	switch code {
	case "unavailable", "not_running", "invalid_request", "stream_limit", "slow_consumer", "engine_error":
		return true
	default:
		return false
	}
}
