package server

import (
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/feeds"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedsDiscoveredContractAndAuthenticatedLifecycle(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	if e := s.BootstrapAdministrator(ctx, "feeds-admin", "feeds-password-strong"); e != nil {
		t.Fatal(e)
	}
	host := httptest.NewServer(s)
	defer host.Close()
	client, e := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	catalog, e := client.Catalog(ctx)
	if e != nil {
		t.Fatal(e)
	}
	call := func(name string, input control.Input) control.Result {
		t.Helper()
		op, e := control.FindIn(catalog, name)
		if e != nil {
			t.Fatal(e)
		}
		return client.Execute(ctx, op, input, nil, nil)
	}
	for _, op := range catalog.Operations {
		if len(op.Name) > 6 && op.Name[:6] == "feeds." {
			path := op.Path
			for _, p := range op.Parameters {
				path = strings.ReplaceAll(path, "{"+p+"}", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
			}
			req, _ := http.NewRequest(op.Method, host.URL+path, nil)
			res, e := http.DefaultClient.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode != 401 {
				t.Fatal(op.Name, res.StatusCode)
			}
		}
	}
	if r := call("auth.login", control.Input{Body: json.RawMessage(`{"username":"feeds-admin","password":"feeds-password-strong"}`)}); r.Err() != nil {
		t.Fatal(r.Error)
	}
	body := json.RawMessage(`{"name":"Private feed","url":"https://example.invalid/feed?token=private","enabled":false,"keywords":[],"channelIds":[],"notificationMode":"off"}`)
	r := call("feeds.create", control.Input{Body: body})
	if r.Err() != nil {
		t.Fatal(r.Error)
	}
	var sub feeds.Subscription
	if e = json.Unmarshal(r.Data, &sub); e != nil || sub.IntervalMinutes != 30 {
		t.Fatal(sub, e)
	}
	params := map[string]string{"id": sub.ID}
	op, _ := control.FindIn(catalog, "feeds.refresh")
	if op.Completion == nil || op.Completion.Operation != "feeds.get" {
		t.Fatal("missing shared observer")
	}
	raw, _ := json.Marshal(map[string]any{"name": sub.Name, "url": sub.URL, "enabled": false, "keywords": []string{}, "channelIds": []string{}, "notificationMode": "off", "intervalMinutes": 1, "revision": sub.Revision})
	r = call("feeds.update", control.Input{Params: params, Body: raw})
	if r.Err() != nil {
		t.Fatal(r.Error)
	}
	json.Unmarshal(r.Data, &sub)
	if sub.IntervalMinutes != 1 {
		t.Fatal(sub)
	}
	stale := call("feeds.update", control.Input{Params: params, Body: raw})
	if stale.Status != 409 {
		t.Fatal("stale edit accepted", stale)
	}
	r = call("feeds.delete", control.Input{Params: params, Body: json.RawMessage(`{"revision":2,"confirm":false}`)})
	if r.Err() == nil {
		t.Fatal("deleted without confirmation")
	}
	r = call("feeds.delete", control.Input{Params: params, Body: json.RawMessage(`{"revision":2,"confirm":true}`)})
	if r.Err() != nil {
		t.Fatal(r.Error)
	}
	if r = call("feeds.get", control.Input{Params: params}); r.Status != 404 {
		t.Fatal(r)
	}
}
