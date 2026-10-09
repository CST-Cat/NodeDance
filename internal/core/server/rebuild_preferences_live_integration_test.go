//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/containerprefs"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/coder/websocket"
)

const (
	s12PreferenceOldContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s12PreferenceNewContainerID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type s12PreferenceEnrollment struct {
	NodeID string `json:"nodeId"`
	Token  string `json:"token"`
}

type s12PreferenceTaskAccepted struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type s12PreferenceTaskView struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
	Result struct {
		Code             string `json:"code"`
		ObservedState    string `json:"observedState"`
		ResourceRevision string `json:"resourceRevision"`
	} `json:"result"`
}

// TestS12RebuildPreferenceMigrationHTTPSAgentWSSEndToEnd drives only the
// authenticated Core preference/task APIs and the registered Agent WSS task
// protocol. The WSS peer reports deterministic Engine postcondition evidence;
// this test does not claim that a Docker Engine performed a rebuild.
func TestS12RebuildPreferenceMigrationHTTPSAgentWSSEndToEnd(t *testing.T) {
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal("restrict S12 temporary root:", err)
	}
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(work, "no-docker.sock"))

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal("create isolated Core certificate:", err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal("write isolated Core trust root:", err)
	}
	core, err := New("s12-rebuild-preferences-live", Options{
		DataDir: filepath.Join(work, "core-data"), PublicOrigin: "https://panel.test", Development: true,
	})
	if err != nil {
		t.Fatal("create Core in TempDir:", err)
	}
	if _, ok := core.preferenceMigrator.(*containerprefs.SQLiteMigrator); !ok {
		_ = core.Close()
		t.Fatalf("production Core preference migrator = %T, want SQLiteMigrator", core.preferenceMigrator)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	t.Cleanup(func() {
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close isolated S12 Core: %v", err)
		}
	})

	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal("install isolated integration administrator:", err)
	}
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 15 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	admin := func(method, path string, payload any, authenticated, withCSRF bool, headers http.Header) (int, []byte) {
		t.Helper()
		var body io.Reader
		if payload != nil {
			encoded, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				t.Fatal("encode S12 API request:", marshalErr)
			}
			body = strings.NewReader(string(encoded))
		}
		request, requestErr := http.NewRequest(method, coreHTTP.URL+path, body)
		if requestErr != nil {
			t.Fatal("create S12 API request:", requestErr)
		}
		request.Header.Set("Origin", "https://panel.test")
		if payload != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if authenticated {
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			if withCSRF {
				request.Header.Set(csrfHeaderName, csrf)
			}
		}
		for name, values := range headers {
			if len(values) != 0 {
				request.Header.Set(name, values[0])
			}
		}
		response, requestErr := client.Do(request)
		if requestErr != nil {
			t.Fatalf("%s %s: %v", method, path, requestErr)
		}
		defer response.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			t.Fatalf("read %s %s response: %v", method, path, readErr)
		}
		return response.StatusCode, responseBody
	}

	if status, body := admin(http.MethodGet, "/api/v1/nodes/00000000-0000-4000-8000-000000000001/preferences", nil, false, false, nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preference API returned HTTP %d, body=%q", status, body)
	}
	status, body := admin(http.MethodPost, "/api/v1/agents/enrollments", map[string]string{"displayName": "S12 preference report Agent"}, true, true, nil)
	if status != http.StatusCreated {
		t.Fatalf("create Agent enrollment through Core admin API returned HTTP %d, body=%q", status, body)
	}
	var enrollment s12PreferenceEnrollment
	if err := json.Unmarshal(body, &enrollment); err != nil || enrollment.NodeID == "" || enrollment.Token == "" {
		t.Fatalf("decode Core enrollment: enrollment=%+v err=%v", enrollment, err)
	}
	agentConfigPath := filepath.Join(work, "agent-state", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), agentConfigPath); err != nil {
		t.Fatalf("enroll registered Agent through Core HTTPS API: %v", err)
	}
	agentConfig, err := agent.LoadConfig(agentConfigPath)
	if err != nil || agentConfig.NodeID != enrollment.NodeID || agentConfig.AgentID == "" || agentConfig.Credential == "" {
		t.Fatalf("load registered Agent identity: config=%+v err=%v", agentConfig, err)
	}

	// Register one real display preference through the authenticated API. Its
	// deliberately non-default values prove the SQLite identity move preserves
	// every display field, including false Visible.
	want := dashboard.Preference{
		NodeID: enrollment.NodeID, TargetKind: "container", Identity: "container:" + s12PreferenceOldContainerID,
		Alias: "Primary vault", Icon: "shield", Notes: "keep this note", ServiceURL: "https://vault.example.test",
		SortOrder: 17, Visible: false, Pinned: true,
	}
	status, body = admin(http.MethodPut, "/api/v1/nodes/"+enrollment.NodeID+"/preferences", want, true, true, nil)
	if status != http.StatusNoContent {
		t.Fatalf("save old-container preference returned HTTP %d, body=%q", status, body)
	}

	// Open a credential-authenticated WSS Agent protocol connection. A fixture
	// peer drives the Core's real task journal/dispatch/report handlers; no
	// Docker socket is opened and no host daemon is contacted.
	agentWSClient := tlsHTTPClient(rootPEM)
	t.Cleanup(agentWSClient.CloseIdleConnections)
	conn, response, err := websocket.Dial(context.Background(), agentWebSocketURLForTest(coreHTTP.URL), &websocket.DialOptions{
		HTTPClient: agentWSClient,
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + agentConfig.Credential}},
	})
	if err != nil {
		if response != nil {
			t.Fatalf("open enrolled Agent WSS connection (HTTP %d): %v", response.StatusCode, err)
		}
		t.Fatal("open enrolled Agent WSS connection:", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	hello := makeAgentHello(agentConfig.AgentID, agentConfig.NodeID)
	hello.Capabilities = []string{protocol.CapabilityDocker, protocol.CapabilityTaskBridge}
	writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: marshalAgentPayload(hello)})
	welcomeEnvelope := readProtocolMessage(t, conn)
	if welcomeEnvelope.Type != protocol.TypeWelcome || welcomeEnvelope.Generation == 0 {
		t.Fatalf("Core did not establish the registered Agent lease: envelope=%+v", welcomeEnvelope)
	}
	var welcome protocol.Welcome
	if err := json.Unmarshal(welcomeEnvelope.Payload, &welcome); err != nil || welcome.AgentID != agentConfig.AgentID || welcome.NodeID != enrollment.NodeID {
		t.Fatalf("decode authenticated Agent WSS welcome: welcome=%+v err=%v", welcome, err)
	}
	waitForAgentStatus(t, core, enrollment.NodeID, "online", 0)
	writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
		Generation: welcome.Generation, Sequence: 1, Payload: marshalAgentPayload(protocol.Heartbeat{})})
	heartbeatAck := readProtocolMessage(t, conn)
	if heartbeatAck.Type != protocol.TypeHeartbeatAck || heartbeatAck.Sequence != 1 {
		t.Fatalf("registered Agent WSS heartbeat not acknowledged: %+v", heartbeatAck)
	}

	now := time.Now().UTC()
	container := protocol.DockerContainer{
		ID: s12PreferenceOldContainerID, Name: "/s12-preference-fixture", Image: "nodedance-test-fixture",
		ImageID: "sha256:" + strings.Repeat("c", 64), State: "running", Running: true,
		Health: protocol.DockerHealthNone, ObservedAt: now,
	}
	batch := protocol.DockerBatch{
		Sequence: 1, SnapshotID: 1, FullSnapshot: true, SnapshotFinal: true,
		Health: &protocol.DockerHealth{Sequence: 1, Availability: protocol.DockerAvailabilityAvailable,
			EventsConnected: true, SnapshotFresh: true, LastSuccessAt: &now, LastSnapshotAt: &now, ObservedAt: now},
		Changes: []protocol.DockerChange{{Sequence: 1, Action: protocol.DockerChangeUpsert, ContainerID: s12PreferenceOldContainerID,
			Container: &container, ObservedAt: now}},
	}
	payload, err := protocol.MarshalDockerBatch(batch)
	if err != nil {
		t.Fatal("encode test-owned authenticated Docker inventory fixture:", err)
	}
	writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeDocker,
		Generation: welcome.Generation, Sequence: 1, Payload: payload})
	waitForCondition(t, 5*time.Second, func() bool {
		state, view, _, viewErr := core.dockerViewStateForNode(context.Background(), enrollment.NodeID)
		return viewErr == nil && state.Status == "online" && view.AgentOnline && view.DockerAvailability == protocol.DockerAvailabilityAvailable &&
			view.DockerSnapshotFresh && !view.DataStale && dockerViewContainsForS12(view, s12PreferenceOldContainerID)
	}, "Core did not persist the authenticated WSS Docker inventory fixture")

	journalID, err := newS12PreferenceJournalID()
	if err != nil {
		t.Fatal("create protocol journal identity:", err)
	}
	writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskJournalHello,
		Generation: welcome.Generation, Payload: marshalAgentPayload(protocol.TaskJournalHello{
			NodeID: enrollment.NodeID, JournalID: journalID, Capacity: 1, CapacityLimit: 1,
		})})
	journalStatusEnvelope := readProtocolMessage(t, conn)
	if journalStatusEnvelope.Type != protocol.TypeTaskJournalStatus {
		t.Fatalf("Core did not accept registered Agent task journal: got %q", journalStatusEnvelope.Type)
	}
	var journalStatus protocol.TaskJournalStatus
	if err := json.Unmarshal(journalStatusEnvelope.Payload, &journalStatus); err != nil || !journalStatus.Accepted || journalStatus.ReviewRequired || journalStatus.JournalID != journalID {
		t.Fatalf("decode Agent journal acceptance: status=%+v err=%v", journalStatus, err)
	}
	snapshotRequestEnvelope := readProtocolMessage(t, conn)
	if snapshotRequestEnvelope.Type != protocol.TypeTaskSnapshotRequest {
		t.Fatalf("Core did not request a complete task journal snapshot: got %q", snapshotRequestEnvelope.Type)
	}
	var snapshotRequest protocol.TaskSnapshotRequest
	if err := json.Unmarshal(snapshotRequestEnvelope.Payload, &snapshotRequest); err != nil || snapshotRequest.JournalID != journalID || snapshotRequest.SnapshotID == "" {
		t.Fatalf("decode task journal snapshot request: request=%+v err=%v", snapshotRequest, err)
	}
	writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskSnapshotPage,
		Generation: welcome.Generation, RequestID: snapshotRequest.SnapshotID, Sequence: 1,
		Payload: marshalAgentPayload(protocol.TaskSnapshotPage{SnapshotID: snapshotRequest.SnapshotID, JournalID: journalID, Final: true, Reports: []protocol.TaskReport{}}),
	})
	acceptedSnapshot := readProtocolMessage(t, conn)
	if acceptedSnapshot.Type != protocol.TypeTaskJournalStatus || acceptedSnapshot.RequestID != snapshotRequest.SnapshotID {
		t.Fatalf("Core did not acknowledge full Agent task journal snapshot: %+v", acceptedSnapshot)
	}
	var snapshotStatus protocol.TaskJournalStatus
	if err := json.Unmarshal(acceptedSnapshot.Payload, &snapshotStatus); err != nil || !snapshotStatus.Accepted || !snapshotStatus.SnapshotAccepted {
		t.Fatalf("decode Agent task snapshot acknowledgement: status=%+v err=%v", snapshotStatus, err)
	}
	waitForCondition(t, 5*time.Second, func() bool {
		return core.taskBridgeReady(enrollment.NodeID, welcome.Generation)
	}, "Core did not mark the authenticated Agent task bridge ready")

	getPreferences := func() []dashboard.Preference {
		t.Helper()
		status, responseBody := admin(http.MethodGet, "/api/v1/nodes/"+enrollment.NodeID+"/preferences", nil, true, false, nil)
		if status != http.StatusOK {
			t.Fatalf("authenticated preference read returned HTTP %d, body=%q", status, responseBody)
		}
		var response struct {
			Preferences []dashboard.Preference `json:"preferences"`
		}
		if err := json.Unmarshal(responseBody, &response); err != nil {
			t.Fatal("decode authenticated preference response:", err)
		}
		return response.Preferences
	}
	assertPreference := func(identity string, exists bool) {
		t.Helper()
		items := getPreferences()
		matches := make([]dashboard.Preference, 0, 1)
		for _, item := range items {
			if item.TargetKind == "container" && item.Identity == identity {
				matches = append(matches, item)
			}
		}
		if exists {
			if len(matches) != 1 {
				t.Fatalf("preference identity %q match count=%d, full API response=%+v", identity, len(matches), items)
			}
			got := matches[0]
			if got.NodeID != want.NodeID || got.Alias != want.Alias || got.Icon != want.Icon || got.Notes != want.Notes ||
				got.ServiceURL != want.ServiceURL || got.SortOrder != want.SortOrder || got.Visible != want.Visible || got.Pinned != want.Pinned {
				t.Fatalf("preference at %q lost display fields: got=%+v want=%+v", identity, got, want)
			}
		} else if len(matches) != 0 {
			t.Fatalf("unexpected preference identity %q in authenticated API response: %+v", identity, matches)
		}
	}
	assertPreference("container:"+s12PreferenceOldContainerID, true)
	assertPreference("container:"+s12PreferenceNewContainerID, false)

	createAndReport := func(idempotencyKey string, terminal taskstate.Status, revision, observedState string) s12PreferenceTaskView {
		t.Helper()
		status, responseBody := admin(http.MethodPost,
			"/api/v1/nodes/"+enrollment.NodeID+"/containers/"+s12PreferenceOldContainerID+"/actions",
			map[string]any{"action": protocol.TaskRebuild, "rebuild": protocol.RebuildSpec{}}, true, true,
			http.Header{"Idempotency-Key": []string{idempotencyKey}})
		if status != http.StatusAccepted {
			t.Fatalf("submit rebuild via authenticated Core task API returned HTTP %d, body=%q", status, responseBody)
		}
		var accepted s12PreferenceTaskAccepted
		if err := json.Unmarshal(responseBody, &accepted); err != nil || accepted.TaskID == "" || accepted.Status != string(taskstate.Queued) {
			t.Fatalf("decode rebuild task acceptance: task=%+v err=%v", accepted, err)
		}
		dispatchEnvelope := readProtocolMessage(t, conn)
		if dispatchEnvelope.Type != protocol.TypeTaskDispatch || dispatchEnvelope.RequestID != accepted.TaskID || dispatchEnvelope.Generation != welcome.Generation {
			t.Fatalf("Core did not dispatch the authenticated rebuild task to its Agent WSS: envelope=%+v accepted=%+v", dispatchEnvelope, accepted)
		}
		var dispatch protocol.TaskDispatch
		if err := json.Unmarshal(dispatchEnvelope.Payload, &dispatch); err != nil ||
			protocol.ValidateTaskDispatch(dispatchEnvelope, dispatch, enrollment.NodeID, journalID, welcome.Generation) != nil ||
			dispatch.Intent.Action != protocol.TaskRebuild || dispatch.Intent.ContainerID != s12PreferenceOldContainerID {
			t.Fatalf("invalid Core rebuild task dispatch over Agent WSS: dispatch=%+v err=%v", dispatch, err)
		}
		postReport := func(report protocol.TaskReport) {
			t.Helper()
			writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeTaskReport,
				Generation: welcome.Generation, RequestID: report.TaskID, Payload: marshalAgentPayload(report),
			})
			ack := readProtocolMessage(t, conn)
			if ack.Type != protocol.TypeTaskReportAck || ack.RequestID != report.TaskID {
				t.Fatalf("Core did not acknowledge Agent task result report: got %+v", ack)
			}
			var accepted protocol.TaskReportAck
			if err := json.Unmarshal(ack.Payload, &accepted); err != nil || !accepted.Accepted || accepted.TaskID != report.TaskID || accepted.ReportRevision != report.ReportRevision {
				t.Fatalf("decode task report acknowledgement: ack=%+v err=%v", accepted, err)
			}
		}
		base := protocol.TaskReport{TaskID: dispatch.TaskID, NodeID: dispatch.NodeID, JournalID: dispatch.JournalID,
			TargetID: dispatch.TargetID, IdempotencyKey: dispatch.IdempotencyKey, RequestDigest: dispatch.RequestDigest,
			Progress: protocol.TaskProgress{Phase: "executing", Completed: 1, Total: 2}}
		base.ReportRevision = 1
		base.Status = taskstate.Running
		base.Evidence.ExecutionAttempted = true
		postReport(base)
		assertPreference("container:"+s12PreferenceOldContainerID, true)
		assertPreference("container:"+s12PreferenceNewContainerID, false)

		base.ReportRevision = 2
		base.Status = terminal
		base.Progress = protocol.TaskProgress{Phase: "verifying", Completed: 2, Total: 2}
		base.Evidence.ExecutionCompleted = true
		base.Result = protocol.TaskResult{Code: "verified", ObservedState: observedState, ResourceRevision: revision}
		if terminal == taskstate.Failed {
			base.Progress.Phase = "verifying"
			base.Evidence.FailureConfirmed = true
			base.Evidence.ActualResultConfirmed = true
			base.Evidence.PostconditionVerified = false
			base.Result = protocol.TaskResult{Code: "failed", ObservedState: observedState, ResourceRevision: revision}
		} else {
			base.Evidence.PostconditionVerified = true
		}
		postReport(base)

		status, responseBody = admin(http.MethodGet, "/api/v1/nodes/"+enrollment.NodeID+"/tasks/"+accepted.TaskID, nil, true, false, nil)
		if status != http.StatusOK {
			t.Fatalf("authenticated task result lookup returned HTTP %d, body=%q", status, responseBody)
		}
		var taskView s12PreferenceTaskView
		if err := json.Unmarshal(responseBody, &taskView); err != nil || taskView.TaskID != accepted.TaskID || taskView.Status != string(terminal) ||
			taskView.Result.Code != base.Result.Code || taskView.Result.ObservedState != observedState || taskView.Result.ResourceRevision != revision {
			t.Fatalf("Core task API did not persist the verified Agent WSS result: task=%+v err=%v body=%q", taskView, err, responseBody)
		}
		return taskView
	}

	failed := createAndReport("s12-pref-migration-failed", taskstate.Failed, s12PreferenceOldContainerID, "restored:health_check_failed")
	assertPreference("container:"+s12PreferenceOldContainerID, true)
	assertPreference("container:"+s12PreferenceNewContainerID, false)
	t.Logf("S12 preference candidate failed rebuild preserved original preference identity: task=%s status=%s", failed.TaskID, failed.Status)

	succeeded := createAndReport("s12-pref-migration-succeeded", taskstate.Succeeded, s12PreferenceNewContainerID, "running")
	assertPreference("container:"+s12PreferenceOldContainerID, false)
	assertPreference("container:"+s12PreferenceNewContainerID, true)
	t.Logf("S12 preference candidate verified succeeded rebuild moved SQLite preference identity: task=%s status=%s node=%s", succeeded.TaskID, succeeded.Status, enrollment.NodeID)
}

func dockerViewContainsForS12(view coredocker.View, containerID string) bool {
	for _, record := range view.Containers {
		if record.Container.ID == containerID {
			return true
		}
	}
	return false
}

func newS12PreferenceJournalID() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
