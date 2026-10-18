package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	corealerts "github.com/CST-Cat/NodeDance/internal/core/alerts"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func defaultNodeAsset() protocol.NodeAsset {
	return protocol.DefaultNodeAsset()
}
func (s *Server) nodeAsset(ctx context.Context, node string) (protocol.NodeAsset, error) {
	a := defaultNodeAsset()
	var raw string
	err := s.store.DB.QueryRowContext(ctx, `SELECT settings_json FROM node_assets WHERE node_id=?`, node).Scan(&raw)
	if err == sql.ErrNoRows {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(raw), &a)
	return a, err
}
func (s *Server) handleNodeAssetAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") || !strings.HasSuffix(r.URL.Path, "/asset") {
		return false
	}
	node := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/"), "/asset")
	if !validUUID(node) {
		http.NotFound(w, r)
		return true
	}
	if err := requireManagedNode(r.Context(), s, node); err != nil {
		http.Error(w, err.Error(), 404)
		return true
	}
	switch r.Method {
	case "GET":
		a, err := s.nodeAsset(r.Context(), node)
		if err != nil {
			http.Error(w, "cannot read asset", 500)
			return true
		}
		writeJSON(w, 200, a)
	case "PUT":
		var a protocol.NodeAsset
		if !decodeJSON(w, r, &a) {
			return true
		}
		if err := protocol.ValidateNodeAsset(a); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		tx, err := s.store.DB.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "cannot save asset", 500)
			return true
		}
		defer tx.Rollback()
		raw, _ := json.Marshal(a)
		_, err = tx.ExecContext(r.Context(), `INSERT INTO node_assets(node_id,settings_json,updated_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET settings_json=excluded.settings_json,updated_at=excluded.updated_at`, node, string(raw), s.now().Unix())
		if err == nil {
			err = audit.Record(r.Context(), tx, audit.Event{OccurredAt: s.now(), Action: "node_asset_update", Outcome: "succeeded", ActorID: requestActor(r), Target: audit.Target{Kind: audit.TargetNode, ID: node}, RemoteAddr: current.RemoteAddr})
		}
		if err != nil || tx.Commit() != nil {
			http.Error(w, "cannot save asset", 500)
			return true
		}
		writeJSON(w, 200, a)
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}
func assetSamples(node, name string, a protocol.NodeAsset, h *protocol.MetricsHost, now time.Time) []corealerts.Sample {
	expiry := corealerts.Sample{NodeID: node, NodeName: name, Kind: corealerts.KindAssetExpiry, Known: a.ExpiresAt != nil, ObservedAt: now}
	if a.ExpiresAt != nil {
		if !now.Before(a.ExpiresAt.AddDate(0, 0, -a.ExpiryNoticeDays)) {
			expiry.State = "warning"
		} else {
			expiry.State = "healthy"
		}
	}
	traffic := corealerts.Sample{NodeID: node, NodeName: name, Kind: corealerts.KindTrafficQuota, ObservedAt: now}
	if a.TrafficLimitBytes > 0 && h != nil && h.Traffic.Status == protocol.MetricKnown && h.Traffic.Value != nil {
		v := h.Traffic.Value
		rx, tx := float64(v.PeriodReceivedBytes), float64(v.PeriodSentBytes)
		n := rx + tx
		switch a.TrafficAccounting {
		case "up":
			n = tx
		case "down":
			n = rx
		case "max":
			n = max(rx, tx)
		case "min":
			n = min(rx, tx)
		}
		percentage := n / float64(a.TrafficLimitBytes) * 100
		traffic.Known = true
		traffic.Value = &percentage
		traffic.ObservedAt = h.Traffic.SampledAt
		if percentage >= a.TrafficWarningPercent {
			traffic.State = "warning"
		} else {
			traffic.State = "healthy"
		}
		if h.Traffic.Reason != "" {
			traffic.Message = "Traffic quota uses partial observed coverage; totals are a lower bound"
		}
	}
	return []corealerts.Sample{expiry, traffic}
}
