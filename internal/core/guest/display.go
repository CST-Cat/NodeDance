package guest

import (
	"time"

	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type MetricValue struct {
	Unit      string                `json:"unit"`
	Status    protocol.MetricStatus `json:"status"`
	Value     *float64              `json:"value,omitempty"`
	SampledAt time.Time             `json:"sampledAt"`
}
type History struct {
	Resolution string               `json:"resolution"`
	From       time.Time            `json:"from"`
	To         time.Time            `json:"to"`
	Series     []corehistory.Series `json:"series"`
}
type Node struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Status  string                 `json:"status,omitempty"`
	Metrics map[string]MetricValue `json:"metrics,omitempty"`
	History *History               `json:"history,omitempty"`
}
type Probe struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	CheckedAt string `json:"checkedAt,omitempty"`
	LatencyMS *int64 `json:"latencyMs,omitempty"`
}
type Display struct {
	Title          string    `json:"title"`
	GeneratedAt    time.Time `json:"generatedAt"`
	RefreshSeconds int       `json:"refreshSeconds"`
	Nodes          []Node    `json:"nodes"`
	Probes         []Probe   `json:"probes"`
}
