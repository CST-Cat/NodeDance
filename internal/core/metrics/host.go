package metrics

import (
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"time"
)

func cloneHost(h *protocol.MetricsHost) *protocol.MetricsHost {
	if h == nil {
		return nil
	}
	out := *h
	out.Observability = protocol.CloneObservability(h.Observability)
	if h.DiskIO != nil {
		value := cloneMetric(*h.DiskIO)
		out.DiskIO = &value
	}
	out.Swap = cloneMetric(h.Swap)
	out.Load = cloneMetric(h.Load)
	out.Processes = cloneMetric(h.Processes)
	out.Sockets = cloneMetric(h.Sockets)
	out.CPU = cloneMetric(h.CPU)
	out.Virtualization = cloneMetric(h.Virtualization)
	out.Memory = cloneMetric(h.Memory)
	out.Addresses = cloneMetric(h.Addresses)
	out.PublicIPv4 = cloneMetric(h.PublicIPv4)
	out.PublicIPv6 = cloneMetric(h.PublicIPv6)
	out.GPUs = cloneMetric(h.GPUs)
	out.Traffic = cloneMetric(h.Traffic)
	if out.Addresses.Value != nil {
		a := append([]protocol.HostAddress{}, (*out.Addresses.Value)...)
		out.Addresses.Value = &a
	}
	if out.GPUs.Value != nil {
		g := append([]protocol.GPUDevice{}, (*out.GPUs.Value)...)
		for i := range g {
			g[i].Utilization = copyValue(g[i].Utilization)
			g[i].TemperatureCelsius = copyValue(g[i].TemperatureCelsius)
			g[i].MemoryTotalBytes = copyValue(g[i].MemoryTotalBytes)
			g[i].MemoryUsedBytes = copyValue(g[i].MemoryUsedBytes)
		}
		out.GPUs.Value = &g
	}
	return &out
}
func copyValue[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
func mergeHost(p, n *protocol.MetricsHost, e time.Duration) *protocol.MetricsHost {
	if p == nil || n == nil {
		return cloneHost(n)
	}
	host := *n
	out := &host
	if p.DiskIO != nil && out.DiskIO != nil {
		value := mergeMetric(*p.DiskIO, *out.DiskIO, e)
		out.DiskIO = &value
	}
	out.Swap = mergeMetric(p.Swap, out.Swap, e)
	out.Load = mergeMetric(p.Load, out.Load, e)
	out.Processes = mergeMetric(p.Processes, out.Processes, e)
	out.Sockets = mergeMetric(p.Sockets, out.Sockets, e)
	out.CPU = mergeMetric(p.CPU, out.CPU, e)
	out.Virtualization = mergeMetric(p.Virtualization, out.Virtualization, e)
	out.Memory = mergeMetric(p.Memory, out.Memory, e)
	out.Addresses = mergeMetric(p.Addresses, out.Addresses, e)
	out.PublicIPv4 = mergeMetric(p.PublicIPv4, out.PublicIPv4, e)
	out.PublicIPv6 = mergeMetric(p.PublicIPv6, out.PublicIPv6, e)
	out.GPUs = mergeMetric(p.GPUs, out.GPUs, e)
	out.Traffic = mergeMetric(p.Traffic, out.Traffic, e)
	return cloneHost(out)
}
func staleHost(h *protocol.MetricsHost, r string) {
	if h == nil {
		return
	}
	protocol.VisitObservability(h.Observability, func(m *protocol.Metric[float64], _ time.Duration) { markMetricStale(m, r) })
	if h.DiskIO != nil {
		markMetricStale(h.DiskIO, r)
	}
	markMetricStale(&h.Swap, r)
	markMetricStale(&h.Load, r)
	markMetricStale(&h.Processes, r)
	markMetricStale(&h.Sockets, r)
	markMetricStale(&h.CPU, r)
	markMetricStale(&h.Virtualization, r)
	markMetricStale(&h.Memory, r)
	markMetricStale(&h.Addresses, r)
	markMetricStale(&h.PublicIPv4, r)
	markMetricStale(&h.PublicIPv6, r)
	markMetricStale(&h.GPUs, r)
	markMetricStale(&h.Traffic, r)
}
func advanceHost(h *protocol.MetricsHost, e time.Duration) {
	if h == nil {
		return
	}
	protocol.VisitObservability(h.Observability, func(m *protocol.Metric[float64], _ time.Duration) { advanceMetricAge(m, e) })
	if h.DiskIO != nil {
		advanceMetricAge(h.DiskIO, e)
	}
	advanceMetricAge(&h.Swap, e)
	advanceMetricAge(&h.Load, e)
	advanceMetricAge(&h.Processes, e)
	advanceMetricAge(&h.Sockets, e)
	advanceMetricAge(&h.CPU, e)
	advanceMetricAge(&h.Virtualization, e)
	advanceMetricAge(&h.Memory, e)
	advanceMetricAge(&h.Addresses, e)
	advanceMetricAge(&h.PublicIPv4, e)
	advanceMetricAge(&h.PublicIPv6, e)
	advanceMetricAge(&h.GPUs, e)
	advanceMetricAge(&h.Traffic, e)
}
func ageHost(h *protocol.MetricsHost) {
	if h == nil {
		return
	}
	ttl := time.Duration(h.FastIntervalSeconds*3) * time.Second
	protocol.VisitObservability(h.Observability, func(m *protocol.Metric[float64], ttl time.Duration) {
		if ttl == 15*time.Second {
			ttl = time.Duration(h.FastIntervalSeconds*3) * time.Second
		}
		markMetricAged(m, ttl)
	})
	if h.DiskIO != nil {
		markMetricAged(h.DiskIO, ttl)
	}
	markMetricAged(&h.Swap, ttl)
	markMetricAged(&h.Load, ttl)
	markMetricAged(&h.Processes, ttl)
	markMetricAged(&h.Sockets, ttl)
	markMetricAged(&h.CPU, ttl)
	markMetricAged(&h.Virtualization, ttl)
	markMetricAged(&h.Memory, ttl)
	markMetricAged(&h.Addresses, ttl)
	markMetricAged(&h.PublicIPv4, 15*time.Minute)
	markMetricAged(&h.PublicIPv6, 15*time.Minute)
	markMetricAged(&h.GPUs, 90*time.Second)
	markMetricAged(&h.Traffic, ttl)
}
func fastTTL(s *protocol.MetricsSnapshot) time.Duration {
	if s.Host != nil {
		return time.Duration(s.Host.FastIntervalSeconds*3) * time.Second
	}
	return fastMetricFreshFor
}
func diskTTL(s *protocol.MetricsSnapshot) time.Duration {
	if s.Host != nil {
		return time.Duration(s.Host.DiskIntervalSeconds*3) * time.Second
	}
	return diskMetricFreshFor
}
