package probes

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"time"
)

type Quality struct {
	ProbeID           string         `json:"probeId"`
	From              time.Time      `json:"from"`
	To                time.Time      `json:"to"`
	Samples           int            `json:"samples"`
	Successes         int            `json:"successes"`
	Failures          int            `json:"failures"`
	Unknown           int            `json:"unknown"`
	PacketLossPercent *float64       `json:"packetLossPercent,omitempty"`
	JitterMS          *float64       `json:"jitterMs,omitempty"`
	FailurePercent    *float64       `json:"failurePercent,omitempty"`
	MeanLatencyMS     *float64       `json:"meanLatencyMs,omitempty"`
	P95LatencyMS      *float64       `json:"p95LatencyMs,omitempty"`
	Errors            map[string]int `json:"errors"`
}

// Quality counts only completed network attempts in the failure denominator;
// offline, scheduler and capacity gaps are reported separately as unknown.
func (s *Store) Quality(ctx context.Context, id string, from, to time.Time) (Quality, error) {
	q := Quality{ProbeID: id, From: from, To: to, Errors: map[string]int{}}
	config, err := s.Get(ctx, id)
	if err != nil {
		return q, err
	}
	if from.IsZero() || !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		return q, errors.New("quality range must be between 0 and 31 days")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status,latency_ms,error_code FROM service_probe_runs WHERE probe_id=? AND completed_at>=? AND completed_at<=? ORDER BY completed_at LIMIT 100001`, id, from.UnixNano(), to.UnixNano())
	if err != nil {
		return q, err
	}
	defer rows.Close()
	latencies := []float64{}
	lostPackets := 0
	for rows.Next() {
		var status string
		var latency sql.NullInt64
		var code sql.NullString
		if err := rows.Scan(&status, &latency, &code); err != nil {
			return q, err
		}
		q.Samples++
		if q.Samples > 100000 {
			return q, errors.New("quality range exceeds 100000 samples")
		}
		if code.Valid && code.String != "" {
			q.Errors[code.String]++
		}
		switch status {
		case "healthy":
			q.Successes++
			if latency.Valid {
				latencies = append(latencies, float64(latency.Int64))
			}
		case "unhealthy":
			q.Failures++
			if config.Kind == "icmp" && code.Valid && code.String == "timeout" {
				lostPackets++
			}
		default:
			q.Unknown++
		}
	}
	if err := rows.Err(); err != nil {
		return q, err
	}
	if q.Successes+q.Failures > 0 {
		p := float64(q.Failures) * 100 / float64(q.Successes+q.Failures)
		q.FailurePercent = &p
	}
	if config.Kind == "icmp" && q.Successes+lostPackets > 0 {
		loss := float64(lostPackets) * 100 / float64(q.Successes+lostPackets)
		q.PacketLossPercent = &loss
	}
	if len(latencies) > 1 {
		jitter := 0.0
		for i := 1; i < len(latencies); i++ {
			jitter += math.Abs(latencies[i] - latencies[i-1])
		}
		jitter /= float64(len(latencies) - 1)
		q.JitterMS = &jitter
	}
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		sum := 0.0
		for _, n := range latencies {
			sum += n
		}
		mean := sum / float64(len(latencies))
		p95 := latencies[(len(latencies)*95+99)/100-1]
		q.MeanLatencyMS = &mean
		q.P95LatencyMS = &p95
	}
	return q, nil
}
