// Package guest owns the explicitly published, read-only display policy.
package guest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid guest display settings")
var ErrConflict = errors.New("guest display settings changed; reload before saving")

type Resource struct {
	ID   string `json:"id" request:"required"`
	Name string `json:"name" request:"required"`
}
type Settings struct {
	Revision     int64      `json:"revision" request:"required"`
	Enabled      bool       `json:"enabled" request:"required"`
	Title        string     `json:"title" request:"required"`
	ShowStatus   bool       `json:"showStatus" request:"required"`
	Metrics      []string   `json:"metrics" request:"required"`
	HistoryHours int        `json:"historyHours" request:"required"`
	Nodes        []Resource `json:"nodes" request:"required"`
	Probes       []Resource `json:"probes" request:"required"`
}
type Metric struct {
	Key        string `json:"key"`
	Unit       string `json:"unit"`
	HistoryKey string `json:"-"`
	History    bool   `json:"history"`
}

// Only these aggregate metrics may be published; host metadata and mount names
// never enter the guest contract. This registry is also discovered by clients.
func Metrics() []Metric {
	return []Metric{{"cpu.usage_percent", "%", "cpu.usage_percent", true}, {"memory.used_percent", "%", "memory.used_percent", true}, {"disk.used_percent", "%", "disk:/:used_percent", true}, {"network.received_bytes_per_second", "B/s", "network.received_bytes_per_second", true}, {"network.sent_bytes_per_second", "B/s", "network.sent_bytes_per_second", true}, {"uptime.seconds", "s", "", false}}
}
func Defaults() Settings {
	return Settings{Title: "NodeDance", Metrics: []string{}, Nodes: []Resource{}, Probes: []Resource{}}
}
func Validate(s Settings) error {
	if s.Revision < 0 || strings.TrimSpace(s.Title) == "" || utf8.RuneCountInString(s.Title) > 80 || strings.ContainsAny(s.Title, "\x00\r\n") || len(s.Nodes) > 20 || len(s.Probes) > 20 {
		return ErrInvalid
	}
	if s.HistoryHours != 0 && s.HistoryHours != 1 && s.HistoryHours != 6 && s.HistoryHours != 24 {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, m := range Metrics() {
		allowed[m.Key] = true
	}
	seen := map[string]bool{}
	for _, key := range s.Metrics {
		if !allowed[key] || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	historical := false
	for _, m := range Metrics() {
		if m.History && seen[m.Key] {
			historical = true
		}
	}
	if s.HistoryHours > 0 && !historical {
		return ErrInvalid
	}
	for _, resources := range [][]Resource{s.Nodes, s.Probes} {
		seen = map[string]bool{}
		for _, r := range resources {
			if r.ID == "" || seen[r.ID] || strings.TrimSpace(r.Name) == "" || utf8.RuneCountInString(r.Name) > 80 || strings.ContainsAny(r.Name, "\x00\r\n") {
				return ErrInvalid
			}
			seen[r.ID] = true
		}
	}
	if s.Enabled && !(len(s.Probes) > 0 || len(s.Nodes) > 0 && (s.ShowStatus || len(s.Metrics) > 0)) {
		return ErrInvalid
	}
	return nil
}

type Store struct{ DB *sql.DB }

func (s Store) Get(ctx context.Context) (Settings, error) {
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT settings_json FROM guest_display WHERE id=1").Scan(&raw)
	if err != nil {
		return Settings{}, err
	}
	var out Settings
	err = json.Unmarshal([]byte(raw), &out)
	return out, err
}
func (s Store) Save(ctx context.Context, in Settings, event audit.Event) (Settings, error) {
	if err := Validate(in); err != nil {
		return Settings{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	if in.Enabled {
		available, err := availableResources(ctx, tx, in)
		if err != nil {
			return Settings{}, err
		}
		if len(available.Nodes) != len(in.Nodes) || len(available.Probes) != len(in.Probes) {
			return Settings{}, ErrInvalid
		}
	}
	previous := in.Revision
	in.Revision++
	in.Title = strings.TrimSpace(in.Title)
	if in.Metrics == nil {
		in.Metrics = []string{}
	}
	if in.Nodes == nil {
		in.Nodes = []Resource{}
	}
	if in.Probes == nil {
		in.Probes = []Resource{}
	}
	for i := range in.Nodes {
		in.Nodes[i].Name = strings.TrimSpace(in.Nodes[i].Name)
	}
	for i := range in.Probes {
		in.Probes[i].Name = strings.TrimSpace(in.Probes[i].Name)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Settings{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE guest_display SET settings_json=?,revision=? WHERE id=1 AND revision=?", string(raw), in.Revision, previous)
	if err != nil {
		return Settings{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Settings{}, err
	}
	if n != 1 {
		return Settings{}, ErrConflict
	}
	if err = audit.Record(ctx, tx, event); err != nil {
		return Settings{}, err
	}
	if err = tx.Commit(); err != nil {
		return Settings{}, err
	}
	return in, nil
}
