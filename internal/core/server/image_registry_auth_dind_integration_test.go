package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	agentimages "github.com/CST-Cat/NodeDance/internal/agent/images"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"golang.org/x/crypto/bcrypt"
)

type s08CoreDINDOwner struct {
	Suite         string `json:"suite"`
	ContainerName string `json:"container_name"`
	Image         string `json:"image"`
	Socket        string `json:"socket"`
	HostDaemon    string `json:"host_daemon"`
	ServerVersion string `json:"server_version"`
	RunID         string `json:"run_id"`
}

type s08CoreLockedImages struct {
	Images map[string]string `json:"images"`
}

type s08CoreCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (capture *s08CoreCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buf.Write(data)
}

func (capture *s08CoreCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buf.String()
}

// TestS08CoreAgentAuthenticatedImagePullOnOwnedDIND joins the authenticated
// Core API, a registered Agent runtime, its SDK Engine, and this run's private
// Registry. It is deliberately one S08 integration slice, not stage sign-off.
func TestS08CoreAgentAuthenticatedImagePullOnOwnedDIND(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("NODEDANCE_S08_DIND_ROOT"))
	runID := strings.TrimSpace(os.Getenv("NODEDANCE_S08_RUN_ID"))
	username := os.Getenv("NODEDANCE_S08_REGISTRY_USER")
	password := os.Getenv("NODEDANCE_S08_REGISTRY_PASSWORD")
	if root == "" || runID == "" || username == "" || password == "" {
		t.Skip("NOT_READY: S08 owner-marked DIND root, run ID, and Registry canaries are required")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,35}$`).MatchString(runID) ||
		len(username) > 256 || strings.ContainsAny(username, ":\r\n\x00\x7f") || len(password) > 4096 || strings.ContainsAny(password, "\r\n\x00\x7f") {
		t.Fatal("invalid S08 run ID or Registry canary format")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal("resolve repository root")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal("resolve S08 DIND root")
	}
	engineVersion := ""
	for _, version := range []string{"28", "29"} {
		expected := filepath.Join(repoRoot, ".artifacts", "s08", "dind", "v"+version, runID)
		if filepath.Clean(root) == filepath.Clean(expected) {
			engineVersion = version
		}
	}
	if engineVersion == "" {
		t.Fatalf("refusing DIND root outside this checkout's exact S08 run directory: %s", root)
	}
	markerBytes, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		t.Fatalf("read S08 Engine owner marker: %v", err)
	}
	var owner s08CoreDINDOwner
	if err := json.Unmarshal(markerBytes, &owner); err != nil {
		t.Fatalf("decode S08 Engine owner marker: %v", err)
	}
	socket := filepath.Join(root, "socket", "docker.sock")
	if owner.Suite != "nodedance-s08-dind" || owner.RunID != runID || filepath.Clean(owner.Socket) != socket ||
		!strings.HasPrefix(owner.ServerVersion, engineVersion+".") || owner.ContainerName == "" || owner.Image == "" || owner.HostDaemon == "" {
		t.Fatal("S08 owner marker does not identify the requested isolated Engine run")
	}
	if info, err := os.Stat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("S08 Engine socket is unavailable: %v", err)
	}
	endpoint := "unix://" + socket
	workRoot := t.TempDir()
	work := filepath.Join(workRoot, "private-work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatalf("create private S08 integration work directory: %v", err)
	}
	dockerConfig := filepath.Join(work, "docker-config")
	if err := os.Mkdir(dockerConfig, 0o700); err != nil {
		t.Fatalf("create isolated Docker CLI config: %v", err)
	}
	t.Setenv("DOCKER_HOST", endpoint)
	t.Setenv("DOCKER_CONFIG", dockerConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	engineActual, err := s08CoreDocker(ctx, endpoint, dockerConfig, "version", "--format", "{{.Server.Version}}")
	if err != nil || strings.TrimSpace(engineActual) != owner.ServerVersion || !strings.HasPrefix(strings.TrimSpace(engineActual), engineVersion+".") {
		t.Fatalf("owner-marked S08 Engine version mismatch: owner=%q actual=%q err=%v", owner.ServerVersion, strings.TrimSpace(engineActual), err)
	}
	architecture, err := s08CoreDocker(ctx, endpoint, dockerConfig, "version", "--format", "{{.Server.Arch}}")
	if err != nil || (strings.TrimSpace(architecture) != "amd64" && strings.TrimSpace(architecture) != "arm64") {
		t.Fatalf("owner-marked S08 Engine has unsupported architecture: %q err=%v", strings.TrimSpace(architecture), err)
	}
	var locked s08CoreLockedImages
	lockBytes, err := os.ReadFile(filepath.Join(repoRoot, "test-images.lock.json"))
	if err != nil || json.Unmarshal(lockBytes, &locked) != nil || !strings.Contains(locked.Images["registry"], "@sha256:") {
		t.Fatal("S08 private Registry image lock is missing or invalid")
	}
	registryImage := locked.Images["registry"]
	if _, err := s08CoreDocker(ctx, endpoint, dockerConfig, "image", "inspect", registryImage); err != nil {
		if _, pullErr := s08CoreDocker(ctx, endpoint, dockerConfig, "pull", registryImage); pullErr != nil {
			t.Fatalf("obtain locked private Registry fixture image: inspect_ok=false pull_err=%v", pullErr)
		}
	}

	var outerConfig string
	outerConfig = filepath.Join(work, "outer-docker-config")
	if err := os.Mkdir(outerConfig, 0o700); err != nil {
		t.Fatalf("create isolated host Docker CLI config: %v", err)
	}
	containerIPs, err := s08CoreDocker(ctx, owner.HostDaemon, outerConfig, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", owner.ContainerName)
	if err != nil {
		t.Fatalf("inspect only the owner-marked DIND container for its private bridge address: %v", err)
	}
	bridgeIP := ""
	for _, candidate := range strings.Fields(containerIPs) {
		if net.ParseIP(candidate) != nil {
			bridgeIP = candidate
			break
		}
	}
	if bridgeIP == "" {
		t.Fatal("owner-marked S08 DIND container has no usable private bridge address")
	}

	registrySuite := "nodedance-s08-core-image-auth-" + runID
	registryName := "nd-s08-core-reg-" + s08CoreSafeSuffix(runID)
	registryRef := "127.0.0.1:5001/nodedance/core-auth-" + s08CoreSafeSuffix(runID) + ":fixture"
	registryHost := net.JoinHostPort(bridgeIP, "5001")
	registryBase := "http://" + registryHost
	registryAuthRoot := filepath.Join(work, "registry-auth")
	if err := os.Mkdir(registryAuthRoot, 0o700); err != nil {
		t.Fatalf("create isolated Registry auth fixture: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal("hash Registry fixture credential")
	}
	htpasswdPath := filepath.Join(registryAuthRoot, "htpasswd")
	if err := os.WriteFile(htpasswdPath, append(append([]byte(username+":"), hash...), '\n'), 0o600); err != nil {
		t.Fatal("write isolated Registry credential verifier")
	}
	if _, err := s08CoreDocker(ctx, endpoint, dockerConfig, "container", "inspect", registryName); err == nil {
		t.Fatalf("refusing to reuse an existing Registry fixture name %q", registryName)
	}
	registryID, err := s08CoreDocker(ctx, endpoint, dockerConfig, "create", "--name", registryName, "--network", "host",
		"--label", "io.nodedance.test=true", "--label", "io.nodedance.suite="+registrySuite,
		"--label", "io.nodedance.run="+runID,
		"--env", "REGISTRY_HTTP_ADDR=0.0.0.0:5001",
		"--env", "REGISTRY_AUTH=htpasswd",
		"--env", "REGISTRY_AUTH_HTPASSWD_REALM=NodeDance S08 Core API integration",
		"--env", "REGISTRY_AUTH_HTPASSWD_PATH=/etc/docker/registry/htpasswd", registryImage)
	if err != nil || strings.TrimSpace(registryID) == "" {
		t.Fatalf("create run-owned private Registry fixture: %v", err)
	}
	registryID = strings.TrimSpace(registryID)
	registryTagCreated := false
	var registrySourceID string
	t.Cleanup(func() {
		if registryTagCreated {
			imageIDs, inspectErr := s08CoreDocker(context.Background(), endpoint, dockerConfig, "image", "ls", "--quiet", "--no-trunc", registryRef)
			exists, ownershipErr := s08CoreCleanupResourceID(imageIDs, inspectErr, registrySourceID)
			if ownershipErr != nil {
				t.Errorf("preserving private Registry image tag because its exact identity could not be confirmed: %v", ownershipErr)
			} else if exists {
				if _, removeErr := s08CoreDocker(context.Background(), endpoint, dockerConfig, "image", "rm", registryRef); removeErr != nil {
					t.Errorf("could not remove only this run's private Registry seed tag: %v", removeErr)
				}
			}
		}
		containerIDs, inspectErr := s08CoreDocker(context.Background(), endpoint, dockerConfig, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "name="+registryName)
		containerExists, ownershipErr := s08CoreCleanupResourceID(containerIDs, inspectErr, registryID)
		if ownershipErr != nil {
			t.Errorf("preserving Registry container because its exact identity could not be confirmed: %v", ownershipErr)
			return
		}
		if !containerExists {
			return
		}
		labels, inspectErr := s08CoreDocker(context.Background(), endpoint, dockerConfig, "container", "inspect", "--format",
			`{{.Id}}|{{ index .Config.Labels "io.nodedance.suite" }}|{{ index .Config.Labels "io.nodedance.run" }}`, registryName)
		if inspectErr == nil {
			if !s08CoreRegistryOwnershipMatches(labels, registryID, registrySuite, runID) {
				t.Errorf("preserving Registry container whose exact run ownership no longer matches")
				return
			}
			if _, removeErr := s08CoreDocker(context.Background(), endpoint, dockerConfig, "container", "rm", "--force", registryName); removeErr != nil {
				t.Errorf("remove exact run-owned private Registry container: %v", removeErr)
			}
		} else {
			t.Errorf("could not verify exact Registry container ownership before cleanup")
		}
	})
	if _, err := s08CoreDocker(ctx, endpoint, dockerConfig, "container", "cp", htpasswdPath, registryName+":/etc/docker/registry/htpasswd"); err != nil {
		t.Fatalf("install private Registry credential verifier: %v", err)
	}
	if _, err := s08CoreDocker(ctx, endpoint, dockerConfig, "container", "start", registryName); err != nil {
		t.Fatalf("start run-owned private Registry: %v", err)
	}
	if err := s08WaitPrivateCoreRegistry(ctx, registryBase, username, password); err != nil {
		t.Fatalf("private Registry did not enforce the configured credential: %v", err)
	}

	imageEngine, err := agentimages.NewSDKEngine(endpoint)
	if err != nil {
		t.Fatalf("create SDK adapter for the owner-marked Engine: %v", err)
	}
	defer func() {
		if err := imageEngine.Close(); err != nil {
			t.Errorf("close test Engine image client: %v", err)
		}
	}()
	setupCtx, cancelSetup := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelSetup()
	sourceImage, err := imageEngine.Inspect(setupCtx, registryImage)
	if err != nil || sourceImage.ID == "" {
		t.Fatalf("inspect locked local Registry image seed: image_id_present=%t err=%v", sourceImage.ID != "", err)
	}
	registrySourceID = sourceImage.ID
	pushClient, err := client.New(client.WithHost(endpoint), client.WithScheme("http"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("create test Engine push client: %v", err)
	}
	defer pushClient.Close()
	if _, err := pushClient.ImageTag(setupCtx, client.ImageTagOptions{Source: registryImage, Target: registryRef}); err != nil {
		t.Fatalf("tag locked fixture image for this Registry run: %v", err)
	}
	registryTagCreated = true
	registryAuth, err := agentimages.EncodeRegistryCredentials(username, password)
	if err != nil {
		t.Fatal("encode transient seed credential")
	}
	pushResponse, err := pushClient.ImagePush(setupCtx, registryRef, client.ImagePushOptions{RegistryAuth: registryAuth})
	registryAuth = ""
	if err != nil {
		t.Fatalf("push locked image seed to local private Registry: %v", err)
	}
	for message, streamErr := range pushResponse.JSONMessages(setupCtx) {
		if streamErr != nil {
			_ = pushResponse.Close()
			t.Fatalf("consume private Registry seed push stream: %v", streamErr)
		}
		if message.Error != nil {
			_ = pushResponse.Close()
			t.Fatal("private Registry rejected the authenticated seed push")
		}
	}
	_ = pushResponse.Close()
	registryPath := strings.TrimPrefix(registryRef, "127.0.0.1:5001/")
	registryParts := strings.SplitN(registryPath, ":", 2)
	if len(registryParts) != 2 {
		t.Fatal("could not split the run-owned Registry repository and tag")
	}
	repository := registryParts[0]
	tag := registryParts[1]
	expectedDigest, err := s08CoreRegistryManifestDigest(ctx, registryBase, repository+"/manifests/"+tag, username, password)
	if err != nil {
		t.Fatalf("read the seeded Registry manifest digest: %v", err)
	}
	if expectedDigest == "" {
		t.Fatal("local private Registry returned an empty manifest digest")
	}
	expectedRepositoryDigest := "127.0.0.1:5001/" + repository + "@" + expectedDigest
	if _, err := s08CoreDocker(ctx, endpoint, dockerConfig, "image", "rm", registryRef); err != nil {
		t.Fatalf("remove only the seed tag before the Core API pull: %v", err)
	}
	registryTagCreated = false
	if _, err := imageEngine.Inspect(setupCtx, registryRef); !errdefs.IsNotFound(err) {
		t.Fatalf("the run-scoped private Registry tag must be absent before the Core API pull: err=%v", err)
	}

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal("create Core-Agent test certificate")
	}
	coreData := filepath.Join(work, "core-data")
	agentData := filepath.Join(work, "agent-data")
	core, err := New("s08-core-image-auth-dind", Options{
		DataDir: coreData, PublicOrigin: "https://panel.test", AgentOfflineTimeout: 30 * time.Second,
		AgentSweepInterval: 250 * time.Millisecond, WebSocketCheckInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("create S08 Core: %v", err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	adminClient := tlsHTTPClient(rootPEM)
	adminClient.Timeout = 5 * time.Second
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		coreHTTP.Close()
		_ = core.Close()
		t.Fatalf("install authenticated integration administrator: %v", err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S08 real Registry auth Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		coreHTTP.Close()
		_ = core.Close()
		t.Fatalf("create one-use real Agent enrollment: %v", err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		coreHTTP.Close()
		_ = core.Close()
		t.Fatalf("write Core test CA: %v", err)
	}
	configPath := filepath.Join(agentData, "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), configPath); err != nil {
		coreHTTP.Close()
		_ = core.Close()
		t.Fatalf("enroll the real Agent over Core HTTPS: %v", err)
	}
	agentConfig, err := agent.LoadConfig(configPath)
	if err != nil || agentConfig.NodeID != enrollment.NodeID || agentConfig.AgentID == "" {
		coreHTTP.Close()
		_ = core.Close()
		t.Fatal("real Agent identity does not match the Core enrollment")
	}

	var runtimeLogs s08CoreCapture
	previousLogOutput := log.Writer()
	log.SetOutput(&runtimeLogs)
	t.Cleanup(func() { log.SetOutput(previousLogOutput) })
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- agent.Run(agentCtx, configPath, "s08-core-image-auth", &runtimeLogs) }()
	agentStopped := false
	coreClosed := false
	stopRuntime := func() {
		if !agentStopped {
			cancelAgent()
			select {
			case runErr := <-agentDone:
				if runErr != nil {
					t.Errorf("real Agent returned an error while stopping: %v", runErr)
				}
			case <-time.After(12 * time.Second):
				t.Error("real Agent did not stop after the S08 image pull")
			}
			agentStopped = true
		}
		if !coreClosed {
			adminClient.CloseIdleConnections()
			coreHTTP.Close()
			if err := core.Close(); err != nil {
				t.Errorf("close S08 Core: %v", err)
			}
			coreClosed = true
		}
	}
	t.Cleanup(stopRuntime)
	waitForAgentStatusWithin(t, core, agentConfig.NodeID, "online", 0, 20*time.Second)
	deadline := time.Now().Add(30 * time.Second)
	var readiness struct {
		nodeStatus      string
		generation      uint64
		viewAvailable   bool
		viewError       bool
		agentOnline     bool
		dataStale       bool
		dockerAvailable string
		snapshotFresh   bool
		eventsConnected bool
		staleReason     string
		taskBridgeReady bool
	}
	for time.Now().Before(deadline) {
		if nodes, listErr := core.agents.ListNodes(context.Background()); listErr == nil {
			for _, node := range nodes {
				if node.NodeID == agentConfig.NodeID {
					readiness.nodeStatus = node.Status
					readiness.generation = node.ConnectionGeneration
					break
				}
			}
		}
		_, view, viewErr := core.dockerViewForNode(context.Background(), agentConfig.NodeID)
		readiness.viewAvailable = viewErr == nil
		readiness.viewError = viewErr != nil
		if viewErr == nil {
			readiness.agentOnline = view.AgentOnline
			readiness.dataStale = view.DataStale
			readiness.dockerAvailable = string(view.DockerAvailability)
			readiness.snapshotFresh = view.DockerSnapshotFresh
			readiness.eventsConnected = view.DockerEventsConnected
			readiness.staleReason = view.StaleReason
		}
		readiness.taskBridgeReady = readiness.generation != 0 && core.taskBridgeReady(agentConfig.NodeID, readiness.generation)
		if readiness.nodeStatus == "online" && readiness.viewAvailable && readiness.agentOnline && !readiness.dataStale &&
			readiness.dockerAvailable == "available" && readiness.snapshotFresh && readiness.taskBridgeReady {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if readiness.nodeStatus != "online" || !readiness.viewAvailable || !readiness.agentOnline || readiness.dataStale ||
		readiness.dockerAvailable != "available" || !readiness.snapshotFresh || !readiness.taskBridgeReady {
		logTail := runtimeLogs.String()
		if s08CoreContainsCanary([]byte(logTail), username, password) {
			logTail = "[withheld: a Registry credential canary was present]"
		} else {
			logTail = s08CoreSafeTail(logTail, 2048)
		}
		t.Fatalf("registered Agent readiness timeout: node_status=%q generation=%d view_available=%t view_error=%t agent_online=%t data_stale=%t docker_availability=%q docker_snapshot_fresh=%t docker_events_connected=%t stale_reason=%q task_bridge_ready=%t runtime_log_tail=%q",
			readiness.nodeStatus, readiness.generation, readiness.viewAvailable, readiness.viewError, readiness.agentOnline,
			readiness.dataStale, readiness.dockerAvailable, readiness.snapshotFresh, readiness.eventsConnected,
			readiness.staleReason, readiness.taskBridgeReady, logTail)
	}

	requestPayload, err := json.Marshal(imagePullRequest{ImageReference: registryRef, Username: username, Password: password})
	if err != nil {
		t.Fatal("encode authenticated image pull request")
	}
	apiOrigin := "https://panel.test"
	postRequest, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+agentConfig.NodeID+"/images/pull", bytes.NewReader(requestPayload))
	if err != nil {
		clear(requestPayload)
		t.Fatal("construct authenticated image pull request")
	}
	postRequest.Header.Set("Content-Type", "application/json")
	postRequest.Header.Set("Origin", apiOrigin)
	postRequest.Header.Set(csrfHeaderName, csrf)
	postRequest.Header.Set("Idempotency-Key", "s08-core-image-auth-"+s08CoreSafeSuffix(runID))
	postRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	postRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
	postResponse, err := adminClient.Do(postRequest)
	clear(requestPayload)
	if err != nil {
		t.Fatalf("submit authenticated image pull through Core HTTPS API: %v", err)
	}
	postBody, err := io.ReadAll(io.LimitReader(postResponse.Body, 1<<20))
	_ = postResponse.Body.Close()
	if err != nil {
		t.Fatalf("read authenticated image pull acknowledgement: %v", err)
	}
	if postResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("authenticated image pull API returned HTTP %d, want %d", postResponse.StatusCode, http.StatusAccepted)
	}
	registryTagCreated = true
	if s08CoreContainsCanary(postBody, username, password) {
		t.Fatal("authenticated image pull API acknowledgement contained a Registry credential")
	}
	var accepted struct {
		TaskID string `json:"taskId"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(postBody, &accepted); err != nil || accepted.TaskID == "" || accepted.Status != "queued" {
		t.Fatalf("Core API did not acknowledge a queued image-pull task: task_id_present=%t status=%q decode_error=%t", accepted.TaskID != "", accepted.Status, err != nil)
	}
	taskPath := "/api/v1/nodes/" + agentConfig.NodeID + "/tasks/" + accepted.TaskID
	var taskView struct {
		TaskID        string `json:"taskId"`
		Status        string `json:"status"`
		DeliveryState string `json:"deliveryState"`
		Result        struct {
			Code string `json:"code"`
		} `json:"result"`
	}
	if err := s08CorePollTask(adminClient, coreHTTP.URL+taskPath, apiOrigin, session, csrf, &taskView, func() bool {
		return taskView.Status == "succeeded" || taskView.Status == "failed" || taskView.Status == "unknown" || taskView.Status == "timed_out"
	}, 2*time.Minute, username, password); err != nil {
		t.Fatalf("authenticated Agent image task did not reach a confirmed terminal state: %v", err)
	}
	if taskView.TaskID != accepted.TaskID || taskView.Status != "succeeded" || taskView.DeliveryState != "done" || taskView.Result.Code != "verified" {
		t.Fatalf("Core did not report verified Agent image-pull success: status=%q delivery=%q result=%q", taskView.Status, taskView.DeliveryState, taskView.Result.Code)
	}
	persistedTask, err := core.tasks.Get(context.Background(), agentConfig.NodeID, accepted.TaskID)
	if err != nil || !persistedTask.RegistryAuthRequired || persistedTask.Status != taskstate.Succeeded {
		t.Fatalf("Core did not preserve the credential-required task state: auth_required=%t succeeded=%t lookup_error=%t",
			persistedTask.RegistryAuthRequired, persistedTask.Status == taskstate.Succeeded, err != nil)
	}
	core.imageAuthMu.Lock()
	remainingCredentials := len(core.imageAuth)
	core.imageAuthMu.Unlock()
	if remainingCredentials != 0 {
		t.Fatalf("Core retained %d one-use Registry credential entries after Agent delivery", remainingCredentials)
	}

	image, err := imageEngine.Inspect(ctx, registryRef)
	if err != nil || image.ID != registrySourceID || !s08CoreContainsString(image.Digests, expectedRepositoryDigest) {
		t.Fatalf("Docker Engine result did not match the authenticated Registry manifest: image_id_matches=%t digest_matches=%t inspect_error=%t",
			image.ID == registrySourceID, s08CoreContainsString(image.Digests, expectedRepositoryDigest), err != nil)
	}
	if err := s08CoreReadAPIWithoutSecrets(adminClient, coreHTTP.URL, apiOrigin, session, csrf, taskPath, username, password); err != nil {
		t.Fatal(err)
	}

	stopRuntime()
	if err := s08CoreScanFilesForCanaries([]string{coreData, agentData}, username, password); err != nil {
		t.Fatal(err)
	}
	if s08CoreContainsCanary([]byte(runtimeLogs.String()), username, password) {
		t.Fatal("Core or Agent runtime logs contained a Registry credential canary")
	}
	registryLogs, err := s08CoreDocker(ctx, endpoint, dockerConfig, "container", "logs", registryName)
	if err != nil {
		t.Fatal("private Registry logs could not be read for the complete credential scan")
	}
	registryScan := s08CoreScanRegistryLogs([]byte(registryLogs), username, password)
	if registryScan.otherUsernameMatch || registryScan.passwordMatch || registryScan.authorizationHeaderValueMatch {
		t.Fatalf("private Registry log scan found an unexpected credential occurrence: other_username=%t password=%t authorization_value=%t username_fields=%s",
			registryScan.otherUsernameMatch, registryScan.passwordMatch, registryScan.authorizationHeaderValueMatch,
			s08CoreFormatRegistryLogOccurrences(registryScan.usernameOccurrences))
	}
	t.Logf("S08 Core-Agent-Engine private Registry pull verified: Engine=%s task=%s image_id_match=true manifest_digest_match=true authenticated_pull=true one_use_credentials_consumed=true persistent_secret_scan=clean registry_username_auth_audit_field_matches=%d registry_username_fields=%s registry_password_canary=false registry_basic_authorization_value_canary=false",
		owner.ServerVersion, accepted.TaskID, registryScan.usernameAuthFieldMatches,
		s08CoreFormatRegistryLogOccurrences(registryScan.usernameOccurrences))
}

func s08CoreSafeSuffix(value string) string {
	value = regexp.MustCompile(`[^A-Za-z0-9.-]+`).ReplaceAllString(value, "-")
	if len(value) > 28 {
		value = value[:28]
	}
	return value
}

// s08CoreCleanupResourceID treats a successful empty listing as an absent
// resource. Command failures and ambiguous or mismatched listings fail closed.
func s08CoreCleanupResourceID(output string, commandErr error, expectedID string) (bool, error) {
	if commandErr != nil {
		return false, errors.New("resource listing command failed")
	}
	ids := strings.Fields(output)
	if len(ids) == 0 {
		return false, nil
	}
	if expectedID == "" || len(ids) != 1 || ids[0] != expectedID {
		return false, errors.New("resource listing did not identify exactly the expected run-owned object")
	}
	return true, nil
}

func s08CoreRegistryOwnershipMatches(output, expectedID, expectedSuite, expectedRun string) bool {
	parts := strings.Split(strings.TrimSpace(output), "|")
	return expectedID != "" && expectedSuite != "" && expectedRun != "" && len(parts) == 3 &&
		parts[0] == expectedID && parts[1] == expectedSuite && parts[2] == expectedRun
}

func TestS08CleanupClassificationFailClosed(t *testing.T) {
	const expectedID = "sha256:owned"
	for _, test := range []struct {
		name       string
		output     string
		commandErr error
		wantFound  bool
		wantError  bool
		wantID     string
	}{
		{name: "successful empty listing is absent", output: "", wantFound: false, wantID: expectedID},
		{name: "exact owned resource", output: expectedID, wantFound: true, wantID: expectedID},
		{name: "wrong resource ID", output: "sha256:other", wantError: true, wantID: expectedID},
		{name: "ambiguous multiple resources", output: expectedID + "\nsha256:other", wantError: true, wantID: expectedID},
		{name: "listing command error is not absence", commandErr: errors.New("safe wrapper error"), wantError: true, wantID: expectedID},
		{name: "missing expected ID fails closed", output: expectedID, wantError: true, wantID: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			found, err := s08CoreCleanupResourceID(test.output, test.commandErr, test.wantID)
			if found != test.wantFound || (err != nil) != test.wantError {
				t.Fatalf("cleanup classification = (found=%t, err=%v), want (found=%t, error=%t)", found, err, test.wantFound, test.wantError)
			}
		})
	}

	if !s08CoreRegistryOwnershipMatches("container-id|suite-id|run-id", "container-id", "suite-id", "run-id") {
		t.Fatal("exact suite/run/container marker did not match")
	}
	for _, value := range []string{
		"other-container|suite-id|run-id",
		"container-id|other-suite|run-id",
		"container-id|suite-id|other-run",
		"container-id|suite-id",
	} {
		if s08CoreRegistryOwnershipMatches(value, "container-id", "suite-id", "run-id") {
			t.Fatalf("cleanup accepted a non-owned or malformed container marker %q", value)
		}
	}
}

func s08CoreDocker(ctx context.Context, endpoint, configDir string, args ...string) (string, error) {
	commandArgs := []string{"--host", endpoint, "--config", configDir}
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(ctx, "docker", commandArgs...)
	command.Env = s08CoreWithoutEnv(os.Environ(), "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New("Docker CLI operation on the explicitly selected daemon failed")
	}
	return string(output), nil
}

func s08CoreWithoutEnv(environment []string, names ...string) []string {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	filtered := make([]string, 0, len(environment))
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if _, skip := blocked[name]; !skip {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func s08WaitPrivateCoreRegistry(ctx context.Context, base, username, password string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		status, err := s08CoreRegistryStatus(ctx, client, base, username, password)
		if err == nil && status == http.StatusOK {
			anonymousStatus, anonymousErr := s08CoreRegistryStatus(ctx, client, base, "", "")
			if anonymousErr == nil && anonymousStatus == http.StatusUnauthorized {
				return nil
			}
			return errors.New("private Registry did not reject anonymous requests")
		}
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for the run-owned Registry")
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("run-owned private Registry did not become ready")
}

func s08CoreRegistryStatus(ctx context.Context, httpClient *http.Client, base, username, password string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	if err != nil {
		return 0, err
	}
	if username != "" {
		request.SetBasicAuth(username, password)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func s08CoreRegistryManifestDigest(ctx context.Context, base, repositoryAndTag, username, password string) (string, error) {
	manifestURL := base + "/v2/" + repositoryAndTag
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return "", errors.New("could not construct Registry manifest check")
	}
	request.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	request.SetBasicAuth(username, password)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return "", errors.New("could not read private Registry manifest digest")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("private Registry manifest lookup returned HTTP %d", response.StatusCode)
	}
	digest := response.Header.Get("Docker-Content-Digest")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
		return "", errors.New("private Registry returned an invalid manifest digest")
	}
	return digest, nil
}

func s08CorePollTask(client *http.Client, url, origin, session, csrf string, destination any, terminal func() bool,
	timeout time.Duration, canaries ...string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return errors.New("could not construct Core task status request")
		}
		request.Header.Set("Origin", origin)
		request.Header.Set(csrfHeaderName, csrf)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		response, err := client.Do(request)
		if err != nil {
			return errors.New("Core task status request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			return fmt.Errorf("Core task status returned HTTP %d", response.StatusCode)
		}
		if s08CoreContainsCanary(body, canaries...) {
			return errors.New("Core task response contained a Registry credential canary")
		}
		if err := json.Unmarshal(body, destination); err != nil {
			return errors.New("Core task response was not valid JSON")
		}
		if terminal() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("Core task did not reach a terminal state before the deadline")
}

func s08CoreReadAPIWithoutSecrets(client *http.Client, base, origin, session, csrf, taskPath string, username, password string) error {
	for _, suffix := range []string{"", "/audit"} {
		request, err := http.NewRequest(http.MethodGet, base+taskPath+suffix, nil)
		if err != nil {
			return errors.New("could not construct task or audit lookup")
		}
		request.Header.Set("Origin", origin)
		request.Header.Set(csrfHeaderName, csrf)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrf})
		response, err := client.Do(request)
		if err != nil {
			return errors.New("task or audit API request failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil || response.StatusCode != http.StatusOK {
			return fmt.Errorf("task or audit API returned HTTP %d", response.StatusCode)
		}
		if s08CoreContainsCanary(body, username, password) {
			return errors.New("task or audit API response contained a Registry credential canary")
		}
	}
	return nil
}

func s08CoreScanFilesForCanaries(roots []string, canaries ...string) error {
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("could not safely inspect Core or Agent persistence file")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return errors.New("could not read Core or Agent persistence file")
			}
			if s08CoreContainsCanary(data, canaries...) {
				return errors.New("Registry credential canary found in Core or Agent persistent files")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func s08CoreContainsCanary(data []byte, canaries ...string) bool {
	for _, canary := range canaries {
		if canary != "" && bytes.Contains(data, []byte(canary)) {
			return true
		}
	}
	return false
}

func s08CoreSafeTail(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

func s08CoreContainsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type s08CoreRegistryLogScan struct {
	usernameAuthFieldMatches      int
	otherUsernameMatch            bool
	passwordMatch                 bool
	authorizationHeaderValueMatch bool
	usernameOccurrences           map[string]s08CoreRegistryFieldOccurrence
}

type s08CoreRegistryFieldOccurrence struct {
	count             int
	valueExactCount   int
	allowedExactCount int
}

type s08CoreRegistryValueOccurrence struct {
	field          string
	value          string
	allowExactAuth bool
}

func s08CoreScanRegistryLogs(logs []byte, username, password string) s08CoreRegistryLogScan {
	scan := s08CoreRegistryLogScan{
		passwordMatch:       bytes.Contains(logs, []byte(password)),
		usernameOccurrences: make(map[string]s08CoreRegistryFieldOccurrence),
	}
	basicAuthorizationValue := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	scan.authorizationHeaderValueMatch = bytes.Contains(logs, []byte(basicAuthorizationValue))
	for _, line := range bytes.Split(logs, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if occurrences, ok := s08CoreParseJSONRegistryLogLine(line, username); ok {
			for _, occurrence := range occurrences {
				s08CoreRecordRegistryUsernameOccurrence(&scan, occurrence, username, password, basicAuthorizationValue)
			}
			continue
		}
		fields, ok := s08CoreParseLogfmtRegistryLogLine(line)
		if !ok {
			if bytes.Contains(line, []byte(username)) {
				s08CoreRecordRegistryUsernameOccurrence(&scan, s08CoreRegistryValueOccurrence{field: "<unparsed>", value: string(line)}, username, password, basicAuthorizationValue)
			}
			continue
		}
		for _, field := range fields {
			if strings.Contains(field.field, username) {
				s08CoreRecordRegistryUsernameOccurrence(&scan, s08CoreRegistryValueOccurrence{field: "<field-name>", value: field.field}, username, password, basicAuthorizationValue)
			}
			allowExactAuth := field.field == "auth.user.name"
			s08CoreRecordRegistryUsernameOccurrence(&scan, s08CoreRegistryValueOccurrence{
				field: field.field, value: field.value, allowExactAuth: allowExactAuth,
			}, username, password, basicAuthorizationValue)
		}
	}
	return scan
}

func s08CoreRecordRegistryUsernameOccurrence(scan *s08CoreRegistryLogScan, occurrence s08CoreRegistryValueOccurrence, username, password, basicAuthorizationValue string) {
	count := strings.Count(occurrence.value, username)
	if count == 0 {
		return
	}
	field := s08CoreSafeRegistryFieldName(occurrence.field, username, password, basicAuthorizationValue)
	current := scan.usernameOccurrences[field]
	current.count += count
	valueExact := occurrence.value == username
	if valueExact {
		current.valueExactCount++
	}
	allowedExact := occurrence.allowExactAuth && occurrence.field == "auth.user.name" && valueExact
	if allowedExact {
		current.allowedExactCount++
		scan.usernameAuthFieldMatches++
	} else {
		scan.otherUsernameMatch = true
	}
	scan.usernameOccurrences[field] = current
}

func s08CoreSafeRegistryFieldName(field, username, password, basicAuthorizationValue string) string {
	if field == "" {
		return "<unparsed>"
	}
	if strings.Contains(field, username) || strings.Contains(field, password) || strings.Contains(field, basicAuthorizationValue) {
		return "<redacted-field>"
	}
	field = regexp.MustCompile(`[^A-Za-z0-9_.<>-]+`).ReplaceAllString(field, "_")
	if len(field) > 96 {
		field = field[:96]
	}
	return field
}

func s08CoreParseJSONRegistryLogLine(line []byte, username string) ([]s08CoreRegistryValueOccurrence, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	var occurrences []s08CoreRegistryValueOccurrence
	if err := s08CoreReadJSONRegistryValue(decoder, "", false, username, &occurrences); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return occurrences, true
}

func s08CoreReadJSONRegistryValue(decoder *json.Decoder, field string, allowExactAuth bool, username string, occurrences *[]s08CoreRegistryValueOccurrence) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if strings.Contains(key, username) {
					*occurrences = append(*occurrences, s08CoreRegistryValueOccurrence{field: "<field-name>", value: key})
				}
				childField := key
				if field != "" {
					childField = field + "." + key
				}
				directAuthValue := field == "" && key == "auth.user.name"
				if err := s08CoreReadJSONRegistryValue(decoder, childField, directAuthValue, username, occurrences); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid JSON object terminator")
			}
		case '[':
			for decoder.More() {
				if err := s08CoreReadJSONRegistryValue(decoder, field+"[]", false, username, occurrences); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array terminator")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
	case string:
		if strings.Contains(value, username) {
			*occurrences = append(*occurrences, s08CoreRegistryValueOccurrence{field: field, value: value, allowExactAuth: allowExactAuth})
		}
	}
	return nil
}

type s08CoreLogfmtRegistryField struct {
	field string
	value string
}

func s08CoreParseLogfmtRegistryLogLine(line []byte) ([]s08CoreLogfmtRegistryField, bool) {
	var fields []s08CoreLogfmtRegistryField
	for index := 0; index < len(line); {
		for index < len(line) && s08CoreLogfmtSpace(line[index]) {
			index++
		}
		if index == len(line) {
			break
		}
		start := index
		for index < len(line) && line[index] != '=' && !s08CoreLogfmtSpace(line[index]) {
			index++
		}
		if index == len(line) || line[index] != '=' {
			for index < len(line) && !s08CoreLogfmtSpace(line[index]) {
				index++
			}
			fields = append(fields, s08CoreLogfmtRegistryField{field: "<unparsed>", value: string(line[start:index])})
			continue
		}
		field := string(line[start:index])
		if field == "" {
			return nil, false
		}
		index++
		if index < len(line) && line[index] == '"' {
			valueStart := index
			index++
			escaped := false
			closed := false
			for index < len(line) {
				current := line[index]
				if escaped {
					escaped = false
					index++
					continue
				}
				if current == '\\' {
					escaped = true
					index++
					continue
				}
				if current == '"' {
					closed = true
					break
				}
				index++
			}
			if !closed {
				return nil, false
			}
			quoted := string(line[valueStart : index+1])
			value, err := strconv.Unquote(quoted)
			if err != nil {
				return nil, false
			}
			index++
			if index < len(line) && !s08CoreLogfmtSpace(line[index]) {
				return nil, false
			}
			fields = append(fields, s08CoreLogfmtRegistryField{field: field, value: value})
			continue
		}
		valueStart := index
		for index < len(line) && !s08CoreLogfmtSpace(line[index]) {
			index++
		}
		fields = append(fields, s08CoreLogfmtRegistryField{field: field, value: string(line[valueStart:index])})
	}
	return fields, true
}

func s08CoreLogfmtSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}

func s08CoreFormatRegistryLogOccurrences(occurrences map[string]s08CoreRegistryFieldOccurrence) string {
	fields := make([]string, 0, len(occurrences))
	for field := range occurrences {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	var summary []string
	for _, field := range fields {
		occurrence := occurrences[field]
		summary = append(summary, fmt.Sprintf("%s{count=%d,value_exact=%d,allowed_exact=%d}", field,
			occurrence.count, occurrence.valueExactCount, occurrence.allowedExactCount))
	}
	return strings.Join(summary, ",")
}

func TestS08RegistryLogScannerAllowsOnlyExternalAuthUsernameField(t *testing.T) {
	username, password := "short-lived-user", "short-lived-password"
	basic := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	for _, test := range []struct {
		name                string
		logs                string
		wantUsernameMatches int
		wantOtherUsername   bool
		wantPassword        bool
		wantHeader          bool
	}{
		{name: "canonical external auth username field", logs: `auth.user.name="` + username + `"`, wantUsernameMatches: 1},
		{name: "canonical external auth username field unquoted", logs: `auth.user.name=` + username, wantUsernameMatches: 1},
		{name: "canonical external auth username JSON field", logs: `{"auth.user.name":"` + username + `"}`, wantUsernameMatches: 1},
		{name: "canonical exact auth field plus another username occurrence", logs: `auth.user.name="` + username + `" message="` + username + `"`, wantUsernameMatches: 1, wantOtherUsername: true},
		{name: "different HTTP request auth field", logs: `http.request.auth.user.name="` + username + `"`, wantOtherUsername: true},
		{name: "username in another field", logs: `message="` + username + `"`, wantOtherUsername: true},
		{name: "username in a different auth field path", logs: `other.auth.user.name="` + username + `"`, wantOtherUsername: true},
		{name: "username field with extra suffix", logs: `auth.user.name="` + username + `-extra"`, wantOtherUsername: true},
		{name: "JSON username field with extra suffix", logs: `{"auth.user.name":"` + username + `-extra"}`, wantOtherUsername: true},
		{name: "nested JSON username field", logs: `{"request":{"auth.user.name":"` + username + `"}}`, wantOtherUsername: true},
		{name: "quoted message containing field-like text", logs: `message="request auth.user.name=\"` + username + `\" failed"`, wantOtherUsername: true},
		{name: "password anywhere", logs: `field="` + password + `"`, wantPassword: true},
		{name: "Basic authorization value", logs: `header="Basic ` + basic + `"`, wantHeader: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := s08CoreScanRegistryLogs([]byte(test.logs), username, password)
			if got.usernameAuthFieldMatches != test.wantUsernameMatches || got.otherUsernameMatch != test.wantOtherUsername ||
				got.passwordMatch != test.wantPassword || got.authorizationHeaderValueMatch != test.wantHeader {
				t.Fatalf("scan classification = (auth_field=%d other_username=%t password=%t authorization=%t occurrences=%s), want (%d,%t,%t,%t)",
					got.usernameAuthFieldMatches, got.otherUsernameMatch, got.passwordMatch, got.authorizationHeaderValueMatch,
					s08CoreFormatRegistryLogOccurrences(got.usernameOccurrences), test.wantUsernameMatches, test.wantOtherUsername, test.wantPassword, test.wantHeader)
			}
		})
	}
}
