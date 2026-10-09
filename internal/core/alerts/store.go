package alerts

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxDeliveryAttempts = 3

type Store struct {
	db   *sql.DB
	aead cipher.AEAD
	now  func() time.Time
}

func NewStore(db *sql.DB, key []byte, now func() time.Time) (*Store, error) {
	if db == nil || len(key) != 32 {
		return nil, errors.New("alert store requires a database and a 32-byte encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, aead: aead, now: now}, nil
}

func (s *Store) seal(secret string) ([]byte, error) {
	if secret == "" {
		return nil, nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, s.aead.Seal(nil, nonce, []byte(secret), []byte("nodedance-alert-channel-v1"))...), nil
}

func (s *Store) open(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	if len(sealed) < s.aead.NonceSize() {
		return "", errors.New("invalid encrypted channel secret")
	}
	plain, err := s.aead.Open(nil, sealed[:s.aead.NonceSize()], sealed[s.aead.NonceSize():], []byte("nodedance-alert-channel-v1"))
	if err != nil {
		return "", errors.New("channel secret cannot be decrypted")
	}
	return string(plain), nil
}

func id() string              { return uuid.NewString() }
func stamp(t time.Time) int64 { return t.UTC().UnixNano() }
func unstamp(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(0, v.Int64).UTC()
	return &t
}

func (s *Store) EnsureDefaultRules(ctx context.Context, nodeID string) error {
	if !uuidPattern.MatchString(nodeID) {
		return errors.New("invalid node ID")
	}
	now := stamp(s.now())
	defaults := []RuleInput{
		{Name: "CPU usage above 90%", Kind: KindCPU, NodeID: nodeID, Severity: SeverityWarning, Threshold: floatPtr(90), DurationSeconds: 300, CooldownSeconds: 300, Enabled: true},
		{Name: "Memory usage above 90%", Kind: KindMemory, NodeID: nodeID, Severity: SeverityWarning, Threshold: floatPtr(90), DurationSeconds: 300, CooldownSeconds: 300, Enabled: true},
		{Name: "Disk usage above 90%", Kind: KindDisk, NodeID: nodeID, Severity: SeverityWarning, Threshold: floatPtr(90), DurationSeconds: 300, CooldownSeconds: 300, Enabled: true},
	}
	for _, r := range defaults {
		channels, _ := json.Marshal([]string{})
		_, err := s.db.ExecContext(ctx, `INSERT INTO alert_rules(id,name,kind,node_id,subject_id,severity,threshold,duration_seconds,cooldown_seconds,expected_state,channel_ids_json,enabled,revision,created_at,updated_at)
			SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM alert_rules WHERE node_id=? AND kind=? AND subject_id='' AND deleted_at IS NULL)`, id(), r.Name, r.Kind, nodeID, "", r.Severity, *r.Threshold, r.DurationSeconds, r.CooldownSeconds, "", string(channels), 1, 1, now, now, nodeID, r.Kind)
		if err != nil {
			return err
		}
	}
	return nil
}

func floatPtr(v float64) *float64 { return &v }

func (s *Store) SaveRule(ctx context.Context, ruleID string, in RuleInput) (Rule, error) {
	if err := ValidateRule(in); err != nil {
		return Rule{}, err
	}
	if _, err := uuid.Parse(in.NodeID); err != nil {
		return Rule{}, errors.New("invalid node ID")
	}
	now := s.now().UTC()
	channelIDs := append([]string{}, in.ChannelIDs...)
	channels, _ := json.Marshal(channelIDs)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Rule{}, err
	}
	defer tx.Rollback()
	for _, channelID := range in.ChannelIDs {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM alert_channels WHERE id=? AND deleted_at IS NULL`, channelID).Scan(&exists); err != nil {
			return Rule{}, errors.New("rule references an unavailable notification channel")
		}
	}
	if ruleID == "" {
		ruleID = id()
		_, err = tx.ExecContext(ctx, `INSERT INTO alert_rules(id,name,kind,node_id,subject_id,severity,threshold,duration_seconds,cooldown_seconds,expected_state,channel_ids_json,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			ruleID, strings.TrimSpace(in.Name), in.Kind, in.NodeID, in.SubjectID, in.Severity, nullableFloat(in.Threshold), in.DurationSeconds, in.CooldownSeconds, in.ExpectedState, string(channels), boolInt(in.Enabled), stamp(now), stamp(now))
	} else {
		if err := closeActiveRule(ctx, tx, ruleID, now, "Alert rule changed; evaluation restarted"); err != nil {
			return Rule{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM alert_rule_state WHERE rule_id=?`, ruleID); err != nil {
			return Rule{}, err
		}
		result, e := tx.ExecContext(ctx, `UPDATE alert_rules SET name=?,kind=?,node_id=?,subject_id=?,severity=?,threshold=?,duration_seconds=?,cooldown_seconds=?,expected_state=?,channel_ids_json=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND deleted_at IS NULL AND revision=?`,
			strings.TrimSpace(in.Name), in.Kind, in.NodeID, in.SubjectID, in.Severity, nullableFloat(in.Threshold), in.DurationSeconds, in.CooldownSeconds, in.ExpectedState, string(channels), boolInt(in.Enabled), stamp(now), ruleID, in.Revision)
		if e != nil {
			return Rule{}, e
		}
		count, _ := result.RowsAffected()
		if count != 1 {
			return Rule{}, errors.New("alert rule changed or no longer exists")
		}
	}
	if err != nil {
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}
	return s.GetRule(ctx, ruleID)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

const ruleColumns = `id,name,kind,node_id,subject_id,severity,threshold,duration_seconds,cooldown_seconds,expected_state,channel_ids_json,enabled,revision,created_at,updated_at`

func scanRule(row interface{ Scan(...any) error }) (Rule, error) {
	var r Rule
	var threshold sql.NullFloat64
	var channels string
	var enabled int
	var created, updated int64
	err := row.Scan(&r.ID, &r.Name, &r.Kind, &r.NodeID, &r.SubjectID, &r.Severity, &threshold, &r.DurationSeconds, &r.CooldownSeconds, &r.ExpectedState, &channels, &enabled, &r.Revision, &created, &updated)
	if err != nil {
		return r, err
	}
	if threshold.Valid {
		r.Threshold = &threshold.Float64
	}
	r.Enabled = enabled == 1
	_ = json.Unmarshal([]byte(channels), &r.ChannelIDs)
	r.CreatedAt = time.Unix(0, created).UTC().Format(time.RFC3339)
	r.UpdatedAt = time.Unix(0, updated).UTC().Format(time.RFC3339)
	return r, nil
}
func (s *Store) GetRule(ctx context.Context, ruleID string) (Rule, error) {
	return scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE id=? AND deleted_at IS NULL`, ruleID))
}
func (s *Store) ListRules(ctx context.Context, nodeID string) ([]Rule, error) {
	query := `SELECT ` + ruleColumns + ` FROM alert_rules WHERE deleted_at IS NULL ORDER BY node_id,kind,subject_id,id`
	args := []any{}
	if nodeID != "" {
		query = `SELECT ` + ruleColumns + ` FROM alert_rules WHERE deleted_at IS NULL AND node_id=? ORDER BY kind,subject_id,id`
		args = append(args, nodeID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, e := scanRule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) DeleteRule(ctx context.Context, ruleID string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := stamp(s.now())
	res, err := tx.ExecContext(ctx, `UPDATE alert_rules SET deleted_at=?,enabled=0,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND deleted_at IS NULL`, now, now, ruleID, revision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("alert rule changed or no longer exists")
	}
	if err := closeActiveRule(ctx, tx, ruleID, s.now().UTC(), "Alert rule removed"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM alert_rule_state WHERE rule_id=?`, ruleID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func closeActiveRule(ctx context.Context, tx *sql.Tx, ruleID string, now time.Time, reason string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM alerts WHERE rule_id=? AND status='active'`, ruleID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var alertID string
		if err := rows.Scan(&alertID); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, alertID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, alertID := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET status='resolved',resolved_at=?,last_seen_at=?,suppression_reason='rule_changed' WHERE id=?`, stamp(now), stamp(now), alertID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='suppressed',last_error='alert rule changed',claimed_at=NULL,updated_at=? WHERE alert_id=? AND status IN ('queued','retry')`, stamp(now), alertID); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, alertID, "recovered", now, reason); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveChannel(ctx context.Context, channelID string, in ChannelInput) (Channel, error) {
	if err := ValidateChannel(in); err != nil {
		return Channel{}, err
	}
	now := stamp(s.now())
	configJSON, err := json.Marshal(in.Config)
	if err != nil {
		return Channel{}, err
	}
	sealed, err := s.seal(in.Secret)
	if err != nil {
		return Channel{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Channel{}, err
	}
	defer tx.Rollback()
	if channelID == "" {
		channelID = id()
		_, err = tx.ExecContext(ctx, `INSERT INTO alert_channels(id,name,kind,config_json,secret_ciphertext,enabled,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,1,?,?)`, channelID, strings.TrimSpace(in.Name), in.Kind, string(configJSON), nilIfEmpty(sealed), boolInt(in.Enabled), now, now)
	} else {
		var existing []byte
		var rev int64
		var oldKind string
		if err := tx.QueryRowContext(ctx, `SELECT secret_ciphertext,revision,kind FROM alert_channels WHERE id=? AND deleted_at IS NULL`, channelID).Scan(&existing, &rev, &oldKind); err != nil {
			return Channel{}, err
		}
		if in.Revision != rev {
			return Channel{}, errors.New("notification channel changed; reload before saving")
		}
		if in.Secret == "" && oldKind == in.Kind {
			sealed = existing
		} else if in.Secret == "" {
			sealed = nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE alert_channels SET name=?,kind=?,config_json=?,secret_ciphertext=?,enabled=?,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, strings.TrimSpace(in.Name), in.Kind, string(configJSON), nilIfEmpty(sealed), boolInt(in.Enabled), now, channelID, in.Revision)
	}
	if err != nil {
		return Channel{}, err
	}
	if in.Kind == ChannelSMTP {
		if (in.Config.SMTPUsername == "") != (len(sealed) == 0) {
			return Channel{}, errors.New("SMTP username and password must be provided together")
		}
	}
	if err = tx.Commit(); err != nil {
		return Channel{}, err
	}
	return s.GetChannel(ctx, channelID)
}
func nilIfEmpty(v []byte) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
func (s *Store) GetChannel(ctx context.Context, channelID string) (Channel, error) {
	rows, e := s.ListChannels(ctx, channelID)
	if e != nil {
		return Channel{}, e
	}
	if len(rows) != 1 {
		return Channel{}, sql.ErrNoRows
	}
	return rows[0], nil
}
func (s *Store) ListChannels(ctx context.Context, onlyID string) ([]Channel, error) {
	query := `SELECT id,name,kind,config_json,secret_ciphertext,enabled,revision,created_at,updated_at FROM alert_channels WHERE deleted_at IS NULL ORDER BY name,id`
	args := []any{}
	if onlyID != "" {
		query = `SELECT id,name,kind,config_json,secret_ciphertext,enabled,revision,created_at,updated_at FROM alert_channels WHERE id=? AND deleted_at IS NULL`
		args = append(args, onlyID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		var c Channel
		var raw string
		var secret []byte
		var enabled int
		var created, updated int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &raw, &secret, &enabled, &c.Revision, &created, &updated); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &c.Config); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		c.HasSecret = len(secret) > 0
		c.CreatedAt = time.Unix(0, created).UTC().Format(time.RFC3339)
		c.UpdatedAt = time.Unix(0, updated).UTC().Format(time.RFC3339)
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *Store) channelForSend(ctx context.Context, channelID string) (Channel, string, error) {
	var c Channel
	var raw string
	var sealed []byte
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,kind,config_json,secret_ciphertext,enabled,revision,created_at,updated_at FROM alert_channels WHERE id=? AND deleted_at IS NULL`, channelID).Scan(&c.ID, &c.Name, &c.Kind, &raw, &sealed, &enabled, &c.Revision, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return c, "", err
	}
	if enabled != 1 {
		return c, "", errors.New("notification channel disabled")
	}
	if err = json.Unmarshal([]byte(raw), &c.Config); err != nil {
		return c, "", err
	}
	secret, err := s.open(sealed)
	return c, secret, err
}
func (s *Store) DeleteChannel(ctx context.Context, channelID string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := stamp(s.now())
	res, err := tx.ExecContext(ctx, `UPDATE alert_channels SET deleted_at=?,enabled=0,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND deleted_at IS NULL`, now, now, channelID, revision)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("notification channel changed or no longer exists")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='failed',last_error='notification channel was deleted',claimed_at=NULL,updated_at=? WHERE channel_id=? AND status IN ('queued','retry')`, now, channelID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddWindow(ctx context.Context, in WindowInput) (Window, error) {
	if in.Kind != "silence" && in.Kind != "maintenance" {
		return Window{}, errors.New("unsupported alert window kind")
	}
	if in.ScopeType != "global" && in.ScopeType != "node" && in.ScopeType != "rule" {
		return Window{}, errors.New("unsupported alert window scope")
	}
	if (in.ScopeType == "global") != (in.ScopeID == "") {
		return Window{}, errors.New("window scope ID does not match scope type")
	}
	if in.ScopeType != "global" && !uuidPattern.MatchString(in.ScopeID) {
		return Window{}, errors.New("window scope must be a UUID")
	}
	if in.ScopeType == "node" {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id=?`, in.ScopeID).Scan(&exists); err != nil {
			return Window{}, errors.New("window node does not exist")
		}
	}
	if in.ScopeType == "rule" {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM alert_rules WHERE id=? AND deleted_at IS NULL`, in.ScopeID).Scan(&exists); err != nil {
			return Window{}, errors.New("window rule does not exist")
		}
	}
	if !in.EndsAt.After(in.StartsAt) || in.EndsAt.Sub(in.StartsAt) > 90*24*time.Hour {
		return Window{}, errors.New("alert window must last between zero and 90 days")
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > 500 {
		return Window{}, errors.New("window reason must contain 1 to 500 bytes")
	}
	now := s.now().UTC()
	wid := id()
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_windows(id,kind,scope_type,scope_id,starts_at,ends_at,reason,created_at) VALUES(?,?,?,?,?,?,?,?)`, wid, in.Kind, in.ScopeType, nilIfString(in.ScopeID), stamp(in.StartsAt), stamp(in.EndsAt), strings.TrimSpace(in.Reason), stamp(now))
	if err != nil {
		return Window{}, err
	}
	return Window{ID: wid, Kind: in.Kind, ScopeType: in.ScopeType, ScopeID: in.ScopeID, StartsAt: in.StartsAt.UTC(), EndsAt: in.EndsAt.UTC(), Reason: strings.TrimSpace(in.Reason), CreatedAt: now}, nil
}
func nilIfString(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func (s *Store) ListWindows(ctx context.Context) ([]Window, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,kind,scope_type,COALESCE(scope_id,''),starts_at,ends_at,reason,created_at FROM alert_windows WHERE disabled_at IS NULL AND ends_at>? ORDER BY starts_at`, stamp(s.now()))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Window{}
	for rows.Next() {
		var w Window
		var a, b, c int64
		if e := rows.Scan(&w.ID, &w.Kind, &w.ScopeType, &w.ScopeID, &a, &b, &w.Reason, &c); e != nil {
			return nil, e
		}
		w.StartsAt = time.Unix(0, a).UTC()
		w.EndsAt = time.Unix(0, b).UTC()
		w.CreatedAt = time.Unix(0, c).UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}
func (s *Store) DeleteWindow(ctx context.Context, id string) error {
	res, e := s.db.ExecContext(ctx, `UPDATE alert_windows SET disabled_at=? WHERE id=? AND disabled_at IS NULL`, stamp(s.now()), id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) Evaluate(ctx context.Context, samples []Sample) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rules, err := queryRules(ctx, tx)
	if err != nil {
		return err
	}
	byKey := make(map[string]Sample, len(samples))
	offline := make(map[string]bool)
	for _, sample := range samples {
		byKey[sampleKey(sample.NodeID, sample.Kind, sample.SubjectID)] = sample
		if sample.Kind == KindNodeOffline && sample.Known && sample.State == "offline" {
			offline[sample.NodeID] = true
		}
	}
	for _, rule := range rules {
		sample, ok := byKey[sampleKey(rule.NodeID, rule.Kind, rule.SubjectID)]
		if !ok {
			continue
		}
		if rule.Kind != KindNodeOffline && offline[rule.NodeID] {
			if err := s.suppressChild(ctx, tx, rule, now); err != nil {
				return err
			}
			// Samples collected before the node went offline are no longer a valid
			// continuous duration window. Do not fire immediately from an old high
			// value when the node comes back.
			if err := resetRuleState(ctx, tx, rule.ID, rule.SubjectID); err != nil {
				return err
			}
			continue
		}
		if !sample.Known {
			if err := resetRuleState(ctx, tx, rule.ID, rule.SubjectID); err != nil {
				return err
			}
			continue
		}
		bad := conditionBad(rule, sample)
		if !bad {
			if err := resetRuleState(ctx, tx, rule.ID, rule.SubjectID); err != nil {
				return err
			}
			if err := s.resolveRule(ctx, tx, rule, sample, now); err != nil {
				return err
			}
			continue
		}
		observedAt := sampleTime(sample, now)
		var since, previous int64
		err := tx.QueryRowContext(ctx, `SELECT condition_since,last_sample_at FROM alert_rule_state WHERE rule_id=? AND subject_id=?`, rule.ID, rule.SubjectID).Scan(&since, &previous)
		if errors.Is(err, sql.ErrNoRows) {
			since = stamp(observedAt)
		} else if err != nil {
			return err
		}
		if stamp(observedAt) <= previous && previous != 0 {
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO alert_rule_state(rule_id,subject_id,condition_since,last_sample_at,last_value,last_state) VALUES(?,?,?,?,?,?) ON CONFLICT(rule_id,subject_id) DO UPDATE SET last_sample_at=excluded.last_sample_at,last_value=excluded.last_value,last_state=excluded.last_state`, rule.ID, rule.SubjectID, since, stamp(observedAt), nullableFloat(sample.Value), sample.State)
		if err != nil {
			return err
		}
		if observedAt.Sub(time.Unix(0, since)) < time.Duration(rule.DurationSeconds)*time.Second {
			continue
		}
		if err := s.fireOrRemind(ctx, tx, rule, sample, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func queryRules(ctx context.Context, tx *sql.Tx) ([]Rule, error) {
	rows, e := tx.QueryContext(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE enabled=1 AND deleted_at IS NULL ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, e := scanRule(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func sampleKey(node, kind, subject string) string { return node + "\x00" + kind + "\x00" + subject }
func sampleTime(s Sample, now time.Time) time.Time {
	if s.ObservedAt.IsZero() {
		return now
	}
	at := s.ObservedAt.UTC()
	if at.After(now) {
		return now
	}
	return at
}
func conditionBad(r Rule, s Sample) bool {
	switch r.Kind {
	case KindCPU, KindMemory, KindDisk:
		return s.Value != nil && r.Threshold != nil && *s.Value > *r.Threshold
	case KindNodeOffline:
		return s.State == "offline"
	case KindDockerUnavailable:
		return s.State == "unavailable"
	case KindContainerState:
		switch r.ExpectedState {
		case "running", "stopped":
			return s.State != r.ExpectedState
		case "healthy", "unhealthy":
			return s.Health != r.ExpectedState
		}
	case KindProbeState:
		return s.State != r.ExpectedState
	}
	return false
}
func resetRuleState(ctx context.Context, tx *sql.Tx, ruleID, subject string) error {
	_, e := tx.ExecContext(ctx, `DELETE FROM alert_rule_state WHERE rule_id=? AND subject_id=?`, ruleID, subject)
	return e
}

func fingerprint(r Rule) string { return r.ID + "\x00" + r.SubjectID }
func (s *Store) fireOrRemind(ctx context.Context, tx *sql.Tx, r Rule, sample Sample, now time.Time) error {
	fp := fingerprint(r)
	var current Alert
	var val sql.NullFloat64
	var ack sql.NullInt64
	var sil sql.NullInt64
	var notified sql.NullInt64
	var first, last int64
	var channels string
	err := tx.QueryRowContext(ctx, `SELECT id,rule_name,node_id,node_name,subject_id,severity,message,current_value,first_seen_at,last_seen_at,acknowledged_at,silenced_until,last_notified_at,channel_ids_json FROM alerts WHERE fingerprint=? AND status='active'`, fp).Scan(&current.ID, &current.RuleName, &current.NodeID, &current.NodeName, &current.SubjectID, &current.Severity, &current.Message, &val, &first, &last, &ack, &sil, &notified, &channels)
	if errors.Is(err, sql.ErrNoRows) {
		current.ID = id()
		current.RuleID = r.ID
		current.RuleName = r.Name
		current.NodeID = r.NodeID
		current.NodeName = sample.NodeName
		current.SubjectID = r.SubjectID
		current.Severity = r.Severity
		current.Status = "active"
		current.Message = sample.Message
		if current.Message == "" {
			current.Message = defaultMessage(r, sample)
		}
		current.FirstSeenAt = now
		current.LastSeenAt = now
		current.CurrentValue = sample.Value
		chJSON, _ := json.Marshal(r.ChannelIDs)
		_, err = tx.ExecContext(ctx, `INSERT INTO alerts(id,fingerprint,rule_id,rule_name,node_id,node_name,subject_id,severity,status,message,current_value,channel_ids_json,first_seen_at,last_seen_at,last_notified_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, current.ID, fp, r.ID, r.Name, r.NodeID, sample.NodeName, r.SubjectID, r.Severity, "active", current.Message, nullableFloat(sample.Value), string(chJSON), stamp(now), stamp(now), stamp(now))
		if err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, current.ID, "firing", now, current.Message); err != nil {
			return err
		}
		suppressed, err := s.notificationSuppressed(ctx, tx, r, current.ID, now)
		if err != nil {
			return err
		}
		if suppressed {
			_, err = tx.ExecContext(ctx, `UPDATE alerts SET suppression_reason='window' WHERE id=?`, current.ID)
			if err != nil {
				return err
			}
		}
		return s.enqueueNotifications(ctx, tx, current.ID, r.ChannelIDs, Notification{AlertID: current.ID, RuleID: r.ID, Event: "firing", RuleName: r.Name, NodeID: r.NodeID, NodeName: sample.NodeName, SubjectID: r.SubjectID, Severity: r.Severity, Message: current.Message, OccurredAt: now}, now)
	} else if err != nil {
		return err
	}
	_ = first
	_ = last
	currentValue := nullableFloat(sample.Value)
	_, err = tx.ExecContext(ctx, `UPDATE alerts SET last_seen_at=?,current_value=?,message=?,node_name=?,suppression_reason=CASE WHEN suppression_reason='node_offline' THEN '' ELSE suppression_reason END WHERE id=?`, stamp(now), currentValue, choose(sample.Message, defaultMessage(r, sample)), sample.NodeName, current.ID)
	if err != nil {
		return err
	}
	if sil.Valid && now.Before(time.Unix(0, sil.Int64)) {
		return nil
	}
	if ack.Valid {
		return nil
	}
	if notified.Valid && now.Sub(time.Unix(0, notified.Int64)) < time.Duration(r.CooldownSeconds)*time.Second {
		return nil
	}
	suppressed, err := s.notificationSuppressed(ctx, tx, r, current.ID, now)
	if err != nil {
		return err
	}
	if suppressed {
		_, err = tx.ExecContext(ctx, `UPDATE alerts SET suppression_reason='window' WHERE id=?`, current.ID)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, current.ID, "suppressed", now, "Notification suppressed by silence or maintenance window")
	}
	_, err = tx.ExecContext(ctx, `UPDATE alerts SET last_notified_at=?,suppression_reason='' WHERE id=?`, stamp(now), current.ID)
	if err != nil {
		return err
	}
	if err = insertEvent(ctx, tx, current.ID, "reminder", now, "Alert remains active"); err != nil {
		return err
	}
	var channelIDs []string
	_ = json.Unmarshal([]byte(channels), &channelIDs)
	return s.enqueueNotifications(ctx, tx, current.ID, channelIDs, Notification{AlertID: current.ID, RuleID: r.ID, Event: "reminder", RuleName: r.Name, NodeID: r.NodeID, NodeName: sample.NodeName, SubjectID: r.SubjectID, Severity: r.Severity, Message: choose(sample.Message, defaultMessage(r, sample)), OccurredAt: now}, now)
}

func defaultMessage(r Rule, s Sample) string {
	if s.Message != "" {
		return s.Message
	}
	if r.Threshold != nil && s.Value != nil {
		return fmt.Sprintf("%s is %.1f%% (threshold %.1f%%)", r.Name, *s.Value, *r.Threshold)
	}
	return fmt.Sprintf("%s condition is %s", r.Name, s.State)
}
func choose(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func insertEvent(ctx context.Context, tx *sql.Tx, alertID, kind string, at time.Time, message string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO alert_events(id,alert_id,kind,occurred_at,message) VALUES(?,?,?,?,?)`, id(), alertID, kind, stamp(at), message)
	return e
}

func (s *Store) resolveRule(ctx context.Context, tx *sql.Tx, r Rule, sample Sample, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_ids_json,acknowledged_at,silenced_until FROM alerts WHERE fingerprint=? AND status='active'`, fingerprint(r))
	if err != nil {
		return err
	}
	defer rows.Close()
	type active struct {
		id       string
		channels string
		ack, sil sql.NullInt64
	}
	list := []active{}
	for rows.Next() {
		var a active
		if err := rows.Scan(&a.id, &a.channels, &a.ack, &a.sil); err != nil {
			return err
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range list {
		_, err = tx.ExecContext(ctx, `UPDATE alerts SET status='resolved',resolved_at=?,last_seen_at=?,current_value=?,suppression_reason='' WHERE id=?`, stamp(now), stamp(now), nullableFloat(sample.Value), a.id)
		if err != nil {
			return err
		}
		if err = insertEvent(ctx, tx, a.id, "recovered", now, "Alert condition recovered"); err != nil {
			return err
		}
		suppressed, suppressionErr := s.notificationSuppressed(ctx, tx, r, a.id, now)
		if suppressionErr != nil {
			return suppressionErr
		}
		if a.ack.Valid || a.sil.Valid && now.Before(time.Unix(0, a.sil.Int64)) || suppressed {
			continue
		}
		var ids []string
		_ = json.Unmarshal([]byte(a.channels), &ids)
		if err = s.enqueueNotifications(ctx, tx, a.id, ids, Notification{AlertID: a.id, RuleID: r.ID, Event: "recovered", RuleName: r.Name, NodeID: r.NodeID, NodeName: sample.NodeName, SubjectID: r.SubjectID, Severity: r.Severity, Message: "Alert condition recovered", OccurredAt: now}, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) suppressChild(ctx context.Context, tx *sql.Tx, r Rule, now time.Time) error {
	res, err := tx.ExecContext(ctx, `UPDATE alerts SET suppression_reason='node_offline' WHERE fingerprint=? AND status='active' AND suppression_reason!='node_offline'`, fingerprint(r))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		var alertID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM alerts WHERE fingerprint=? AND status='active'`, fingerprint(r)).Scan(&alertID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='suppressed',last_error='node is offline',claimed_at=NULL,updated_at=? WHERE alert_id=? AND status IN ('queued','retry')`, stamp(now), alertID); err != nil {
			return err
		}
		return insertEvent(ctx, tx, alertID, "suppressed", now, "Child alert notifications suppressed while node is offline")
	}
	return nil
}

func (s *Store) notificationSuppressed(ctx context.Context, tx *sql.Tx, r Rule, alertID string, now time.Time) (bool, error) {
	var until sql.NullInt64
	if alertID != "" {
		err := tx.QueryRowContext(ctx, `SELECT silenced_until FROM alerts WHERE id=?`, alertID).Scan(&until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if until.Valid && now.Before(time.Unix(0, until.Int64)) {
			return true, nil
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT kind,scope_type,COALESCE(scope_id,'') FROM alert_windows WHERE disabled_at IS NULL AND starts_at<=? AND ends_at>?`, stamp(now), stamp(now))
	if e != nil {
		return false, e
	}
	defer rows.Close()
	for rows.Next() {
		var kind, scope, id string
		if err := rows.Scan(&kind, &scope, &id); err != nil {
			return false, err
		}
		_ = kind
		if scope == "global" || scope == "node" && id == r.NodeID || scope == "rule" && id == r.ID {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func (s *Store) enqueueNotifications(ctx context.Context, tx *sql.Tx, alertID string, channelIDs []string, n Notification, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,kind FROM alert_channels WHERE deleted_at IS NULL AND enabled=1 ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type channel struct{ id, name, kind string }
	all := []channel{}
	for rows.Next() {
		var c channel
		if err := rows.Scan(&c.id, &c.name, &c.kind); err != nil {
			return err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	wanted := map[string]bool{}
	for _, id := range channelIDs {
		wanted[id] = true
	}
	if len(wanted) > 0 {
		filtered := all[:0]
		for _, c := range all {
			if wanted[c.id] {
				filtered = append(filtered, c)
			}
		}
		all = filtered
	}
	payload, err := json.Marshal(n)
	if err != nil {
		return err
	}
	suppressed := false
	if alertID != "" {
		var suppressionErr error
		suppressed, suppressionErr = s.notificationSuppressed(ctx, tx, Rule{ID: n.RuleID, NodeID: n.NodeID}, alertID, now)
		if suppressionErr != nil {
			return suppressionErr
		}
	}
	for _, c := range all {
		if suppressed {
			_, err = tx.ExecContext(ctx, `INSERT INTO alert_deliveries(id,alert_id,channel_id,channel_name,kind,payload_json,status,max_attempts,last_error,created_at,updated_at) VALUES(?,?,?,?,? ,?,'suppressed',?,?,?,?)`, id(), alertID, c.id, c.name, c.kind, string(payload), maxDeliveryAttempts, "silenced or maintenance window", stamp(now), stamp(now))
			if err != nil {
				return err
			}
			continue
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO alert_deliveries(id,alert_id,channel_id,channel_name,kind,payload_json,status,max_attempts,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`, id(), nullableString(alertID), c.id, c.name, c.kind, string(payload), maxDeliveryAttempts, stamp(now), stamp(now), stamp(now))
		if err != nil {
			return err
		}
	}
	return nil
}
func nullableString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Store) Acknowledge(ctx context.Context, alertID, by, note string) error {
	if len(note) > 500 || strings.ContainsRune(note, '\x00') {
		return errors.New("acknowledgement note is too long or invalid")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := s.now().UTC()
	res, e := tx.ExecContext(ctx, `UPDATE alerts SET acknowledged_at=?,acknowledged_by=?,acknowledged_note=? WHERE id=? AND status='active' AND acknowledged_at IS NULL`, stamp(now), by, strings.TrimSpace(note), alertID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("alert is not active or already acknowledged")
	}
	if e = insertEvent(ctx, tx, alertID, "acknowledged", now, "Alert acknowledged"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='suppressed',last_error='alert acknowledged before delivery',claimed_at=NULL,updated_at=? WHERE alert_id=? AND status IN ('queued','retry')`, stamp(now), alertID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SilenceAlert(ctx context.Context, alertID string, until time.Time, reason string) error {
	now := s.now().UTC()
	if !until.After(now) || until.After(now.Add(90*24*time.Hour)) || len(strings.TrimSpace(reason)) == 0 || len(reason) > 500 {
		return errors.New("alert silence must end within 90 days and include a short reason")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE alerts SET silenced_until=?,suppression_reason='silenced' WHERE id=? AND status='active'`, stamp(until), alertID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("alert is not active")
	}
	if e = insertEvent(ctx, tx, alertID, "silenced", now, strings.TrimSpace(reason)); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='suppressed',last_error='alert silenced before delivery',claimed_at=NULL,updated_at=? WHERE alert_id=? AND status IN ('queued','retry')`, stamp(now), alertID); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) ListAlerts(ctx context.Context, active bool, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	status := "resolved"
	if active {
		status = "active"
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,rule_id,rule_name,node_id,node_name,subject_id,severity,status,message,current_value,first_seen_at,last_seen_at,resolved_at,acknowledged_at,acknowledged_by,acknowledged_note,silenced_until,last_notified_at,suppression_reason FROM alerts WHERE status=? ORDER BY first_seen_at DESC LIMIT ?`, status, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var v sql.NullFloat64
		var first, last int64
		var resolved, ack, sil, notified sql.NullInt64
		if e := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.NodeID, &a.NodeName, &a.SubjectID, &a.Severity, &a.Status, &a.Message, &v, &first, &last, &resolved, &ack, &a.AcknowledgedBy, &a.AcknowledgedNote, &sil, &notified, &a.SuppressionReason); e != nil {
			return nil, e
		}
		if v.Valid {
			a.CurrentValue = &v.Float64
		}
		a.FirstSeenAt = time.Unix(0, first).UTC()
		a.LastSeenAt = time.Unix(0, last).UTC()
		a.ResolvedAt = unstamp(resolved)
		a.AcknowledgedAt = unstamp(ack)
		a.SilencedUntil = unstamp(sil)
		a.LastNotifiedAt = unstamp(notified)
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ListEvents(ctx context.Context, alertID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `SELECT id,alert_id,kind,occurred_at,message,details_json FROM alert_events ORDER BY occurred_at DESC,id DESC LIMIT ?`
	args := []any{limit}
	if alertID != "" {
		q = `SELECT id,alert_id,kind,occurred_at,message,details_json FROM alert_events WHERE alert_id=? ORDER BY occurred_at DESC,id DESC LIMIT ?`
		args = []any{alertID, limit}
	}
	rows, e := s.db.QueryContext(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var ev Event
		var at int64
		var raw string
		if e := rows.Scan(&ev.ID, &ev.AlertID, &ev.Kind, &at, &ev.Message, &raw); e != nil {
			return nil, e
		}
		ev.OccurredAt = time.Unix(0, at).UTC()
		_ = json.Unmarshal([]byte(raw), &ev.Details)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueTest(ctx context.Context, channelID string) error {
	channel, e := s.GetChannel(ctx, channelID)
	if e != nil {
		return e
	}
	if !channel.Enabled {
		return errors.New("notification channel is disabled")
	}
	n := Notification{Event: "test", Message: "NodeDance notification channel test", OccurredAt: s.now().UTC()}
	payload, _ := json.Marshal(n)
	now := stamp(s.now())
	_, e = s.db.ExecContext(ctx, `INSERT INTO alert_deliveries(id,channel_id,channel_name,kind,payload_json,test_send,status,max_attempts,next_attempt_at,created_at,updated_at) VALUES(?,?,?,?,?,1,'queued',?,?,?,?)`, id(), channel.ID, channel.Name, channel.Kind, string(payload), maxDeliveryAttempts, now, now, now)
	return e
}

func (s *Store) ClaimDelivery(ctx context.Context, now time.Time) (Delivery, Channel, string, bool, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Delivery{}, Channel{}, "", false, e
	}
	defer tx.Rollback()
	var d Delivery
	var test int
	var next sql.NullInt64
	var deliveryCreated int64
	e = tx.QueryRowContext(ctx, `SELECT id,COALESCE(alert_id,''),channel_id,channel_name,kind,test_send,status,attempts,max_attempts,next_attempt_at,created_at,payload_json FROM alert_deliveries WHERE status IN ('queued','retry') AND next_attempt_at<=? ORDER BY next_attempt_at,created_at LIMIT 1`, stamp(now)).Scan(&d.ID, &d.AlertID, &d.ChannelID, &d.ChannelName, &d.Kind, &test, &d.Status, &d.Attempts, &d.MaxAttempts, &next, &deliveryCreated, &d.PayloadJSON)
	if errors.Is(e, sql.ErrNoRows) {
		return d, Channel{}, "", false, nil
	}
	if e != nil {
		return d, Channel{}, "", false, e
	}
	d.TestSend = test == 1
	res, e := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='sending',attempts=attempts+1,claimed_at=?,updated_at=? WHERE id=? AND status IN ('queued','retry')`, stamp(now), stamp(now), d.ID)
	if e != nil {
		return d, Channel{}, "", false, e
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		return d, Channel{}, "", false, nil
	}
	d.Attempts++
	var config string
	var sealed []byte
	var enabled int
	var rev int64
	var created, updated int64
	var name string
	var kind string
	failClaim := func(reason string) (Delivery, Channel, string, bool, error) {
		if _, e := tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='failed',last_error=?,claimed_at=NULL,updated_at=? WHERE id=? AND status='sending'`, reason, stamp(now), d.ID); e != nil {
			return d, Channel{}, "", false, e
		}
		if e := tx.Commit(); e != nil {
			return d, Channel{}, "", false, e
		}
		return d, Channel{}, "", false, nil
	}
	e = tx.QueryRowContext(ctx, `SELECT name,kind,config_json,secret_ciphertext,enabled,revision,created_at,updated_at FROM alert_channels WHERE id=? AND deleted_at IS NULL`, d.ChannelID).Scan(&name, &kind, &config, &sealed, &enabled, &rev, &created, &updated)
	if errors.Is(e, sql.ErrNoRows) {
		if _, e = tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='failed',last_error='notification channel was deleted',claimed_at=NULL,updated_at=? WHERE id=?`, stamp(now), d.ID); e != nil {
			return d, Channel{}, "", false, e
		}
		if e = tx.Commit(); e != nil {
			return d, Channel{}, "", false, e
		}
		return d, Channel{}, "", false, nil
	}
	if e != nil {
		return d, Channel{}, "", false, e
	}
	var c Channel
	c.ID = d.ChannelID
	c.Name = name
	c.Kind = kind
	c.Enabled = enabled == 1
	c.Revision = rev
	c.CreatedAt = time.Unix(0, created).UTC().Format(time.RFC3339)
	c.UpdatedAt = time.Unix(0, updated).UTC().Format(time.RFC3339)
	if e = json.Unmarshal([]byte(config), &c.Config); e != nil {
		return failClaim("notification channel configuration is invalid")
	}
	if !c.Enabled {
		if _, e = tx.ExecContext(ctx, `UPDATE alert_deliveries SET status='failed',last_error='notification channel is disabled',claimed_at=NULL,updated_at=? WHERE id=?`, stamp(now), d.ID); e != nil {
			return d, c, "", false, e
		}
		if e = tx.Commit(); e != nil {
			return d, c, "", false, e
		}
		return d, c, "", false, nil
	}
	secret, e := s.open(sealed)
	if e != nil {
		// A changed/lost key must not leave this delivery in a permanent queued
		// loop. Preserve the failure in history, without exposing crypto details.
		return failClaim("notification channel secret cannot be decrypted")
	}
	if e = tx.Commit(); e != nil {
		return d, c, "", false, e
	}
	d.Status = "sending"
	d.CreatedAt = time.Unix(0, deliveryCreated).UTC()
	_ = next
	return d, c, secret, true, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, d Delivery, success bool, httpStatus int, deliveryErr string, now time.Time) error {
	status := "sent"
	var delivered any = stamp(now)
	var next any = nil
	attempts := d.Attempts
	if !success {
		delivered = nil
		if attempts < d.MaxAttempts {
			status = "retry"
			delay := time.Duration(5*int(math.Pow(2, float64(attempts-1)))) * time.Second
			next = stamp(now.Add(delay))
		} else {
			status = "failed"
		}
	}
	if len(deliveryErr) > 300 {
		deliveryErr = deliveryErr[:300]
	}
	_, e := s.db.ExecContext(ctx, `UPDATE alert_deliveries SET status=?,delivered_at=?,next_attempt_at=?,http_status=?,last_error=?,claimed_at=NULL,updated_at=? WHERE id=? AND status='sending'`, status, delivered, next, httpStatus, sanitizeDeliveryError(deliveryErr), stamp(now), d.ID)
	return e
}
func sanitizeDeliveryError(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, v)
	for _, secretLike := range []string{"password=", "token=", "authorization:"} {
		if i := strings.Index(strings.ToLower(v), secretLike); i >= 0 {
			return v[:i] + "[redacted]"
		}
	}
	return v
}
func (s *Store) RecoverDeliveries(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE alert_deliveries SET status=CASE WHEN attempts>=max_attempts THEN 'failed' ELSE 'retry' END,next_attempt_at=?,last_error='delivery interrupted by Core restart',claimed_at=NULL,updated_at=? WHERE status='sending'`, stamp(s.now()), stamp(s.now()))
	return e
}
func (s *Store) ListDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,COALESCE(alert_id,''),channel_id,channel_name,kind,test_send,status,attempts,max_attempts,next_attempt_at,delivered_at,http_status,last_error,created_at FROM alert_deliveries ORDER BY created_at DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var test int
		var httpStatus sql.NullInt64
		var next, delivered sql.NullInt64
		var created int64
		if e := rows.Scan(&d.ID, &d.AlertID, &d.ChannelID, &d.ChannelName, &d.Kind, &test, &d.Status, &d.Attempts, &d.MaxAttempts, &next, &delivered, &httpStatus, &d.LastError, &created); e != nil {
			return nil, e
		}
		if httpStatus.Valid {
			d.HTTPStatus = int(httpStatus.Int64)
		}
		d.TestSend = test == 1
		d.NextAttempt = unstamp(next)
		d.DeliveredAt = unstamp(delivered)
		d.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// Helpers used by the scheduler to make output stable in tests and reports.
func SortSamples(samples []Sample) {
	sort.Slice(samples, func(i, j int) bool {
		return sampleKey(samples[i].NodeID, samples[i].Kind, samples[i].SubjectID) < sampleKey(samples[j].NodeID, samples[j].Kind, samples[j].SubjectID)
	})
}
func EncodeSecretForTest(secret []byte) string { return base64.StdEncoding.EncodeToString(secret) }
