package server

import (
	"context"
	"time"

	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func (s *Server) alertEvaluationScheduler() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.evaluateAlerts(s.agentContext); err != nil && s.agentContext.Err() != nil {
			return
		}
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) evaluateAlerts(ctx context.Context) error {
	now := s.now().UTC()
	nodes, err := s.agents.ListNodes(ctx)
	if err != nil {
		return err
	}
	probeConfigs, err := s.probes.List(ctx)
	if err != nil {
		return err
	}
	probesByNode := groupAlertProbeConfigsByNode(probeConfigs)
	samples := make([]corealerts.Sample, 0, len(nodes)*8)
	for _, node := range nodes {
		if node.Status == "pending" || node.Status == "revoked" || node.NodeID == "" {
			continue
		}
		if err := s.alerts.EnsureDefaultRules(ctx, node.NodeID); err != nil {
			return err
		}
		state, lease, err := s.currentMetricsState(ctx, node.NodeID)
		if err != nil {
			return err
		}
		samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindNodeOffline, Known: true, State: map[bool]string{true: "online", false: "offline"}[state.Status == "online"], ObservedAt: now})
		if state.Status != "online" || lease == nil {
			continue
		}
		metricView, ok := s.metrics.SnapshotAt(node.NodeID, lease, now)
		if ok {
			received := metricView.ReceivedAt
			cpu := metricView.Metrics.CPU.UsagePercent
			samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindCPU, Known: cpu.Status == protocol.MetricKnown && cpu.Value != nil, Value: cpu.Value, ObservedAt: sampleObserved(cpu.SampledAt, received)})
			memory := metricView.Metrics.Memory
			var memoryValue *float64
			if memory.Value != nil {
				v := memory.Value.UsedPercent
				memoryValue = &v
			}
			samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindMemory, Known: memory.Status == protocol.MetricKnown && memoryValue != nil, Value: memoryValue, ObservedAt: sampleObserved(memory.SampledAt, received)})
			disk := metricView.Metrics.Disk
			var highest *float64
			if disk.Status == protocol.MetricKnown {
				for _, mount := range disk.Mounts {
					if mount.Usage.Status != protocol.MetricKnown || mount.Usage.Value == nil {
						continue
					}
					v := mount.Usage.Value.UsedPercent
					if highest == nil || v > *highest {
						highest = &v
					}
					samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindDisk, SubjectID: mount.Mountpoint, Known: true, Value: &v, ObservedAt: sampleObserved(mount.Usage.SampledAt, received)})
				}
			}
			samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindDisk, Known: disk.Status == protocol.MetricKnown && highest != nil, Value: highest, ObservedAt: sampleObserved(disk.SampledAt, received)})
		}
		_, dockerView, _, err := s.dockerViewStateForNode(ctx, node.NodeID)
		if err != nil {
			return err
		}
		if dockerView.AgentOnline && dockerView.Health != nil && now.Sub(dockerView.Health.ObservedAt) <= 30*time.Second {
			state := "available"
			if dockerView.DockerAvailability == protocol.DockerAvailabilityUnavailable {
				state = "unavailable"
			}
			samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindDockerUnavailable, Known: dockerView.DockerAvailability != protocol.DockerAvailabilityUnknown, State: state, ObservedAt: dockerView.Health.ObservedAt})
		}
		if !dockerView.DataStale && dockerView.AgentOnline {
			for _, record := range dockerView.Containers {
				container := record.Container
				state := "stopped"
				if container.Running {
					state = "running"
				}
				health := string(container.Health)
				if !container.HealthcheckConfigured {
					health = "none"
				}
				samples = append(samples, corealerts.Sample{NodeID: node.NodeID, NodeName: node.DisplayName, Kind: corealerts.KindContainerState, SubjectID: container.ID, Known: true, State: state, Health: health, ObservedAt: sampleObserved(container.ObservedAt, now)})
			}
		}
		samples = append(samples, alertProbeSamples(node.NodeID, node.DisplayName, probesByNode[node.NodeID], now)...)
	}
	rules, err := s.alerts.ListRules(ctx, "")
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(samples))
	for _, sample := range samples {
		known[alertSampleKey(sample.NodeID, sample.Kind, sample.SubjectID)] = true
	}
	for _, rule := range rules {
		key := alertSampleKey(rule.NodeID, rule.Kind, rule.SubjectID)
		if !known[key] {
			samples = append(samples, corealerts.Sample{NodeID: rule.NodeID, Kind: rule.Kind, SubjectID: rule.SubjectID, Known: false, ObservedAt: now})
			known[key] = true
		}
	}
	return s.alerts.Evaluate(ctx, samples)
}

func groupAlertProbeConfigsByNode(configs []coreprobes.Config) map[string][]coreprobes.Config {
	byNode := make(map[string][]coreprobes.Config)
	for _, config := range configs {
		byNode[config.NodeID] = append(byNode[config.NodeID], config)
	}
	return byNode
}

func alertProbeSamples(nodeID, nodeName string, configs []coreprobes.Config, fallback time.Time) []corealerts.Sample {
	samples := make([]corealerts.Sample, 0, len(configs))
	for _, probe := range configs {
		known := probe.Status == protocol.ProbeResultHealthy || probe.Status == protocol.ProbeResultUnhealthy
		probeAt, _ := time.Parse(time.RFC3339Nano, probe.LastCheckedAt)
		samples = append(samples, corealerts.Sample{NodeID: nodeID, NodeName: nodeName, Kind: corealerts.KindProbeState, SubjectID: probe.ID, Known: known, State: probe.Status, ObservedAt: sampleObserved(probeAt, fallback)})
	}
	return samples
}

func sampleObserved(sampledAt, fallback time.Time) time.Time {
	if sampledAt.IsZero() {
		return fallback
	}
	return sampledAt.UTC()
}
func alertSampleKey(node, kind, subject string) string {
	return node + "\x00" + kind + "\x00" + subject
}

func (s *Server) alertDeliveryScheduler() {
	defer s.agentWait.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := s.dispatchAlertDelivery(s.agentContext); err != nil && s.agentContext.Err() != nil {
			return
		}
		select {
		case <-s.agentContext.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) dispatchAlertDelivery(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	delivery, channel, secret, claimed, err := s.alerts.ClaimDelivery(ctx, s.now().UTC())
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	status, sendErr := s.alertSender.Send(ctx, channel, secret, delivery.PayloadJSON)
	return s.alerts.CompleteDelivery(context.Background(), delivery, sendErr == nil, status, errorString(sendErr), s.now().UTC())
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
