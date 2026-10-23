package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComposeSaveQueuesWithoutSeparateAgentValidation(t *testing.T) {
	s := featureCore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	enrollment, err := s.agents.CreateEnrollment(ctx, "save", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.agents.ConsumeEnrollment(ctx, auth.DigestToken(enrollment.Token), auth.DigestToken("compose-save-device"), "save-request", "test")
	if err != nil {
		t.Fatal(err)
	}
	now := s.now()
	if _, err := s.store.DB.ExecContext(ctx, `UPDATE nodes SET status='online',connection_generation=1,last_seen_at=? WHERE id=?`, now.UnixNano(), identity.NodeID); err != nil {
		t.Fatal(err)
	}
	dockerIdentity := coredocker.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}
	if err := s.docker.BindConnection(dockerIdentity, 1); err != nil {
		t.Fatal(err)
	}
	batch := protocol.DockerBatch{Sequence: 1, SnapshotID: 1, FullSnapshot: true, SnapshotFinal: true, Changes: []protocol.DockerChange{}, Health: &protocol.DockerHealth{Sequence: 1, Availability: protocol.DockerAvailabilityAvailable, EventsConnected: true, SnapshotFresh: true, ObservedAt: now}}
	if err := s.docker.Accept(dockerIdentity, 1, 1, batch, now); err != nil {
		t.Fatal(err)
	}
	journal := strings.Repeat("a", 64)
	if _, err := s.tasks.ObserveAgentConnection(ctx, coretasks.AgentConnection{NodeID: identity.NodeID, ConnectionGeneration: 1, JournalID: journal}); err != nil {
		t.Fatal(err)
	}
	connection := &agentConnection{ctx: ctx, nodeID: identity.NodeID, generation: 1, commands: make(chan protocol.Envelope, 2), taskSignal: make(chan struct{}, 1), activeCapabilities: map[string]struct{}{protocol.CapabilityCompose: {}, protocol.CapabilityTaskBridge: {}}}
	connection.setTaskJournal(journal, true)
	s.agentConnections[identity.AgentID] = connection
	root := t.TempDir()
	ref := protocol.ComposeProjectRef{Name: "save", WorkingDirectory: root, ConfigFiles: []string{filepath.Join(root, "compose.yaml")}}
	ref.Key = protocol.ComposeProjectKey(ref.Name, ref.WorkingDirectory, ref.ConfigFiles)
	actions := make(chan string, 4)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case envelope := <-connection.commands:
				var request protocol.ComposeRequest
				if json.Unmarshal(envelope.Payload, &request) != nil {
					return
				}
				actions <- request.Action
				response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "failed", ErrorCode: "config_invalid"}
				if request.Action == "" {
					response.Status, response.Verified = "succeeded", true
					response.Projects = []protocol.ComposeProject{{Ref: ref, ConfigAvailable: true, Services: []protocol.ComposeService{}}}
				}
				if waiter, ok := connection.takeComposeWaiter(request.OperationID); ok {
					waiter.result <- response
				}
			}
		}
	}()
	body, _ := json.Marshal(map[string]any{"action": "config_save", "fileIndex": 0, "baseSha256": strings.Repeat("b", 64), "content": "services: {}\n"})
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))).WithContext(ctx)
	request.Header.Set("Idempotency-Key", "save-without-validation")
	response := httptest.NewRecorder()
	s.handleCreateComposeTask(response, request, &session{}, identity.NodeID, ref.Key)
	if response.Code != http.StatusAccepted {
		t.Fatalf("Save returned %d: %s", response.Code, response.Body.String())
	}
	if action := <-actions; action != "" || len(actions) != 0 {
		t.Fatalf("Save performed additional Agent operations: %q, %d", action, len(actions))
	}
}

func TestComposeDeleteConfirmationIsBoundToRouteProject(t *testing.T) {
	for _, body := range []string{`{"action":"delete"}`, `{"action":"delete","deleteConfirmationKey":"other"}`, `{"action":"start","deleteConfirmationKey":"project"}`} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "test")
		response := httptest.NewRecorder()
		(&Server{}).handleCreateComposeTask(response, request, nil, "node", "project")
		if response.Code != http.StatusBadRequest {
			t.Fatal(body, response.Code)
		}
	}
}

func TestTaskAdmissionRejectsInvalidIntentsBeforeResourceGates(t *testing.T) {
	s := featureCore(t)
	for _, action := range []string{"rename", "unsupported"} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"action":"`+action+`","newName":"invalid name"}`))
		request.Header.Set("Idempotency-Key", "invalid-intent-"+action)
		response := httptest.NewRecorder()
		s.handleCreateContainerTask(response, request, &session{}, "53d48ea1-466e-4267-910c-ecb6ab8e594a", strings.Repeat("a", 64))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d: %s", action, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("Idempotency-Key", "invalid-image-intent")
	response := httptest.NewRecorder()
	s.createImageTask(response, request, &session{}, "53d48ea1-466e-4267-910c-ecb6ab8e594a", protocol.TaskIntent{Action: protocol.TaskImagePull, ContainerID: strings.Repeat("a", 64), ImageReference: "invalid reference"}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("Invalid image intent returned %d: %s", response.Code, response.Body.String())
	}
	var count int
	if err := s.store.DB.QueryRow(`SELECT count(*) FROM core_tasks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Invalid tasks persisted: %d, error=%v", count, err)
	}
}
func TestComposeDisplayNameUsesSharedNodeScopedPreference(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	enrollment, err := s.agents.CreateEnrollment(ctx, "project alias", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	preference := dashboard.Preference{NodeID: enrollment.NodeID, TargetKind: "compose_project", Identity: "compose_project:" + strings.Repeat("a", 64), Alias: "My app", Visible: true}
	if err := s.dashboardPreferences.Put(ctx, preference); err != nil {
		t.Fatal(err)
	}
	saved, err := s.dashboardPreferences.List(ctx, enrollment.NodeID)
	if err != nil || len(saved) != 1 || saved[0].Alias != "My app" {
		t.Fatal(saved, err)
	}
	preference.Identity = "compose_project:../other"
	if s.dashboardPreferences.Put(ctx, preference) == nil {
		t.Fatal("unsafe project identity accepted")
	}
}
