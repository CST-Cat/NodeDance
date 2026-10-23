package domains

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/google/uuid"
)

type RevokeInput struct {
	ConfirmInput
	AccountID string `json:"accountId"`
	Reason    int    `json:"reason"`
}
type jobRequest struct {
	IssueInput
	TargetRevision   int64 `json:"targetRevision"`
	RevocationReason int   `json:"revocationReason"`
}

func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var c, u int64
	e := row.Scan(&j.ID, &j.Kind, &j.Status, &j.CertificateID, &j.Error, &c, &u)
	j.CreatedAt = time.Unix(c, 0).UTC()
	j.UpdatedAt = time.Unix(u, 0).UTC()
	return j, e
}

const jobColumns = `id,kind,status,certificate_id,error,created_at,updated_at`

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM certificate_jobs WHERE id=?`, id))
}
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+jobColumns+` FROM certificate_jobs ORDER BY created_at DESC,id DESC LIMIT 500`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, e := scanJob(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) QueueIssue(ctx context.Context, in IssueInput, key string, event audit.Event) (Job, error) {
	if e := normalizePolicy(&in.Policy); e != nil {
		return Job{}, e
	}
	return s.queue(ctx, "issue", "", jobRequest{IssueInput: in}, key, event)
}
func (s *Store) QueueRenew(ctx context.Context, id string, in ConfirmInput, key string, event audit.Event) (Job, error) {
	c, e := s.Certificate(ctx, id)
	if e != nil {
		return Job{}, e
	}
	if c.Source != "acme" {
		return Job{}, errors.New("only active ACME certificates can be renewed")
	}
	request := IssueInput{Name: c.Name, DomainIDs: c.DomainIDs, AccountID: c.AccountID, Challenge: c.Challenge, ProviderID: c.ProviderID, KeyType: c.KeyType, Policy: c.Policy, Confirm: in.Confirm}
	return s.queue(ctx, "renew", id, jobRequest{IssueInput: request, TargetRevision: in.Revision}, key, event)
}
func (s *Store) QueueRevoke(ctx context.Context, id string, in RevokeInput, key string, event audit.Event) (Job, error) {
	c, e := s.Certificate(ctx, id)
	if e != nil {
		return Job{}, e
	}
	switch in.Reason {
	case 0, 1, 3, 4, 5, 9:
	default:
		return Job{}, errors.New("invalid revocation reason")
	}
	account := in.AccountID
	if account == "" {
		account = c.AccountID
	}
	if !validID(account) {
		return Job{}, errors.New("a CA account is required to revoke this certificate")
	}
	return s.queue(ctx, "revoke", id, jobRequest{IssueInput: IssueInput{AccountID: account, Confirm: in.Confirm}, TargetRevision: in.Revision, RevocationReason: in.Reason}, key, event)
}
func (s *Store) queue(ctx context.Context, kind, id string, in jobRequest, key string, event audit.Event) (Job, error) {
	if !in.Confirm || len(key) < 8 || len(key) > 128 {
		return Job{}, errors.New("confirmation and a stable 8..128 byte Idempotency-Key are required")
	}
	raw, _ := json.Marshal(in)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s", event.ActorID.Int64, kind, key)))
	identity := hex.EncodeToString(digest[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	var previous, previousID string
	var previousCert string
	e := s.db.QueryRowContext(ctx, `SELECT id,certificate_id,input_json FROM certificate_jobs WHERE identity=?`, identity).Scan(&previousID, &previousCert, &previous)
	if e == nil {
		if previous != string(raw) || previousCert != id {
			return Job{}, ErrConflict
		}
		return s.Job(ctx, previousID)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return Job{}, e
	}
	if kind != "revoke" {
		if e = s.validateIssue(ctx, &in.IssueInput); e != nil {
			return Job{}, e
		}
		if e = s.policyChannels(ctx, in.ChannelIDs); e != nil {
			return Job{}, e
		}
	} else {
		var found int
		if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_accounts WHERE id=?`, in.AccountID).Scan(&found); e != nil {
			return Job{}, e
		}
		if found != 1 {
			return Job{}, sql.ErrNoRows
		}
	}
	if id != "" {
		c, e := s.Certificate(ctx, id)
		if e != nil {
			return Job{}, e
		}
		if c.Revision != in.TargetRevision {
			return Job{}, ErrConflict
		}
		if c.Status == "revoked" {
			return Job{}, ErrConflict
		}
		var n int
		if e = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_jobs WHERE certificate_id=? AND status IN ('queued','running')`, id).Scan(&n); e != nil {
			return Job{}, e
		}
		if n > 0 {
			return Job{}, ErrConflict
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Job{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM certificate_jobs WHERE status IN ('queued','running')`).Scan(&count); e != nil {
		return Job{}, e
	}
	if count >= 500 {
		return Job{}, errors.New("certificate job queue is full")
	}
	if kind == "issue" {
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM managed_certificates)+(SELECT COUNT(*) FROM certificate_jobs WHERE kind='issue' AND status IN ('queued','running'))`).Scan(&count); e != nil {
			return Job{}, e
		}
		if count >= 5000 {
			return Job{}, errors.New("certificate inventory limit is 5000")
		}
	}
	jobID := uuid.NewString()
	now := s.now().Unix()
	if _, e = tx.ExecContext(ctx, `INSERT INTO certificate_jobs(id,kind,certificate_id,status,input_json,identity,created_at,updated_at) VALUES(?,?,?,'queued',?,?,?,?)`, jobID, kind, id, string(raw), identity, now, now); e != nil {
		return Job{}, e
	}
	if e = record(ctx, tx, event, "certificate_job_submit", audit.TargetCertificateJob, jobID); e != nil {
		return Job{}, e
	}
	if e = tx.Commit(); e != nil {
		return Job{}, e
	}
	return s.Job(ctx, jobID)
}
func (s *Store) Retry(ctx context.Context, id, key string, event audit.Event) (Job, error) {
	j, e := s.Job(ctx, id)
	if e != nil {
		return j, e
	}
	if j.Status != "failed" && j.Status != "canceled" {
		return Job{}, ErrConflict
	}
	var raw string
	if e = s.db.QueryRowContext(ctx, `SELECT input_json FROM certificate_jobs WHERE id=?`, id).Scan(&raw); e != nil {
		return Job{}, e
	}
	var in jobRequest
	if e = json.Unmarshal([]byte(raw), &in); e != nil {
		return Job{}, e
	}
	return s.queue(ctx, j.Kind, j.CertificateID, in, key, event)
}
func (s *Store) Cancel(ctx context.Context, id string, event audit.Event) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, e := s.Job(ctx, id)
	if e != nil {
		return j, e
	}
	if j.Status != "queued" && j.Status != "running" {
		return j, nil
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return j, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE certificate_jobs SET status='canceled',updated_at=? WHERE id=? AND status IN ('queued','running')`, s.now().Unix(), id); e != nil {
		return j, e
	}
	if e = record(ctx, tx, event, "certificate_job_cancel", audit.TargetCertificateJob, id); e != nil {
		return j, e
	}
	if e = tx.Commit(); e != nil {
		return j, e
	}
	if cancel := s.active[id]; cancel != nil {
		cancel()
	}
	return s.Job(ctx, id)
}
func (s *Store) Recover(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE certificate_jobs SET status='failed',error='Core restarted; inspect and explicitly retry',updated_at=? WHERE status='running'`, s.now().Unix())
	return e
}
func (s *Store) Run(ctx context.Context, notify func(context.Context, string, string) error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	maintenance := time.Time{}
	for {
		if ctx.Err() != nil {
			return
		}
		if s.now().Sub(maintenance) >= time.Minute {
			maintenance = s.now()
			s.maintain(ctx, notify)
		}
		s.work(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Store) work(parent context.Context) {
	s.mu.Lock()
	j, e := scanJob(s.db.QueryRowContext(parent, `SELECT `+jobColumns+` FROM certificate_jobs WHERE status='queued' ORDER BY created_at,id LIMIT 1`))
	if e != nil {
		s.mu.Unlock()
		return
	}
	var raw string
	if e = s.db.QueryRowContext(parent, `SELECT input_json FROM certificate_jobs WHERE id=?`, j.ID).Scan(&raw); e != nil {
		s.mu.Unlock()
		return
	}
	if _, e = s.db.ExecContext(parent, `UPDATE certificate_jobs SET status='running',updated_at=? WHERE id=? AND status='queued'`, s.now().Unix(), j.ID); e != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	ctx = context.WithValue(ctx, jobContextKey{}, j.ID)
	s.active[j.ID] = cancel
	s.mu.Unlock()
	defer cancel()
	var in jobRequest
	e = json.Unmarshal([]byte(raw), &in)
	var certPEM, keyPEM []byte
	if e == nil && j.Kind == "revoke" {
		var c Certificate
		c, e = s.Certificate(ctx, j.CertificateID)
		if e == nil {
			c.AccountID = in.AccountID
			certPEM, keyPEM, e = s.Material(ctx, c.ID)
			if e == nil {
				if issuer, ok := s.issuer.(*acmeIssuer); ok {
					e = issuer.revoke(ctx, c, certPEM, keyPEM, in.RevocationReason)
				} else {
					e = s.issuer.Revoke(ctx, c, certPEM, keyPEM)
				}
			}
		}
	} else if e == nil {
		var names []string
		names, e = s.Names(ctx, in.DomainIDs)
		if e == nil {
			certPEM, keyPEM, e = s.issuer.Issue(ctx, in.IssueInput, names)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, j.ID)
	// Cancellation and shutdown must never overwrite a newer job decision. A
	// previous certificate remains intact until an entire renewal commits.
	finalCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	current, readErr := s.Job(finalCtx, j.ID)
	if readErr != nil || current.Status != "running" {
		return
	}
	if ctx.Err() != nil && e == nil {
		e = ctx.Err()
	}
	if e == nil {
		e = s.commitJob(finalCtx, j, in, certPEM, keyPEM)
	}
	if e != nil {
		_, _ = s.db.ExecContext(finalCtx, `UPDATE certificate_jobs SET status='failed',error=?,updated_at=? WHERE id=? AND status='running'`, safeJobError(e), s.now().Unix(), j.ID)
	}
}
func (s *Store) commitJob(ctx context.Context, j Job, in jobRequest, certPEM, keyPEM []byte) error {
	var c Certificate
	var e error
	if j.Kind != "revoke" {
		c, e = parseCertificate(certPEM, keyPEM)
		if e != nil {
			return e
		}
		c.DomainIDs = in.DomainIDs
		if e = s.validateCoverage(ctx, c, certPEM, keyPEM); e != nil {
			return e
		}
		if !c.NotAfter.After(s.now()) || c.NotBefore.After(s.now().Add(5*time.Minute)) {
			return errors.New("CA returned a certificate outside its valid time range")
		}
		c.Name = in.Name
		c.Source = "acme"
		c.AccountID = in.AccountID
		c.Challenge = in.Challenge
		c.ProviderID = in.ProviderID
		c.Policy = in.Policy
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := s.now().Unix()
	id := j.CertificateID
	if j.Kind == "revoke" {
		r, e := tx.ExecContext(ctx, `UPDATE managed_certificates SET revoked=1,revision=revision+1,updated_at=? WHERE id=? AND revision=?`, now, id, in.TargetRevision)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrConflict
		}
	} else {
		if id == "" {
			id = uuid.NewString()
		}
		c.ID = id
		raw, _ := json.Marshal(c)
		if j.Kind == "issue" {
			_, e = tx.ExecContext(ctx, `INSERT INTO managed_certificates(id,metadata_json,certificate_pem,key_pem,revision,created_at,updated_at) VALUES(?,?,?,?,1,?,?)`, id, string(raw), certPEM, keyPEM, now, now)
		} else {
			var result sql.Result
			result, e = tx.ExecContext(ctx, `UPDATE managed_certificates SET metadata_json=?,certificate_pem=?,key_pem=?,revision=revision+1,updated_at=?,notification_bucket='',notification_error='' WHERE id=? AND revision=?`, string(raw), certPEM, keyPEM, now, id, in.TargetRevision)
			if e == nil {
				n, _ := result.RowsAffected()
				if n != 1 {
					e = ErrConflict
				}
			}
		}
		if e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE certificate_jobs SET status='succeeded',certificate_id=?,error='',updated_at=? WHERE id=? AND status='running'`, id, now, j.ID); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) maintain(ctx context.Context, notify func(context.Context, string, string) error) {
	if issuer, ok := s.issuer.(*acmeIssuer); ok {
		issuer.recoverDNS(ctx)
	}
	certificates, e := s.Certificates(ctx)
	if e != nil {
		return
	}
	for _, c := range certificates {
		if ctx.Err() != nil {
			return
		}
		if c.Status == "revoked" {
			continue
		}
		if c.AutoRenew && c.Source == "acme" && c.NotAfter.Sub(s.now()) <= time.Duration(c.RenewBeforeDays)*24*time.Hour {
			_, _ = s.QueueRenew(ctx, c.ID, ConfirmInput{Confirm: true, Revision: c.Revision}, fmt.Sprintf("auto/%s/%s", c.ID, s.now().UTC().Format("2006-01-02")), audit.Event{OccurredAt: s.now(), RemoteAddr: "unknown"})
		}
		if len(c.ChannelIDs) == 0 || c.NotAfter.Sub(s.now()) > time.Duration(c.NotifyBeforeDays)*24*time.Hour || notify == nil {
			continue
		}
		var previous string
		if s.db.QueryRowContext(ctx, `SELECT notification_bucket FROM managed_certificates WHERE id=?`, c.ID).Scan(&previous) != nil {
			continue
		}
		state := struct {
			Day  string          `json:"day"`
			Sent map[string]bool `json:"sent"`
			Next time.Time       `json:"next"`
		}{Sent: map[string]bool{}}
		if previous != "" && json.Unmarshal([]byte(previous), &state) != nil {
			continue
		}
		day := s.now().UTC().Format("2006-01-02")
		if state.Day != day {
			state.Day = day
			state.Sent = map[string]bool{}
			state.Next = time.Time{}
		}
		if s.now().Before(state.Next) {
			continue
		}
		failed := false
		for _, channel := range c.ChannelIDs {
			if state.Sent[channel] {
				continue
			}
			deliveryCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			e = notify(deliveryCtx, channel, fmt.Sprintf("NodeDance certificate %s (%s) expires at %s; %d days remaining", c.Name, c.ID, c.NotAfter.Format(time.RFC3339), c.DaysRemaining))
			cancel()
			if e != nil {
				failed = true
			} else {
				state.Sent[channel] = true
			}
		}
		message := ""
		if failed {
			message = "expiry notification delivery failed; will retry"
			state.Next = s.now().Add(5 * time.Minute)
		} else {
			state.Next = time.Time{}
		}
		raw, _ := json.Marshal(state)
		_, _ = s.db.ExecContext(ctx, `UPDATE managed_certificates SET notification_bucket=?,notification_error=? WHERE id=? AND revision=?`, string(raw), message, c.ID, c.Revision)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM certificate_jobs WHERE status IN ('succeeded','failed','canceled') AND updated_at<?`, s.now().Add(-90*24*time.Hour).Unix())
}
