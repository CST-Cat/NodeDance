package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	testFileSessionID = "fedcba9876543210fedcba9876543210"
	testFileNodeID    = "01234567-89ab-4cde-8fab-0123456789ab"
	testFileTransfer  = "01234567-89ab-4cde-8fab-0123456789ac"
)

func newTestFileConnection(sessionID string) (*agentConnection, *coreFileTransfer) {
	ctx, cancel := context.WithCancel(context.Background())
	connection := &agentConnection{ctx: ctx, cancel: cancel, nodeID: testFileNodeID, generation: 9, filesEnabled: true,
		fileTransfers: make(map[string]*coreFileTransfer), fileTombstones: make(map[string]struct{}), commands: make(chan protocol.Envelope, 4)}
	transfer := &coreFileTransfer{requestID: testFileTransfer, nodeID: testFileNodeID, sessionID: sessionID, generation: 9,
		allowed: map[string]struct{}{protocol.FileUploadBegin: {}, protocol.FileUploadChunk: {}, protocol.FileUploadCommit: {}},
		updates: make(chan protocol.Envelope, fileTransferQueue), done: make(chan struct{})}
	connection.fileTransfers[transfer.requestID] = transfer
	return connection, transfer
}

func TestLogoutCancelsSessionBoundFileTransfer(t *testing.T) {
	s := newTestServer(t)
	_, _ = seedAdminSession(t, s, "ignored-test-password")
	connection, transfer := newTestFileConnection(testFileSessionID)
	s.agentConnections = map[string]*agentConnection{"agent-id": connection}

	request := httptest.NewRequest(http.MethodPost, "http://panel.example.test/api/v1/auth/logout", nil)
	response := httptest.NewRecorder()
	s.handleLogout(response, request, &session{ID: testFileSessionID, RemoteAddr: "127.0.0.1"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-transfer.done:
	default:
		t.Fatal("logout left the Session's file transfer active")
	}
	select {
	case envelope := <-connection.commands:
		var canceled protocol.FileCancel
		if err := json.Unmarshal(envelope.Payload, &canceled); err != nil || protocol.ValidateFileCancel(envelope, connection.generation, canceled) != nil || canceled.TransferID != testFileTransfer {
			t.Fatalf("invalid transfer cancellation: %#v, err=%v", envelope, err)
		}
	default:
		t.Fatal("logout did not send Agent-side temporary-file cancellation")
	}
}

func TestRevocationClosesStalledUploadBodyAndCancelsAgentTransfer(t *testing.T) {
	s := newTestServer(t)
	_, _ = seedAdminSession(t, s, "ignored-test-password")
	connection, transfer := newTestFileConnection(testFileSessionID)
	s.agentConnections = map[string]*agentConnection{"agent-id": connection}

	body, peer := io.Pipe()
	defer peer.Close()
	request := httptest.NewRequest(http.MethodPost, "http://panel.example.test/api/v1/nodes/"+testFileNodeID+"/files/upload", nil)
	request.Body = body
	var activity atomic.Int64
	activity.Store(time.Now().UnixNano())
	stopWatch := s.watchFileUploadBody(request, &session{ID: testFileSessionID}, connection, transfer, &activity)
	defer stopWatch()
	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(body, make([]byte, 1))
		readDone <- err
	}()

	if _, err := s.store.DB.Exec(`DELETE FROM browser_sessions WHERE id=?`, testFileSessionID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("stalled body unexpectedly produced data")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Session revocation did not close the stalled HTTP request body")
	}
	select {
	case <-transfer.done:
	case <-time.After(time.Second):
		t.Fatal("Session revocation did not cancel its Core transfer")
	}
	select {
	case envelope := <-connection.commands:
		var canceled protocol.FileCancel
		if err := json.Unmarshal(envelope.Payload, &canceled); err != nil || canceled.TransferID != testFileTransfer {
			t.Fatalf("invalid transfer cancellation: %#v err=%v", envelope, err)
		}
	default:
		t.Fatal("stalled upload did not signal Agent temporary-file cleanup")
	}
}

func TestCanceledDispatchedFileWriteReturnsUnknownAndCancelsAgent(t *testing.T) {
	s := newTestServer(t)
	_, _ = seedAdminSession(t, s, "ignored-test-password")
	connection, transfer := newTestFileConnection(testFileSessionID)
	transfer.allowed = map[string]struct{}{protocol.FileMkdir: {}}

	ctx, cancel := context.WithCancel(context.Background())
	request := protocol.FileRequest{Operation: protocol.FileMkdir, Path: "/may-have-been-created"}
	if err := s.sendFileRequest(ctx, connection, transfer, request); err != nil {
		t.Fatalf("dispatch file write: %v", err)
	}
	dispatched := <-connection.commands
	if dispatched.Type != protocol.TypeFileRequest {
		t.Fatalf("first queued frame type=%q, want file request", dispatched.Type)
	}
	cancel()
	if _, err := s.waitFileMessage(ctx, connection, transfer); err == nil {
		t.Fatal("wait unexpectedly succeeded without an Agent result")
	}

	response := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "http://panel.example.test/api/v1/nodes/"+testFileNodeID+"/files/directories", nil)
	metadata, err := newFileAuditMetadata(testFileNodeID, testFileTransfer, request)
	if err != nil {
		t.Fatal(err)
	}
	s.writeFileMutationError(response, httpRequest, &session{ID: testFileSessionID, RemoteAddr: "127.0.0.1"}, metadata, context.Canceled)
	if response.Code != http.StatusAccepted {
		t.Fatalf("canceled dispatched write status=%d body=%s, want unknown/202", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["status"] != "unknown" || body["transferId"] != testFileTransfer {
		t.Fatalf("canceled write response=%s err=%v", response.Body.String(), err)
	}
	select {
	case envelope := <-connection.commands:
		var canceled protocol.FileCancel
		if err := json.Unmarshal(envelope.Payload, &canceled); err != nil || envelope.Type != protocol.TypeFileCancel || canceled.TransferID != testFileTransfer {
			t.Fatalf("dispatched write was not canceled: %#v err=%v", envelope, err)
		}
	default:
		t.Fatal("canceled dispatched write did not send Agent cancellation")
	}
	var targetKind, targetID string
	if err := s.store.DB.QueryRow(`SELECT target_kind, target_id FROM audit_entries WHERE action='file_mkdir' AND outcome='unknown'`).Scan(&targetKind, &targetID); err != nil {
		t.Fatal(err)
	}
	if targetKind != "file" || !strings.HasPrefix(targetID, testFileNodeID+":"+testFileTransfer+":") {
		t.Fatalf("file audit does not include node/transfer/target identity: kind=%q id=%q", targetKind, targetID)
	}
	encodedPath := strings.TrimPrefix(targetID, testFileNodeID+":"+testFileTransfer+":")
	decodedPath, err := url.QueryUnescape(encodedPath)
	if err != nil || decodedPath != `["/may-have-been-created"]` {
		t.Fatalf("file audit target path=%q err=%v", decodedPath, err)
	}
}
