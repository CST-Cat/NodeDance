package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/update"
)

func TestUpdateAPIRequiresSessionCSRFConfirmationAndUsesBuiltInExecutor(t *testing.T) {
	received := make(chan update.RPC, 4)
	core, err := New("core-v0.1.9", Options{DataDir: t.TempDir(), Development: true, UpdateInvoke: func(ctx context.Context, rpc update.RPC) (update.Response, error) {
		received <- rpc
		return update.Response{Status: 202, Data: json.RawMessage(`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"queued"}`)}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err = core.BootstrapAdministrator(context.Background(), "test-admin", "test-password-strong"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	response, err := http.Get(host.URL + "/api/v1/updates/status?component=core")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("unauthenticated status: %d", response.StatusCode)
	}
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true, StateDir: filepath.Join(t.TempDir(), "client")})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog := control.Describe()
	login, _ := control.FindIn(catalog, "auth.login")
	result := client.Execute(context.Background(), login, control.Input{Body: json.RawMessage(`{"username":"test-admin","password":"test-password-strong"}`)}, nil, nil)
	if err = result.Err(); err != nil {
		t.Fatal(err)
	}
	apply, _ := control.FindIn(catalog, "updates.apply")
	for _, body := range []string{`{"component":"core","confirm":false}`, `{"component":"agent","confirm":true}`, `{"component":"core","confirm":true,"nodeId":"unexpected"}`} {
		result = client.Execute(context.Background(), apply, control.Input{Body: json.RawMessage(body), IdempotencyKey: "update-test-key"}, nil, nil)
		if result.Status != 400 {
			t.Fatalf("invalid update status: %+v", result)
		}
	}
	result = client.Execute(context.Background(), apply, control.Input{Body: json.RawMessage(`{"component":"core","confirm":true}`), IdempotencyKey: "update-test-key"}, nil, nil)
	if err = result.Err(); err != nil || result.Status != 202 {
		t.Fatalf("update submission: %+v %v", result, err)
	}
	rpc := <-received
	if rpc.Action != "apply" || rpc.Request.Component != "core" || !rpc.Request.Confirm || rpc.IdempotencyKey != "update-test-key" {
		t.Fatalf("invalid forwarded request: %+v", rpc)
	}
	if len(received) != 0 {
		t.Fatal("invalid requests reached update executor")
	}
}
