package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
	corefiletasks "github.com/CST-Cat/NodeDance/internal/core/filetasks"
	coretasks "github.com/CST-Cat/NodeDance/internal/core/tasks"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
)

func TestFileTasksAppearInExistingTaskListLookupAndAuditAPI(t *testing.T) {
	core, err := New("file-task-api", Options{DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, _, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000021"
	const taskID = "00000000-0000-4000-8000-000000000022"
	now := time.Now().UnixNano()
	if _, err := core.store.DB.Exec(`INSERT INTO nodes(id,display_name,status,connection_generation,last_seen_at,created_at,updated_at) VALUES(?, 'file task API node', 'online', 1, ?, ?, ?)`, nodeID, now, now, now); err != nil {
		t.Fatal("create task API node:", err)
	}
	actor := sql.NullInt64{Int64: 1, Valid: true}
	containerTask, err := core.tasks.Enqueue(context.Background(), coretasks.EnqueueRequest{NodeID: nodeID, IdempotencyKey: "file-task-api-container",
		Intent: protocol.TaskIntent{Action: protocol.TaskRestart, ContainerID: strings.Repeat("a", 64)}, ActorID: actor, RemoteAddr: "127.0.0.1"})
	if err != nil {
		t.Fatal("create existing container task for mixed list:", err)
	}
	if _, err := core.fileTasks.Create(context.Background(), corefiletasks.CreateRequest{TaskID: taskID, NodeID: nodeID,
		Operation: corefiletasks.OperationRename, TargetPath: "/srv/old-name", NewPath: "/srv/new-name", ActorID: actor, RemoteAddr: "127.0.0.1"}); err != nil {
		t.Fatal("create durable file task:", err)
	}
	if err := core.fileTasks.MarkDispatched(context.Background(), nodeID, taskID, actor, "127.0.0.1"); err != nil {
		t.Fatal("mark file task dispatched:", err)
	}
	if err := core.fileTasks.Resolve(context.Background(), nodeID, taskID, taskstate.Succeeded, "verified", actor, "127.0.0.1"); err != nil {
		t.Fatal("resolve file task:", err)
	}
	if _, err := core.store.DB.Exec(`UPDATE file_write_tasks SET created_at_ns=?,updated_at_ns=? WHERE task_id=?`, containerTask.Task.CreatedAt.UnixNano()+1, containerTask.Task.CreatedAt.UnixNano()+1, taskID); err != nil {
		t.Fatal("order mixed task API fixture:", err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://panel.test"+path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		w := httptest.NewRecorder()
		core.ServeHTTP(w, r)
		return w
	}
	list := request("/api/v1/nodes/" + nodeID + "/tasks?limit=1")
	if list.Code != http.StatusOK {
		t.Fatalf("unified task list status=%d body=%s", list.Code, list.Body.String())
	}
	var page struct {
		Tasks      []taskView `json:"tasks"`
		NextCursor string     `json:"nextCursor"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Tasks) != 1 {
		t.Fatalf("decode unified task list: tasks=%+v err=%v", page.Tasks, err)
	}
	view := page.Tasks[0]
	if view.TaskID != taskID || view.Kind != "file" || view.Operation != "rename" || view.Action != "file_rename" ||
		view.TargetPath != "/srv/old-name" || view.NewPath != "/srv/new-name" || view.Status != taskstate.Succeeded || view.Result.Code != "verified" {
		t.Fatalf("file task list view=%+v", view)
	}
	if page.NextCursor == "" {
		t.Fatal("mixed container/file task list omitted the shared next cursor")
	}
	secondPage := request("/api/v1/nodes/" + nodeID + "/tasks?limit=1&after=" + page.NextCursor)
	var next struct {
		Tasks []taskView `json:"tasks"`
	}
	if secondPage.Code != http.StatusOK || json.Unmarshal(secondPage.Body.Bytes(), &next) != nil || len(next.Tasks) != 1 || next.Tasks[0].TaskID != containerTask.Task.TaskID {
		t.Fatalf("mixed task cursor page status=%d body=%s want container task %s", secondPage.Code, secondPage.Body.String(), containerTask.Task.TaskID)
	}
	if strings.Contains(list.Body.String(), "file content") || strings.Contains(list.Body.String(), "data:image") {
		t.Fatalf("task list leaked file content: %s", list.Body.String())
	}
	lookup := request("/api/v1/nodes/" + nodeID + "/tasks/" + taskID)
	if lookup.Code != http.StatusOK || !strings.Contains(lookup.Body.String(), `"taskId":"`+taskID+`"`) {
		t.Fatalf("file task lookup status=%d body=%s", lookup.Code, lookup.Body.String())
	}
	audit := request("/api/v1/nodes/" + nodeID + "/tasks/" + taskID + "/audit")
	if audit.Code != http.StatusOK || !strings.Contains(audit.Body.String(), `"toStatus":"succeeded"`) {
		t.Fatalf("file task audit status=%d body=%s", audit.Code, audit.Body.String())
	}
	otherNodeLookup := request("/api/v1/nodes/00000000-0000-4000-8000-000000000099/tasks/" + taskID)
	if otherNodeLookup.Code != http.StatusNotFound {
		t.Fatalf("cross-node task lookup status=%d, want 404: %s", otherNodeLookup.Code, otherNodeLookup.Body.String())
	}
}

func TestFileAgentFailureStatusKeepsAmbiguousMutationsUnknown(t *testing.T) {
	for _, testCase := range []struct {
		operation string
		code      string
		want      taskstate.Status
	}{
		{operation: protocol.FileSaveText, code: "result_unknown", want: taskstate.Unknown},
		{operation: protocol.FileUploadCommit, code: "result_unknown", want: taskstate.Unknown},
		{operation: protocol.FileDelete, code: "unavailable", want: taskstate.Unknown}, // RemoveAll may have partially deleted a tree.
		{operation: protocol.FileDelete, code: "invalid_path", want: taskstate.Failed},
		{operation: protocol.FileSaveText, code: "conflict", want: taskstate.Failed},
	} {
		if got := fileAgentFailureStatus(testCase.operation, testCase.code); got != testCase.want {
			t.Errorf("fileAgentFailureStatus(%q, %q)=%s, want %s", testCase.operation, testCase.code, got, testCase.want)
		}
	}
}

func TestFileWriteIsPersistedBeforeDispatchAndAgentDisconnectBecomesUnknown(t *testing.T) {
	core, err := New("file-write-disconnect-test", Options{DataDir: filepath.Join(t.TempDir(), "core"), Development: true, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "file task disconnect", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal("create Agent enrollment:", err)
	}
	credential := strings.Repeat("a", 64)
	requestID := "44444444-4444-4444-8444-444444444444"
	identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatal("consume Agent enrollment:", err)
	}
	lease, err := core.agents.BeginConnection(context.Background(), identity, auth.DigestToken(credential), protocol.CurrentVersion, "test-agent",
		`["agent.files.v1"]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal("create online Agent lease:", err)
	}
	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	connection := &agentConnection{ctx: connectionCtx, cancel: cancelConnection, agentID: lease.AgentID, nodeID: lease.NodeID,
		generation: lease.ConnectionGeneration, filesEnabled: true, commands: make(chan protocol.Envelope, 8),
		fileTransfers: make(map[string]*coreFileTransfer), fileTombstones: make(map[string]struct{})}
	core.agentConnectionsMu.Lock()
	core.agentConnections[lease.AgentID] = connection
	core.agentConnectionsMu.Unlock()
	t.Cleanup(func() { cancelConnection() })

	body := bytes.NewBufferString(`{"path":"/tmp/nodedance-disconnect-marker"}`)
	r := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/nodes/"+lease.NodeID+"/files/directories", body)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://panel.test")
	r.Header.Set(csrfHeaderName, csrf)
	r.Header.Set("Idempotency-Key", "file-mkdir-once")
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	w := httptest.NewRecorder()
	responseDone := make(chan struct{})
	go func() {
		core.ServeHTTP(w, r)
		close(responseDone)
	}()

	var command protocol.Envelope
	select {
	case command = <-connection.commands:
	case <-time.After(3 * time.Second):
		t.Fatal("file API did not deliver a request frame")
	}
	var fileRequest protocol.FileRequest
	if err := json.Unmarshal(command.Payload, &fileRequest); err != nil || fileRequest.Operation != protocol.FileMkdir {
		t.Fatalf("unexpected dispatched file request=%+v err=%v", fileRequest, err)
	}
	queued, err := core.fileTasks.Get(context.Background(), lease.NodeID, command.RequestID)
	if err != nil || queued.Status != taskstate.Queued || queued.DispatchStartedAt == nil || queued.StartedAt != nil {
		t.Fatalf("file intent was not durable before Agent dispatch or forged running: task=%+v err=%v", queued, err)
	}
	duplicate := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		replay := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/nodes/"+lease.NodeID+"/files/directories", strings.NewReader(`{"path":"`+path+`"}`))
		replay.Header.Set("Content-Type", "application/json")
		replay.Header.Set("Origin", "https://panel.test")
		replay.Header.Set(csrfHeaderName, csrf)
		replay.Header.Set("Idempotency-Key", "file-mkdir-once")
		replay.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		replay.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		replayRecorder := httptest.NewRecorder()
		core.ServeHTTP(replayRecorder, replay)
		return replayRecorder
	}
	replayed := duplicate("/tmp/nodedance-disconnect-marker")
	var replayResponse struct {
		TaskID string           `json:"taskId"`
		Status taskstate.Status `json:"status"`
	}
	if replayed.Code != http.StatusAccepted || json.Unmarshal(replayed.Body.Bytes(), &replayResponse) != nil || replayResponse.TaskID != command.RequestID || replayResponse.Status != taskstate.Queued {
		t.Fatalf("same-key replay status=%d response=%+v body=%s", replayed.Code, replayResponse, replayed.Body.String())
	}
	conflictingReplay := duplicate("/tmp/other-target")
	if conflictingReplay.Code != http.StatusConflict {
		t.Fatalf("same-key changed target status=%d body=%s; want 409", conflictingReplay.Code, conflictingReplay.Body.String())
	}
	select {
	case extra := <-connection.commands:
		t.Fatalf("idempotency replay dispatched another file write: %+v", extra)
	default:
	}

	cancelConnection()
	select {
	case <-responseDone:
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP file request did not finish after Agent disconnect")
	}
	if w.Code != http.StatusAccepted {
		t.Fatalf("disconnect response status=%d body=%s; want unknown/202", w.Code, w.Body.String())
	}
	var response struct {
		TaskID string           `json:"taskId"`
		Status taskstate.Status `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.TaskID != command.RequestID || response.Status != taskstate.Unknown {
		t.Fatalf("disconnect task response=%+v body=%s err=%v", response, w.Body.String(), err)
	}
	unknown, err := core.fileTasks.Get(context.Background(), lease.NodeID, command.RequestID)
	if err != nil || unknown.Status != taskstate.Unknown || unknown.ResultCode != "result_pending" {
		t.Fatalf("disconnected write state=%+v err=%v; want non-replayed unknown result", unknown, err)
	}
	for {
		select {
		case extra := <-connection.commands:
			if extra.Type == protocol.TypeFileRequest {
				t.Fatalf("Core replayed a non-idempotent file write after Agent disconnect: %+v", extra)
			}
		default:
			return
		}
	}
}

func TestCoreStartupRecoversDispatchedFileWriteAsUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "core")
	core, err := New("file-write-restart-test", Options{DataDir: dir, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	const nodeID = "00000000-0000-4000-8000-000000000031"
	const taskID = "00000000-0000-4000-8000-000000000032"
	now := time.Now().UnixNano()
	if _, err := core.store.DB.Exec(`INSERT INTO nodes(id,display_name,status,created_at,updated_at) VALUES(?, 'restart task node', 'pending', ?, ?)`, nodeID, now, now); err != nil {
		t.Fatal(err)
	}
	actor := sql.NullInt64{Int64: 1, Valid: true}
	if _, err := core.fileTasks.Create(context.Background(), corefiletasks.CreateRequest{TaskID: taskID, NodeID: nodeID,
		Operation: corefiletasks.OperationDelete, TargetPath: "/tmp/restart-maybe-removed", ActorID: actor}); err != nil {
		t.Fatal("create restart fixture task:", err)
	}
	if err := core.fileTasks.MarkDispatched(context.Background(), nodeID, taskID, actor, "127.0.0.1"); err != nil {
		t.Fatal("persist dispatch boundary:", err)
	}
	if err := core.Close(); err != nil {
		t.Fatal("stop Core before restart:", err)
	}
	restarted, err := New("file-write-restart-test", Options{DataDir: dir, Development: true})
	if err != nil {
		t.Fatal("restart Core:", err)
	}
	defer restarted.Close()
	task, err := restarted.fileTasks.Get(context.Background(), nodeID, taskID)
	if err != nil || task.Status != taskstate.Unknown || task.ResultCode != "result_pending" {
		t.Fatalf("Core startup did not reconcile dispatched write to unknown: task=%+v err=%v", task, err)
	}
}

func TestFileUploadSuccessPersistsOnlyVerifiedResult(t *testing.T) {
	// The API test exercises the request path with a fake Agent frame source;
	// bytes remain streamed and are never placed in the task schema.
	core := newTestServer(t)
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "file upload success", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	credential := strings.Repeat("b", 64)
	requestID := "55555555-5555-4555-8555-555555555555"
	identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := core.agents.BeginConnection(context.Background(), identity, auth.DigestToken(credential), protocol.CurrentVersion, "test-agent",
		`["agent.files.v1"]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	connectionCtx, cancelConnection := context.WithCancel(context.Background())
	connection := &agentConnection{ctx: connectionCtx, cancel: cancelConnection, agentID: lease.AgentID, nodeID: lease.NodeID,
		generation: lease.ConnectionGeneration, filesEnabled: true, commands: make(chan protocol.Envelope, 16),
		fileTransfers: make(map[string]*coreFileTransfer), fileTombstones: make(map[string]struct{})}
	core.agentConnectionsMu.Lock()
	core.agentConnections[lease.AgentID] = connection
	core.agentConnectionsMu.Unlock()
	t.Cleanup(cancelConnection)

	data := []byte("streamed fixture bytes")
	digest := sha256.Sum256(data)
	request := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/nodes/"+lease.NodeID+"/files/upload?path=%2Ftmp%2Fuploaded.bin", bytes.NewReader(data))
	request.ContentLength = int64(len(data))
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-File-SHA256", hex.EncodeToString(digest[:]))
	request.Header.Set("Idempotency-Key", "upload-success-once")
	request.Header.Set("Origin", "https://panel.test")
	request.Header.Set(csrfHeaderName, csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	responseRecorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		core.ServeHTTP(responseRecorder, request)
		close(done)
	}()

	var taskID string
	var total int64
	for {
		select {
		case command := <-connection.commands:
			if command.Type == protocol.TypeFileCancel {
				t.Fatalf("upload was canceled unexpectedly before completion: %+v", command)
			}
			var fileRequest protocol.FileRequest
			if err := json.Unmarshal(command.Payload, &fileRequest); err != nil {
				t.Fatal("decode Core upload request:", err)
			}
			if taskID == "" {
				taskID = command.RequestID
			}
			if command.RequestID != taskID {
				t.Fatalf("upload task identity changed: %s vs %s", command.RequestID, taskID)
			}
			response := protocol.FileResponse{Operation: fileRequest.Operation}
			switch fileRequest.Operation {
			case protocol.FileUploadBegin:
			case protocol.FileUploadChunk:
				chunkData, err := protocol.DecodeFileChunk(protocol.FileChunk{TransferID: taskID, Sequence: fileRequest.Sequence, Data: fileRequest.Data})
				if err != nil {
					t.Fatal("decode streamed Core upload chunk:", err)
				}
				total += int64(len(chunkData))
				response.Completed = total
			case protocol.FileUploadCommit:
				response.Entry = &protocol.FileEntry{Path: "/tmp/uploaded.bin", Kind: "file", Size: total, Version: "v2"}
			default:
				t.Fatalf("unexpected upload operation %q", fileRequest.Operation)
			}
			payload, _ := json.Marshal(response)
			transfer, _ := connection.findFileTransfer(taskID)
			if transfer == nil {
				t.Fatal("Core transfer disappeared before Agent response")
			}
			if err := transfer.enqueue(protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileResponse, Generation: lease.ConnectionGeneration,
				RequestID: taskID, Sequence: transfer.lastSeq + 1, Payload: payload}, fileRequest.Operation, fileRequest.Operation == protocol.FileUploadCommit); err != nil {
				t.Fatal("deliver fake Agent file response:", err)
			}
			if fileRequest.Operation == protocol.FileUploadCommit {
				goto completed
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for Core upload frame")
		}
	}

completed:
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("upload API did not complete after Agent commit response")
	}
	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("upload API status=%d body=%s", responseRecorder.Code, responseRecorder.Body.String())
	}
	var reply struct {
		TaskID     string           `json:"taskId"`
		TransferID string           `json:"transferId"`
		Status     taskstate.Status `json:"status"`
	}
	if err := json.Unmarshal(responseRecorder.Body.Bytes(), &reply); err != nil || reply.TaskID != taskID || reply.TransferID != taskID || reply.Status != taskstate.Succeeded {
		t.Fatalf("upload response=%+v body=%s err=%v", reply, responseRecorder.Body.String(), err)
	}
	task, err := core.fileTasks.Get(context.Background(), lease.NodeID, taskID)
	if err != nil || task.Status != taskstate.Succeeded || task.ResultCode != "verified" || task.StartedAt == nil || task.FinishedAt == nil {
		t.Fatalf("upload result not durably verified: %+v err=%v", task, err)
	}
	var taskText string
	if err := core.store.DB.QueryRow(`SELECT target_path || operation || result_code FROM file_write_tasks WHERE task_id=?`, taskID).Scan(&taskText); err != nil || strings.Contains(taskText, string(data)) {
		t.Fatalf("file task schema stores content or lacks metadata: value=%q err=%v", taskText, err)
	}
}

func TestFileUploadCancellationRequiresAgentConfirmation(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		ackCanceled bool
		want        taskstate.Status
		wantResult  string
	}{
		{name: "Agent confirms temporary upload was canceled", ackCanceled: true, want: taskstate.Canceled, wantResult: "cancel_confirmed"},
		{name: "Agent reports no confirmed cancellation", ackCanceled: false, want: taskstate.Unknown, wantResult: "result_pending"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			core := newTestServer(t)
			session, csrf, err := installIntegrationAdmin(core)
			if err != nil {
				t.Fatal(err)
			}
			enrollment, err := core.agents.CreateEnrollment(context.Background(), "file upload cancel", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
			if err != nil {
				t.Fatal(err)
			}
			credential := strings.Repeat("c", 64)
			identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), "66666666-6666-4666-8666-666666666666", "127.0.0.1")
			if err != nil {
				t.Fatal(err)
			}
			lease, err := core.agents.BeginConnection(context.Background(), identity, auth.DigestToken(credential), protocol.CurrentVersion, "test-agent",
				`["agent.files.v1"]`, protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"})
			if err != nil {
				t.Fatal(err)
			}
			connectionCtx, cancelConnection := context.WithCancel(context.Background())
			connection := &agentConnection{ctx: connectionCtx, cancel: cancelConnection, agentID: lease.AgentID, nodeID: lease.NodeID,
				generation: lease.ConnectionGeneration, filesEnabled: true, commands: make(chan protocol.Envelope, 8),
				fileTransfers: make(map[string]*coreFileTransfer), fileTombstones: make(map[string]struct{}), fileCancelAcks: make(map[string]chan protocol.FileCancelAck)}
			core.agentConnectionsMu.Lock()
			core.agentConnections[lease.AgentID] = connection
			core.agentConnectionsMu.Unlock()
			t.Cleanup(cancelConnection)

			bodyReader, bodyWriter := io.Pipe()
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			defer cancelRequest()
			r := httptest.NewRequestWithContext(requestCtx, http.MethodPost, "https://panel.test/api/v1/nodes/"+lease.NodeID+"/files/upload?path=%2Ftmp%2Fcancel-upload", bodyReader)
			r.ContentLength = 4
			digest := sha256.Sum256([]byte("data"))
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Header.Set("X-File-SHA256", hex.EncodeToString(digest[:]))
			r.Header.Set("Idempotency-Key", "cancel-upload-key")
			r.Header.Set("Origin", "https://panel.test")
			r.Header.Set(csrfHeaderName, csrf)
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			r.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
			responseRecorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				core.ServeHTTP(responseRecorder, r)
				close(done)
			}()
			defer bodyWriter.Close()

			begin := receiveCoreFileCommand(t, connection)
			var beginRequest protocol.FileRequest
			if err := json.Unmarshal(begin.Payload, &beginRequest); err != nil || beginRequest.Operation != protocol.FileUploadBegin {
				t.Fatalf("unexpected upload begin request=%+v err=%v", beginRequest, err)
			}
			beginResponse, _ := json.Marshal(protocol.FileResponse{Operation: protocol.FileUploadBegin})
			if err := core.handleAgentFileMessage(connection, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileResponse,
				Generation: lease.ConnectionGeneration, RequestID: begin.RequestID, Sequence: 1, Payload: beginResponse}); err != nil {
				t.Fatal("deliver Agent upload-begin ACK:", err)
			}
			var running corefiletasks.Task
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				running, err = core.fileTasks.Get(context.Background(), lease.NodeID, begin.RequestID)
				if err != nil || running.Status == taskstate.Running {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || running.Status != taskstate.Running || running.StartedAt == nil {
				t.Fatalf("upload did not enter running after Agent begin ACK: task=%+v err=%v", running, err)
			}

			cancelRequest()
			cancel := receiveCoreFileCommand(t, connection)
			if cancel.Type != protocol.TypeFileCancel {
				t.Fatalf("expected FileCancel after client cancellation, got %+v", cancel)
			}
			ackPayload, _ := json.Marshal(protocol.FileCancelAck{TransferID: begin.RequestID, Canceled: testCase.ackCanceled})
			if err := core.handleAgentFileMessage(connection, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileCancelAck,
				Generation: lease.ConnectionGeneration, RequestID: begin.RequestID, Payload: ackPayload}); err != nil {
				t.Fatal("deliver Agent cancellation ACK:", err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("canceled upload handler did not return")
			}
			task, err := core.fileTasks.Get(context.Background(), lease.NodeID, begin.RequestID)
			if err != nil || task.Status != testCase.want || task.ResultCode != testCase.wantResult {
				t.Fatalf("upload cancellation state=%+v err=%v, want %s/%s; HTTP=%d %s", task, err, testCase.want, testCase.wantResult, responseRecorder.Code, responseRecorder.Body.String())
			}
		})
	}
}

func receiveCoreFileCommand(t *testing.T, connection *agentConnection) protocol.Envelope {
	t.Helper()
	select {
	case command := <-connection.commands:
		return command
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Core file command")
		return protocol.Envelope{}
	}
}
