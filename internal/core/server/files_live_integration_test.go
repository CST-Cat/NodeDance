//go:build linux

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type s10LiveEnrollment struct {
	NodeID string `json:"nodeId"`
	Token  string `json:"token"`
}

type s10LiveFileMutation struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
}

type s10LiveFileText struct {
	Path    string `json:"path"`
	Text    string `json:"text"`
	Version string `json:"version"`
}

type s10LiveFileEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// TestRealAgentHostFilesAPIEndToEnd exercises the administrator's HTTPS file
// API against an enrolled Agent's real WSS connection and an isolated host
// file root. The Agent never receives a path outside fileRoot.
func TestRealAgentHostFilesAPIEndToEnd(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s10-files-test.sock")
	work := t.TempDir()
	fileRoot := filepath.Join(work, "host-file-root")
	if err := os.Mkdir(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(work, "outside-root-fixture.txt")
	outsideContent := []byte("outside-root-canary\n")
	if err := os.WriteFile(outsidePath, outsideContent, 0o600); err != nil {
		t.Fatal(err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s10-live-files", Options{
		DataDir: filepath.Join(work, "core-data"), PublicOrigin: "https://panel.test", Development: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()

	var agentDone chan error
	var stopAgent context.CancelFunc
	closed := false
	closeServices := func() error {
		if closed {
			return nil
		}
		closed = true
		var closeErr error
		if stopAgent != nil {
			stopAgent()
			select {
			case runErr := <-agentDone:
				closeErr = runErr
			case <-time.After(8 * time.Second):
				closeErr = errors.New("Agent did not stop after context cancellation")
			}
		}
		coreHTTP.Close()
		if coreErr := core.Close(); closeErr == nil {
			closeErr = coreErr
		}
		return closeErr
	}
	t.Cleanup(func() {
		if err := closeServices(); err != nil {
			t.Errorf("stop isolated Core/Agent services: %v", err)
		}
	})

	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	client := tlsHTTPClient(rootPEM)
	client.Timeout = 20 * time.Second
	defer client.CloseIdleConnections()

	request := func(method, endpoint string, body []byte, authenticated, withCSRF bool, headers http.Header) (int, http.Header, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, requestErr := http.NewRequest(method, coreHTTP.URL+endpoint, reader)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		req.Header.Set("Origin", "https://panel.test")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
			req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
			if withCSRF {
				req.Header.Set(csrfHeaderName, csrfToken)
			}
		}
		for name, values := range headers {
			if len(values) > 0 {
				req.Header.Set(name, values[0])
			}
		}
		response, requestErr := client.Do(req)
		if requestErr != nil {
			t.Fatalf("%s %s: %v", method, endpoint, requestErr)
		}
		responseBody, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s %s response: %v", method, endpoint, readErr)
		}
		return response.StatusCode, response.Header.Clone(), responseBody
	}

	if status, _, body := request(http.MethodGet, "/api/v1/nodes", nil, false, false, nil); status != http.StatusUnauthorized {
		t.Fatalf("private node API without a Session returned HTTP %d, body=%q", status, body)
	}

	enrollmentBody, _ := json.Marshal(map[string]string{"displayName": "S10 isolated file Agent"})
	status, _, body := request(http.MethodPost, "/api/v1/agents/enrollments", enrollmentBody, true, true, nil)
	if status != http.StatusCreated {
		t.Fatalf("create isolated enrollment returned HTTP %d, body=%q", status, body)
	}
	var enrollment s10LiveEnrollment
	if err := json.Unmarshal(body, &enrollment); err != nil || enrollment.NodeID == "" || enrollment.Token == "" {
		t.Fatalf("decode isolated enrollment: value=%+v err=%v", enrollment, err)
	}
	configPath := filepath.Join(work, "agent-state", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), configPath); err != nil {
		t.Fatalf("real Agent HTTPS enrollment failed: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.NodeID != enrollment.NodeID || config.AgentID == "" {
		t.Fatalf("enrolled Agent identity does not match the protected Core enrollment: config=%+v enrollment=%+v", config, enrollment)
	}
	t.Setenv("NODEDANCE_AGENT_FILE_ROOT", fileRoot)
	t.Setenv("NODEDANCE_AGENT_MAX_FILE_BYTES", "1048576")
	runCtx, cancel := context.WithCancel(context.Background())
	stopAgent = cancel
	agentDone = make(chan error, 1)
	go func() { agentDone <- agent.Run(runCtx, configPath, "s10-live-files", nil) }()
	waitForAgentStatus(t, core, enrollment.NodeID, "online", 0)
	nodes, err := core.agents.ListNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var registeredNodeID string
	var registeredGeneration uint64
	var registeredCapabilities []string
	for _, node := range nodes {
		if node.NodeID == enrollment.NodeID {
			registeredNodeID = node.NodeID
			registeredGeneration = node.ConnectionGeneration
			registeredCapabilities = node.Capabilities
			break
		}
	}
	if registeredNodeID == "" || !hasCapability(registeredCapabilities, protocol.CapabilityFiles) || !hasCapability(registeredCapabilities, protocol.CapabilityFileJournal) {
		t.Fatalf("registered Agent did not negotiate durable file capability: node=%s capabilities=%v", registeredNodeID, registeredCapabilities)
	}
	t.Logf("real TLS/WSS Agent online: node=%s generation=%d fileRoot=isolated TempDir", registeredNodeID, registeredGeneration)

	apiNode := "/api/v1/nodes/" + enrollment.NodeID + "/files"
	filename := "上传 样例.txt"
	filePath := "/" + filename
	fileContent := []byte("NodeDance S10 live upload · 中文内容\n")
	digestBytes := sha256.Sum256(fileContent)
	digest := hex.EncodeToString(digestBytes[:])
	uploadHeaders := http.Header{
		"X-File-SHA256":   []string{digest},
		"Idempotency-Key": []string{"s10-upload-live-" + newTestFileIdempotencySuffix(t)},
		"Content-Type":    []string{"application/octet-stream"},
	}
	status, _, body = request(http.MethodPost, apiNode+"/upload?path="+url.QueryEscape(filePath), fileContent, true, true, uploadHeaders)
	var uploadResult struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
		SHA256 string `json:"sha256"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &uploadResult) != nil || uploadResult.Status != "succeeded" || uploadResult.SHA256 != digest {
		t.Fatalf("live upload returned HTTP %d, result=%+v body=%q", status, uploadResult, body)
	}
	stored, err := os.ReadFile(filepath.Join(fileRoot, filename))
	if err != nil || !bytes.Equal(stored, fileContent) {
		t.Fatalf("Agent did not write uploaded bytes inside the isolated file root: readErr=%v size=%d", err, len(stored))
	}
	status, _, body = request(http.MethodGet, apiNode+"?path=%2F", nil, true, true, nil)
	var listing struct {
		Entries []s10LiveFileEntry `json:"entries"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &listing) != nil {
		t.Fatalf("list uploaded fixture returned HTTP %d, body=%q", status, body)
	}
	found := false
	for _, entry := range listing.Entries {
		if entry.Name == filename && entry.Path == filePath && entry.Kind == "file" {
			found = true
		}
	}
	if !found {
		t.Fatalf("real file listing did not preserve Chinese/spaced name %q: %+v", filename, listing.Entries)
	}
	status, _, body = request(http.MethodGet, apiNode+"/stat?path="+url.QueryEscape(filePath), nil, true, true, nil)
	var stat s10LiveFileEntry
	if status != http.StatusOK || json.Unmarshal(body, &stat) != nil || stat.Name != filename || stat.Path != filePath || stat.Kind != "file" {
		t.Fatalf("stat uploaded fixture returned HTTP %d, entry=%+v body=%q", status, stat, body)
	}
	renamedPath := "/已上传 资料.txt"
	renameBody, _ := json.Marshal(map[string]string{"path": filePath, "newPath": renamedPath})
	renameHeaders := http.Header{"Idempotency-Key": []string{"s10-rename-live-" + newTestFileIdempotencySuffix(t)}}
	status, _, body = request(http.MethodPost, apiNode+"/rename", renameBody, true, true, renameHeaders)
	var renameResult s10LiveFileMutation
	if status != http.StatusOK || json.Unmarshal(body, &renameResult) != nil || renameResult.Status != "succeeded" || renameResult.TaskID == "" {
		t.Fatalf("rename Chinese/spaced fixture returned HTTP %d, result=%+v body=%q", status, renameResult, body)
	}
	if _, err := os.Stat(filepath.Join(fileRoot, filename)); !os.IsNotExist(err) {
		t.Fatalf("renamed source still exists or stat failed unexpectedly: err=%v", err)
	}
	status, downloadHeaders, downloaded := request(http.MethodGet, apiNode+"/download?path="+url.QueryEscape(renamedPath), nil, true, true, nil)
	downloadHash := sha256.Sum256(downloaded)
	if status != http.StatusOK || !bytes.Equal(downloaded, fileContent) || hex.EncodeToString(downloadHash[:]) != digest || downloadHeaders.Get("X-File-SHA256") != digest {
		t.Fatalf("download integrity mismatch: HTTP=%d size=%d computed=%x header=%q wanted=%s", status, len(downloaded), downloadHash, downloadHeaders.Get("X-File-SHA256"), digest)
	}
	t.Logf("candidate S10-01: upload/list/stat/rename/download preserved Unicode-space filenames and SHA-256 %s", digest)

	textPath := "/编辑冲突.txt"
	textFile := filepath.Join(fileRoot, strings.TrimPrefix(textPath, "/"))
	if err := os.WriteFile(textFile, []byte("初始版本\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	status, _, body = request(http.MethodGet, apiNode+"/text?path="+url.QueryEscape(textPath), nil, true, true, nil)
	var textVersion s10LiveFileText
	if status != http.StatusOK || json.Unmarshal(body, &textVersion) != nil || textVersion.Text != "初始版本\n" || textVersion.Version == "" {
		t.Fatalf("read text through Agent returned HTTP %d, value=%+v body=%q", status, textVersion, body)
	}
	externalText := []byte("SSH 外部修改，必须保留\n")
	if err := os.WriteFile(textFile, externalText, 0o640); err != nil {
		t.Fatal(err)
	}
	staleEdit, _ := json.Marshal(map[string]string{"path": textPath, "version": textVersion.Version, "text": "Web 端的过期编辑\n"})
	staleHeaders := http.Header{"Idempotency-Key": []string{"s10-edit-conflict-" + newTestFileIdempotencySuffix(t)}}
	status, _, body = request(http.MethodPut, apiNode+"/text", staleEdit, true, true, staleHeaders)
	preserved, readErr := os.ReadFile(textFile)
	if status != http.StatusConflict || readErr != nil || !bytes.Equal(preserved, externalText) {
		t.Fatalf("stale text edit must return 409 and preserve external write: HTTP=%d readErr=%v content=%q body=%q", status, readErr, preserved, body)
	}
	t.Logf("candidate S10-07: stale editor version returned HTTP 409 and preserved the external file hash %x", sha256.Sum256(externalText))

	if err := os.Symlink(outsidePath, filepath.Join(fileRoot, "逃逸链接.txt")); err != nil {
		t.Fatal(err)
	}
	for _, escapePath := range []string{"/../outside-root-fixture.txt", "/逃逸链接.txt"} {
		status, _, body = request(http.MethodGet, apiNode+"/text?path="+url.QueryEscape(escapePath), nil, true, true, nil)
		if status < http.StatusBadRequest || status == http.StatusAccepted {
			t.Fatalf("file root escape %q was not refused: HTTP %d, body=%q", escapePath, status, body)
		}
		outsideAfter, readErr := os.ReadFile(outsidePath)
		if readErr != nil || !bytes.Equal(outsideAfter, outsideContent) {
			t.Fatalf("rejected escape path %q changed the outside-root canary: readErr=%v content=%q", escapePath, readErr, outsideAfter)
		}
	}
	t.Logf("candidate S10-02: .. and escaping symlink were refused; outside-root fixture remained unchanged (symlink denial may map to generic 503)")

	deletePath := "/删除 确认样例.txt"
	deleteTarget := filepath.Join(fileRoot, strings.TrimPrefix(deletePath, "/"))
	deleteContent := []byte("delete only after exact confirmation\n")
	if err := os.WriteFile(deleteTarget, deleteContent, 0o600); err != nil {
		t.Fatal(err)
	}
	deleteRequest := func(targetPath, confirm string, withCSRF bool) (int, []byte) {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"path": targetPath, "confirmPath": confirm})
		headers := http.Header{"Idempotency-Key": []string{"s10-delete-live-" + newTestFileIdempotencySuffix(t)}}
		status, _, responseBody := request(http.MethodPost, apiNode+"/delete", payload, true, withCSRF, headers)
		return status, responseBody
	}
	if status, body = deleteRequest(deletePath, deletePath, false); status != http.StatusForbidden {
		t.Fatalf("delete without CSRF header returned HTTP %d, body=%q; want 403", status, body)
	}
	if current, err := os.ReadFile(deleteTarget); err != nil || !bytes.Equal(current, deleteContent) {
		t.Fatalf("CSRF-rejected delete changed its target: readErr=%v content=%q", err, current)
	}
	if status, body = deleteRequest(deletePath, deletePath+"-wrong", true); status != http.StatusBadRequest {
		t.Fatalf("delete with non-exact confirmation returned HTTP %d, body=%q; want 400", status, body)
	}
	if current, err := os.ReadFile(deleteTarget); err != nil || !bytes.Equal(current, deleteContent) {
		t.Fatalf("mismatched-confirmation delete changed its target: readErr=%v content=%q", err, current)
	}
	validKey := "s10-delete-confirmed-" + newTestFileIdempotencySuffix(t)
	deleteBody, _ := json.Marshal(map[string]string{"path": deletePath, "confirmPath": deletePath})
	status, _, body = request(http.MethodPost, apiNode+"/delete", deleteBody, true, true, http.Header{"Idempotency-Key": []string{validKey}})
	var deleteResult s10LiveFileMutation
	if status != http.StatusOK || json.Unmarshal(body, &deleteResult) != nil || deleteResult.Status != "succeeded" || deleteResult.TaskID == "" {
		t.Fatalf("exactly confirmed delete returned HTTP %d, result=%+v body=%q", status, deleteResult, body)
	}
	if _, err := os.Lstat(deleteTarget); !os.IsNotExist(err) {
		t.Fatalf("confirmed delete did not remove exact file: lstat err=%v", err)
	}
	assertDeleteAudit := func(targetPath string, result s10LiveFileMutation) {
		t.Helper()
		// Match the documented audit wire format independently of the production
		// metadata helper so this verifies node, task, and the exact URL-escaped
		// JSON path list that was durably stored in SQLite.
		pathList, err := json.Marshal([]string{targetPath})
		if err != nil {
			t.Fatalf("encode expected audit target path: %v", err)
		}
		wantTarget := enrollment.NodeID + ":" + result.TaskID + ":" + url.QueryEscape(string(pathList))
		var count int
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM audit_entries WHERE action='file_delete' AND outcome='succeeded' AND target_kind=? AND target_id=?`,
			string(audit.TargetFile), wantTarget).Scan(&count); err != nil {
			t.Fatalf("count exact-target delete audit entries: %v", err)
		}
		if count != 1 {
			t.Fatalf("persistent delete audit count=%d for exact node/task/path target %q, want 1", count, wantTarget)
		}
		var action, outcome, targetKind, targetID string
		if err := core.store.DB.QueryRow(`SELECT action, outcome, target_kind, target_id FROM audit_entries WHERE target_id=? ORDER BY id DESC LIMIT 1`, wantTarget).
			Scan(&action, &outcome, &targetKind, &targetID); err != nil {
			t.Fatalf("read exact-target delete audit entry: %v", err)
		}
		if action != "file_delete" || outcome != "succeeded" || targetKind != string(audit.TargetFile) || targetID != wantTarget {
			t.Fatalf("delete audit does not identify exact node/task/path: action=%q outcome=%q kind=%q target=%q want=%q", action, outcome, targetKind, targetID, wantTarget)
		}
	}
	assertDeleteAudit(deletePath, deleteResult)

	// A non-empty nested directory must use the same exact-path confirmation as
	// a file. Rejected CSRF and mismatched confirmation requests must leave the
	// directory and nested bytes untouched before the confirmed operation runs.
	directoryPath := "/删除目录 确认样例"
	nestedDirectory := filepath.Join(fileRoot, strings.TrimPrefix(directoryPath, "/"), "nested", "deeper")
	if err := os.MkdirAll(nestedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedFile := filepath.Join(nestedDirectory, "keep.txt")
	nestedContent := []byte("nested directory delete confirmation fixture\n")
	if err := os.WriteFile(nestedFile, nestedContent, 0o600); err != nil {
		t.Fatal(err)
	}
	assertDirectoryUntouched := func(attempt string) {
		t.Helper()
		info, err := os.Lstat(filepath.Join(fileRoot, strings.TrimPrefix(directoryPath, "/")))
		if err != nil || !info.IsDir() {
			t.Fatalf("%s removed or changed the non-empty directory: info=%v err=%v", attempt, info, err)
		}
		current, err := os.ReadFile(nestedFile)
		if err != nil || !bytes.Equal(current, nestedContent) {
			t.Fatalf("%s changed nested directory content: readErr=%v content=%q", attempt, err, current)
		}
	}
	if status, body = deleteRequest(directoryPath, directoryPath, false); status != http.StatusForbidden {
		t.Fatalf("non-empty directory delete without CSRF returned HTTP %d, body=%q; want 403", status, body)
	}
	assertDirectoryUntouched("CSRF-rejected non-empty directory delete")
	if status, body = deleteRequest(directoryPath, directoryPath+"-wrong", true); status != http.StatusBadRequest {
		t.Fatalf("non-empty directory delete with non-exact confirmation returned HTTP %d, body=%q; want 400", status, body)
	}
	assertDirectoryUntouched("mismatched-confirmation non-empty directory delete")
	directoryDeleteBody, _ := json.Marshal(map[string]string{"path": directoryPath, "confirmPath": directoryPath})
	directoryDeleteKey := "s10-delete-directory-confirmed-" + newTestFileIdempotencySuffix(t)
	status, _, body = request(http.MethodPost, apiNode+"/delete", directoryDeleteBody, true, true,
		http.Header{"Idempotency-Key": []string{directoryDeleteKey}})
	var directoryDeleteResult s10LiveFileMutation
	if status != http.StatusOK || json.Unmarshal(body, &directoryDeleteResult) != nil || directoryDeleteResult.Status != "succeeded" || directoryDeleteResult.TaskID == "" {
		t.Fatalf("exactly confirmed non-empty directory delete returned HTTP %d, result=%+v body=%q", status, directoryDeleteResult, body)
	}
	directoryTarget := filepath.Join(fileRoot, strings.TrimPrefix(directoryPath, "/"))
	if _, err := os.Lstat(directoryTarget); !os.IsNotExist(err) {
		t.Fatalf("confirmed non-empty directory still exists: lstat err=%v", err)
	}
	if _, err := os.Lstat(nestedFile); !os.IsNotExist(err) {
		t.Fatalf("confirmed directory delete left its nested file: lstat err=%v", err)
	}
	assertDeleteAudit(directoryPath, directoryDeleteResult)

	emptyDirectoryPath := "/空目录 删除确认样例"
	emptyDirectoryTarget := filepath.Join(fileRoot, strings.TrimPrefix(emptyDirectoryPath, "/"))
	if err := os.Mkdir(emptyDirectoryTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	assertEmptyDirectoryUntouched := func(attempt string) {
		t.Helper()
		info, err := os.Lstat(emptyDirectoryTarget)
		if err != nil || !info.IsDir() {
			t.Fatalf("%s removed or changed the empty directory: info=%v err=%v", attempt, info, err)
		}
		entries, err := os.ReadDir(emptyDirectoryTarget)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s changed empty directory contents: entries=%v err=%v", attempt, entries, err)
		}
	}
	if status, body = deleteRequest(emptyDirectoryPath, emptyDirectoryPath, false); status != http.StatusForbidden {
		t.Fatalf("empty directory delete without CSRF returned HTTP %d, body=%q; want 403", status, body)
	}
	assertEmptyDirectoryUntouched("CSRF-rejected empty directory delete")
	if status, body = deleteRequest(emptyDirectoryPath, emptyDirectoryPath+"-wrong", true); status != http.StatusBadRequest {
		t.Fatalf("empty directory delete with non-exact confirmation returned HTTP %d, body=%q; want 400", status, body)
	}
	assertEmptyDirectoryUntouched("mismatched-confirmation empty directory delete")
	emptyDirectoryDeleteBody, _ := json.Marshal(map[string]string{"path": emptyDirectoryPath, "confirmPath": emptyDirectoryPath})
	emptyDirectoryDeleteKey := "s10-delete-empty-directory-confirmed-" + newTestFileIdempotencySuffix(t)
	status, _, body = request(http.MethodPost, apiNode+"/delete", emptyDirectoryDeleteBody, true, true,
		http.Header{"Idempotency-Key": []string{emptyDirectoryDeleteKey}})
	var emptyDirectoryDeleteResult s10LiveFileMutation
	if status != http.StatusOK || json.Unmarshal(body, &emptyDirectoryDeleteResult) != nil || emptyDirectoryDeleteResult.Status != "succeeded" || emptyDirectoryDeleteResult.TaskID == "" {
		t.Fatalf("exactly confirmed empty directory delete returned HTTP %d, result=%+v body=%q", status, emptyDirectoryDeleteResult, body)
	}
	if _, err := os.Lstat(emptyDirectoryTarget); !os.IsNotExist(err) {
		t.Fatalf("confirmed empty directory still exists: lstat err=%v", err)
	}
	assertDeleteAudit(emptyDirectoryPath, emptyDirectoryDeleteResult)
	t.Logf("candidate S10-09: file and empty/non-empty directory deletion used exact confirmation; CSRF/mismatch rejection preserved targets; persistent audit matched node/task/exact path once")

	if matches, err := filepath.Glob(filepath.Join(core.dataDir, ".nodedance-download-*")); err != nil || len(matches) != 0 {
		t.Fatalf("Core download spool cleanup left artifacts: matches=%v err=%v", matches, err)
	}
	if matches, err := filepath.Glob(filepath.Join(fileRoot, ".nodedance-upload-*")); err != nil || len(matches) != 0 {
		t.Fatalf("Agent upload temp cleanup left artifacts: matches=%v err=%v", matches, err)
	}
	if err := closeServices(); err != nil {
		t.Fatalf("stop isolated Core/Agent services: %v", err)
	}
	if err := os.RemoveAll(work); err != nil {
		t.Fatalf("remove only test-owned TempDir: %v", err)
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("test-owned TempDir remains after cleanup: stat err=%v", err)
	}
	t.Logf("cleanup verified: Core data, Agent state, file root, and outside-root canary were removed with only this test's TempDir")
}

func newTestFileIdempotencySuffix(t *testing.T) string {
	t.Helper()
	value, err := newContainerStreamRequestID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
