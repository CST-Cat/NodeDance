package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

func TestSignedReleaseUploadIdempotentManualTaskAndAgentArtifactAuth(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, err := New("1.0.0", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true, AgentUpdatePublicKeyBase64: base64.StdEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("signed local Agent artifact")
	manifest, err := agentupdate.Sign(agentupdate.Manifest{FormatVersion: 1, Version: "1.0.1", OS: "linux", Architecture: runtime.GOARCH, SHA256: agentupdate.Digest(artifact), Size: int64(len(artifact)), MinProtocol: 1, MaxProtocol: 1, CoreMin: "1.0.0"}, private)
	if err != nil {
		t.Fatal(err)
	}
	upload := func(m agentupdate.Manifest, data []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		mf, _ := writer.CreateFormFile("manifest", "manifest.json")
		_ = json.NewEncoder(mf).Encode(m)
		af, _ := writer.CreateFormFile("artifact", "agent")
		_, _ = af.Write(data)
		_ = writer.Close()
		request := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/updates/releases", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", "https://panel.test")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		request.Header.Set(csrfHeaderName, csrf)
		response := httptest.NewRecorder()
		core.ServeHTTP(response, request)
		return response
	}
	if got := upload(manifest, []byte(strings.Repeat("x", len(artifact)))); got.Code != http.StatusBadRequest {
		t.Fatalf("tampered artifact digest status=%d body=%s", got.Code, got.Body.String())
	}
	created := upload(manifest, artifact)
	if created.Code != http.StatusCreated {
		t.Fatalf("signed release upload status=%d body=%s", created.Code, created.Body.String())
	}
	var release struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &release); err != nil || !validUUID(release.ID) {
		t.Fatalf("invalid uploaded release response: %+v %v", release, err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "update test node", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	credential := strings.Repeat("a", 64)
	requestID := "44444444-4444-4444-8444-444444444444"
	if _, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	manual := func(releaseID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"releaseId": releaseID})
		request := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/updates/nodes/"+enrollment.NodeID+"/update", bytes.NewReader(body))
		request.Header.Set("Origin", "https://panel.test")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		request.Header.Set(csrfHeaderName, csrf)
		response := httptest.NewRecorder()
		core.ServeHTTP(response, request)
		return response
	}
	first := manual(release.ID)
	second := manual(release.ID)
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("manual update status first=%d second=%d", first.Code, second.Code)
	}
	var firstTask, secondTask struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &firstTask)
	_ = json.Unmarshal(second.Body.Bytes(), &secondTask)
	if firstTask.ID == "" || firstTask.ID != secondTask.ID {
		t.Fatalf("duplicate update did not reuse one task: %+v %+v", firstTask, secondTask)
	}
	artifactPath := "/api/v1/agent-updates/releases/" + release.ID + "/artifact"
	unauth := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+artifactPath, nil)
	unauth.RemoteAddr = "127.0.0.1:12345"
	unauthResponse := httptest.NewRecorder()
	core.ServeHTTP(unauthResponse, unauth)
	if unauthResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated artifact status=%d", unauthResponse.Code)
	}
	download := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+artifactPath, nil)
	download.RemoteAddr = "127.0.0.1:12345"
	download.Header.Set("Authorization", "Bearer "+credential)
	downloadResponse := httptest.NewRecorder()
	core.ServeHTTP(downloadResponse, download)
	if downloadResponse.Code != http.StatusOK || !bytes.Equal(downloadResponse.Body.Bytes(), artifact) {
		t.Fatalf("authenticated artifact response status=%d body=%q", downloadResponse.Code, downloadResponse.Body.String())
	}
	manifestRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/agent-updates/releases/"+release.ID+"/manifest", nil)
	manifestRequest.RemoteAddr = "127.0.0.1:12345"
	manifestRequest.Header.Set("Authorization", "Bearer "+credential)
	manifestResponse := httptest.NewRecorder()
	core.ServeHTTP(manifestResponse, manifestRequest)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest endpoint status=%d", manifestResponse.Code)
	}
}

func TestAgentUpdateAPIRemainsSessionProtectedWithoutReleaseKey(t *testing.T) {
	core, err := New("test", Options{DataDir: filepath.Join(t.TempDir(), "data"), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/updates", nil)
	response := httptest.NewRecorder()
	core.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated update API status=%d", response.Code)
	}
}
