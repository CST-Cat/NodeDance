package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type Frame struct {
	At       time.Time                `json:"at"`
	BootID   string                   `json:"bootId"`
	Snapshot protocol.MetricsSnapshot `json:"snapshot"`
}
type Incident struct {
	At    time.Time `json:"at"`
	Key   string    `json:"key"`
	Value float64   `json:"value"`
}
type Change struct {
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Outcome string    `json:"outcome"`
}
type Recording struct {
	Frames    []Frame    `json:"frames"`
	Incidents []Incident `json:"incidents"`
	Changes   []Change   `json:"changes"`
	Truncated bool       `json:"truncated"`
	From      time.Time  `json:"from"`
	To        time.Time  `json:"to"`
}

// The recorder retains a two-hour rolling window plus +/- five-minute incident
// windows for seven days, bounded to 10,000 frames per node even under storms.
func recordSnapshot(ctx context.Context, tx *sql.Tx, node string, snapshot protocol.MetricsSnapshot, at time.Time) error {
	if snapshot.Host == nil || snapshot.Host.Observability == nil {
		return nil
	}
	boot := ""
	if snapshot.Uptime.Value != nil {
		boot = snapshot.Uptime.Value.BootID
	}
	if boot != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_epoch(node_id,boot_id,started_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET boot_id=excluded.boot_id,started_at=excluded.started_at WHERE observation_epoch.boot_id!=excluded.boot_id`, node, boot, at.UnixNano()); err != nil {
			return err
		}
	}
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(received_at) FROM observation_frames WHERE node_id=?`, node).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && at.UnixNano()-latest.Int64 < int64(5*time.Second) {
		return nil
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO observation_frames(node_id,received_at,boot_id,snapshot_json) VALUES(?,?,?,?)`, node, at.UnixNano(), boot, string(body)); err != nil {
		return err
	}
	thresholds := map[string]float64{"cpu.steal_percent": 20, "network.conntrack_percent": 90, "memory.oom_kills_per_second": 0}
	for key, threshold := range thresholds {
		m := snapshot.Host.Observability.Values[key]
		if m.Status != protocol.MetricKnown || m.Value == nil || *m.Value <= threshold {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO observation_incidents(node_id,occurred_at,metric_key,value) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM observation_incidents WHERE node_id=? AND metric_key=? AND occurred_at>?)`, node, at.UnixNano(), key, *m.Value, node, key, at.Add(-15*time.Minute).UnixNano()); err != nil {
			return err
		}
	}
	// Bounded work is indexed by node/time. The global retention worker also
	// expires disconnected nodes; an active node cannot bypass its row budget.
	if _, err = tx.ExecContext(ctx, `DELETE FROM observation_frames WHERE node_id=? AND received_at IN (SELECT received_at FROM observation_frames WHERE node_id=? ORDER BY received_at DESC LIMIT -1 OFFSET 10000)`, node, node); err != nil {
		return err
	}
	return nil
}
func (s Store) Recording(ctx context.Context, node string, from, to time.Time) (Recording, error) {
	out := Recording{Frames: []Frame{}, Incidents: []Incident{}, Changes: []Change{}, From: from, To: to}
	if node == "" || from.IsZero() || !from.Before(to) || to.Sub(from) > time.Hour {
		return out, ErrInvalidQuery
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT received_at,boot_id,snapshot_json FROM observation_frames WHERE node_id=? AND received_at>=? AND received_at<=? ORDER BY received_at LIMIT 601`, node, from.UnixNano(), to.UnixNano())
	if err != nil {
		return out, err
	}
	usedBytes := 0
	for rows.Next() {
		var f Frame
		var at int64
		var body string
		if err = rows.Scan(&at, &f.BootID, &body); err != nil {
			rows.Close()
			return out, err
		}
		usedBytes += len(body) + 256
		if len(out.Frames) == 600 || usedBytes > 8<<20 {
			out.Truncated = true
			break
		}
		f.At = time.Unix(0, at).UTC()
		if err = json.Unmarshal([]byte(body), &f.Snapshot); err != nil {
			rows.Close()
			return out, err
		}
		out.Frames = append(out.Frames, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT occurred_at,metric_key,value FROM observation_incidents WHERE node_id=? AND occurred_at>=? AND occurred_at<=? ORDER BY occurred_at LIMIT 201`, node, from.UnixNano(), to.UnixNano())
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var i Incident
		var at int64
		if err = rows.Scan(&at, &i.Key, &i.Value); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Incidents) == 200 {
			out.Truncated = true
			break
		}
		i.At = time.Unix(0, at).UTC()
		out.Incidents = append(out.Incidents, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = s.DB.QueryContext(ctx, `SELECT occurred_at,action,outcome FROM audit_entries WHERE target_id=? AND occurred_at>=? AND occurred_at<=? ORDER BY occurred_at LIMIT 201`, node, from.UnixNano(), to.UnixNano())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Change
		var at int64
		if err = rows.Scan(&at, &c.Action, &c.Outcome); err != nil {
			return out, err
		}
		if len(out.Changes) == 200 {
			out.Truncated = true
			break
		}
		c.At = time.Unix(0, at).UTC()
		out.Changes = append(out.Changes, c)
	}
	return out, rows.Err()
}

type Analysis struct {
	Key           string     `json:"key"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	Samples       int        `json:"samples"`
	Coverage      float64    `json:"coverage"`
	BaselineMean  *float64   `json:"baselineMean,omitempty"`
	RecentMean    *float64   `json:"recentMean,omitempty"`
	ChangePercent *float64   `json:"changePercent,omitempty"`
	BaselineP95   *float64   `json:"baselineP95,omitempty"`
	SlopePerHour  *float64   `json:"slopePerHour,omitempty"`
	Fit           *float64   `json:"fit,omitempty"`
	ExhaustionAt  *time.Time `json:"exhaustionAt,omitempty"`
	BeforeMean    *float64   `json:"beforeMean,omitempty"`
	AfterMean     *float64   `json:"afterMean,omitempty"`
}

// Summarize requires sufficient coverage, fresh data, no minute gaps over five
// minutes and a stable positive regression before projecting exhaustion.
func Summarize(key string, points []Point, now time.Time, capacity *float64, changeAt *time.Time) Analysis {
	a := Analysis{Key: key, Status: "unknown", Reason: "insufficient_history", Samples: len(points)}
	if len(points) < 60 {
		return a
	}
	points = append([]Point{}, points...)
	sort.Slice(points, func(i, j int) bool { return points[i].BucketAt.Before(points[j].BucketAt) })
	first, last := points[0].BucketAt, points[len(points)-1].BucketAt
	hours := last.Sub(first).Hours()
	if hours < 1 {
		return a
	}
	a.Coverage = min(1, float64(len(points))/(last.Sub(first).Minutes()+1))
	if a.Coverage < 0.8 {
		a.Reason = "incomplete_coverage"
		return a
	}
	if now.Sub(last) > 3*time.Minute {
		a.Reason = "stale_history"
		return a
	}
	for i := 1; i < len(points); i++ {
		if points[i].BucketAt.Sub(points[i-1].BucketAt) > 5*time.Minute {
			a.Reason = "history_gap"
			return a
		}
	}
	baseline, recent, before, after := []float64{}, []float64{}, []float64{}, []float64{}
	var sx, sy, sxx, sxy float64
	for _, p := range points {
		if !finite(p.Value) {
			a.Reason = "invalid_data"
			return a
		}
		x := p.BucketAt.Sub(first).Hours()
		sx += x
		sy += p.Value
		sxx += x * x
		sxy += x * p.Value
		if p.BucketAt.Before(now.Add(-30 * time.Minute)) {
			baseline = append(baseline, p.Value)
		} else {
			recent = append(recent, p.Value)
		}
		if changeAt != nil {
			if !p.BucketAt.Before(changeAt.Add(-30*time.Minute)) && p.BucketAt.Before(*changeAt) {
				before = append(before, p.Value)
			}
			if !p.BucketAt.Before(*changeAt) && p.BucketAt.Before(changeAt.Add(30*time.Minute)) {
				after = append(after, p.Value)
			}
		}
	}
	mean := func(v []float64) *float64 {
		if len(v) < 10 {
			return nil
		}
		n := 0.0
		for _, x := range v {
			n += x
		}
		n /= float64(len(v))
		return &n
	}
	a.BaselineMean = mean(baseline)
	a.RecentMean = mean(recent)
	a.BeforeMean = mean(before)
	a.AfterMean = mean(after)
	if a.BaselineMean != nil && a.RecentMean != nil && *a.BaselineMean != 0 {
		n := (*a.RecentMean - *a.BaselineMean) / math.Abs(*a.BaselineMean) * 100
		a.ChangePercent = &n
	}
	if len(baseline) >= 30 {
		sort.Float64s(baseline)
		n := baseline[int(math.Ceil(.95*float64(len(baseline))))-1]
		a.BaselineP95 = &n
	}
	n := float64(len(points))
	den := n*sxx - sx*sx
	if den <= 0 {
		return a
	}
	slope := (n*sxy - sx*sy) / den
	intercept := (sy - slope*sx) / n
	var residual, total float64
	avg := sy / n
	for _, p := range points {
		x := p.BucketAt.Sub(first).Hours()
		residual += math.Pow(p.Value-(intercept+slope*x), 2)
		total += math.Pow(p.Value-avg, 2)
	}
	fit := 0.0
	if total > 0 {
		fit = max(0, 1-residual/total)
	}
	a.Status, a.Reason = "known", ""
	a.SlopePerHour = &slope
	a.Fit = &fit
	if capacity != nil && *capacity > 0 && slope > 0 && fit >= .8 && points[len(points)-1].Value < *capacity {
		remaining := (*capacity - points[len(points)-1].Value) / slope
		if remaining > 0 && remaining <= 24*365 {
			at := last.Add(time.Duration(remaining * float64(time.Hour)))
			a.ExhaustionAt = &at
		}
	}
	return a
}
func (s Store) Analyze(ctx context.Context, node, key string, now time.Time, capacity *float64, changeAt *time.Time) (Analysis, error) {
	if !ValidKey(key) || capacity != nil && (!finite(*capacity) || *capacity <= 0) {
		return Analysis{}, ErrInvalidQuery
	}
	from := now.Add(-24 * time.Hour)
	var epoch int64
	if err := s.DB.QueryRowContext(ctx, `SELECT started_at FROM observation_epoch WHERE node_id=?`, node).Scan(&epoch); err == nil {
		if at := time.Unix(0, epoch); at.After(from) {
			from = at
		}
	} else if err != sql.ErrNoRows {
		return Analysis{}, err
	}
	r, err := s.query(ctx, node, "minute", from, now, []string{key})
	if err != nil {
		return Analysis{}, err
	}
	for _, series := range r.Series {
		if series.Key == key {
			return Summarize(key, series.Points, now, capacity, changeAt), nil
		}
	}
	return Summarize(key, nil, now, capacity, changeAt), nil
}

type Correlation struct {
	NodeID      string   `json:"nodeId"`
	Key         string   `json:"key"`
	Status      string   `json:"status"`
	Reason      string   `json:"reason,omitempty"`
	Coefficient *float64 `json:"coefficient,omitempty"`
	Pairs       int      `json:"pairs"`
}

func Correlate(node, key string, a, b []Point) Correlation {
	out := Correlation{NodeID: node, Key: key, Status: "unknown", Reason: "insufficient_overlap"}
	by := map[int64]float64{}
	for _, p := range a {
		by[p.BucketAt.Unix()] = p.Value
	}
	var sx, sy, sxx, syy, sxy float64
	for _, p := range b {
		if x, ok := by[p.BucketAt.Unix()]; ok {
			out.Pairs++
			sx += x
			sy += p.Value
			sxx += x * x
			syy += p.Value * p.Value
			sxy += x * p.Value
		}
	}
	if out.Pairs < 30 {
		return out
	}
	n := float64(out.Pairs)
	den := (n*sxx - sx*sx) * (n*syy - sy*sy)
	if den <= 0 {
		out.Reason = "constant_series"
		return out
	}
	r := (n*sxy - sx*sy) / math.Sqrt(den)
	r = max(-1, min(1, r))
	out.Status, out.Reason, out.Coefficient = "known", "", &r
	return out
}
