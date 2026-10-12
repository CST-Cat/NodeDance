package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type hostBatchResult struct {
	NodeID  string               `json:"nodeId"`
	JobID   string               `json:"jobId,omitempty"`
	ProbeID string               `json:"probeId,omitempty"`
	Job     *protocol.CommandJob `json:"job,omitempty"`
	Error   string               `json:"error,omitempty"`
	Stale   bool                 `json:"stale"`
}
type hostBatch struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	NodeIDs   []string          `json:"nodeIds"`
	Results   []hostBatchResult `json:"results"`
	CreatedAt time.Time         `json:"createdAt"`
}

func hashText(s string) string            { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func batchNodeKey(id, node string) string { return hashText(id + "/" + node) }
func (s *Server) beginHostBatch(ctx context.Context, key, kind string, request any, nodes []string) (hostBatch, error) {
	if len(key) < 8 || len(key) > 128 || len(nodes) < 1 || len(nodes) > 64 {
		return hostBatch{}, errors.New("batch requires Idempotency-Key and 1..64 nodes")
	}
	seen := map[string]bool{}
	for _, id := range nodes {
		if seen[id] {
			return hostBatch{}, errors.New("duplicate node ID")
		}
		seen[id] = true
		if err := requireManagedNode(ctx, s, id); err != nil {
			return hostBatch{}, err
		}
	}
	data, _ := json.Marshal(request)
	digest := hashText(string(data))
	id := hashText(kind + "/" + key)
	raw, _ := json.Marshal(nodes)
	now := s.now().UTC()
	_, err := s.store.DB.ExecContext(ctx, `INSERT OR IGNORE INTO host_batches(id,kind,request_hash,nodes_json,results_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, kind, digest, string(raw), "[]", now.UnixNano(), now.UnixNano())
	if err != nil {
		return hostBatch{}, err
	}
	var stored string
	if err := s.store.DB.QueryRowContext(ctx, `SELECT request_hash FROM host_batches WHERE id=?`, id).Scan(&stored); err != nil {
		return hostBatch{}, err
	}
	if stored != digest {
		return hostBatch{}, errors.New("idempotency key was used for a different batch")
	}
	return s.readHostBatch(ctx, id)
}
func (s *Server) readHostBatch(ctx context.Context, id string) (hostBatch, error) {
	var b hostBatch
	var nodes, results string
	var created int64
	err := s.store.DB.QueryRowContext(ctx, `SELECT id,kind,nodes_json,results_json,created_at FROM host_batches WHERE id=?`, id).Scan(&b.ID, &b.Kind, &nodes, &results, &created)
	if err != nil {
		return b, err
	}
	if err = json.Unmarshal([]byte(nodes), &b.NodeIDs); err != nil {
		return b, err
	}
	if err = json.Unmarshal([]byte(results), &b.Results); err != nil {
		return b, err
	}
	b.CreatedAt = time.Unix(0, created).UTC()
	return b, nil
}
func (s *Server) saveHostBatch(ctx context.Context, b hostBatch) error {
	data, err := json.Marshal(b.Results)
	if err != nil {
		return err
	}
	_, err = s.store.DB.ExecContext(ctx, `UPDATE host_batches SET results_json=?,updated_at=? WHERE id=?`, string(data), s.now().UnixNano(), b.ID)
	return err
}
func (s *Server) handleHostBatchAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path == "/api/v1/probes/batch" {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		var request protocol.BatchProbe
		if !decodeJSON(w, r, &request) {
			return true
		}
		template := probeConfigRequest{Name: request.Name, Kind: request.Kind, Target: request.Target, ExpectedHTTPStatus: request.ExpectedHTTPStatus, IntervalSeconds: request.IntervalSeconds, TimeoutSeconds: request.TimeoutSeconds, Enabled: request.Enabled}
		if len(request.NodeIDs) > 0 {
			template.NodeID = request.NodeIDs[0]
		}
		if _, err := configFromRequest(template, true); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		b, err := s.beginHostBatch(r.Context(), r.Header.Get("Idempotency-Key"), "probe", request, request.NodeIDs)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		results := map[string]hostBatchResult{}
		for _, v := range b.Results {
			results[v.NodeID] = v
		}
		for _, node := range b.NodeIDs {
			if v := results[node]; v.ProbeID != "" {
				continue
			}
			template.NodeID = node
			config, _ := configFromRequest(template, true)
			config.ID = stableProbeID(batchNodeKey(b.ID, node))
			existing, e := s.probes.Get(r.Context(), config.ID)
			if e == nil {
				results[node] = hostBatchResult{NodeID: node, ProbeID: existing.ID}
				continue
			}
			if !errors.Is(e, sql.ErrNoRows) {
				http.Error(w, "probe state unavailable", 500)
				return true
			}
			created, e := s.probes.Create(r.Context(), config, probeAuditEvent(s, current, "probe_batch_create"))
			result := hostBatchResult{NodeID: node}
			if e != nil {
				result.Error = e.Error()
			} else {
				result.ProbeID = created.ID
			}
			results[node] = result
			b.Results = nil
			for _, n := range b.NodeIDs {
				if v, ok := results[n]; ok {
					b.Results = append(b.Results, v)
				}
			}
			if s.saveHostBatch(r.Context(), b) != nil {
				http.Error(w, "batch state write failed", 500)
				return true
			}
		}
		b.Results = nil
		for _, n := range b.NodeIDs {
			b.Results = append(b.Results, results[n])
		}
		if err := s.saveHostBatch(r.Context(), b); err != nil {
			http.Error(w, "batch state write failed", 500)
			return true
		}
		writeJSON(w, 201, b)
		return true
	}
	prefix := "/api/v1/commands/batches"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	if r.URL.Path == prefix {
		switch r.Method {
		case "GET":
			rows, e := s.store.DB.QueryContext(r.Context(), `SELECT id FROM host_batches WHERE kind='command' ORDER BY created_at DESC LIMIT 100`)
			if e != nil {
				http.Error(w, "batch history unavailable", 500)
				return true
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if e := rows.Scan(&id); e != nil {
					rows.Close()
					http.Error(w, "batch history unavailable", 500)
					return true
				}
				ids = append(ids, id)
			}
			rows.Close()
			batches := []hostBatch{}
			for _, id := range ids {
				if b, e := s.readHostBatch(r.Context(), id); e == nil {
					batches = append(batches, b)
				}
			}
			writeJSON(w, 200, map[string]any{"batches": batches})
			return true
		case "POST":
			var request protocol.BatchCommand
			if !decodeJSON(w, r, &request) {
				return true
			}
			spec := protocol.CommandSpec{Script: request.Script, TimeoutSeconds: request.TimeoutSeconds, Confirm: request.Confirm}
			if err := protocol.ValidateCommand(spec); err != nil {
				http.Error(w, err.Error(), 400)
				return true
			}
			b, err := s.beginHostBatch(r.Context(), r.Header.Get("Idempotency-Key"), "command", request, request.NodeIDs)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return true
			}
			if err := audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: "commands.batch", Outcome: "accepted", ActorID: sql.NullInt64{Int64: 1, Valid: true}, RemoteAddr: current.RemoteAddr}); err != nil {
				http.Error(w, "cannot audit batch", 500)
				return true
			}
			existing := map[string]hostBatchResult{}
			for _, v := range b.Results {
				existing[v.NodeID] = v
			}
			results := make([]hostBatchResult, len(b.NodeIDs))
			slots := make(chan struct{}, 4)
			var wg sync.WaitGroup
			for i, node := range b.NodeIDs {
				if v := existing[node]; v.JobID != "" {
					results[i] = v
					continue
				}
				wg.Add(1)
				go func(i int, node string) {
					defer wg.Done()
					slots <- struct{}{}
					defer func() { <-slots }()
					ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
					defer cancel()
					response, e := s.agentHost(ctx, node, protocol.HostRequest{Action: "commands.submit", Command: &spec, IdempotencyKey: batchNodeKey(b.ID, node)})
					result := hostBatchResult{NodeID: node}
					if e != nil {
						result.Error = e.Error()
					} else if response.Error != "" {
						result.Error = response.Error
					} else if response.Job != nil {
						result.JobID = response.Job.ID
					}
					results[i] = result
				}(i, node)
			}
			wg.Wait()
			b.Results = results
			if err := s.saveHostBatch(r.Context(), b); err != nil {
				http.Error(w, "batch state write failed; retry with same key", 500)
				return true
			}
			writeJSON(w, 202, b)
			return true
		default:
			http.Error(w, "method not allowed", 405)
			return true
		}
	}
	id := strings.TrimPrefix(r.URL.Path, prefix+"/")
	if len(id) != 64 || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return true
	}
	if r.Method != "GET" && r.Method != "DELETE" {
		http.Error(w, "method not allowed", 405)
		return true
	}
	b, err := s.readHostBatch(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	if b.Kind != "command" {
		http.NotFound(w, r)
		return true
	}
	if r.Method == "DELETE" {
		if err := audit.Record(r.Context(), s.store.DB, audit.Event{OccurredAt: s.now(), Action: "commands.batch-cancel", Outcome: "accepted", ActorID: sql.NullInt64{Int64: 1, Valid: true}, Target: audit.Target{Kind: audit.TargetCommandBatch, ID: b.ID}, RemoteAddr: current.RemoteAddr}); err != nil {
			http.Error(w, "cannot audit batch cancellation", 500)
			return true
		}
	}
	// Recover accepted children even when Core lost the HTTP response before
	// saving results. Derived identity is shared with Agent; never resubmit here.
	known := map[string]hostBatchResult{}
	for _, v := range b.Results {
		known[v.NodeID] = v
	}
	b.Results = nil
	for _, node := range b.NodeIDs {
		v, ok := known[node]
		if !ok {
			v = hostBatchResult{NodeID: node, Stale: true}
		}
		b.Results = append(b.Results, v)
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, v := range b.Results {
		wg.Add(1)
		go func(i int, v hostBatchResult) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if v.JobID == "" {
				v.JobID = protocol.CommandJobID(batchNodeKey(b.ID, v.NodeID))
			}
			action := "commands.get"
			if r.Method == "DELETE" {
				action = "commands.cancel"
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			response, e := s.agentHost(ctx, v.NodeID, protocol.HostRequest{Action: action, JobID: v.JobID})
			cancel()
			if e != nil {
				b.Results[i].Stale = true
				b.Results[i].Error = e.Error()
				return
			}
			if response.Error != "" || response.Job == nil {
				b.Results[i].Stale = true
				b.Results[i].Error = response.Error
				if response.Job == nil && response.Error == "" {
					b.Results[i].Error = "Agent omitted command result"
				}
				return
			}
			b.Results[i].JobID = response.Job.ID
			b.Results[i].Job = response.Job
			b.Results[i].Stale = false
			b.Results[i].Error = ""
		}(i, v)
	}
	wg.Wait()
	// Outputs stay on the Agent; the Core retains only bounded job summaries.
	summary := b
	summary.Results = append([]hostBatchResult{}, b.Results...)
	for i := range summary.Results {
		if j := summary.Results[i].Job; j != nil {
			copy := *j
			copy.Output = ""
			summary.Results[i].Job = &copy
		}
	}
	if s.saveHostBatch(r.Context(), summary) != nil {
		http.Error(w, "batch state write failed", 500)
		return true
	}
	writeJSON(w, 200, b)
	return true
}
func stableProbeID(hash string) string {
	return hash[:8] + "-" + hash[8:12] + "-4" + hash[13:16] + "-a" + hash[17:20] + "-" + hash[20:32]
}
