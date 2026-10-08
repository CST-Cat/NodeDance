package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/metrics"
)

const (
	loadMemoryBytes          = 256 << 20
	loadRecoveryWaitSeconds  = 10
	loadDurationSeconds      = 12
	loadQuietCPUPercentLimit = 15.0
	loadBaselineWaitSeconds  = 30
)

type sampleEvidence struct {
	CapturedAtUTC    time.Time        `json:"capturedAtUtc"`
	CapturedUnixNano int64            `json:"capturedUnixNano"`
	RawBootID        string           `json:"rawBootId"`
	RawUptimeSeconds float64          `json:"rawUptimeSeconds"`
	ProcessRSSBytes  uint64           `json:"processRssBytes"`
	Snapshot         metrics.Snapshot `json:"snapshot"`
}

type loadChecks struct {
	CPUIncreased      bool `json:"cpuIncreased"`
	MemoryIncreased   bool `json:"memoryIncreased"`
	ResidentIncreased bool `json:"residentIncreased"`
	CPURecovered      bool `json:"cpuRecovered"`
	MemoryRecovered   bool `json:"memoryRecovered"`
	ResidentRecovered bool `json:"residentRecovered"`
}

type loadEvidence struct {
	Mode                 string           `json:"mode"`
	Status               string           `json:"status"`
	Reason               string           `json:"reason,omitempty"`
	CPUWorkers           int              `json:"cpuWorkers"`
	AllocationBytes      uint64           `json:"allocationBytes"`
	LoadDurationSeconds  int              `json:"loadDurationSeconds"`
	RecoveryWaitSeconds  int              `json:"recoveryWaitSeconds"`
	BaselineWaitSeconds  int              `json:"baselineWaitSeconds"`
	Baseline             sampleEvidence   `json:"baseline"`
	BaselineSamples      []sampleEvidence `json:"baselineSamples"`
	DuringLoad           sampleEvidence   `json:"duringLoad"`
	AfterRelease         sampleEvidence   `json:"afterRelease"`
	Checks               loadChecks       `json:"checks"`
	BaselineCPUPercent   float64          `json:"baselineCpuPercent,omitempty"`
	DuringCPUPercent     float64          `json:"duringCpuPercent,omitempty"`
	RecoveredCPUPercent  float64          `json:"recoveredCpuPercent,omitempty"`
	MemoryUsedDeltaBytes int64            `json:"memoryUsedDeltaBytes,omitempty"`
	RSSDeltaBytes        int64            `json:"rssDeltaBytes,omitempty"`
}

func main() {
	mode := flag.String("mode", "sample", "probe mode: sample or load")
	agentMarker := flag.String("agent-marker", "", "write this file immediately before starting controlled CPU load")
	flag.Parse()
	switch *mode {
	case "sample":
		result, err := capture(metrics.NewCollector())
		if err != nil {
			fail("capture host sample: %v", err)
		}
		emit(result)
	case "load":
		os.Exit(runLoad(*agentMarker))
	default:
		fail("unknown probe mode %q", *mode)
	}
}

func capture(collector *metrics.Collector) (sampleEvidence, error) {
	snapshot := collector.Sample(context.Background())
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return sampleEvidence{}, fmt.Errorf("read boot_id: %w", err)
	}
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return sampleEvidence{}, fmt.Errorf("read /proc/uptime: %w", err)
	}
	fields := strings.Fields(string(uptime))
	if len(fields) == 0 {
		return sampleEvidence{}, fmt.Errorf("/proc/uptime is empty")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return sampleEvidence{}, fmt.Errorf("parse /proc/uptime: %w", err)
	}
	rss, err := processRSSBytes()
	if err != nil {
		return sampleEvidence{}, err
	}
	if snapshot.Uptime.Value == nil || snapshot.Uptime.State != metrics.StateOK {
		return sampleEvidence{}, fmt.Errorf("collector uptime is not known: state=%s reason=%s", snapshot.Uptime.State, snapshot.Uptime.Reason)
	}
	now := time.Now().UTC()
	result := sampleEvidence{
		CapturedAtUTC:    now,
		CapturedUnixNano: now.UnixNano(),
		RawBootID:        strings.TrimSpace(string(bootID)),
		RawUptimeSeconds: seconds,
		ProcessRSSBytes:  rss,
		Snapshot:         snapshot,
	}
	if result.RawBootID == "" || result.Snapshot.Uptime.Value.BootID != result.RawBootID {
		return sampleEvidence{}, fmt.Errorf("collector/raw boot_id mismatch: collector=%q raw=%q", result.Snapshot.Uptime.Value.BootID, result.RawBootID)
	}
	if difference := absUint64(result.Snapshot.Uptime.Value.Seconds, uint64(seconds)); difference > 1 {
		return sampleEvidence{}, fmt.Errorf("collector/raw uptime mismatch: collector=%d proc=%.3f seconds", result.Snapshot.Uptime.Value.Seconds, seconds)
	}
	return result, nil
}

func runLoad(agentMarker string) int {
	collector := metrics.NewCollector()
	_ = collector.Sample(context.Background()) // CPU's first sample intentionally warms up.
	time.Sleep(3 * time.Second)
	baseline, err := capture(collector)
	if err != nil {
		fail("capture load baseline: %v", err)
	}
	evidence := loadEvidence{
		Mode:                "load",
		Status:              "NOT_READY",
		CPUWorkers:          1,
		AllocationBytes:     loadMemoryBytes,
		LoadDurationSeconds: loadDurationSeconds,
		RecoveryWaitSeconds: loadRecoveryWaitSeconds,
		Baseline:            baseline,
		BaselineSamples:     []sampleEvidence{baseline},
	}
	for evidence.BaselineWaitSeconds < loadBaselineWaitSeconds &&
		(baseline.Snapshot.CPU.UsagePercent.Value == nil || *baseline.Snapshot.CPU.UsagePercent.Value > loadQuietCPUPercentLimit) {
		time.Sleep(3 * time.Second)
		evidence.BaselineWaitSeconds += 3
		baseline, err = capture(collector)
		if err != nil {
			fail("capture quiet load baseline: %v", err)
		}
		evidence.Baseline = baseline
		evidence.BaselineSamples = append(evidence.BaselineSamples, baseline)
	}
	if baseline.Snapshot.CPU.UsagePercent.Value == nil || baseline.Snapshot.Memory.Value == nil {
		evidence.Reason = "CPU or memory baseline is unknown"
		emit(evidence)
		return 2
	}
	if baseline.Snapshot.Memory.Value.AvailableBytes < 1<<30 {
		evidence.Reason = "less than 1 GiB is available for the controlled resident allocation"
		emit(evidence)
		return 2
	}
	if *baseline.Snapshot.CPU.UsagePercent.Value > loadQuietCPUPercentLimit {
		evidence.Reason = "baseline guest CPU usage exceeds the quiet-guest limit"
		emit(evidence)
		return 2
	}

	processRSSBefore := baseline.ProcessRSSBytes
	allocation := make([]byte, loadMemoryBytes)
	for offset := 0; offset < len(allocation); offset += 4096 {
		allocation[offset] = byte(offset / 4096)
	}
	if agentMarker != "" {
		if err := os.WriteFile(agentMarker, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
			fail("write controlled Agent-load marker: %v", err)
		}
	}
	stop := make(chan struct{})
	var sink atomic.Uint64
	go func() {
		value := uint64(1)
		for {
			select {
			case <-stop:
				sink.Store(value)
				return
			default:
				value = value*1664525 + 1013904223
			}
		}
	}()
	time.Sleep(loadDurationSeconds * time.Second)
	evidence.DuringLoad, err = capture(collector)
	close(stop)
	runtime.KeepAlive(allocation)
	if err != nil {
		fail("capture active load: %v", err)
	}
	_ = sink.Load()
	evidence.BaselineCPUPercent = *baseline.Snapshot.CPU.UsagePercent.Value
	if evidence.DuringLoad.Snapshot.CPU.UsagePercent.Value != nil {
		evidence.DuringCPUPercent = *evidence.DuringLoad.Snapshot.CPU.UsagePercent.Value
	}
	if evidence.DuringLoad.Snapshot.Memory.Value != nil {
		evidence.MemoryUsedDeltaBytes = signedDelta(evidence.DuringLoad.Snapshot.Memory.Value.UsedBytes,
			baseline.Snapshot.Memory.Value.UsedBytes)
	}
	evidence.RSSDeltaBytes = signedDelta(evidence.DuringLoad.ProcessRSSBytes, processRSSBefore)
	allocation = nil
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(loadRecoveryWaitSeconds * time.Second)
	evidence.AfterRelease, err = capture(collector)
	if err != nil {
		fail("capture released load: %v", err)
	}
	if evidence.AfterRelease.Snapshot.CPU.UsagePercent.Value != nil {
		evidence.RecoveredCPUPercent = *evidence.AfterRelease.Snapshot.CPU.UsagePercent.Value
	}

	evidence.Checks.CPUIncreased = evidence.DuringCPUPercent >= evidence.BaselineCPUPercent+20 && evidence.DuringCPUPercent >= 25
	evidence.Checks.MemoryIncreased = evidence.MemoryUsedDeltaBytes >= 128<<20
	evidence.Checks.ResidentIncreased = evidence.RSSDeltaBytes >= 128<<20
	if evidence.AfterRelease.Snapshot.Memory.Value != nil {
		evidence.Checks.MemoryRecovered = evidence.AfterRelease.Snapshot.Memory.Value.UsedBytes <= baseline.Snapshot.Memory.Value.UsedBytes+(96<<20)
	}
	evidence.Checks.ResidentRecovered = evidence.AfterRelease.ProcessRSSBytes <= processRSSBefore+(96<<20)
	evidence.Checks.CPURecovered = evidence.AfterRelease.Snapshot.CPU.UsagePercent.Value != nil &&
		evidence.RecoveredCPUPercent <= evidence.BaselineCPUPercent+15 &&
		evidence.RecoveredCPUPercent+15 < evidence.DuringCPUPercent
	if evidence.Checks.CPUIncreased && evidence.Checks.MemoryIncreased && evidence.Checks.ResidentIncreased &&
		evidence.Checks.CPURecovered && evidence.Checks.MemoryRecovered && evidence.Checks.ResidentRecovered {
		evidence.Status = "PASS"
	} else {
		evidence.Status = "FAIL"
		evidence.Reason = "controlled CPU and resident-memory response/recovery thresholds were not met"
	}
	emit(evidence)
	if evidence.Status != "PASS" {
		return 1
	}
	return 0
}

func processRSSBytes() (uint64, error) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, fmt.Errorf("read /proc/self/statm: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, fmt.Errorf("invalid /proc/self/statm")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse /proc/self/statm: %w", err)
	}
	return pages * uint64(os.Getpagesize()), nil
}

func absUint64(left, right uint64) uint64 {
	if left > right {
		return left - right
	}
	return right - left
}

func signedDelta(left, right uint64) int64 {
	if left >= right {
		return int64(left - right)
	}
	return -int64(right - left)
}

func emit(value any) {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fail("write evidence: %v", err)
	}
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
