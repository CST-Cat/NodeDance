package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const streamTestRequestID = "c9af68f4-3f5a-4a6b-9b23-3f31de622f19"

func TestValidateContainerStreamOpenBoundsIdentityAndOptions(t *testing.T) {
	valid := ContainerStreamOpen{Kind: StreamLogs, ContainerID: strings.Repeat("a", 64), Tail: "200", Follow: true, ShowStdout: true, ShowStderr: true}
	if err := ValidateContainerStreamOpen(valid); err != nil {
		t.Fatalf("valid log stream rejected: %v", err)
	}
	if err := ValidateContainerStreamOpen(ContainerStreamOpen{Kind: StreamStats, ContainerID: valid.ContainerID}); err != nil {
		t.Fatalf("valid stats stream rejected: %v", err)
	}
	for name, request := range map[string]ContainerStreamOpen{
		"short-id":       {Kind: StreamLogs, ContainerID: "abcd", Tail: "20"},
		"invalid-id":     {Kind: StreamStats, ContainerID: strings.Repeat("A", 64)},
		"unknown-kind":   {Kind: "shell", ContainerID: valid.ContainerID},
		"fraction-tail":  {Kind: StreamLogs, ContainerID: valid.ContainerID, Tail: "1.5"},
		"leading-zero":   {Kind: StreamLogs, ContainerID: valid.ContainerID, Tail: "01"},
		"large-tail":     {Kind: StreamLogs, ContainerID: valid.ContainerID, Tail: "1000001"},
		"stats-log-opts": {Kind: StreamStats, ContainerID: valid.ContainerID, Follow: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateContainerStreamOpen(request); err == nil {
				t.Fatalf("invalid request accepted: %+v", request)
			}
		})
	}
}

func TestValidateContainerStreamEnvelopeRejectsWrongIdentityAndPayload(t *testing.T) {
	valid := Envelope{Version: CurrentVersion, Type: TypeContainerStreamOpen, Generation: 7, RequestID: streamTestRequestID,
		Payload: mustJSONStream(t, ContainerStreamOpen{Kind: StreamLogs, ContainerID: strings.Repeat("a", 64), Tail: "10", Follow: true})}
	if err := ValidateContainerStreamEnvelope(valid, 7); err != nil {
		t.Fatalf("valid stream open rejected: %v", err)
	}
	badCases := map[string]Envelope{
		"wrong-generation": func() Envelope { copy := valid; copy.Generation = 8; return copy }(),
		"stale-generation": func() Envelope { copy := valid; copy.Generation = 6; return copy }(),
		"bad-request-id":   func() Envelope { copy := valid; copy.RequestID = "secret-token"; return copy }(),
		"wrong-version":    func() Envelope { copy := valid; copy.Version++; return copy }(),
		"wrong-type":       func() Envelope { copy := valid; copy.Type = TypeTaskDispatch; return copy }(),
		"oversized": func() Envelope {
			copy := valid
			copy.Payload = json.RawMessage(`"` + strings.Repeat("x", MaxContainerStreamPayloadBytes) + `"`)
			return copy
		}(),
		"unknown-field": func() Envelope {
			copy := valid
			copy.Payload = json.RawMessage(`{"kind":"logs","containerId":"` + strings.Repeat("a", 64) + `","command":"cat /etc/shadow"}`)
			return copy
		}(),
	}
	for name, envelope := range badCases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateContainerStreamEnvelope(envelope, 7); err == nil {
				t.Fatal("invalid envelope accepted")
			}
		})
	}
}

func TestValidateContainerStreamFramesBoundDataAndMetrics(t *testing.T) {
	id := strings.Repeat("b", 64)
	logs := Envelope{Version: CurrentVersion, Type: TypeContainerLog, Generation: 3, Sequence: 1, RequestID: streamTestRequestID,
		Payload: mustJSONStream(t, ContainerStreamLog{ContainerID: id, Channel: "stderr", Data: []byte("错误 🔥\n")})}
	if err := ValidateContainerStreamEnvelope(logs, 3); err != nil {
		t.Fatalf("Unicode log frame rejected: %v", err)
	}
	tooLarge := logs
	tooLarge.Payload = mustJSONStream(t, ContainerStreamLog{ContainerID: id, Channel: "stdout", Data: make([]byte, MaxContainerLogFrameBytes+1)})
	if err := ValidateContainerStreamEnvelope(tooLarge, 3); err == nil {
		t.Fatal("oversized log frame accepted")
	}
	wrongChannel := logs
	wrongChannel.Payload = mustJSONStream(t, ContainerStreamLog{ContainerID: id, Channel: "control", Data: []byte("x")})
	if err := ValidateContainerStreamEnvelope(wrongChannel, 3); err == nil {
		t.Fatal("invalid log channel accepted")
	}
	observed := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	percent := 12.5
	stats := Envelope{Version: CurrentVersion, Type: TypeContainerStats, Generation: 3, Sequence: 1, RequestID: streamTestRequestID,
		Payload: mustJSONStream(t, ContainerStreamStats{Snapshot: ContainerStatsSnapshot{ContainerID: id, ObservedAt: &observed,
			CPUPercent: ContainerStreamMetric[float64]{State: "ok", Value: &percent},
			Memory:     ContainerStreamMetric[ContainerMemoryUsage]{State: "unknown"},
			Network:    ContainerStreamMetric[ContainerNetworkRate]{State: "unknown"},
			BlockIO:    ContainerStreamMetric[ContainerBlockIORate]{State: "unknown"}}})}
	if err := ValidateContainerStreamEnvelope(stats, 3); err != nil {
		t.Fatalf("valid stats frame rejected: %v", err)
	}
	wrongContainer := stats
	wrongContainer.Payload = mustJSONStream(t, ContainerStreamStats{Snapshot: ContainerStatsSnapshot{ContainerID: strings.Repeat("c", 64)}})
	if err := ValidateContainerStreamEnvelope(wrongContainer, 3); err == nil {
		t.Fatal("unbound stats container accepted")
	}
	negative := -1.0
	badMetric := stats
	badMetric.Payload = mustJSONStream(t, ContainerStreamStats{Snapshot: ContainerStatsSnapshot{ContainerID: id,
		CPUPercent: ContainerStreamMetric[float64]{State: "ok", Value: &negative}}})
	if err := ValidateContainerStreamEnvelope(badMetric, 3); err == nil {
		t.Fatal("negative CPU sample accepted")
	}
}

func TestValidateContainerStreamHeartbeat(t *testing.T) {
	valid := Envelope{Version: CurrentVersion, Type: TypeContainerStreamHeartbeat, Generation: 3,
		Sequence: 1, RequestID: streamTestRequestID, Payload: json.RawMessage(`{}`)}
	if err := ValidateContainerStreamEnvelope(valid, 3); err != nil {
		t.Fatalf("valid heartbeat rejected: %v", err)
	}
	for name, mutate := range map[string]func(*Envelope){
		"zero-sequence":    func(frame *Envelope) { frame.Sequence = 0 },
		"wrong-generation": func(frame *Envelope) { frame.Generation++ },
		"unknown-payload-field": func(frame *Envelope) {
			frame.Payload = json.RawMessage(`{"containerId":"` + strings.Repeat("a", 64) + `"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			frame := valid
			mutate(&frame)
			if err := ValidateContainerStreamEnvelope(frame, 3); err == nil {
				t.Fatal("invalid heartbeat accepted")
			}
		})
	}
}

func mustJSONStream(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
