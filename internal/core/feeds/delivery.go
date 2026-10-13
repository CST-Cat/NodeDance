package feeds

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"time"
)

func (s *Store) queueNotifications(ctx context.Context) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT `+columns+` FROM feed_subscriptions WHERE json_extract(config_json,'$.enabled')=1 AND json_extract(config_json,'$.notificationMode')!='off' AND EXISTS (SELECT 1 FROM feed_items WHERE feed_id=feed_subscriptions.id AND notify_pending=1)`)
	if e != nil {
		return e
	}
	subs := []Subscription{}
	for rows.Next() {
		v, e := scan(rows)
		if e != nil {
			rows.Close()
			return e
		}
		subs = append(subs, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	now := stamp(s.now())
	if _, e = tx.ExecContext(ctx, `UPDATE feed_deliveries SET status='retry' WHERE status='sending' AND next_attempt<=?`, now); e != nil {
		return e
	}

	for _, sub := range subs {
		if !sub.Enabled || sub.NotificationMode == "off" || len(sub.ChannelIDs) == 0 {
			continue
		}
		var queued int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM feed_deliveries WHERE feed_id=? AND status IN ('queued','retry','sending')`, sub.ID).Scan(&queued); e != nil {
			return e
		}
		if queued+len(sub.ChannelIDs) > 100 {
			continue
		}
		var due int64
		if e = tx.QueryRowContext(ctx, `SELECT next_digest FROM feed_subscriptions WHERE id=?`, sub.ID).Scan(&due); e != nil {
			return e
		}
		if sub.NotificationMode == "digest" && due > now {
			continue
		}
		rows, e = tx.QueryContext(ctx, `SELECT id,title,url FROM feed_items WHERE feed_id=? AND notify_pending=1 ORDER BY first_seen,id LIMIT 50`, sub.ID)
		if e != nil {
			return e
		}
		type brief struct {
			ID    string `json:"id"`
			Title string `json:"title"`
			URL   string `json:"url"`
		}
		items := []brief{}
		for rows.Next() {
			var v brief
			if e = rows.Scan(&v.ID, &v.Title, &v.URL); e != nil {
				rows.Close()
				return e
			}
			items = append(items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(items) == 0 {
			continue
		}
		event := "feed_digest"
		if sub.NotificationMode == "immediate" {
			event = "feed_entries"
		}
		payload, _ := json.Marshal(map[string]any{"event": event, "feedId": sub.ID, "feedName": sub.Name, "occurredAt": s.now().UTC(), "items": items})
		for _, channel := range sub.ChannelIDs {
			id, e := newID()
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO feed_deliveries(id,feed_id,channel_id,revision,payload,status,next_attempt,created) VALUES(?,?,?,?,?,'queued',?,?)`, id, sub.ID, channel, sub.Revision, string(payload), now, now); e != nil {
				return e
			}
		}
		for _, item := range items {
			if _, e = tx.ExecContext(ctx, `UPDATE feed_items SET notify_pending=0 WHERE id=?`, item.ID); e != nil {
				return e
			}
		}
		if _, e = tx.ExecContext(ctx, `UPDATE feed_subscriptions SET next_digest=? WHERE id=?`, stamp(s.now().Add(time.Duration(sub.DigestMinutes)*time.Minute)), sub.ID); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM feed_deliveries WHERE created<? AND status NOT IN ('queued','retry','sending')`, stamp(s.now().Add(-90*24*time.Hour))); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) deliverOne(ctx context.Context, notify func(context.Context, string, string) error) {
	s.mu.Lock()
	e := s.queueNotifications(ctx)
	if e != nil {
		s.mu.Unlock()
		return
	}
	var id, feedID, channel, payload string
	var revision int64
	var attempts int
	e = s.db.QueryRowContext(ctx, `SELECT d.id,d.feed_id,d.channel_id,d.payload,d.revision,d.attempts FROM feed_deliveries d JOIN feed_subscriptions f ON f.id=d.feed_id WHERE d.status IN ('queued','retry') AND d.next_attempt<=? AND d.revision=f.revision ORDER BY d.created,d.id LIMIT 1`, stamp(s.now())).Scan(&id, &feedID, &channel, &payload, &revision, &attempts)
	if e != nil {
		s.mu.Unlock()
		return
	}
	sub, e := s.Get(ctx, feedID)
	if e != nil || !sub.Enabled || sub.NotificationMode == "off" {
		s.db.ExecContext(ctx, `UPDATE feed_deliveries SET status='canceled' WHERE id=?`, id)
		s.mu.Unlock()
		return
	}
	_, e = s.db.ExecContext(ctx, `UPDATE feed_deliveries SET status='sending',attempts=attempts+1,next_attempt=? WHERE id=?`, stamp(s.now().Add(time.Minute)), id)
	if e != nil {
		s.mu.Unlock()
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	s.sending[feedID] = cancel
	s.mu.Unlock()
	e = notify(sendCtx, channel, payload)
	cancel()
	s.mu.Lock()
	delete(s.sending, feedID)
	s.mu.Unlock()
	status := "succeeded"
	message := ""
	next := stamp(s.now())
	if e != nil {
		status = "retry"
		message = "subscription notification failed"
		if attempts >= 5 {
			status = "failed"
		}
		next = stamp(s.now().Add(time.Duration(1<<min(attempts, 10)) * time.Minute))
	}
	done, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	s.db.ExecContext(done, `UPDATE feed_deliveries SET status=?,error=?,next_attempt=? WHERE id=? AND status='sending'`, status, message, next, id)
	s.db.ExecContext(done, `DELETE FROM feed_deliveries WHERE feed_id=? AND status NOT IN ('queued','retry','sending') AND id NOT IN (SELECT id FROM feed_deliveries WHERE feed_id=? ORDER BY created DESC,id LIMIT 500)`, feedID, feedID)

}
func (s *Store) Deliveries(ctx context.Context, feedID string) ([]Delivery, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT id,feed_id,channel_id,status,attempts,error,created FROM feed_deliveries WHERE feed_id=? ORDER BY created DESC,id LIMIT 100`, feedID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var v Delivery
		var created int64
		if e = rows.Scan(&v.ID, &v.FeedID, &v.ChannelID, &v.Status, &v.Attempts, &v.Error, &created); e != nil {
			return nil, e
		}
		v.Created = clock(created)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) RetryDelivery(ctx context.Context, feedID, id string, event audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, e := s.Get(ctx, feedID)
	if e != nil {
		return e
	}
	if !sub.Enabled || sub.NotificationMode == "off" {
		return errors.New("subscription notifications are disabled")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.ExecContext(ctx, `UPDATE feed_deliveries SET status='retry',attempts=0,error='',next_attempt=? WHERE id=? AND feed_id=? AND revision=? AND status='failed'`, stamp(s.now()), id, feedID, sub.Revision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	event.Target = audit.Target{Kind: audit.TargetFeed, ID: feedID}
	if e = audit.Record(ctx, tx, event); e != nil {
		return e
	}
	return tx.Commit()
}
