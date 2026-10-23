package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
	"time"
)

func TestUpdateAPIRequiresSessionCSRFConfirmationAndUsesBuiltInExecutor(t *testing.T) {
	received := make(chan update.RPC, 4)
	core, err := New("core-v0.1.9", Options{DataDir: t.TempDir(), Development: true, UpdateInvoke: func(ctx context.Context, rpc update.RPC) (update.Response, error) {
		received <- rpc
		if rpc.Action == "delete-backup" {
			return update.Response{Status: 200, Data: json.RawMessage(`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"succeeded","backupDeleted":true}`)}, nil
		}
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
	cleanup, _ := control.FindIn(catalog, "updates.delete-backup")
	result = client.Execute(context.Background(), cleanup, control.Input{Body: json.RawMessage(`{"component":"core","jobId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","confirm":false}`), IdempotencyKey: "delete-backup-test-key"}, nil, nil)
	if result.Status != 400 {
		t.Fatalf("unconfirmed backup deletion: %+v", result)
	}
	result = client.Execute(context.Background(), cleanup, control.Input{Body: json.RawMessage(`{"component":"core","jobId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","confirm":true}`), IdempotencyKey: "delete-backup-test-key"}, nil, nil)
	if err := result.Err(); err != nil || result.Status != 200 {
		t.Fatalf("backup deletion: %+v %v", result, err)
	}
	deleted := <-received
	if deleted.Action != "delete-backup" || deleted.Request.JobID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || !deleted.Request.Confirm {
		t.Fatalf("wrong cleanup request: %+v", deleted)
	}

	if len(received) != 0 {
		t.Fatal("invalid requests reached update executor")
	}
}

func TestAgentUpdateDoesNotRequireCapabilityNegotiation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	connection := &agentConnection{nodeID: "fixture", generation: 3, ctx: ctx, commands: make(chan protocol.Envelope, 1)}
	server := &Server{agentConnections: map[string]*agentConnection{"fixture": connection}}
	done := make(chan error, 1)
	go func() {
		envelope := <-connection.commands
		var request update.RPC
		if envelope.Type != protocol.TypeUpdateRequest || json.Unmarshal(envelope.Payload, &request) != nil || request.Request.Version != "agent-v2.0.0" {
			done <- fmt.Errorf("unexpected update request: %+v", envelope)
			return
		}
		reply := protocol.Envelope{Generation: connection.generation - 1, RequestID: envelope.RequestID, Payload: json.RawMessage(`{"status":202,"data":{"id":"fixture"}}`)}
		if err := server.handleUpdateResponse(connection, reply); err == nil {
			done <- fmt.Errorf("stale generation accepted")
			return
		}
		reply.Generation = connection.generation
		done <- server.handleUpdateResponse(connection, reply)
	}()
	result, err := server.agentUpdate(ctx, "fixture", update.RPC{Action: "apply", Request: update.Request{Component: "agent", Version: "agent-v2.0.0", Confirm: true}, IdempotencyKey: "update-fixture-key"})
	if err != nil || result.Status != 202 {
		t.Fatalf("update blocked without optional capability: %+v %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
