package feeds

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	db, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)
	return New(db.DB, func() time.Time { return now }), &now
}
func event(action string) audit.Event { return audit.Event{Action: action, Outcome: "succeeded"} }
func create(t *testing.T, s *Store, in Input) Subscription {
	t.Helper()
	if in.Name == "" {
		in.Name = "Example"
	}
	if in.URL == "" {
		in.URL = "https://example.org/feed"
	}
	if in.NotificationMode == "" {
		in.NotificationMode = "off"
	}
	if in.Keywords == nil {
		in.Keywords = []string{}
	}
	if in.ChannelIDs == nil {
		in.ChannelIDs = []string{}
	}
	v, e := s.Save(context.Background(), "", in, event("feed_save"))
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func begin(t *testing.T, s *Store, sub Subscription) Subscription {
	t.Helper()
	sub.RunID, _ = newID()
	_, e := s.db.Exec(`UPDATE feed_subscriptions SET status='running',run_id=? WHERE id=?`, sub.RunID, sub.ID)
	if e != nil {
		t.Fatal(e)
	}
	return sub
}
func TestIntervalsAndRevisionSafety(t *testing.T) {
	s, _ := fixture(t)
	sub := create(t, s, Input{Enabled: true})
	if sub.IntervalMinutes != 30 {
		t.Fatal(sub)
	}
	in := sub.Input
	in.IntervalMinutes = 1
	v, e := s.Save(context.Background(), sub.ID, in, event("feed_save"))
	if e != nil || v.IntervalMinutes != 1 {
		t.Fatal(v, e)
	}
	if _, e = s.Save(context.Background(), sub.ID, in, event("feed_save")); !errors.Is(e, ErrConflict) {
		t.Fatal("lost update", e)
	}
	in = v.Input
	in.IntervalMinutes = -1
	if _, e = s.Save(context.Background(), sub.ID, in, event("feed_save")); e == nil {
		t.Fatal("invalid interval")
	}
	in = v.Input
	in.IntervalMinutes = 10081
	if _, e = s.Save(context.Background(), sub.ID, in, event("feed_save")); e == nil {
		t.Fatal("unbounded interval")
	}
	if e = s.Delete(context.Background(), v.ID, sub.Revision, event("feed_delete")); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
func TestDedupReadStateFirstImportAndDigest(t *testing.T) {
	s, now := fixture(t)
	sub := create(t, s, Input{Enabled: true, NotificationMode: "digest", DigestMinutes: 5, Keywords: []string{"security"}, ChannelIDs: []string{"channel"}})
	first := fetched{items: []Item{{ID: "a", FeedID: sub.ID, Title: "security one", Summary: "old"}}, etag: "first"}
	if e := s.finish(context.Background(), begin(t, s, sub), first, nil); e != nil {
		t.Fatal(e)
	}
	*now = now.Add(6 * time.Minute)
	if e := s.queueNotifications(context.Background()); e != nil {
		t.Fatal(e)
	}
	d, _ := s.Deliveries(context.Background(), sub.ID)
	if len(d) != 0 {
		t.Fatal("initial notification storm", d)
	}
	if e := s.MarkRead(context.Background(), sub.ID, "a", true, event("feed_read")); e != nil {
		t.Fatal(e)
	}
	next := fetched{items: []Item{{ID: "a", FeedID: sub.ID, Title: "security edited", Summary: "new"}, {ID: "b", FeedID: sub.ID, Title: "security two"}, {ID: "c", FeedID: sub.ID, Title: "ordinary"}}}
	if e := s.finish(context.Background(), begin(t, s, sub), next, nil); e != nil {
		t.Fatal(e)
	}
	if e := s.queueNotifications(context.Background()); e != nil {
		t.Fatal(e)
	}
	d, e := s.Deliveries(context.Background(), sub.ID)
	if e != nil || len(d) != 1 {
		t.Fatal(d, e)
	}
	items, e := s.Items(context.Background(), sub.ID, 0, 50, false, "")
	if e != nil || len(items) != 3 {
		t.Fatal(items, e)
	}
	for _, v := range items {
		if v.ID == "a" && (!v.Read || v.Summary != "new") {
			t.Fatal("read state lost", v)
		}
	}
	if e := s.finish(context.Background(), begin(t, s, sub), next, nil); e != nil {
		t.Fatal(e)
	}
	*now = now.Add(6 * time.Minute)
	s.queueNotifications(context.Background())
	d, _ = s.Deliveries(context.Background(), sub.ID)
	if len(d) != 1 {
		t.Fatal("duplicate notifications", d)
	}
	in := sub.Input
	in.Enabled = false
	updated, e := s.Save(context.Background(), sub.ID, in, event("feed_save"))
	if e != nil {
		t.Fatal(e)
	}
	d, _ = s.Deliveries(context.Background(), sub.ID)
	if d[0].Status != "canceled" {
		t.Fatal(d)
	}
	if e = s.Delete(context.Background(), sub.ID, updated.Revision, event("feed_delete")); e != nil {
		t.Fatal(e)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM feed_items`).Scan(&count)
	if count != 0 {
		t.Fatal("delete did not cascade")
	}
}
func TestStaleOutcomeCancelAndRecovery(t *testing.T) {
	s, _ := fixture(t)
	sub := begin(t, s, create(t, s, Input{Enabled: true}))
	s.Action(context.Background(), sub.ID, "cancel", event("feed_cancel"))
	s.finish(context.Background(), sub, fetched{items: []Item{{ID: "late", FeedID: sub.ID}}}, nil)
	items, _ := s.Items(context.Background(), sub.ID, 0, 50, false, "")
	if len(items) != 0 {
		t.Fatal("late result published")
	}
	sub = begin(t, s, sub)
	if e := s.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Get(context.Background(), sub.ID)
	if v.Status != "queued" {
		t.Fatal(v.Status)
	}
	s.db.Exec(`UPDATE feed_subscriptions SET status='running',run_id=? WHERE id=?`, sub.RunID, sub.ID)
	in := sub.Input
	in.Enabled = false
	s.Save(context.Background(), sub.ID, in, event("feed_save"))
	s.finish(context.Background(), sub, fetched{}, nil)
	v, _ = s.Get(context.Background(), sub.ID)
	if v.Status != "inactive" || !v.LastSuccess.IsZero() {
		t.Fatal("stale finish overwrote config", v)
	}
}
func TestPublicAddressPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "100.100.100.200", "169.254.169.254", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1", "2001:db8::1", "64:ff9b::7f00:1", "192.0.2.1", "198.18.0.1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatal("allowed", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		if !publicIP(netip.MustParseAddr(ip)) {
			t.Fatal("rejected public", ip)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, e := safeDial(ctx, "tcp", "127.0.0.1:80"); e == nil {
		c.Close()
		t.Fatal("dialed loopback")
	}
	for _, u := range []string{"file:///etc/passwd", "https://user:pass@example.org/", "https://example.org:22/", "https://example.org/#secret"} {
		if _, e := ValidateURL(u); e == nil {
			t.Fatal("allowed URL", u)
		}
	}
}
func TestConditionalRSSAtomAndMaliciousContent(t *testing.T) {
	s, _ := fixture(t)
	var body = `<rss version="2.0"><channel><title>Example</title><link>https://example.org</link><description>News</description><item><guid>not-a-url</guid><title><![CDATA[<b>Title</b><script>bad()</script>]]></title><description><![CDATA[<img src="https://tracker.example"><p>Hello</p>]]></description><link>/article</link></item></channel></rss>`
	var requests int
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("If-None-Match") == `"one"` {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("ETag", `"one"`)
		io.WriteString(w, body)
	}))
	defer host.Close()
	// Only this fixture client can reach loopback; production always uses safeDial.
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(host.URL, "http://"))
	}}}
	sub := create(t, s, Input{URL: "http://example.org/feed", Enabled: true})
	result, e := s.fetch(context.Background(), sub, "", "")
	if e != nil || len(result.items) != 1 {
		t.Fatal(result, e)
	}
	v := result.items[0]
	if v.Title != "Title" || v.Summary != "Hello" || v.URL != "http://example.org/article" {
		t.Fatal(v)
	}
	result, e = s.fetch(context.Background(), sub, `"one"`, "")
	if e != nil || !result.unchanged || requests != 2 {
		t.Fatal(result, e)
	}
	body = `<feed xmlns="http://www.w3.org/2005/Atom"><id>urn:feed</id><title>Atom</title><updated>2026-10-13T00:00:00Z</updated><entry><id>urn:entry</id><title>Atom item</title><updated>2026-10-13T00:00:00Z</updated><link href="javascript:alert(1)"/><summary>Text</summary></entry></feed>`
	result, e = s.fetch(context.Background(), sub, "", "")
	if e != nil || len(result.items) != 1 || result.items[0].URL != "" {
		t.Fatal(result, e)
	}
	for _, bad := range []string{`<!DOCTYPE rss [<!ENTITY x SYSTEM "file:///etc/passwd">]><rss/>`, strings.Repeat("x", maxFeedBytes+1), strings.Repeat("<a>", 65) + strings.Repeat("</a>", 65)} {
		body = bad
		if _, e = s.fetch(context.Background(), sub, "", ""); e == nil {
			t.Fatal("accepted malformed or unbounded input")
		}
	}
}
func TestRefreshDoesNotHoldDBDuringIOAndCancels(t *testing.T) {
	s, _ := fixture(t)
	sub := create(t, s, Input{Enabled: true})
	entered := make(chan struct{})
	s.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	done := make(chan struct{})
	go func() { s.refreshOne(context.Background()); close(done) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, e := s.Get(ctx, sub.ID); e != nil {
		t.Fatal("DB blocked during fetch", e)
	}
	if _, e := s.Action(ctx, sub.ID, "cancel", event("feed_cancel")); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("cancel did not stop request")
	}
	v, _ := s.Get(ctx, sub.ID)
	if v.Status != "canceled" {
		t.Fatal(v)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNotificationRetryAndRecovery(t *testing.T) {
	s, now := fixture(t)
	sub := create(t, s, Input{Enabled: true, NotificationMode: "immediate", ChannelIDs: []string{"c"}})
	s.db.Exec(`UPDATE feed_subscriptions SET initialized=1 WHERE id=?`, sub.ID)
	if e := s.finish(context.Background(), begin(t, s, sub), fetched{items: []Item{{ID: "a", FeedID: sub.ID, Title: "New"}}}, nil); e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	send := func(context.Context, string, string) error {
		calls.Add(1)
		return fmt.Errorf("credential=must-not-be-stored")
	}
	s.deliverOne(context.Background(), send)
	d, _ := s.Deliveries(context.Background(), sub.ID)
	if len(d) != 1 || d[0].Status != "retry" || strings.Contains(d[0].Error, "credential") {
		t.Fatal(d)
	}
	s.deliverOne(context.Background(), send)
	if calls.Load() != 1 {
		t.Fatal("ignored backoff")
	}
	*now = now.Add(time.Minute)
	s.deliverOne(context.Background(), func(context.Context, string, string) error { return nil })
	d, _ = s.Deliveries(context.Background(), sub.ID)
	if d[0].Status != "succeeded" || d[0].Attempts != 2 {
		t.Fatal(d)
	}
	s.db.Exec(`UPDATE feed_deliveries SET status='sending' WHERE id=?`, d[0].ID)
	s.Recover(context.Background())
	d, _ = s.Deliveries(context.Background(), sub.ID)
	if d[0].Status != "retry" {
		t.Fatal(d)
	}
}
func TestAuditFailureRollsBackSubscription(t *testing.T) {
	s, _ := fixture(t)
	_, e := s.Save(context.Background(), "", Input{Name: "x", URL: "https://example.org", NotificationMode: "off"}, audit.Event{})
	if e == nil {
		t.Fatal("ignored audit failure")
	}
	list, _ := s.List(context.Background())
	if len(list) != 0 {
		t.Fatal(list)
	}
	if _, e = s.Get(context.Background(), "absent"); !errors.Is(e, sql.ErrNoRows) {
		t.Fatal(e)
	}
}
func TestChangingSourceResetsImportAndCancelsNotifications(t *testing.T) {
	s, _ := fixture(t)
	sub := create(t, s, Input{Enabled: true})
	if e := s.finish(context.Background(), begin(t, s, sub), fetched{items: []Item{{ID: "old", FeedID: sub.ID}}, etag: "old-etag"}, nil); e != nil {
		t.Fatal(e)
	}
	in := sub.Input
	in.URL = "https://other.example/feed"
	v, e := s.Save(context.Background(), sub.ID, in, event("feed_save"))
	if e != nil {
		t.Fatal(e)
	}
	items, _ := s.Items(context.Background(), sub.ID, 0, 50, false, "")
	if len(items) != 0 || !v.LastSuccess.IsZero() {
		t.Fatal(items, v)
	}
	var etag string
	var initialized int
	s.db.QueryRow(`SELECT etag,initialized FROM feed_subscriptions WHERE id=?`, sub.ID).Scan(&etag, &initialized)
	if etag != "" || initialized != 0 {
		t.Fatal("old source state retained")
	}
}
func TestEditingCancelsNotificationInFlight(t *testing.T) {
	s, _ := fixture(t)
	sub := create(t, s, Input{Enabled: true, NotificationMode: "immediate", ChannelIDs: []string{"c"}})
	s.db.Exec(`UPDATE feed_subscriptions SET initialized=1 WHERE id=?`, sub.ID)
	s.finish(context.Background(), begin(t, s, sub), fetched{items: []Item{{ID: "a", FeedID: sub.ID}}}, nil)
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		s.deliverOne(context.Background(), func(ctx context.Context, channel, payload string) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
		close(done)
	}()
	<-entered
	in := sub.Input
	in.Enabled = false
	if _, e := s.Save(context.Background(), sub.ID, in, event("feed_save")); e != nil {
		t.Fatal(e)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notification not canceled")
	}
	d, _ := s.Deliveries(context.Background(), sub.ID)
	if len(d) != 1 || d[0].Status != "canceled" {
		t.Fatal(d)
	}
}
func TestManualRefreshSharesMinimumIntervalAndCoalesces(t *testing.T) {
	s, now := fixture(t)
	sub := create(t, s, Input{Enabled: true})
	s.db.Exec(`UPDATE feed_subscriptions SET last_started=?,status='succeeded' WHERE id=?`, stamp(*now), sub.ID)
	v, e := s.Action(context.Background(), sub.ID, "refresh", event("feed_refresh"))
	if e != nil || !v.NextCheck.Equal(now.Add(time.Minute)) {
		t.Fatal(v, e)
	}
	v2, e := s.Action(context.Background(), sub.ID, "refresh", event("feed_refresh"))
	if e != nil || v2.NextCheck != v.NextCheck {
		t.Fatal(v2, e)
	}
	due, e := s.due(context.Background())
	if e != nil || len(due) != 0 {
		t.Fatal("minimum fetch interval bypassed", due, e)
	}
	*now = now.Add(time.Minute)
	due, e = s.due(context.Background())
	if e != nil || len(due) != 1 {
		t.Fatal(due, e)
	}
}
