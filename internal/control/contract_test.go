package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/server"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestPublishedContractAgainstRealCore(t *testing.T) {
	core, err := server.New("contract-test", server.Options{DataDir: t.TempDir(), Development: true, FileTransferLimit: 123456})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err = core.BootstrapAdministrator(context.Background(), "contract-admin", "private-test-password"); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(core)
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(name string, input control.Input) control.Result {
		t.Helper()
		op, err := control.FindIn(catalog, name)
		if err != nil {
			t.Fatal(err)
		}
		return client.Execute(context.Background(), op, input, nil, nil)
	}
	login := invoke("auth.login", control.Input{Body: json.RawMessage(`{"username":"contract-admin","password":"private-test-password"}`)})
	if login.Err() != nil {
		t.Fatal(login.Error)
	}
	upload, _ := control.FindIn(catalog, "files.upload")
	if upload.Upload.MaxBytes != 123456 {
		t.Fatal("running Core file limit not published")
	}
	metrics := invoke("metrics.catalog", control.Input{})
	if metrics.Err() != nil {
		t.Fatal(metrics.Error)
	}
	var metricData struct {
		Metrics map[string]string `json:"metrics"`
	}
	json.Unmarshal(metrics.Data, &metricData)
	if metricData.Metrics["swap.used_percent"] == "" {
		t.Fatal("metric response contract drift")
	}
	enrollment := invoke("agents.enrollment", control.Input{Body: json.RawMessage(`{"displayName":"Contract fixture"}`)})
	if enrollment.Err() != nil {
		t.Fatal(enrollment.Error)
	}
	var node struct {
		NodeID string `json:"nodeId"`
	}
	json.Unmarshal(enrollment.Data, &node)
	for _, unit := range []string{"year", "month", "hour", "minute"} {
		asset := protocol.DefaultNodeAsset()
		asset.BillingCycleUnit = unit
		asset.Price = 8.5
		asset.TrafficLimitBytes = 900000000000
		body, _ := json.Marshal(asset)
		saved := invoke("nodes.save-asset", control.Input{Params: map[string]string{"nodeId": node.NodeID}, Body: body})
		if saved.Err() != nil {
			t.Fatal(saved.Error)
		}
		read := invoke("nodes.asset", control.Input{Params: map[string]string{"nodeId": node.NodeID}})
		var got protocol.NodeAsset
		if read.Err() != nil || json.Unmarshal(read.Data, &got) != nil || got.BillingCycleUnit != unit || got.Price != asset.Price || got.TrafficLimitBytes != asset.TrafficLimitBytes {
			t.Fatalf("shared client asset round trip failed: %+v", read)
		}
	}
	asset := protocol.DefaultNodeAsset()
	asset.BillingCycleUnit = "minute"
	body, _ := json.Marshal(map[string]any{"displayName": "Contract asset enrollment", "asset": asset})
	created := invoke("agents.enrollment", control.Input{Body: body})
	if created.Err() != nil {
		t.Fatal(created.Error)
	}
	var createdNode struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.Unmarshal(created.Data, &createdNode); err != nil {
		t.Fatal(err)
	}
	readAsset := invoke("nodes.asset", control.Input{Params: map[string]string{"nodeId": createdNode.NodeID}})
	var enrolledAsset protocol.NodeAsset
	if readAsset.Err() != nil || json.Unmarshal(readAsset.Data, &enrolledAsset) != nil || enrolledAsset.BillingCycleUnit != "minute" {
		t.Fatal("discovered enrollment asset lost", readAsset.Error)
	}
	pref, _ := json.Marshal(map[string]any{"targetKind": "node", "identity": "node:" + node.NodeID, "alias": "", "icon": "", "notes": "", "serviceUrl": "", "sshHost": "", "sshUser": "", "sshPort": 22, "group": "", "sortOrder": 0, "visible": true, "pinned": false})
	result := invoke("nodes.save-preference", control.Input{Params: map[string]string{"nodeId": node.NodeID}, Body: pref})
	if result.Status != 204 {
		t.Fatal("valid empty preference rejected", result.Error)
	}
	for _, script := range []string{"#" + strings.Repeat("a", 20*1024), "#" + strings.Repeat("\t", protocol.MaxCommandScriptBytes-1)} {
		body, _ := json.Marshal(protocol.CommandSpec{Script: script, TimeoutSeconds: 60, Confirm: true})
		result = invoke("commands.submit", control.Input{Params: map[string]string{"nodeId": node.NodeID}, Body: body, IdempotencyKey: "contract-script-limit"})
		// The pending fixture has no Agent: a valid request reaches the shared
		// offline gate, never a root process, and is not rejected as oversized.
		if result.Status != http.StatusServiceUnavailable {
			t.Fatalf("valid bounded script status %d: %s", result.Status, result.Error)
		}
	}
	var data bytes.Buffer
	png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	op, _ := control.FindIn(catalog, "appearance.upload-avatar")
	result = client.Execute(context.Background(), op, control.Input{}, &data, nil)
	if result.Err() != nil {
		t.Fatal("discovered multipart rejected", result.Error)
	}
	secret, _ := control.FindIn(catalog, "channels.create")
	properties := secret.BodySchema.(map[string]any)["properties"].(map[string]any)
	if properties["secret"].(map[string]any)["writeOnly"] != true {
		t.Fatal("notification secret is not protected")
	}
}

func TestCompletionUsesDiscoveredRoutesWithoutResubmission(t *testing.T) {
	submissions := 0
	start := control.Operation{Name: "future.submit", Method: "POST", Path: "/api/v1/custom/start", Parameters: []string{}, Query: []string{}, Transport: "json", Idempotent: true,
		Completion: &control.CompletionContract{Operation: "future.observe", Params: map[string]string{"id": "data.id"}, StatusPath: "state", TerminalStatuses: []string{"done", "unknown"}, SuccessStatuses: []string{"done"}}}
	read := control.Operation{Name: "future.observe", Method: "GET", Path: "/api/v1/custom/{id}", Parameters: []string{"id"}, Query: []string{}, Transport: "json"}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{start, read}})
		case "/api/v1/auth/csrf":
			json.NewEncoder(w).Encode(map[string]string{"token": "test"})
		case start.Path:
			submissions++
			w.Write([]byte(`{"id":"work","state":"running"}`))
		case "/api/v1/custom/work":
			w.Write([]byte(`{"id":"work","state":"done"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	client, err := control.NewClient(control.ClientOptions{Server: host.URL, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	catalog, err := client.Catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o, _ := control.FindIn(catalog, "future.submit")
	input := control.Input{IdempotencyKey: "same-intent"}
	accepted := client.Execute(context.Background(), o, input, nil, nil)
	result := client.Wait(context.Background(), catalog, o, input, accepted, nil)
	if result.Err() != nil || submissions != 1 || result.Operation != "future.observe" {
		t.Fatal("completion replayed or bypassed discovered route", result.Error, submissions)
	}
}

func TestSchemaPresenceAndTransportIsolation(t *testing.T) {
	op, _ := control.Find("nodes.save-preference")
	if err := control.ValidateJSON(op.BodySchema, []byte(`{"targetKind":"node","identity":"node:test","alias":""}`)); err != nil {
		t.Fatal(err)
	}
	if err := control.ValidateJSON(op.BodySchema, []byte(`{"targetKind":"node","identity":"node:test","retiredField":true}`)); err == nil {
		t.Fatal("unknown property accepted")
	}
	copy, _ := control.Find("files.upload")
	copy.Upload.MaxBytes = 1
	copy.Headers["X-File-Version"] = nil
	current, _ := control.Find("files.upload")
	if current.Upload.MaxBytes == 1 || current.Headers["X-File-Version"] == nil {
		t.Fatal("caller mutated shared transport contract")
	}
	for _, op := range control.Operations() {
		if op.Completion != nil {
			if _, err := control.Find(op.Completion.Operation); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAppearanceRosterContractListsLoginAnimals(t *testing.T) {
	op, err := control.Find("appearance.update")
	if err != nil {
		t.Fatal(err)
	}
	properties := op.BodySchema.(map[string]any)["properties"].(map[string]any)
	sharks := properties["sharks"].(map[string]any)
	items := sharks["items"].(map[string]any)
	animalProperties := items["properties"].(map[string]any)
	enum := animalProperties["kind"].(map[string]any)["enum"].([]string)
	for _, kind := range []string{"turtle", "manta", "dolphin", "whale", "tang"} {
		found := false
		for _, candidate := range enum {
			if candidate == kind {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("appearance.update does not publish animal kind %q", kind)
		}
	}
}
