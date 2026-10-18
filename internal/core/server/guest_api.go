package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/guest"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type guestMetric = guest.MetricValue
type guestHistory = guest.History
type guestNode = guest.Node
type guestProbe = guest.Probe
type guestDisplay = guest.Display

func (s *Server) handleGuestPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "guest queries are not supported", 400)
		return
	}
	select {
	case s.guestSlots <- struct{}{}:
		defer func() { <-s.guestSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "guest display busy", 503)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(15 * time.Second))
	settings, err := (guest.Store{DB: s.store.DB}).Get(r.Context())
	if err != nil {
		http.Error(w, "guest display unavailable", 503)
		return
	}
	if r.URL.Path == "/api/v1/public/guest" {
		writeJSON(w, 200, map[string]bool{"enabled": settings.Enabled})
		return
	}
	if !settings.Enabled {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	data, err := s.cachedGuestDisplay(ctx, settings)
	if err != nil {
		http.Error(w, "guest display unavailable", 503)
		return
	}
	writeJSON(w, 200, data)
}
func (s *Server) cachedGuestDisplay(ctx context.Context, settings guest.Settings) (*guestDisplay, error) {
	s.guestMu.Lock()
	defer s.guestMu.Unlock()
	// Resource removal changes the public scope independently of publication
	// settings. Recheck it on every read, even when metric data is cached.
	settings, err := (guest.Store{DB: s.store.DB}).AvailableResources(ctx, settings)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	scope := sha256.Sum256(raw)
	if s.guestCache != nil && s.guestScope == scope && s.now().Sub(s.guestCache.GeneratedAt) >= 0 && s.now().Sub(s.guestCache.GeneratedAt) < 15*time.Second {
		return s.guestCache, nil
	}
	data, err := s.assembleGuestDisplay(ctx, settings)
	if err != nil {
		return nil, err
	}
	s.guestCache = &data
	s.guestScope = scope
	return &data, nil
}

func (s *Server) handleGuestSettings(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/guest/settings" && r.URL.Path != "/api/v1/guest/preview" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	store := guest.Store{DB: s.store.DB}
	if r.URL.Path == "/api/v1/guest/preview" {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		settings, err := store.Get(r.Context())
		if err != nil {
			http.Error(w, "settings unavailable", 503)
			return true
		}
		data, err := s.buildGuestDisplay(r.Context(), settings)
		if err != nil {
			http.Error(w, "preview unavailable", 503)
			return true
		}
		writeJSON(w, 200, data)
		return true
	}
	switch r.Method {
	case "GET":
		settings, err := store.Get(r.Context())
		if err != nil {
			http.Error(w, "settings unavailable", 503)
		} else {
			writeJSON(w, 200, map[string]any{"settings": settings, "metrics": guest.Metrics(), "historyHours": []int{0, 1, 6, 24}})
		}
	case "PUT":
		var input guest.Settings
		if !decodeJSON(w, r, &input) {
			return true
		}
		saved, err := store.Save(r.Context(), input, audit.Event{OccurredAt: s.now(), Action: "guest_display_update", Outcome: "succeeded", ActorID: requestActor(r), RemoteAddr: s.effectiveRemoteAddr(r)})
		if errors.Is(err, guest.ErrConflict) {
			http.Error(w, err.Error(), 409)
		} else if errors.Is(err, guest.ErrInvalid) {
			http.Error(w, err.Error(), 400)
		} else if err != nil {
			http.Error(w, "settings could not be saved", 500)
		} else {
			writeJSON(w, 200, saved)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}
func (s *Server) guestID(kind, id string) string {
	hash := hmac.New(sha256.New, s.csrfKey)
	hash.Write([]byte("guest:" + kind + ":" + id))
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
func guestValue[T any](metric protocol.Metric[T], convert func(T) float64) guestMetric {
	out := guestMetric{Status: metric.Status, SampledAt: metric.SampledAt}
	if out.Status == "" {
		out.Status = protocol.MetricUnknown
	}
	if metric.Status == protocol.MetricKnown && metric.Value != nil {
		v := convert(*metric.Value)
		out.Value = &v
	}
	return out
}
func (s *Server) buildGuestDisplay(ctx context.Context, settings guest.Settings) (guestDisplay, error) {
	settings, err := (guest.Store{DB: s.store.DB}).AvailableResources(ctx, settings)
	if err != nil {
		return guestDisplay{}, err
	}
	return s.assembleGuestDisplay(ctx, settings)
}
func (s *Server) assembleGuestDisplay(ctx context.Context, settings guest.Settings) (guestDisplay, error) {
	out := guestDisplay{Title: settings.Title, GeneratedAt: s.now().UTC(), RefreshSeconds: 30, Nodes: []guestNode{}, Probes: []guestProbe{}}
	descriptors := map[string]guest.Metric{}
	for _, m := range guest.Metrics() {
		descriptors[m.Key] = m
	}
	for _, selected := range settings.Nodes {
		if !settings.ShowStatus && len(settings.Metrics) == 0 {
			continue
		}
		state, view, err := s.metricsViewForNode(ctx, selected.ID)
		if err != nil {
			return out, err
		}
		if !state.Exists || state.Status == "pending" || state.Status == "revoked" {
			continue
		}
		n := guestNode{ID: s.guestID("node", selected.ID), Name: selected.Name, Metrics: map[string]guestMetric{}}
		if settings.ShowStatus {
			n.Status = state.Status
		}
		for _, key := range settings.Metrics {
			value := guestMetric{Status: protocol.MetricUnknown}
			if view != nil {
				snapshot := view.Metrics
				switch key {
				case "cpu.usage_percent":
					value = guestValue(snapshot.CPU.UsagePercent, func(v float64) float64 { return v })
				case "memory.used_percent":
					value = guestValue(snapshot.Memory, func(v protocol.Memory) float64 { return v.UsedPercent })
				case "network.received_bytes_per_second":
					value = guestValue(snapshot.Network.Summary, func(v protocol.NetworkRate) float64 { return v.ReceivedBytesPerSecond })
				case "network.sent_bytes_per_second":
					value = guestValue(snapshot.Network.Summary, func(v protocol.NetworkRate) float64 { return v.SentBytesPerSecond })
				case "uptime.seconds":
					value = guestValue(snapshot.Uptime, func(v protocol.Uptime) float64 { return float64(v.Seconds) })
				case "disk.used_percent":
					for _, mount := range snapshot.Disk.Mounts {
						if mount.Mountpoint == "/" {
							value = guestValue(mount.Usage, func(v protocol.DiskUsage) float64 { return v.UsedPercent })
							break
						}
					}
				}
			}
			value.Unit = descriptors[key].Unit
			n.Metrics[key] = value
		}
		if settings.HistoryHours > 0 && len(settings.Metrics) > 0 {
			resolution := "hour"
			if settings.HistoryHours == 1 {
				resolution = "minute"
			}
			historyKeys := []string{}
			for _, key := range settings.Metrics {
				if descriptors[key].History {
					historyKeys = append(historyKeys, descriptors[key].HistoryKey)
				}
			}
			data, err := s.history.QueryKeys(ctx, selected.ID, resolution, out.GeneratedAt.Add(-time.Duration(settings.HistoryHours)*time.Hour), out.GeneratedAt, historyKeys)
			if err != nil {
				return out, err
			}
			h := guestHistory{Resolution: data.Resolution, From: data.From, To: data.To, Series: []corehistory.Series{}}
			for _, key := range settings.Metrics {
				descriptor := descriptors[key]
				if !descriptor.History {
					continue
				}
				for _, series := range data.Series {
					if series.Key == descriptor.HistoryKey {
						series.Key = key
						h.Series = append(h.Series, series)
					}
				}
			}
			n.History = &h
		}
		out.Nodes = append(out.Nodes, n)
	}
	for _, selected := range settings.Probes {
		config, err := s.probes.Get(ctx, selected.ID)
		if errors.Is(err, coreprobes.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		probe := guestProbe{ID: s.guestID("probe", selected.ID), Name: selected.Name, Status: config.Status, CheckedAt: config.LastCheckedAt}
		checked, err := time.Parse(time.RFC3339Nano, config.LastCheckedAt)
		state, _, stateErr := s.currentMetricsState(ctx, config.NodeID)
		if stateErr != nil {
			return out, stateErr
		}
		if !config.Enabled || err != nil || out.GeneratedAt.Before(checked) || out.GeneratedAt.Sub(checked) > time.Duration(config.IntervalSeconds+config.TimeoutSeconds)*time.Second || state.Status != "online" {
			probe.Status = protocol.ProbeResultUnknown
		}
		runs, err := s.probes.History(ctx, selected.ID, 1)
		if err != nil {
			return out, err
		}
		if len(runs) > 0 && probe.Status != protocol.ProbeResultUnknown && runs[0].Status == probe.Status {
			probe.LatencyMS = runs[0].LatencyMS
		}
		out.Probes = append(out.Probes, probe)
	}
	return out, nil
}
