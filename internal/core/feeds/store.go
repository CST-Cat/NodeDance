package feeds

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/google/uuid"
	"log"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Store struct {
	db      *sql.DB
	now     func() time.Time
	client  *http.Client
	mu      sync.Mutex
	active  map[string]context.CancelFunc
	sending map[string]context.CancelFunc
	hosts   map[string]bool
}

func New(db *sql.DB, now func() time.Time) *Store {
	return &Store{db: db, now: now, client: newHTTPClient(), active: map[string]context.CancelFunc{}, sending: map[string]context.CancelFunc{}, hosts: map[string]bool{}}
}
func (s *Store) Recover(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `UPDATE feed_subscriptions SET status='queued' WHERE status='running'`)
	if e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, `UPDATE feed_deliveries SET status='retry' WHERE status='sending'`)
	return e
}
func stamp(t time.Time) int64 { return t.Unix() }
func clock(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

const columns = `id,config_json,revision,status,run_id,error,last_checked,last_success,next_check,failures`

func scan(row interface{ Scan(...any) error }) (Subscription, error) {
	var sub Subscription
	var raw string
	var checked, success, next int64
	e := row.Scan(&sub.ID, &raw, &sub.Revision, &sub.Status, &sub.RunID, &sub.Error, &checked, &success, &next, &sub.Failures)
	rev := sub.Revision
	if e != nil {
		return sub, e
	}
	e = json.Unmarshal([]byte(raw), &sub.Input)
	sub.Revision = rev
	sub.LastChecked = clock(checked)
	sub.LastSuccess = clock(success)
	sub.NextCheck = clock(next)
	return sub, e
}
func (s *Store) Get(ctx context.Context, id string) (Subscription, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM feed_subscriptions WHERE id=?`, id))
}
func (s *Store) List(ctx context.Context) ([]Subscription, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+columns+` FROM feed_subscriptions ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Save(ctx context.Context, id string, in Input, event audit.Event) (Subscription, error) {
	if e := validate(&in); e != nil {
		return Subscription{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Subscription{}, e
	}
	defer tx.Rollback()
	status := "idle"
	if !in.Enabled {
		status = "inactive"
	}
	raw, _ := json.Marshal(in)
	if id == "" {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM feed_subscriptions`).Scan(&count); e != nil {
			return Subscription{}, e
		}
		if count >= 200 {
			return Subscription{}, errors.New("subscription limit is 200")
		}
		id, e = newID()
		if e != nil {
			return Subscription{}, e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO feed_subscriptions(id,config_json,revision,status,next_check,next_digest) VALUES(?,?,1,?,?,?)`, id, string(raw), status, stamp(s.now()), stamp(s.now().Add(time.Duration(in.DigestMinutes)*time.Minute)))
	} else {
		var previous string
		if e = tx.QueryRowContext(ctx, `SELECT config_json FROM feed_subscriptions WHERE id=? AND revision=?`, id, in.Revision).Scan(&previous); e != nil {
			return Subscription{}, ErrConflict
		}
		var old Input
		if e = json.Unmarshal([]byte(previous), &old); e != nil {
			return Subscription{}, e
		}
		if old.URL != in.URL {
			if _, e = tx.ExecContext(ctx, `DELETE FROM feed_items WHERE feed_id=?`, id); e != nil {
				return Subscription{}, e
			}
			if _, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET etag='',modified='',initialized=0,last_checked=0,last_success=0,failures=0 WHERE id=?`, id); e != nil {
				return Subscription{}, e
			}
		}
		var res sql.Result
		res, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET config_json=?,revision=revision+1,status=?,error='',next_check=?,next_digest=? WHERE id=? AND revision=?`, string(raw), status, stamp(s.now()), stamp(s.now().Add(time.Duration(in.DigestMinutes)*time.Minute)), id, in.Revision)
		if e == nil {
			n, _ := res.RowsAffected()
			if n != 1 {
				e = ErrConflict
			}
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE feed_deliveries SET status='canceled',error='subscription changed' WHERE feed_id=? AND status IN ('queued','retry','sending')`, id)
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE feed_items SET notify_pending=0 WHERE feed_id=?`, id)
		}
	}
	if e != nil {
		return Subscription{}, e
	}
	event.Target = audit.Target{Kind: audit.TargetFeed, ID: id}
	if e = audit.Record(ctx, tx, event); e != nil {
		return Subscription{}, e
	}
	if e = tx.Commit(); e != nil {
		return Subscription{}, e
	}
	if cancel := s.active[id]; cancel != nil {
		cancel()
	}
	if cancel := s.sending[id]; cancel != nil {
		cancel()
	}
	return s.Get(ctx, id)
}
func (s *Store) Delete(ctx context.Context, id string, revision int64, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `DELETE FROM feed_subscriptions WHERE id=? AND revision=?`, id, revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	event.Target = audit.Target{Kind: audit.TargetFeed, ID: id}
	if e = audit.Record(ctx, tx, event); e != nil {
		return e
	}
	if e = tx.Commit(); e == nil {
		if cancel := s.sending[id]; cancel != nil {
			cancel()
		}
		if cancel := s.active[id]; cancel != nil {
			cancel()
		}
	}
	return e
}
func (s *Store) Action(ctx context.Context, id, action string, event audit.Event) (Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Subscription{}, e
	}
	defer tx.Rollback()
	sub, e := scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM feed_subscriptions WHERE id=?`, id))
	if e != nil {
		return sub, e
	}
	if action == "refresh" {
		if !sub.Enabled {
			return sub, errors.New("subscription is disabled")
		}
		if sub.Status != "running" && sub.Status != "queued" {
			_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET status='queued',error='',next_check=MAX(?,last_started+60) WHERE id=?`, stamp(s.now()), id)
		}
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET status='canceled',next_check=? WHERE id=?`, stamp(s.now().Add(time.Duration(sub.IntervalMinutes)*time.Minute)), id)
	}
	if e != nil {
		return sub, e
	}
	event.Target = audit.Target{Kind: audit.TargetFeed, ID: id}
	if e = audit.Record(ctx, tx, event); e != nil {
		return sub, e
	}
	if e = tx.Commit(); e != nil {
		return sub, e
	}
	if action == "cancel" {
		if cancel := s.active[id]; cancel != nil {
			cancel()
		}
	}
	return s.Get(ctx, id)
}
func (s *Store) Items(ctx context.Context, id string, offset, limit int, unread bool, search string) ([]Item, error) {
	if offset < 0 || offset > 10000 || limit < 1 || limit > 100 || len(search) > 200 {
		return nil, errors.New("invalid subscription pagination or search")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,feed_id,title,summary,url,published,first_seen,is_read FROM feed_items WHERE feed_id=? AND id IN (SELECT id FROM feed_items WHERE feed_id=? ORDER BY first_seen DESC,id LIMIT 500) AND (?=0 OR is_read=0) AND (?='' OR instr(lower(title||' '||summary),lower(?))>0) ORDER BY first_seen DESC,id LIMIT ? OFFSET ?`, id, id, unread, search, search, limit, offset)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var item Item
		var p sql.NullInt64
		var seen int64
		if e = rows.Scan(&item.ID, &item.FeedID, &item.Title, &item.Summary, &item.URL, &p, &seen, &item.Read); e != nil {
			return nil, e
		}
		item.FirstSeen = clock(seen)
		if p.Valid {
			v := clock(p.Int64)
			item.Published = &v
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) MarkRead(ctx context.Context, id, itemID string, read bool, event audit.Event) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE feed_items SET is_read=? WHERE feed_id=? AND id=?`, read, id, itemID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return sql.ErrNoRows
	}
	event.Target = audit.Target{Kind: audit.TargetFeed, ID: id}
	if e = audit.Record(ctx, tx, event); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Run(ctx context.Context, notify func(context.Context, string, string) error) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				s.refreshOne(ctx)
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			if notify != nil {
				s.deliverOne(ctx, notify)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	<-ctx.Done()
	wg.Wait()
	if t, ok := s.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}
func (s *Store) refreshOne(parent context.Context) {
	s.mu.Lock()
	if _, e := s.db.ExecContext(parent, `UPDATE feed_subscriptions SET status='queued' WHERE status='running' AND last_started<?`, stamp(s.now().Add(-time.Minute))); e != nil {
		s.mu.Unlock()
		return
	}
	list, e := s.due(parent)
	if e != nil {
		s.mu.Unlock()
		return
	}
	var sub Subscription
	var host, etag, modified string
	for _, v := range list {
		if !v.Enabled || v.Status == "running" || s.active[v.ID] != nil {
			continue
		}
		if v.Status != "queued" && v.NextCheck.After(s.now()) {
			continue
		}
		u, _ := ValidateURL(v.URL)
		if u == nil || s.hosts[u.Hostname()] {
			continue
		}
		var started int64
		if e = s.db.QueryRowContext(parent, `SELECT last_started,etag,modified FROM feed_subscriptions WHERE id=?`, v.ID).Scan(&started, &etag, &modified); e != nil {
			continue
		}
		if started > stamp(s.now().Add(-time.Minute)) {
			continue
		}
		sub = v
		host = u.Hostname()
		break
	}
	if sub.ID == "" {
		s.mu.Unlock()
		return
	}
	sub.RunID, e = newID()
	if e != nil {
		s.mu.Unlock()
		return
	}
	_, e = s.db.ExecContext(parent, `UPDATE feed_subscriptions SET status='running',run_id=?,last_started=? WHERE id=?`, sub.RunID, stamp(s.now()), sub.ID)
	if e != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 25*time.Second)
	s.active[sub.ID] = cancel
	s.hosts[host] = true
	s.mu.Unlock()
	result, fetchErr := s.fetch(ctx, sub, etag, modified)
	cancel()
	// Store outcomes even on shutdown using an independently bounded context.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if e := s.finish(finishCtx, sub, result, fetchErr); e != nil {
		log.Printf("NodeDance subscription result persistence failed: %v", e)
	}
	s.mu.Lock()
	delete(s.active, sub.ID)
	delete(s.hosts, host)
	s.mu.Unlock()
}
func (s *Store) finish(ctx context.Context, sub Subscription, result fetched, fetchErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var revision int64
	var status, run string
	var initialized int
	if e = tx.QueryRowContext(ctx, `SELECT revision,status,run_id,initialized FROM feed_subscriptions WHERE id=?`, sub.ID).Scan(&revision, &status, &run, &initialized); e != nil {
		return e
	}
	if revision != sub.Revision || status != "running" || run != sub.RunID {
		return nil
	}
	now := s.now()
	next := now.Add(time.Duration(sub.IntervalMinutes)*time.Minute + time.Duration(rand.IntN(10))*time.Second)
	if fetchErr != nil {
		delay := time.Duration(1<<min(sub.Failures, 10)) * time.Minute
		delay = max(delay, time.Duration(sub.IntervalMinutes)*time.Minute)
		delay = min(delay, 24*time.Hour)
		next = now.Add(max(delay, result.retry))
		_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET status='failed',error=?,failures=failures+1,last_checked=?,next_check=? WHERE id=?`, safeError(fetchErr), stamp(now), stamp(next), sub.ID)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	if !result.unchanged {
		for _, item := range result.items {
			pending := initialized == 1 && sub.NotificationMode != "off" && len(sub.ChannelIDs) > 0 && matches(sub.Keywords, item)
			var published any
			if item.Published != nil {
				published = stamp(*item.Published)
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO feed_items(id,feed_id,title,summary,url,published,first_seen,last_seen,notify_pending) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,summary=excluded.summary,url=excluded.url,published=excluded.published,last_seen=excluded.last_seen`, item.ID, sub.ID, item.Title, item.Summary, item.URL, published, stamp(now), stamp(now), pending)
			if e != nil {
				return e
			}
		}
		// Retain current feed identifiers for deduplication; old identifiers expire after 180 days.
		if _, e = tx.ExecContext(ctx, `DELETE FROM feed_items WHERE feed_id=? AND last_seen<?`, sub.ID, stamp(now.Add(-180*24*time.Hour))); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `DELETE FROM feed_items WHERE feed_id=? AND id NOT IN (SELECT id FROM feed_items WHERE feed_id=? ORDER BY last_seen DESC,id LIMIT 10000)`, sub.ID, sub.ID); e != nil {
			return e
		}
		// Keep only 500 full summaries. Identity/read metadata remains available for deduplication.
		if _, e = tx.ExecContext(ctx, `UPDATE feed_items SET summary='' WHERE feed_id=? AND summary!='' AND id NOT IN (SELECT id FROM feed_items WHERE feed_id=? ORDER BY first_seen DESC,id LIMIT 500)`, sub.ID, sub.ID); e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET etag=?,modified=?,initialized=1 WHERE id=?`, result.etag, result.modified, sub.ID)
		if e != nil {
			return e
		}
	} else {
		if result.etag != "" {
			_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET etag=? WHERE id=?`, result.etag, sub.ID)
			if e != nil {
				return e
			}
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET status='succeeded',error='',failures=0,last_checked=?,last_success=?,next_check=? WHERE id=?`, stamp(now), stamp(now), stamp(next), sub.ID)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func safeError(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, context.Canceled) {
		return "subscription refresh canceled"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "subscription refresh timed out"
	}
	v := e.Error()
	if len(v) > 200 {
		return "subscription operation failed"
	}
	return v
}
func matches(keywords []string, item Item) bool {
	if len(keywords) == 0 {
		return true
	}
	v := strings.ToLower(item.Title + " " + item.Summary)
	for _, k := range keywords {
		if strings.Contains(v, strings.ToLower(strings.TrimSpace(k))) {
			return true
		}
	}
	return false
}
func ParsePage(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	return strconv.Atoi(value)
}

func newID() (string, error) { v, e := uuid.NewRandom(); return v.String(), e }

func (s *Store) due(ctx context.Context) ([]Subscription, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+columns+` FROM feed_subscriptions WHERE json_extract(config_json,'$.enabled')=1 AND status!='running' AND (status='queued' OR next_check<=?) AND last_started<=? ORDER BY next_check,id LIMIT 200`, stamp(s.now()), stamp(s.now().Add(-time.Minute)))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
