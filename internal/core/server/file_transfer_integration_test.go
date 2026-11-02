package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	agentrsync "github.com/CST-Cat/NodeDance/internal/agent/rsync"
	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"github.com/coder/websocket"
)

func TestMain(m *testing.M) {
	if handled, err := agentrsync.Internal(os.Args[1:]); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "__test-file-agent" {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		if err := agentruntime.Run(ctx, os.Args[2], "dev", os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type transferTestLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *transferTestLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(data)
}

func (l *transferTestLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type transferIntegrationFixture struct {
	core           *Server
	admin          accountTestClient
	source, target string
	sourceRoot     string
	targetRoot     string
	targetState    string
	blockDirect    atomic.Bool
	controlBytes   atomic.Int64
	chunkStreams   atomic.Int64
	relayStreams   atomic.Int64
	proxy          *httptest.Server
	logs           transferTestLog
}

func newTransferIntegrationFixture(t *testing.T, rejectRsyncData bool) *transferIntegrationFixture {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated file transfers require root Agents")
	}
	core, newClient := newAccountFixture(t)
	f := &transferIntegrationFixture{core: core, admin: newClient()}
	f.blockDirect.Store(rejectRsyncData)
	f.admin.login("admin", "administrator-password")
	upstream, _ := url.Parse(f.admin.base)
	forward := httputil.NewSingleHostReverseProxy(upstream)
	unreachable, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedEndpoint := "wss://" + unreachable.Addr().String() + protocol.FileRsyncDataRoute
	unreachable.Close()
	f.proxy = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == protocol.FileRsyncDataRoute {
			f.relayStreams.Add(1)
			if rejectRsyncData {
				http.Error(w, "isolated unavailable rsync data endpoint", http.StatusBadGateway)
				return
			}
		} else if r.URL.Path == protocol.FileChunkDataRoute {
			f.chunkStreams.Add(1)
		}
		if r.URL.Path != "/ws/v1/agent" {
			forward.ServeHTTP(w, r)
			return
		}
		a, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer a.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		b, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.admin.base, "http")+r.URL.RequestURI(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {r.Header.Get("Authorization")}}})
		if err != nil {
			return
		}
		defer b.CloseNow()
		a.SetReadLimit(16 << 20)
		b.SetReadLimit(16 << 20)
		done := make(chan struct{}, 2)
		copyFrames := func(from, to *websocket.Conn, fromCore bool) {
			defer func() { done <- struct{}{} }()
			for {
				kind, data, err := from.Read(ctx)
				if err != nil {
					return
				}
				var envelope protocol.Envelope
				if json.Unmarshal(data, &envelope) == nil {
					if envelope.Type == protocol.TypeFileChunk {
						f.controlBytes.Add(1)
					}
					if fromCore && envelope.Type == protocol.TypeTaskDispatch {
						var task map[string]json.RawMessage
						if json.Unmarshal(envelope.Payload, &task) == nil && task["fileContent"] != nil {
							f.controlBytes.Add(1)
						}
					}
					if fromCore && f.blockDirect.Load() && envelope.Type == protocol.TypeFileRsyncRequest {
						var request protocol.FileRsyncRequest
						if json.Unmarshal(envelope.Payload, &request) == nil && request.Operation == "send" && request.Transport == "direct" {
							request.Endpoints = []string{closedEndpoint}
							envelope.Payload, _ = json.Marshal(request)
							data, _ = json.Marshal(envelope)
						}
					}
				}
				if to.Write(ctx, kind, data) != nil {
					return
				}
			}
		}
		go copyFrames(a, b, false)
		go copyFrames(b, a, true)
		<-done
		cancel()
		a.CloseNow()
		b.CloseNow()
		<-done
	}))
	t.Cleanup(f.proxy.Close)
	for index := range 2 {
		state, root := t.TempDir(), t.TempDir()
		if err := os.Chmod(state, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "out"), 0755); err != nil {
			t.Fatal(err)
		}
		ca := filepath.Join(state, "core-ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.proxy.Certificate().Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		enrollment, err := core.agents.CreateEnrollment(context.Background(), "file fixture", "127.0.0.1", sql.NullInt64{})
		if err != nil {
			t.Fatal(err)
		}
		credential, err := agentruntime.NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), fmt.Sprintf("file-fixture-%d", index), "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		config := filepath.Join(state, "agent.json")
		if err := agentruntime.SaveConfig(config, agentruntime.Config{Schema: agentruntime.ConfigSchema, Server: f.proxy.URL, CAFile: ca, Development: true, AgentID: identity.AgentID, NodeID: identity.NodeID, Credential: credential, Shell: "/bin/sh"}, true); err != nil {
			t.Fatal(err)
		}
		settings := protocol.DefaultAgentSettings()
		settings.Transport, settings.EnableGPU, settings.FastIntervalSeconds = "websocket", false, 1
		if err := localstate.Write(filepath.Join(state, "settings.json"), settings); err != nil {
			t.Fatal(err)
		}
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(executable, "__test-file-agent", config)
		cmd.Env = append(os.Environ(), "NODEDANCE_AGENT_FILE_ROOT="+root, "DOCKER_HOST=unix:///nonexistent/nodedance-file-transfer-fixture.sock", "PATH=/nonexistent")
		cmd.Stdout, cmd.Stderr = &f.logs, &f.logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() {
			cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Error("root Agent did not stop")
			}
		})
		deadline := time.Now().Add(15 * time.Second)
		for {
			connection := core.agentConnectionForNode(identity.NodeID)
			if connection != nil {
				_, synced := connection.taskBridgeState()
				if synced && connection.capabilityEnabled(protocol.CapabilityFiles) {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("file Agent did not connect", f.logs.String())
			}
			time.Sleep(20 * time.Millisecond)
		}
		if index == 0 {
			f.source, f.sourceRoot = identity.NodeID, root
		} else {
			f.target, f.targetRoot, f.targetState = identity.NodeID, root, state
		}
	}
	return f
}

func (f *transferIntegrationFixture) request(ctx context.Context, operation string, params map[string]string, body any, key string) (int, []byte, error) {
	op, err := control.Find(operation)
	if err != nil {
		return 0, nil, err
	}
	path, err := op.URLPath(params)
	if err != nil {
		return 0, nil, err
	}
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	csrf := f.admin.call("GET", "/api/v1/auth/csrf", nil, 200)
	var token struct{ Token string }
	json.Unmarshal(csrf, &token)
	req, err := http.NewRequestWithContext(ctx, op.Method, f.admin.base+path, bytes.NewReader(data))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Origin", f.admin.base)
	req.Header.Set("X-CSRF-Token", token.Token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.admin.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(response.Body)
	return response.StatusCode, data, err
}

func requireBundledTransferRsync(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("isolated file transfers require root Agents")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	_, cleanupRsync, err := agentrsync.Available(state)
	if err != nil {
		if strings.Contains(err.Error(), "requires Landlock") {
			t.Skipf("rsync confinement unavailable: %v", err)
		}
		t.Fatal("bundled rsync is unavailable", err)
	}
	cleanupRsync()
}

func TestCoreSchedulesDirectAndFallsBackToIndependentRsyncRelay(t *testing.T) {
	requireBundledTransferRsync(t)
	f := newTransferIntegrationFixture(t, false)
	content := bytes.Repeat([]byte("binary\x00payload\xff"), 16000)
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "payload.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	for _, transport := range []string{"rsync_direct", "rsync_relay"} {
		f.blockDirect.Store(transport == "rsync_relay")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		status, data, err := f.request(ctx, "files.transfer", map[string]string{"nodeId": f.source}, protocol.FileTransferRequest{TargetNodeID: f.target, Paths: []string{"/payload.bin"}, TargetPath: "/out"}, transport)
		cancel()
		var result struct {
			Transferred []fileTransferResult
			Failed      []fileTransferFailure
		}
		if err != nil || status != 200 || json.Unmarshal(data, &result) != nil || len(result.Transferred) != 1 || len(result.Failed) != 0 {
			t.Fatal("scheduled transfer", status, string(data), err, f.logs.String())
		}
		if result.Transferred[0].Transport != transport {
			t.Fatalf("wanted %s, got %+v", transport, result.Transferred[0])
		}
		actual, err := os.ReadFile(filepath.Join(f.targetRoot, "out", "payload.bin"))
		if err != nil || !bytes.Equal(actual, content) {
			t.Fatal("destination differs", err)
		}
	}
	if f.controlBytes.Load() != 0 || f.chunkStreams.Load() != 0 || f.relayStreams.Load() != 2 {
		t.Fatalf("wrong data path: control=%d chunk=%d rsync=%d", f.controlBytes.Load(), f.chunkStreams.Load(), f.relayStreams.Load())
	}
}

func TestCoreChunkFallbackKeepsControlChannelFreeOfFileBytes(t *testing.T) {
	f := newTransferIntegrationFixture(t, true)
	content := bytes.Repeat([]byte("chunk fixture\x00"), 6000)
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "fallback.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, data, err := f.request(ctx, "files.transfer", map[string]string{"nodeId": f.source}, protocol.FileTransferRequest{TargetNodeID: f.target, Paths: []string{"/fallback.bin"}, TargetPath: "/out"}, "chunk-fallback")
	var result struct{ Transferred []fileTransferResult }
	if err != nil || status != 200 || json.Unmarshal(data, &result) != nil || len(result.Transferred) != 1 || result.Transferred[0].Transport != "chunk_relay" {
		t.Fatal(status, string(data), err, f.logs.String())
	}
	actual, err := os.ReadFile(filepath.Join(f.targetRoot, "out", "fallback.bin"))
	if err != nil || !bytes.Equal(actual, content) || f.controlBytes.Load() != 0 || f.chunkStreams.Load() != 2 {
		t.Fatal("invalid fallback path", err, f.controlBytes.Load(), f.chunkStreams.Load())
	}
}

func TestCoreTextSaveUsesIndependentDataConnection(t *testing.T) {
	f := newTransferIntegrationFixture(t, true)
	content := strings.Repeat("verified UTF-8 文本\r\n", 20000)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, data, err := f.request(ctx, "files.action", map[string]string{"nodeId": f.target}, protocol.FileActionRequest{Action: "save_text", Path: "/out/config.txt", Content: content}, "text-data-stream")
	var accepted struct{ TaskID string }
	if err != nil || status != http.StatusAccepted || json.Unmarshal(data, &accepted) != nil || accepted.TaskID == "" {
		t.Fatal(status, string(data), err)
	}
	for {
		task, err := f.core.tasks.Get(ctx, f.target, accepted.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if taskstate.IsTerminal(task.Status) {
			if task.Status != taskstate.Succeeded || task.Result.ObservedState != "saved" {
				t.Fatal(task.Status, task.Result)
			}
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err(), f.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	actual, err := os.ReadFile(filepath.Join(f.targetRoot, "out", "config.txt"))
	if err != nil || string(actual) != content || f.controlBytes.Load() != 0 || f.chunkStreams.Load() != 1 {
		t.Fatal("text body did not use an independent data stream", err, f.controlBytes.Load(), f.chunkStreams.Load())
	}
}

func TestCoreCancelsRetainsPartialAndRetriesTheOriginalTransfer(t *testing.T) {
	requireBundledTransferRsync(t)
	f := newTransferIntegrationFixture(t, false)
	f.blockDirect.Store(true)
	content := make([]byte, 2<<20)
	for index := range content {
		content[index] = byte(index*17 + index/251)
	}
	if err := os.WriteFile(filepath.Join(f.sourceRoot, "resume.bin"), content, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.request(ctx, "files.transfer", map[string]string{"nodeId": f.source}, protocol.FileTransferRequest{TargetNodeID: f.target, Paths: []string{"/resume.bin"}, TargetPath: "/out", BandwidthLimit: 128 << 10}, "retained-partial")
	}()
	var taskID string
	for taskID == "" {
		tasks, err := f.core.tasks.List(ctx, f.target, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range tasks.Tasks {
			name := filepath.Join(f.targetState, "file-transfers", task.TaskID, "payload")
			if info, err := os.Stat(name); err == nil && info.Size() > 0 && info.Size() < int64(len(content)) {
				taskID = task.TaskID
				break
			}
		}
		if ctx.Err() != nil {
			t.Fatal("partial was not produced", f.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	f.core.fileTransferMu.Lock()
	var interrupted *coreRsyncRelay
	for _, relay := range f.core.fileRsyncRelays {
		if relay.descriptor.TaskID == taskID {
			interrupted = relay
		}
	}
	f.core.fileTransferMu.Unlock()
	if interrupted == nil {
		t.Fatal("no active relay for the partial")
	}
	interrupted.mu.Lock()
	interrupted.connections["sender"].CloseNow()
	interrupted.mu.Unlock()
	for f.relayStreams.Load() < 4 {
		if ctx.Err() != nil {
			t.Fatal("data-only disconnect did not reconnect", f.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	if f.core.agentConnectionForNode(f.source) == nil || f.core.agentConnectionForNode(f.target) == nil {
		t.Fatal("data disconnect ended a control connection")
	}
	var headers http.Header
	for headers == nil {
		f.core.fileTransferMu.Lock()
		for _, relay := range f.core.fileRsyncRelays {
			if relay != interrupted && relay.descriptor.TaskID == taskID {
				relay.mu.Lock()
				if relay.ctx.Err() == nil && relay.used["sender"] && relay.used["receiver"] {
					headers = relay.grants["sender"].Headers()
				}
				relay.mu.Unlock()
			}
		}
		f.core.fileTransferMu.Unlock()
		if ctx.Err() != nil {
			t.Fatal("new relay credentials were not consumed", f.logs.String())
		}
		if headers == nil {
			time.Sleep(25 * time.Millisecond)
		}
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", f.proxy.URL+protocol.FileRsyncDataRoute, nil)
	request.Header = headers
	replay, err := f.proxy.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	replay.Body.Close()
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("consumed task credential was accepted: %d", replay.StatusCode)
	}
	status, data, err := f.request(ctx, "tasks.cancel", map[string]string{"nodeId": f.target, "taskId": taskID}, nil, "")
	if err != nil || status != http.StatusAccepted {
		t.Fatal("cancel request", status, string(data), err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		task, err := f.core.tasks.Get(ctx, f.target, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == taskstate.Canceled {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("cancellation was not confirmed", task)
		}
		time.Sleep(25 * time.Millisecond)
	}
	partial, err := os.ReadFile(filepath.Join(f.targetState, "file-transfers", taskID, "payload"))
	if err != nil || len(partial) == 0 || len(partial) >= len(content) {
		t.Fatal("canceled partial was discarded", err)
	}
	if _, err := os.Stat(filepath.Join(f.targetRoot, "out", "resume.bin")); !os.IsNotExist(err) {
		t.Fatal("cancel committed the destination", err)
	}
	status, data, err = f.request(ctx, "files.transfer-retry", map[string]string{"nodeId": f.target, "taskId": taskID}, nil, "fresh-retry-key")
	var result struct {
		Transferred []fileTransferResult
		Failed      []fileTransferFailure
	}
	if err != nil || status != 200 || json.Unmarshal(data, &result) != nil || len(result.Transferred) != 1 {
		t.Fatal("retry", status, string(data), err, f.logs.String())
	}
	actual, err := os.ReadFile(filepath.Join(f.targetRoot, "out", "resume.bin"))
	sum := sha256.Sum256(content)
	if err != nil || !bytes.Equal(actual, content) || result.Transferred[0].SHA256 != hex.EncodeToString(sum[:]) || result.Transferred[0].TaskID == taskID {
		t.Fatal("retry did not verify a new task", err, result)
	}
	if f.controlBytes.Load() != 0 {
		t.Fatal("recovery sent file bytes on the control channel")
	}
}
