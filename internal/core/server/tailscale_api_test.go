package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/storage"
	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func newUnknownDeploymentForTest(t *testing.T, dataDir string) (*storage.Store, *coretasks.Store, coretasks.Task) {
	t.Helper()
	ctx := context.Background()
	database, err := storage.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	store, err := coretasks.New(database.DB, coretasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DB.ExecContext(ctx, `INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES('node-1','stage6-test','pending',1,1)`); err != nil {
		t.Fatal(err)
	}
	intent := protocol.TaskIntent{
		Action: protocol.TaskAgentDeploy, ContainerID: "tailscale-peer:peer-key",
		AgentDeploy: &protocol.AgentDeployTaskSpec{PeerIdentity: "peer-key", PeerName: "stage6-test", CoreURL: "https://core.example",
			HostFingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789", SSHUser: "root", Authentication: "password"},
	}
	accepted, err := store.EnqueueAndStartLocal(ctx, coretasks.EnqueueRequest{NodeID: "node-1", IdempotencyKey: "resolve-test-key", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := store.MarkLocalUnknown(ctx, "node-1", accepted.Task.TaskID, sql.NullInt64{}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return database, store, unknown
}

func TestResolveUnknownDeploymentAssociatesPeerBeforeSuccess(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := tailscale.NewStateStore(dataDir).Associate("peer-key", "node-1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"succeeded","observedState":"connected","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 200 {
		t.Fatalf("resolve response: status=%d body=%s", response.Code, response.Body.String())
	}
	state, err := tailscale.NewStateStore(dataDir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Managed["peer-key"] != "node-1" {
		t.Fatalf("resolved successful deployment was not associated: %#v", state.Managed)
	}
	resolved, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || resolved.Status != taskstate.Succeeded {
		t.Fatalf("Core task should be durably succeeded: status=%s err=%v", resolved.Status, err)
	}
}

func TestResolveFailedDeploymentClearsMatchingPeerAssociation(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := tailscale.NewStateStore(dataDir).Associate("peer-key", "node-1"); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"failed","observedState":"installation_failed","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 200 {
		t.Fatalf("resolve response: status=%d body=%s", response.Code, response.Body.String())
	}
	state, err := tailscale.NewStateStore(dataDir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := state.Managed["peer-key"]; exists {
		t.Fatalf("failed deployment retained peer association: %#v", state.Managed)
	}
	resolved, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || resolved.Status != taskstate.Failed {
		t.Fatalf("Core task should be durably failed: status=%s err=%v", resolved.Status, err)
	}
}

func TestTailscaleDeploymentListRestoresUnknownTaskAfterRefresh(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	request := httptest.NewRequest("GET", "/api/v1/discovery/deployments", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	if !(&Server{tasks: store, dataDir: dataDir}).handleTailscaleAPI(response, request, &session{}) {
		t.Fatal("deployment collection route was not handled")
	}
	if response.Code != 200 {
		t.Fatalf("list response: status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Tasks []tailscale.DeploymentTask `json:"tasks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tasks) != 1 || result.Tasks[0].ID != task.TaskID || result.Tasks[0].Status != string(taskstate.Unknown) {
		t.Fatalf("refresh did not restore the unresolved Core task: %#v", result.Tasks)
	}
}

func TestResolveUnknownDeploymentDoesNotSucceedWhenAssociationCannotPersist(t *testing.T) {
	dataDir := t.TempDir()
	_, store, task := newUnknownDeploymentForTest(t, dataDir)
	if err := os.Mkdir(filepath.Join(dataDir, "tailscale-deployments.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/v1/discovery/deployments/"+task.TaskID+"/resolve", strings.NewReader(`{"outcome":"succeeded","observedState":"connected","confirmed":true}`))
	request.RemoteAddr = "192.0.2.10:1234"
	response := httptest.NewRecorder()
	(&Server{tasks: store, dataDir: dataDir}).handleResolveTailscaleDeployment(response, request, &session{}, task.TaskID)
	if response.Code != 500 {
		t.Fatalf("association failure should not be reported as success: status=%d body=%s", response.Code, response.Body.String())
	}
	current, err := store.Get(context.Background(), "node-1", task.TaskID)
	if err != nil || current.Status != taskstate.Unknown {
		t.Fatalf("failed peer association must leave task unresolved and claimed: status=%s err=%v", current.Status, err)
	}
}
