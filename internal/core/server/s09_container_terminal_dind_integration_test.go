//go:build linux

package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coredocker "github.com/CST-Cat/NodeDance/internal/core/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type s09ServerDINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	ServerVersion string `json:"server_version"`
}

type s09ServerLockedImages struct {
	Images map[string]string `json:"images"`
}

// TestRealAgentBrowserContainerTerminalOnOwnedDIND verifies the container
// terminal path through the authenticated Core API and browser WebSocket,
// the real enrolled Agent runtime, and this workflow's exact Engine 28/29.
func TestRealAgentBrowserContainerTerminalOnOwnedDIND(t *testing.T) {
	endpoint, cli, runCtx := connectS09ServerDIND(t)
	defer cli.Close()
	defer runCtx.cancel()
	t.Setenv("DOCKER_HOST", endpoint)
	suite := fmt.Sprintf("nodedance-s09-container-%d", time.Now().UnixNano())

	work := t.TempDir()
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s09-live-container-terminal", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	var agentDone chan error
	var stopAgent context.CancelFunc
	var fixtureIDs []string
	var shelllessImageTag, shelllessImageID string
	defer func() {
		if stopAgent != nil {
			stopAgent()
			select {
			case err := <-agentDone:
				if err != nil {
					t.Errorf("real Agent stopped with error: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("real Agent did not stop after context cancellation")
			}
		}
		coreHTTP.Close()
		if err := core.Close(); err != nil {
			t.Errorf("close Core: %v", err)
		}
		for _, id := range fixtureIDs {
			removeS09ServerFixture(t, cli, id, suite)
		}
		if shelllessImageID != "" {
			removeS09ServerShelllessImage(t, cli, shelllessImageTag, shelllessImageID, suite)
		}
	}()

	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S09 live container terminal Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), configPath); err != nil {
		t.Fatalf("real Agent enrollment failed: %v", err)
	}
	config, err := agent.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	config.Shell = "/bin/sh"
	if err := agent.SaveConfig(configPath, config, false); err != nil {
		t.Fatalf("set deterministic test shell: %v", err)
	}
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	stopAgent = cancelAgent
	agentDone = make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s09-container-terminal-test", nil) }()
	waitForS09TerminalAgent(t, core, enrollment.NodeID)
	// Agent online can precede the initial Docker snapshot/event subscription.
	// Wait for both before mutating Engine state, especially before creating the
	// short-lived stopped fixture, which could otherwise fall into that startup
	// gap and remain absent until the periodic full scan.
	waitForS09ServerDockerReady(t, core, enrollment.NodeID, 30*time.Second)

	image := s09ServerLockedBusyboxImage(t)
	if _, err := cli.ImageInspect(runCtx.ctx, image); err != nil {
		reader, pullErr := cli.ImagePull(runCtx.ctx, image, client.ImagePullOptions{})
		if pullErr != nil {
			t.Fatalf("pull locked S09 BusyBox fixture image: %v", pullErr)
		}
		if _, copyErr := io.Copy(io.Discard, reader); copyErr != nil {
			_ = reader.Close()
			t.Fatalf("finish locked S09 BusyBox fixture pull: %v", copyErr)
		}
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close locked S09 BusyBox fixture pull: %v", closeErr)
		}
	}
	alphaMarker := "ND_S09_CONTAINER_ALPHA_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	betaMarker := "ND_S09_CONTAINER_BETA_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	alphaID := createS09ServerRunningFixture(t, runCtx.ctx, cli, &fixtureIDs, image, suite, "alpha", alphaMarker)
	betaID := createS09ServerRunningFixture(t, runCtx.ctx, cli, &fixtureIDs, image, suite, "beta", betaMarker)
	stoppedID := createS09ServerStoppedFixture(t, runCtx.ctx, cli, &fixtureIDs, image, suite)

	shelllessImageTag = "nodedance-s09-shellless:" + strings.TrimPrefix(suite, "nodedance-s09-container-")
	shelllessImageID = createS09ServerShelllessImage(t, runCtx.ctx, cli, alphaID, shelllessImageTag, suite)
	shelllessID := createS09ServerShelllessFixture(t, runCtx.ctx, cli, &fixtureIDs, shelllessImageTag, suite)
	fixtures := map[string]s09ServerFixtureExpectation{
		"alpha":     {id: alphaID, state: "running", running: true},
		"beta":      {id: betaID, state: "running", running: true},
		"shellless": {id: shelllessID, state: "running", running: true},
		"stopped":   {id: stoppedID, state: "exited", running: false},
	}
	waitForS09ServerContainerInventory(t, core, enrollment.NodeID, cli, runCtx.ctx, fixtures, 30*time.Second)

	httpClient, err := httpClientForRoots(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	defer httpClient.CloseIdleConnections()
	connectTerminal := func(containerID string) (*websocket.Conn, s09TerminalAuthorization) {
		t.Helper()
		body, err := json.Marshal(map[string]string{"targetKind": protocol.TerminalTargetContainer, "containerId": containerID})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+enrollment.NodeID+"/terminals", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", "https://panel.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(csrfHeaderName, csrfToken)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatalf("authorize container terminal for exact target %s: %v", containerID, err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			data, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
			t.Fatalf("authorize container terminal for exact target %s: HTTP %d: %s", containerID, response.StatusCode, strings.TrimSpace(string(data)))
		}
		var authorization s09TerminalAuthorization
		if err := json.NewDecoder(response.Body).Decode(&authorization); err != nil || authorization.StreamID == "" || authorization.Ticket == "" {
			t.Fatalf("decode container terminal authorization: value=%+v err=%v", authorization, err)
		}
		parsed, err := url.Parse(coreHTTP.URL)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Scheme = "wss"
		parsed.Path = "/ws/v1/streams/terminal"
		query := parsed.Query()
		query.Set("ticket", authorization.Ticket)
		parsed.RawQuery = query.Encode()
		browser, handshake, err := websocket.Dial(context.Background(), parsed.String(), &websocket.DialOptions{
			HTTPClient: httpClient,
			HTTPHeader: http.Header{"Cookie": []string{sessionCookieName + "=" + sessionToken}, "Origin": []string{"https://panel.test"}},
		})
		if err != nil {
			status := ""
			if handshake != nil {
				status = fmt.Sprintf(" (HTTP %d)", handshake.StatusCode)
			}
			t.Fatalf("connect authenticated browser container terminal WebSocket%s: %v", status, err)
		}
		t.Cleanup(func() { _ = browser.CloseNow() })
		return browser, authorization
	}
	openTerminal := func(containerID string) (*websocket.Conn, s09TerminalAuthorization) {
		t.Helper()
		browser, authorization := connectTerminal(containerID)
		frame := readS09TerminalFrame(t, browser, 8*time.Second)
		if frame.StreamID != authorization.StreamID || frame.Action != protocol.TerminalActionReady {
			_ = browser.CloseNow()
			t.Fatalf("first container terminal frame=%+v, want ready for stream %s", frame, authorization.StreamID)
		}
		return browser, authorization
	}
	writeFrame := func(browser *websocket.Conn, frame protocol.TerminalFrame) {
		t.Helper()
		data, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := browser.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatalf("write browser container terminal frame %q: %v", frame.Action, err)
		}
	}

	// Core must reject a stopped target before issuing a one-use stream ticket.
	stoppedRequestBody, _ := json.Marshal(map[string]string{"targetKind": protocol.TerminalTargetContainer, "containerId": stoppedID})
	stoppedRequest, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+enrollment.NodeID+"/terminals", bytes.NewReader(stoppedRequestBody))
	if err != nil {
		t.Fatal(err)
	}
	stoppedRequest.Header.Set("Origin", "https://panel.test")
	stoppedRequest.Header.Set("Content-Type", "application/json")
	stoppedRequest.Header.Set(csrfHeaderName, csrfToken)
	stoppedRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	stoppedRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	stoppedResponse, err := httpClient.Do(stoppedRequest)
	if err != nil {
		t.Fatalf("request stopped-container terminal: %v", err)
	}
	stoppedBody, _ := io.ReadAll(io.LimitReader(stoppedResponse.Body, 2048))
	stoppedResponse.Body.Close()
	if stoppedResponse.StatusCode != http.StatusConflict || !strings.Contains(string(stoppedBody), "running container was not found") {
		t.Fatalf("stopped-container terminal status=%d body=%q, want explicit HTTP 409 unavailable-target error", stoppedResponse.StatusCode, strings.TrimSpace(string(stoppedBody)))
	}

	// A running shell-less target passes Core's running-container authorization
	// but is rejected by the real Agent/Engine provider with a visible close.
	shelllessBrowser, shelllessAuth := connectTerminal(shelllessID)
	if _, err := s09ReadTerminalRejection(shelllessBrowser, shelllessAuth.StreamID, 8*time.Second); err != nil {
		_ = shelllessBrowser.CloseNow()
		t.Fatalf("shell-less container did not return an explicit terminal error: %v", err)
	}
	readUntilBrowserClose(t, shelllessBrowser, 5*time.Second)
	waitForCoreTerminalGone(t, core, shelllessAuth.StreamID)
	_ = shelllessBrowser.CloseNow()

	// The authenticated browser stream is bound to alpha's immutable Docker ID.
	// Its marker is not present in the command text or in beta's environment.
	alphaBrowser, alphaAuth := openTerminal(alphaID)
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput,
		Data: []byte("stty -echo; printf 'ND_S09_TERMINAL_READY\\n'\r")})
	readyOutput := readS09TerminalOutputUntil(t, alphaBrowser, alphaAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "ND_S09_TERMINAL_READY")
	})
	_ = readyOutput
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionResize, Rows: 37, Columns: 109})
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput, Data: []byte("stty size; printf 'ND_S09_UTF8_你好\\n'; cat /tmp/nodedance-s09-target\r")})
	output := readS09TerminalOutputUntil(t, alphaBrowser, alphaAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "37 109") && strings.Contains(text, "ND_S09_UTF8_你好") && strings.Contains(text, alphaMarker)
	})
	if strings.Contains(output, betaMarker) {
		t.Fatalf("browser terminal for exact alpha target exposed beta container data: %q", output)
	}

	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput,
		Data: []byte("printf 'ND_S09_SLEEP_STARTED\\n'; sleep 298\r")})
	readS09TerminalOutputUntil(t, alphaBrowser, alphaAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "ND_S09_SLEEP_STARTED")
	})
	waitForS09ServerProcess(t, runCtx.ctx, cli, alphaID, "298", true)
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput, Data: []byte{3}})
	waitForS09ServerProcess(t, runCtx.ctx, cli, alphaID, "298", false)
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput, Data: []byte("printf 'ND_S09_CTRL_C_OK\\n'\r")})
	readS09TerminalOutputUntil(t, alphaBrowser, alphaAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "ND_S09_CTRL_C_OK")
	})
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionInput,
		Data: []byte("printf 'ND_S09_CLOSE_SLEEP_STARTED\\n'; sleep 297\r")})
	readS09TerminalOutputUntil(t, alphaBrowser, alphaAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "ND_S09_CLOSE_SLEEP_STARTED")
	})
	waitForS09ServerProcess(t, runCtx.ctx, cli, alphaID, "297", true)
	writeFrame(alphaBrowser, protocol.TerminalFrame{StreamID: alphaAuth.StreamID, Action: protocol.TerminalActionClose})
	readUntilBrowserClose(t, alphaBrowser, 5*time.Second)
	waitForCoreTerminalGone(t, core, alphaAuth.StreamID)
	waitForS09ServerProcess(t, runCtx.ctx, cli, alphaID, "297", false)
	inspected, err := cli.ContainerInspect(runCtx.ctx, alphaID, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.ID != alphaID || inspected.Container.State == nil || !inspected.Container.State.Running {
		t.Fatalf("closing terminal changed the target container lifecycle: id=%q running=%v err=%v", inspected.Container.ID, inspected.Container.State != nil && inspected.Container.State.Running, err)
	}
	_ = alphaBrowser.CloseNow()
	t.Log("S09 live Core→authenticated browser→enrolled Agent→Engine container Exec passed: exact target, Unicode, resize, Ctrl-C, stopped target rejection, shell-less error, and Exec cleanup")
}

type s09ServerRunContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func connectS09ServerDIND(t *testing.T) (string, *client.Client, s09ServerRunContext) {
	t.Helper()
	socket := strings.TrimSpace(os.Getenv("NODEDANCE_S09_DIND_SOCKET"))
	if socket == "" {
		t.Skip("NOT_READY: real Core/browser Docker terminal case requires NODEDANCE_S09_DIND_SOCKET")
	}
	socket, err := filepath.Abs(socket)
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	engineVersion := ""
	for _, version := range []string{"28", "29"} {
		if socket == filepath.Join(repoRoot, ".artifacts", "dind", "v"+version, "socket", "docker.sock") {
			engineVersion = version
		}
	}
	if engineVersion == "" {
		t.Fatalf("refusing Docker terminal mutations outside this checkout's exact owned Engine 28/29 sockets: %q", socket)
	}
	engineDir := filepath.Dir(filepath.Dir(socket))
	markerBytes, err := os.ReadFile(filepath.Join(engineDir, "owner.json"))
	if err != nil {
		t.Fatalf("read exact S09 DIND owner marker: %v", err)
	}
	var owner s09ServerDINDOwner
	if err := json.Unmarshal(markerBytes, &owner); err != nil || owner.Suite != "nodedance-s00-dind" || owner.ContainerName != "nodedance-s00-dind-v"+engineVersion || !strings.HasPrefix(owner.ServerVersion, engineVersion+".") {
		t.Fatalf("refusing unverified S09 DIND Engine owner marker: %+v err=%v", owner, err)
	}
	endpoint := "unix://" + socket
	cli, err := client.New(client.WithHTTPClient(&http.Client{
		Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}, CheckRedirect: client.CheckRedirect,
	}), client.WithHost(endpoint), client.WithScheme("http"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	version, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil || version.Version != owner.ServerVersion || !strings.HasPrefix(version.Version, engineVersion+".") {
		cancel()
		_ = cli.Close()
		t.Fatalf("S09 owner marker/Engine version mismatch: owner=%q actual=%q err=%v", owner.ServerVersion, version.Version, err)
	}
	return endpoint, cli, s09ServerRunContext{ctx: ctx, cancel: cancel}
}

func s09ServerLockedBusyboxImage(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "test-images.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lock s09ServerLockedImages
	if err := json.Unmarshal(data, &lock); err != nil || lock.Images["busybox"] == "" {
		t.Fatalf("locked S09 BusyBox image is missing: %v", err)
	}
	return lock.Images["busybox"]
}

func createS09ServerRunningFixture(t *testing.T, ctx context.Context, cli *client.Client, fixtureIDs *[]string, image, suite, role, marker string) string {
	t.Helper()
	name := suite + "-" + role
	labels := map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: image, Cmd: []string{"sh", "-c", "printf '%s\\n' \"$NODEDANCE_S09_MARKER\" > /tmp/nodedance-s09-target; exec sleep 300"},
		Env: []string{"NODEDANCE_S09_MARKER=" + marker}, Labels: labels,
	}, Name: name})
	if err != nil || created.ID == "" {
		t.Fatalf("create exact-owner S09 %s fixture: id=%q err=%v", role, created.ID, err)
	}
	*fixtureIDs = append(*fixtureIDs, created.ID)
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner S09 %s fixture: %v", role, err)
	}
	t.Logf("S09_DIND_FIXTURE role=%s id=%s expected_engine_state=running owner_suite=%s", role, created.ID, suite)
	return created.ID
}

func createS09ServerStoppedFixture(t *testing.T, ctx context.Context, cli *client.Client, fixtureIDs *[]string, image, suite string) string {
	t.Helper()
	name := suite + "-stopped"
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: image, Cmd: []string{"true"}, Labels: map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite},
	}, Name: name})
	if err != nil || created.ID == "" {
		t.Fatalf("create exact-owner stopped fixture: id=%q err=%v", created.ID, err)
	}
	*fixtureIDs = append(*fixtureIDs, created.ID)
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner stopped fixture: %v", err)
	}
	waiter := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case err := <-waiter.Error:
		t.Fatalf("wait for S09 stopped fixture: %v", err)
	case <-ctx.Done():
		t.Fatalf("S09 short-lived fixture did not stop: %v", ctx.Err())
	case <-waiter.Result:
	}
	t.Logf("S09_DIND_FIXTURE role=stopped id=%s expected_engine_state=exited owner_suite=%s", created.ID, suite)
	return created.ID
}

func createS09ServerShelllessImage(t *testing.T, ctx context.Context, cli *client.Client, sourceContainerID, imageTag, suite string) string {
	t.Helper()
	t.Cleanup(func() { removeS09ServerShelllessImage(t, cli, imageTag, "", suite) })
	rootFS, err := cli.ContainerExport(ctx, sourceContainerID, client.ContainerExportOptions{})
	if err != nil {
		t.Fatalf("export locked BusyBox S09 fixture: %v", err)
	}
	defer rootFS.Close()
	reader := tar.NewReader(rootFS)
	var busybox []byte
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("read locked BusyBox S09 export: %v", nextErr)
		}
		cleanName := strings.TrimPrefix(header.Name, "./")
		if cleanName == "bin/[" && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) {
			if header.Size <= 0 || header.Size > 8<<20 {
				t.Fatalf("locked BusyBox executable has unsafe size: %d", header.Size)
			}
			busybox, err = io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(busybox)) != header.Size {
				t.Fatalf("read BusyBox executable: bytes=%d size=%d err=%v", len(busybox), header.Size, err)
			}
			break
		}
	}
	if len(busybox) == 0 {
		t.Fatal("locked BusyBox image did not contain the expected standalone [ applet executable")
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "bin/busybox", Mode: 0o755, Size: int64(len(busybox)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatalf("write shell-less S09 rootfs header: %v", err)
	}
	if _, err := writer.Write(busybox); err != nil {
		t.Fatalf("write shell-less S09 rootfs executable: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close shell-less S09 rootfs archive: %v", err)
	}
	loaded, err := cli.ImageImport(ctx, client.ImageImportSource{SourceName: "-", Source: bytes.NewReader(archive.Bytes())}, imageTag,
		client.ImageImportOptions{Changes: []string{
			`CMD ["/bin/busybox","sleep","300"]`,
			`LABEL io.nodedance.test=true`,
			`LABEL io.nodedance.suite=` + suite,
		}})
	if err != nil {
		t.Fatalf("import shell-less S09 fixture image: %v", err)
	}
	if _, err := io.Copy(io.Discard, loaded); err != nil {
		_ = loaded.Close()
		t.Fatalf("finish shell-less S09 fixture import: %v", err)
	}
	if err := loaded.Close(); err != nil {
		t.Fatalf("close shell-less S09 fixture import: %v", err)
	}
	image, err := cli.ImageInspect(ctx, imageTag)
	if err != nil || image.ID == "" || !s09ServerContains(image.RepoTags, imageTag) || image.Config == nil || image.Config.Labels["io.nodedance.test"] != "true" || image.Config.Labels["io.nodedance.suite"] != suite {
		t.Fatalf("inspect exact shell-less S09 fixture image: id=%q tags=%v err=%v", image.ID, image.RepoTags, err)
	}
	t.Logf("S09_DIND_FIXTURE role=shellless_image id=%s tag=%s owner_suite=%s", image.ID, imageTag, suite)
	return image.ID
}

func createS09ServerShelllessFixture(t *testing.T, ctx context.Context, cli *client.Client, fixtureIDs *[]string, imageTag, suite string) string {
	t.Helper()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{
		Image: imageTag, Cmd: []string{"/bin/busybox", "sleep", "300"},
		Labels: map[string]string{"io.nodedance.test": "true", "io.nodedance.suite": suite},
	}, Name: suite + "-shellless"})
	if err != nil || created.ID == "" {
		t.Fatalf("create exact-owner shell-less fixture: id=%q err=%v", created.ID, err)
	}
	*fixtureIDs = append(*fixtureIDs, created.ID)
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start exact-owner shell-less fixture: %v", err)
	}
	t.Logf("S09_DIND_FIXTURE role=shellless id=%s expected_engine_state=running owner_suite=%s", created.ID, suite)
	return created.ID
}

type s09ServerFixtureExpectation struct {
	id      string
	state   string
	running bool
}

type s09ServerInventoryObservation struct {
	found             bool
	name              string
	state             string
	running           bool
	stale             bool
	unavailableReason string
}

func waitForS09ServerDockerReady(t *testing.T, core *Server, nodeID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last coredocker.View
	var lastState dashboardNodeState
	for time.Now().Before(deadline) {
		state, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			lastState, last = state, view
			if state.Exists && state.Status == "online" && view.AgentOnline && view.DockerAvailability == "available" &&
				view.DockerSnapshotFresh && view.DockerEventsConnected && !view.DataStale {
				t.Logf("S09_DIND_INVENTORY baseline_ready generation=%d containers=%d snapshot_fresh=%t events_connected=%t", view.ActiveGeneration, len(view.Containers), view.DockerSnapshotFresh, view.DockerEventsConnected)
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("S09 Docker observer did not establish its initial full snapshot/event stream before fixture creation: node_status=%s agent_online=%t docker=%s snapshot_fresh=%t events_connected=%t data_stale=%t stale_reason=%q containers=%d",
		lastState.Status, last.AgentOnline, last.DockerAvailability, last.DockerSnapshotFresh, last.DockerEventsConnected, last.DataStale, last.StaleReason, len(last.Containers))
}

func waitForS09ServerContainerInventory(t *testing.T, core *Server, nodeID string, cli *client.Client, ctx context.Context, expected map[string]s09ServerFixtureExpectation, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	latest := make(map[string]s09ServerInventoryObservation, len(expected))
	var lastView coredocker.View
	var lastState dashboardNodeState
	var lastError error
	for time.Now().Before(deadline) {
		state, view, err := core.dockerViewForNode(context.Background(), nodeID)
		if err == nil {
			lastError = nil
			lastState, lastView = state, view
			latest = make(map[string]s09ServerInventoryObservation, len(expected))
			for role, fixture := range expected {
				latest[role] = s09ServerInventoryObservation{}
				for _, record := range view.Containers {
					if record.Container.ID == fixture.id {
						latest[role] = s09ServerInventoryObservation{found: true, name: record.Container.Name,
							state: record.Container.State, running: record.Container.Running,
							stale: record.Container.Stale, unavailableReason: record.Container.UnavailableReason}
						break
					}
				}
			}
			if state.Exists && state.Status == "online" && view.AgentOnline && view.DockerSnapshotFresh && view.DockerEventsConnected &&
				!view.DataStale && view.DockerAvailability == "available" && s09ServerFixtureInventoryMatches(expected, latest) {
				return
			}
		} else {
			lastError = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	engine := make(map[string]string, len(expected))
	for role, fixture := range expected {
		inspectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		inspected, err := cli.ContainerInspect(inspectCtx, fixture.id, client.ContainerInspectOptions{})
		cancel()
		if err != nil {
			engine[role] = "inspect_error=" + err.Error()
			continue
		}
		if inspected.Container.State == nil {
			engine[role] = fmt.Sprintf("id=%s name=%s state=<nil>", inspected.Container.ID, inspected.Container.Name)
			continue
		}
		engine[role] = fmt.Sprintf("id=%s name=%s state=%s running=%t", inspected.Container.ID, inspected.Container.Name, inspected.Container.State.Status, inspected.Container.State.Running)
	}
	t.Fatalf("Core did not receive all role-tagged S09 fixtures after a known fresh baseline: expected=%+v core={error:%v status:%s generation:%d docker:%s snapshot_fresh:%t events_connected:%t data_stale:%t stale_reason:%q containers:%d} observed=%+v engine=%+v",
		expected, lastError, lastState.Status, lastView.ActiveGeneration, lastView.DockerAvailability, lastView.DockerSnapshotFresh,
		lastView.DockerEventsConnected, lastView.DataStale, lastView.StaleReason, len(lastView.Containers), latest, engine)
}

func s09ServerFixtureInventoryMatches(expected map[string]s09ServerFixtureExpectation, actual map[string]s09ServerInventoryObservation) bool {
	for role, fixture := range expected {
		observed := actual[role]
		if !observed.found || observed.state != fixture.state || observed.running != fixture.running || observed.stale {
			return false
		}
	}
	return true
}

func waitForS09ServerProcess(t *testing.T, ctx context.Context, cli *client.Client, containerID, needle string, present bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		result, err := cli.ContainerTop(ctx, containerID, client.ContainerTopOptions{Arguments: []string{"-ef"}})
		if err == nil {
			found := false
			for _, process := range result.Processes {
				if strings.Contains(strings.Join(process, " "), needle) {
					found = true
					break
				}
			}
			if found == present {
				return
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for S09 target process %q: %v", needle, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if present {
		t.Fatalf("container process %q did not start", needle)
	}
	t.Fatalf("container process %q remained after terminal Ctrl-C", needle)
}

func removeS09ServerFixture(t *testing.T, cli *client.Client, containerID, suite string) {
	t.Helper()
	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelInspect()
	inspected, err := cli.ContainerInspect(inspectCtx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return
	}
	if inspected.Container.ID != containerID || inspected.Container.Config == nil || inspected.Container.Config.Labels["io.nodedance.test"] != "true" || inspected.Container.Config.Labels["io.nodedance.suite"] != suite {
		t.Errorf("refusing to remove S09 container that does not match its exact test labels: %s", containerID)
		return
	}
	removeCtx, cancelRemove := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRemove()
	if _, err := cli.ContainerRemove(removeCtx, containerID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Errorf("remove exact-owner S09 fixture container: %v", err)
	}
}

func removeS09ServerShelllessImage(t *testing.T, cli *client.Client, tag, expectedID, suite string) {
	t.Helper()
	inspectCtx, cancelInspect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelInspect()
	image, err := cli.ImageInspect(inspectCtx, tag)
	if err != nil {
		return
	}
	if image.ID == "" || expectedID != "" && image.ID != expectedID || !s09ServerContains(image.RepoTags, tag) || image.Config == nil || image.Config.Labels["io.nodedance.test"] != "true" || image.Config.Labels["io.nodedance.suite"] != suite {
		t.Errorf("refusing to remove S09 shell-less image whose exact ID/tag/owner changed: expected=%s actual=%s tags=%v", expectedID, image.ID, image.RepoTags)
		return
	}
	removeCtx, cancelRemove := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRemove()
	if _, err := cli.ImageRemove(removeCtx, image.ID, client.ImageRemoveOptions{}); err != nil {
		t.Errorf("remove exact-owner S09 shell-less image: %v", err)
	}
}

func s09ReadTerminalRejection(browser *websocket.Conn, streamID string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	messageType, data, err := browser.Read(ctx)
	if err != nil {
		var closeErr websocket.CloseError
		if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusPolicyViolation && strings.TrimSpace(closeErr.Reason) != "" {
			return closeErr.Reason, nil
		}
		return "", fmt.Errorf("read shell-less terminal rejection: %w", err)
	}
	if messageType != websocket.MessageText {
		return "", fmt.Errorf("shell-less terminal message type=%d", messageType)
	}
	var message s09TerminalMessage
	if err := json.Unmarshal(data, &message); err != nil || message.Type != "terminal" || message.Frame.StreamID != streamID || message.Frame.Action != protocol.TerminalActionError || strings.TrimSpace(message.Frame.Message) == "" {
		return "", fmt.Errorf("shell-less terminal frame was not an explicit target error: type=%q frame=%+v decode=%v", message.Type, message.Frame, err)
	}
	return message.Frame.Message, nil
}

func s09ServerContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
