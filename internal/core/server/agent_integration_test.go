package server

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

// This integration test uses the real Core HTTP/WSS handler and three real
// Agent runtimes. The local TLS proxy has two controlled faults only: it can
// drop one already-committed enrollment response and one WELCOME frame.
func TestRealAgentEnrollmentReconnectRotationAndRevocation(t *testing.T) {
	// S02's real Agent runtime test must never discover the developer host's
	// business Docker daemon now that Docker discovery is negotiated by default.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s02-test-docker.sock")
	root := filepath.Join("..", "..", "..", ".artifacts", "work-s02")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(root, "real-agent-*")
	if err != nil {
		t.Fatal(err)
	}
	work, err = filepath.Abs(work)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)

	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	dataDir := filepath.Join(work, "core-data")
	core, err := New("integration", Options{DataDir: dataDir, PublicOrigin: "https://panel.test"})
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[Server]
	current.Store(core)
	coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active := current.Load()
		if active == nil {
			http.Error(w, "Core restarting", http.StatusServiceUnavailable)
			return
		}
		active.ServeHTTP(w, r)
	}))
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()
	defer coreHTTP.Close()

	proxy := newAgentTestProxy(t, &current, coreHTTP.URL, rootPEM)
	proxyServer := httptest.NewUnstartedServer(proxy)
	proxyServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	proxyServer.StartTLS()
	defer proxyServer.Close()
	proxy.proxyURL = proxyServer.URL
	defer func() {
		if active := current.Swap(nil); active != nil {
			_ = active.Close()
		}
	}()

	adminClient, err := httpClientForRoots(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	session, csrf, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	admin := func(method, path string, body io.Reader) (*http.Response, error) {
		request, err := http.NewRequest(method, proxyServer.URL+path, body)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Origin", "https://panel.test")
		request.Header.Set("Cookie", sessionCookieName+"="+session+"; "+csrfCookieName+"="+csrf)
		if method != http.MethodGet {
			request.Header.Set(csrfHeaderName, csrf)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		return adminClient.Do(request)
	}
	createEnrollment := func(name string) (agents.Enrollment, error) {
		body := strings.NewReader(fmt.Sprintf(`{"displayName":%q}`, name))
		response, err := admin(http.MethodPost, "/api/v1/agents/enrollments", body)
		if err != nil {
			return agents.Enrollment{}, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			return agents.Enrollment{}, fmt.Errorf("admin enrollment API returned HTTP %d", response.StatusCode)
		}
		var value struct {
			NodeID string `json:"nodeId"`
			Token  string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&value); err != nil {
			return agents.Enrollment{}, errorsForTest("decode enrollment response")
		}
		return agents.Enrollment{NodeID: value.NodeID, Token: value.Token}, nil
	}
	postAgentEnrollment := func(token, requestID, credential string) (int, error) {
		body, err := json.Marshal(map[string]string{"requestId": requestID, "credential": credential})
		if err != nil {
			return 0, err
		}
		request, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/api/v1/agents/enroll", strings.NewReader(string(body)))
		if err != nil {
			return 0, err
		}
		request.Header.Set("Authorization", "Enrollment "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := adminClient.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return response.StatusCode, nil
	}
	runCase := func(name string, body func(*testing.T)) {
		if !t.Run(name, body) {
			t.FailNow()
		}
	}

	// S02-05 verifies real TLS trust and bearer rejection on the Core routes.
	runCase("S02-05", func(t *testing.T) {
		badEnrollment, err := createEnrollment("bad-ca")
		if err != nil {
			t.Fatal(err)
		}
		_, wrongRoot, err := makeAgentTestCertificate()
		if err != nil {
			t.Fatal(err)
		}
		wrongCAPath := filepath.Join(work, "untrusted-ca.pem")
		if err := os.WriteFile(wrongCAPath, wrongRoot, 0o600); err != nil {
			t.Fatal(err)
		}
		badConfigPath := filepath.Join(work, "bad-ca-agent", "agent.json")
		if err := agent.Enroll(context.Background(), proxyServer.URL, wrongCAPath, false, strings.NewReader(badEnrollment.Token), badConfigPath); err == nil {
			t.Fatal("Agent accepted a Core certificate signed by an untrusted CA")
		}
		var consumed sql.NullInt64
		if err := core.store.DB.QueryRow(`SELECT consumed_at FROM agent_enrollments WHERE node_id=?`, badEnrollment.NodeID).Scan(&consumed); err != nil || consumed.Valid {
			t.Fatalf("untrusted TLS request consumed enrollment: consumed=%v err=%v", consumed, err)
		}
		if _, response, err := websocket.Dial(context.Background(), agentWebSocketURLForTest(proxyServer.URL), &websocket.DialOptions{
			HTTPClient: tlsHTTPClient(rootPEM), HTTPHeader: http.Header{"Authorization": []string{"Bearer " + strings.Repeat("f", 64)}},
		}); err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
			t.Fatal("Core did not reject an unknown device credential before WSS upgrade")
		}
	})

	const agentCount = 3
	type runningAgent struct {
		configPath string
		cancel     context.CancelFunc
		done       chan error
		identity   agent.Config
		log        *agentTestLog
	}
	running := make([]runningAgent, agentCount)
	stopAgents := func() {
		for index := range running {
			if running[index].cancel != nil {
				running[index].cancel()
			}
		}
		for index := range running {
			if running[index].done != nil {
				select {
				case <-running[index].done:
				case <-time.After(5 * time.Second):
					t.Errorf("Agent %d did not stop after context cancellation", index+1)
				}
			}
		}
	}
	defer stopAgents()

	runCase("S02-01", func(t *testing.T) {
		for index := range running {
			name := fmt.Sprintf("test-node-%d", index+1)
			enrollment, err := createEnrollment(name)
			if err != nil {
				t.Fatalf("create enrollment %d: %v", index+1, err)
			}
			configPath := filepath.Join(work, fmt.Sprintf("agent-%d", index+1), "agent.json")
			if index == 0 {
				proxy.dropNextEnrollment.Store(true)
			}
			if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
				t.Fatalf("real Agent %d enrollment and device-credential recovery failed: %v", index+1, err)
			}
			if index == 0 {
				if !proxy.droppedEnrollment.Load() {
					t.Fatal("test proxy did not actually drop the committed enrollment response")
				}
				t.Log("S02-SUP-07: Core committed enrollment, the proxy dropped the HTTP response, and Agent recovered with its pre-saved device credential")
			}
			config, err := agent.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			log := &agentTestLog{}
			running[index] = runningAgent{configPath: configPath, identity: config, log: log}
			ctx, cancel := context.WithCancel(context.Background())
			running[index].cancel = cancel
			running[index].done = make(chan error, 1)
			go func(item *runningAgent) { item.done <- agent.Run(ctx, item.configPath, "integration", item.log) }(&running[index])
		}
		for index := range running {
			waitForAgentStatus(t, core, running[index].identity.NodeID, "online", 0)
		}
		identities := make(map[string]bool)
		for index := range running {
			if identities[running[index].identity.AgentID] {
				t.Fatal("different enrollment tokens assigned the same device identity")
			}
			identities[running[index].identity.AgentID] = true
			var deviceCount int
			if err := core.store.DB.QueryRow(`SELECT count(*) FROM agent_devices WHERE id=? AND node_id=?`, running[index].identity.AgentID, running[index].identity.NodeID).Scan(&deviceCount); err != nil || deviceCount != 1 {
				t.Fatalf("Agent identity is not bound to its enrolled node: count=%d err=%v", deviceCount, err)
			}
		}
	})

	runCase("S02-02", func(t *testing.T) {
		duplicate, err := createEnrollment("duplicate-token")
		if err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(work, "duplicate-token-agent", "agent.json")
		if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(duplicate.Token), configPath); err != nil {
			t.Fatalf("first use of enrollment credential failed: %v", err)
		}
		config, err := agent.LoadConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var nodesBefore, devicesBefore int
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodesBefore); err != nil {
			t.Fatal(err)
		}
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM agent_devices`).Scan(&devicesBefore); err != nil {
			t.Fatal(err)
		}
		status, err := postAgentEnrollment(duplicate.Token, config.RequestID, config.Credential)
		if err != nil || status != http.StatusUnauthorized {
			t.Fatalf("consumed enrollment credential replay was not rejected: status=%d err=%v", status, err)
		}
		var nodesAfter, devicesAfter int
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodesAfter); err != nil {
			t.Fatal(err)
		}
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM agent_devices`).Scan(&devicesAfter); err != nil {
			t.Fatal(err)
		}
		if nodesAfter != nodesBefore || devicesAfter != devicesBefore {
			t.Fatalf("duplicate token changed identity counts: nodes %d→%d devices %d→%d", nodesBefore, nodesAfter, devicesBefore, devicesAfter)
		}

		expired, err := createEnrollment("expired-token")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := core.store.DB.Exec(`UPDATE agent_enrollments SET expires_at=? WHERE node_id=?`, time.Now().Unix()-1, expired.NodeID); err != nil {
			t.Fatal(err)
		}
		requestID, err := agent.NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		credential, err := agent.NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		status, err = postAgentEnrollment(expired.Token, requestID, credential)
		if err != nil || status != http.StatusUnauthorized {
			t.Fatalf("expired enrollment credential was not rejected: status=%d err=%v", status, err)
		}
		var deviceCount int
		if err := core.store.DB.QueryRow(`SELECT count(*) FROM agent_devices WHERE node_id=?`, expired.NodeID).Scan(&deviceCount); err != nil || deviceCount != 0 {
			t.Fatalf("expired token created an Agent identity: count=%d err=%v", deviceCount, err)
		}
	})

	runCase("S02-04", func(t *testing.T) {
		// An authenticated device cannot claim another node, even with valid JSON,
		// valid capabilities, and a valid credential.
		wrongIdentityConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[0].identity.Credential)
		wrongHello := makeAgentHello(running[0].identity.AgentID, running[1].identity.NodeID)
		writeProtocolMessage(t, wrongIdentityConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello, Payload: marshalAgentPayload(wrongHello)})
		assertProtocolError(t, wrongIdentityConn)
		_ = wrongIdentityConn.Close(websocket.StatusNormalClosure, "test complete")
	})

	// Unknown but syntactically valid future capabilities are negotiated away,
	// rather than making a same-version Agent incompatible.
	var welcomePayload protocol.Welcome
	runCase("S02-SUP-01", func(t *testing.T) {
		unknownCapabilityConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[0].identity.Credential)
		writeProtocolMessage(t, unknownCapabilityConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(running[0].identity.AgentID, running[0].identity.NodeID))})
		welcome := readProtocolMessage(t, unknownCapabilityConn)
		if welcome.Type != protocol.TypeWelcome {
			t.Fatalf("unknown future capability was rejected: got message type %q", welcome.Type)
		}
		if err := json.Unmarshal(welcome.Payload, &welcomePayload); err != nil {
			t.Fatal("decode negotiated welcome")
		}
		for _, capability := range welcomePayload.Capabilities {
			if capability == "agent.future.v99" {
				t.Fatal("Core echoed an unsupported capability as negotiated")
			}
		}
		_ = unknownCapabilityConn.Close(websocket.StatusNormalClosure, "test complete")
	})
	// The manually opened valid connection advanced the lease generation and
	// displaced Agent 1. Its real runtime reconnects with a higher generation.
	waitForAgentStatus(t, core, running[0].identity.NodeID, "online", welcomePayload.Generation)

	runCase("S02-09", func(t *testing.T) {
		// Malformed JSON, unsupported protocol versions and over-limit frames are
		// exercised on the real Core WSS route and must close without mutating a lease.
		for _, payload := range [][]byte{[]byte("{"), make([]byte, protocol.MaxMessageBytes+1)} {
			conn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[1].identity.Credential)
			writeRawProtocolMessage(t, conn, payload)
			if len(payload) <= protocol.MaxMessageBytes {
				assertProtocolError(t, conn)
			} else {
				readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _, readErr := conn.Read(readCtx)
				cancel()
				if readErr == nil {
					t.Fatal("Core did not close an over-limit Agent frame")
				}
			}
			_ = conn.CloseNow()
		}
		badVersionConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[1].identity.Credential)
		writeProtocolMessage(t, badVersionConn, protocol.Envelope{Version: protocol.CurrentVersion + 1, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(running[1].identity.AgentID, running[1].identity.NodeID))})
		assertProtocolError(t, badVersionConn)
		_ = badVersionConn.CloseNow()
	})

	runCase("S02-10", func(t *testing.T) {
		// Establish two authenticated administrator browser streams, close both,
		// then check a later Agent heartbeat sequence persisted independently.
		browserURL := strings.Replace(proxyServer.URL, "https://", "wss://", 1) + "/ws/v1/dashboard"
		browserHeader := http.Header{"Origin": []string{"https://panel.test"},
			"Cookie": []string{sessionCookieName + "=" + session}}
		browsers := make([]*websocket.Conn, 2)
		for index := range browsers {
			conn, response, err := websocket.Dial(context.Background(), browserURL, &websocket.DialOptions{
				HTTPClient: tlsHTTPClient(rootPEM), HTTPHeader: browserHeader.Clone(),
			})
			if err != nil {
				status := ""
				if response != nil {
					status = fmt.Sprintf("HTTP %d", response.StatusCode)
				}
				t.Fatalf("authenticated administrator dashboard WebSocket %d failed (%s)", index+1, status)
			}
			browsers[index] = conn
		}
		if got := proxy.dashboardOpened.Load(); got < 2 {
			t.Fatalf("test did not establish both browser connections: observed %d", got)
		}
		generation := generationForNode(t, core, running[2].identity.NodeID)
		before := heartbeatSequenceForNode(t, core, running[2].identity.NodeID)
		for _, conn := range browsers {
			if err := conn.Close(websocket.StatusNormalClosure, "browser closed"); err != nil {
				t.Errorf("close administrator browser WebSocket: %v", err)
			}
		}
		closeDeadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(closeDeadline) && proxy.dashboardClosed.Load() < proxy.dashboardOpened.Load() {
			time.Sleep(25 * time.Millisecond)
		}
		if proxy.dashboardClosed.Load() < proxy.dashboardOpened.Load() {
			t.Fatalf("browser connections did not all close: opened=%d closed=%d", proxy.dashboardOpened.Load(), proxy.dashboardClosed.Load())
		}
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) && heartbeatSequenceForNode(t, core, running[2].identity.NodeID) <= before {
			time.Sleep(50 * time.Millisecond)
		}
		if got := heartbeatSequenceForNode(t, core, running[2].identity.NodeID); got <= before {
			t.Fatalf("Agent heartbeat sequence did not advance after all browser connections closed: before=%d after=%d", before, got)
		}
		if got := generationForNode(t, core, running[2].identity.NodeID); got != generation {
			t.Fatalf("Agent connection changed unexpectedly while browsers closed: generation %d -> %d", generation, got)
		}
	})

	runCase("S02-11", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("process listener ownership inspection requires Linux /proc")
		}
		enrollment, err := createEnrollment("compiled-cli-listener-test")
		if err != nil {
			t.Fatal(err)
		}
		configPath := filepath.Join(work, "compiled-agent", "agent.json")
		if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
			t.Fatalf("compiled CLI Agent enrollment failed: %v", err)
		}
		repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
		if err != nil {
			t.Fatal(err)
		}
		binaryPath := filepath.Join(work, "nodedance-agent-s02")
		build := exec.Command("go", "build", "-trimpath", "-o", binaryPath, "./cmd/nodedance-agent")
		build.Dir = repositoryRoot
		build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("compile real Agent CLI failed: %v: %s", err, strings.TrimSpace(string(output)))
		}
		command := exec.Command(binaryPath, "run", "--config", configPath)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Start(); err != nil {
			t.Fatal("start compiled Agent CLI")
		}
		processDone := make(chan error, 1)
		go func() { processDone <- command.Wait() }()
		defer func() {
			if command.Process != nil {
				_ = command.Process.Signal(os.Interrupt)
			}
			select {
			case <-processDone:
			case <-time.After(3 * time.Second):
				if command.Process != nil {
					_ = command.Process.Kill()
				}
				<-processDone
			}
		}()
		waitForAgentStatus(t, core, enrollment.NodeID, "online", 0)
		listeners, err := processListenerSockets(command.Process.Pid)
		if err != nil {
			t.Fatalf("inspect compiled Agent PID %d sockets: %v", command.Process.Pid, err)
		}
		if len(listeners) != 0 {
			t.Fatalf("compiled Agent PID %d opened inbound sockets: %v", command.Process.Pid, listeners)
		}
		t.Logf("compiled Agent PID=%d established outbound WSS; /proc socket inode mapping found no inbound TCP listeners or bound UDP sockets", command.Process.Pid)
	})

	runCase("S02-03", func(t *testing.T) {
		// Restart Agent 2 from the same secure config: identity remains stable and
		// a new durable generation supersedes the prior one.
		oldGeneration := generationForNode(t, core, running[1].identity.NodeID)
		running[1].cancel()
		select {
		case <-running[1].done:
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop before restart test")
		}
		ctx2, cancel2 := context.WithCancel(context.Background())
		running[1].cancel = cancel2
		running[1].done = make(chan error, 1)
		go func() { running[1].done <- agent.Run(ctx2, running[1].configPath, "integration", running[1].log) }()
		waitForAgentStatus(t, core, running[1].identity.NodeID, "online", oldGeneration)
		configAfterRestart, err := agent.LoadConfig(running[1].configPath)
		if err != nil || configAfterRestart.AgentID != running[1].identity.AgentID || configAfterRestart.NodeID != running[1].identity.NodeID {
			t.Fatalf("Agent restart changed identity: err=%v", err)
		}
	})

	var restarted *Server
	// S02-07 injects an Agent-network outage, observes an actual bounded-backoff
	// retry and recovery, then restarts Core and checks durable new generations.
	runCase("S02-07", func(t *testing.T) {
		priorAttempts := proxy.connectionAttempts.Load()
		proxy.networkDown.Store(true)
		proxy.closeAgentTunnels()
		outageDeadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(outageDeadline) && proxy.connectionAttempts.Load() == priorAttempts {
			time.Sleep(25 * time.Millisecond)
		}
		if proxy.connectionAttempts.Load() == priorAttempts {
			t.Fatal("proxy observed no Agent retry during the injected outage")
		}
		logDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(logDeadline) && !strings.Contains(running[0].log.String(), "retrying in") {
			time.Sleep(25 * time.Millisecond)
		}
		if !strings.Contains(running[0].log.String(), "retrying in") {
			t.Fatal("Agent did not report a bounded reconnect delay after the outage")
		}
		proxy.networkDown.Store(false)
		networkGeneration := generationForNode(t, core, running[0].identity.NodeID)
		waitForAgentStatus(t, core, running[0].identity.NodeID, "online", networkGeneration)

		oldGenerations := make([]uint64, agentCount)
		for index := range running {
			oldGenerations[index] = generationForNode(t, core, running[index].identity.NodeID)
		}
		waitForAgentProxyTunnelCount(t, proxy, agentCount, 3*time.Second, "all three Agent WSS tunnels before Core restart")
		current.Store(nil)
		closeS02CoreWithin(t, core, proxy, 5*time.Second)
		restarted, err = New("integration-restarted", Options{DataDir: dataDir, PublicOrigin: "https://panel.test"})
		if err != nil {
			t.Fatal(err)
		}
		core = restarted
		current.Store(restarted)
		for index := range running {
			waitForAgentStatus(t, restarted, running[index].identity.NodeID, "online", oldGenerations[index])
		}
	})

	runCase("S02-08", func(t *testing.T) {
		// Submit a prior-generation frame on the real Core route after a newer
		// connection has become active. The lease and heartbeat sequence must not
		// be changed by the stale message.
		running[0].cancel()
		select {
		case <-running[0].done:
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop before stale-generation test")
		}
		oldConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[0].identity.Credential)
		writeProtocolMessage(t, oldConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(running[0].identity.AgentID, running[0].identity.NodeID))})
		oldWelcomeEnvelope := readProtocolMessage(t, oldConn)
		if oldWelcomeEnvelope.Type != protocol.TypeWelcome {
			t.Fatalf("prior Agent connection did not become active: %q", oldWelcomeEnvelope.Type)
		}
		var oldWelcome protocol.Welcome
		if err := json.Unmarshal(oldWelcomeEnvelope.Payload, &oldWelcome); err != nil {
			t.Fatal("decode prior generation welcome")
		}
		newConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[0].identity.Credential)
		writeProtocolMessage(t, newConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(running[0].identity.AgentID, running[0].identity.NodeID))})
		newWelcomeEnvelope := readProtocolMessage(t, newConn)
		if newWelcomeEnvelope.Type != protocol.TypeWelcome {
			t.Fatalf("reconnected Agent was not welcomed: %q", newWelcomeEnvelope.Type)
		}
		var newWelcome protocol.Welcome
		if err := json.Unmarshal(newWelcomeEnvelope.Payload, &newWelcome); err != nil || newWelcome.Generation <= oldWelcome.Generation {
			t.Fatalf("reconnection did not advance generation: old=%d new=%d err=%v", oldWelcome.Generation, newWelcome.Generation, err)
		}
		writeProtocolMessage(t, newConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
			Generation: oldWelcome.Generation, Sequence: 1, Payload: marshalAgentPayload(protocol.Heartbeat{})})
		readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _, staleErr := newConn.Read(readCtx)
		cancel()
		if staleErr == nil {
			t.Fatal("Core accepted a stale-generation frame on the new connection")
		}
		if got := generationForNode(t, core, running[0].identity.NodeID); got != newWelcome.Generation {
			t.Fatalf("stale frame changed active generation: got %d want %d", got, newWelcome.Generation)
		}
		if got := heartbeatSequenceForNode(t, core, running[0].identity.NodeID); got != 0 {
			t.Fatalf("stale frame changed heartbeat sequence: got %d want 0", got)
		}
		_ = oldConn.CloseNow()
		_ = newConn.CloseNow()
		ctx, cancelRun := context.WithCancel(context.Background())
		running[0].cancel = cancelRun
		running[0].done = make(chan error, 1)
		go func() { running[0].done <- agent.Run(ctx, running[0].configPath, "integration", running[0].log) }()
		waitForAgentStatus(t, core, running[0].identity.NodeID, "online", newWelcome.Generation)
	})

	var oldAgentTwoCredential string
	runCase("S02-SUP-02", func(t *testing.T) {
		// Recovery with a merely pending verifier must preserve both the old active
		// credential and local pending credential. Hold Agent 2 before Core commits.
		oldAgentTwoCredential = running[1].identity.Credential
		proxy.holdNextHello.Store(true)
		pendingRotationResponse, err := admin(http.MethodPost, "/api/v1/agents/"+running[1].identity.AgentID+"/rotate", nil)
		if err != nil {
			t.Fatal(err)
		}
		if pendingRotationResponse.StatusCode != http.StatusAccepted {
			_ = pendingRotationResponse.Body.Close()
			t.Fatalf("pending-only rotation request returned HTTP %d", pendingRotationResponse.StatusCode)
		}
		_ = pendingRotationResponse.Body.Close()
		select {
		case <-proxy.helloHeld:
		case <-time.After(10 * time.Second):
			t.Fatal("Agent did not reach the pending-credential hello boundary")
		}
		running[1].cancel()
		select {
		case <-running[1].done:
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop at the pending-credential hello boundary")
		}
		proxy.releaseHello <- struct{}{}
		if err := agent.Recover(context.Background(), running[1].configPath); err != nil {
			t.Fatalf("pending-only credential recovery failed: %v", err)
		}
		pendingOnlyConfig, err := agent.LoadConfig(running[1].configPath)
		if err != nil || pendingOnlyConfig.Credential != oldAgentTwoCredential || pendingOnlyConfig.PendingCredential == "" {
			t.Fatalf("pending-only recovery discarded a required credential: err=%v", err)
		}
		agentTwoGeneration := generationForNode(t, restarted, running[1].identity.NodeID)
		agentTwoCtx, agentTwoCancel := context.WithCancel(context.Background())
		running[1].cancel = agentTwoCancel
		running[1].done = make(chan error, 1)
		go func() {
			running[1].done <- agent.Run(agentTwoCtx, running[1].configPath, "integration", running[1].log)
		}()
		waitForAgentStatus(t, restarted, running[1].identity.NodeID, "online", agentTwoGeneration)
		waitForConfig(t, running[1].configPath, func(value agent.Config) bool {
			return value.PendingCredential == "" && value.Credential != oldAgentTwoCredential
		}, 10*time.Second)
	})

	runCase("S02-SUP-03", func(t *testing.T) {
		// Core commits pending during the first rotation but its WELCOME is lost.
		// Restart normal Agent Run while identity recovery is held, then release it;
		// startup must promote only the credential Core reports as active.
		generationBeforeRotation := generationForNode(t, restarted, running[0].identity.NodeID)
		proxy.dropCommittedRotationWelcome(running[0].identity.AgentID, running[0].identity.NodeID, generationBeforeRotation)
		rotateResponse, err := admin(http.MethodPost, "/api/v1/agents/"+running[0].identity.AgentID+"/rotate", nil)
		if err != nil {
			t.Fatal(err)
		}
		if rotateResponse.StatusCode != http.StatusAccepted {
			_ = rotateResponse.Body.Close()
			t.Fatalf("first rotation request returned HTTP %d", rotateResponse.StatusCode)
		}
		_ = rotateResponse.Body.Close()
		select {
		case dropped := <-proxy.droppedWelcome:
			if dropped.AgentID != running[0].identity.AgentID || dropped.NodeID != running[0].identity.NodeID ||
				dropped.Generation <= generationBeforeRotation || !dropped.CredentialRotationCommitted {
				t.Fatalf("proxy dropped an unrelated WELCOME: agent=%s node=%s generation=%d committed=%t",
					dropped.AgentID, dropped.NodeID, dropped.Generation, dropped.CredentialRotationCommitted)
			}
			t.Logf("injected lost committed-rotation WELCOME agent=%s node=%s generation=%d committed=%t",
				dropped.AgentID, dropped.NodeID, dropped.Generation, dropped.CredentialRotationCommitted)
		case <-time.After(10 * time.Second):
			t.Fatal("Core did not commit the targeted Agent rotation and emit its WELCOME")
		}
		select {
		case <-proxy.identityHeld:
		case <-time.After(5 * time.Second):
			t.Fatal("Agent did not attempt identity recovery after lost WELCOME")
		}
		running[0].cancel()
		select {
		case <-running[0].done:
			running[0].done = nil
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop while recovery request was held")
		}
		configBeforeRecovery, err := agent.LoadConfig(running[0].configPath)
		if err != nil || configBeforeRecovery.PendingCredential == "" || configBeforeRecovery.Credential != running[0].identity.Credential {
			t.Fatalf("lost-WELCOME setup did not retain old+pending credentials: err=%v", err)
		}
		pendingIdentity, pendingIdentityErr := restarted.agents.IdentityByCredential(context.Background(), auth.DigestToken(configBeforeRecovery.PendingCredential))
		corePendingCredentialActive := pendingIdentityErr == nil && !pendingIdentity.CredentialIsPending
		if !corePendingCredentialActive {
			t.Fatal("Core did not report the pending credential as active after the lost WELCOME")
		}
		oldGenerationAfterRecovery := generationForNode(t, restarted, running[0].identity.NodeID)
		close(proxy.releaseIdentity)
		restartedAgentCtx, restartedAgentCancel := context.WithCancel(context.Background())
		running[0].cancel = restartedAgentCancel
		running[0].done = make(chan error, 1)
		go func() {
			running[0].done <- agent.Run(restartedAgentCtx, running[0].configPath, "integration", running[0].log)
		}()
		waitForConfig(t, running[0].configPath, func(value agent.Config) bool {
			return value.PendingCredential == "" && value.Credential == configBeforeRecovery.PendingCredential
		}, 10*time.Second)
		waitForAgentStatus(t, restarted, running[0].identity.NodeID, "online", oldGenerationAfterRecovery)
		firstRotation, err := agent.LoadConfig(running[0].configPath)
		if err != nil || firstRotation.PendingCredential != "" || firstRotation.Credential == running[0].identity.Credential {
			t.Fatalf("automatic lost-WELCOME recovery failed: err=%v oldRetained=%t pendingRetained=%t", err,
				firstRotation.Credential == running[0].identity.Credential, firstRotation.PendingCredential != "")
		}

		// A second actual rotation proves the automatic restart left no stale ID.
		secondRotationResponse, err := admin(http.MethodPost, "/api/v1/agents/"+running[0].identity.AgentID+"/rotate", nil)
		if err != nil {
			t.Fatal(err)
		}
		if secondRotationResponse.StatusCode != http.StatusAccepted {
			_ = secondRotationResponse.Body.Close()
			t.Fatalf("second rotation request returned HTTP %d", secondRotationResponse.StatusCode)
		}
		_ = secondRotationResponse.Body.Close()
		waitForConfig(t, running[0].configPath, func(value agent.Config) bool {
			return value.PendingCredential == "" && value.Credential != firstRotation.Credential
		}, 10*time.Second)
	})

	runCase("S02-SUP-09", func(t *testing.T) {
		// An ordinary WebSocket close keeps its lease watcher until another
		// authenticated generation arrives. The replacement must cancel and join
		// the old watcher instead of accumulating one timer goroutine per reconnect.
		agentID := running[0].identity.AgentID
		nodeID := running[0].identity.NodeID
		running[0].cancel()
		select {
		case <-running[0].done:
			running[0].done = nil
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop before lease-watcher reconnect test")
		}
		waitForAgentConnectionDetached(t, restarted, agentID, 3*time.Second)
		restarted.agentConnectionsMu.Lock()
		oldWatcher := restarted.agentLeaseWatchers[agentID]
		restarted.agentConnectionsMu.Unlock()
		if oldWatcher == nil || oldWatcher.watchDone == nil {
			t.Fatal("ordinary disconnect did not retain the active lease watcher")
		}
		oldGeneration := oldWatcher.generation
		oldDone := oldWatcher.watchDone
		select {
		case <-oldDone:
			t.Fatal("ordinary disconnect stopped its lease watcher before the lease expired")
		default:
		}

		config, err := agent.LoadConfig(running[0].configPath)
		if err != nil || config.AgentID != agentID || config.NodeID != nodeID {
			t.Fatalf("load current device identity before reconnect: err=%v", err)
		}
		conn := dialAgentSocket(t, proxyServer.URL, rootPEM, config.Credential)
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(config.AgentID, config.NodeID))})
		welcomeEnvelope := readProtocolMessage(t, conn)
		if welcomeEnvelope.Type != protocol.TypeWelcome {
			_ = conn.CloseNow()
			t.Fatalf("new Agent generation did not receive WELCOME: %q", welcomeEnvelope.Type)
		}
		var welcome protocol.Welcome
		if err := json.Unmarshal(welcomeEnvelope.Payload, &welcome); err != nil || welcome.Generation <= oldGeneration {
			_ = conn.CloseNow()
			t.Fatalf("new reconnect generation did not advance: old=%d new=%d err=%v", oldGeneration, welcome.Generation, err)
		}
		waitForLeaseWatcherGeneration(t, restarted, agentID, welcome.Generation, 2*time.Second)
		select {
		case <-oldDone:
		case <-time.After(2 * time.Second):
			_ = conn.CloseNow()
			t.Fatal("superseded Agent lease watcher did not exit promptly")
		}
		if count := leaseWatcherCount(restarted, agentID); count != 1 {
			_ = conn.CloseNow()
			t.Fatalf("reconnection accumulated %d lease watchers for one Agent; want exactly one", count)
		}
		_ = conn.CloseNow()
		waitForAgentConnectionDetached(t, restarted, agentID, 3*time.Second)

		ctx, cancel := context.WithCancel(context.Background())
		running[0].cancel = cancel
		running[0].done = make(chan error, 1)
		go func() { running[0].done <- agent.Run(ctx, running[0].configPath, "integration", running[0].log) }()
		waitForAgentStatus(t, restarted, nodeID, "online", welcome.Generation)
		latestGeneration := generationForNode(t, restarted, nodeID)
		waitForLeaseWatcherGeneration(t, restarted, agentID, latestGeneration, 2*time.Second)
		if count := leaseWatcherCount(restarted, agentID); count != 1 {
			t.Fatalf("second reconnect accumulated %d lease watchers for one Agent; want exactly one", count)
		}
		t.Logf("rapid disconnect/reconnect advanced generation %d→%d; each replacement joined its predecessor and retained exactly one watcher for Agent %s",
			oldGeneration, latestGeneration, agentID)
	})

	runCase("S02-SUP-06", func(t *testing.T) {
		if protocol.HeartbeatIntervalSeconds != 5 || protocol.OfflineAfterSeconds != 30 || core.agentOfflineTimeout != 30*time.Second {
			t.Fatalf("production lease defaults changed: heartbeat=%ds protocol-offline=%ds Core-offline=%s",
				protocol.HeartbeatIntervalSeconds, protocol.OfflineAfterSeconds, core.agentOfflineTimeout)
		}
		nodeID := running[0].identity.NodeID
		first := nodeLeaseSnapshot(t, restarted, nodeID)
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) && nodeLeaseSnapshot(t, restarted, nodeID).Sequence <= first.Sequence {
			time.Sleep(25 * time.Millisecond)
		}
		first = nodeLeaseSnapshot(t, restarted, nodeID)
		deadline = time.Now().Add(12 * time.Second)
		second := first
		for time.Now().Before(deadline) && second.Sequence <= first.Sequence {
			time.Sleep(25 * time.Millisecond)
			second = nodeLeaseSnapshot(t, restarted, nodeID)
		}
		if second.Sequence <= first.Sequence {
			t.Fatal("production Agent did not record two consecutive real heartbeat samples")
		}
		interval := time.Duration(second.LastSeen - first.LastSeen)
		if interval < 4*time.Second || interval > 7*time.Second {
			t.Fatalf("Agent heartbeat interval is outside the 5-second production schedule: %s", interval)
		}

		faultAt := time.Now().UTC()
		proxy.networkDown.Store(true)
		proxy.closeAgentTunnels()
		tunnelDeadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(tunnelDeadline) && proxy.activeTunnelCount() != 0 {
			time.Sleep(10 * time.Millisecond)
		}
		if remaining := proxy.activeTunnelCount(); remaining != 0 {
			t.Fatalf("network fault did not close every Agent tunnel: %d remain", remaining)
		}
		lastValid := nodeLeaseSnapshot(t, restarted, nodeID)
		lastValidAt := time.Unix(0, lastValid.LastSeen).UTC()
		if lastValid.Sequence == 0 || lastValidAt.After(time.Now().Add(100*time.Millisecond)) {
			t.Fatalf("last valid heartbeat sample is invalid: sequence=%d lastSeen=%s", lastValid.Sequence, lastValidAt.Format(time.RFC3339Nano))
		}
		if faultAt.Sub(lastValidAt) > 6*time.Second {
			t.Fatalf("fault injection did not follow a fresh production heartbeat: fault=%s lastSeen=%s", faultAt.Format(time.RFC3339Nano), lastValidAt.Format(time.RFC3339Nano))
		}
		oldGeneration := lastValid.Generation
		leaseValidUntil := lastValidAt.Add(30 * time.Second)
		if wait := time.Until(leaseValidUntil); wait > 0 {
			time.Sleep(wait)
		}
		observationDeadline := time.Now().Add(3 * time.Second)
		var apiNodeStatus string
		var apiServerTime, apiLeaseValidUntil time.Time
		for time.Now().Before(observationDeadline) {
			response, err := admin(http.MethodGet, "/api/v1/agents", nil)
			if err != nil {
				t.Fatalf("query authenticated Agent list at lease expiry: %v", err)
			}
			var list testAgentListResponse
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&list)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || decodeErr != nil || len(list.Nodes) < 1 {
				t.Fatalf("authenticated Agent list failed at lease expiry: status=%d nodes=%d decode=%v", response.StatusCode, len(list.Nodes), decodeErr)
			}
			apiNodeStatus = ""
			for _, listed := range list.Nodes {
				if listed.NodeID == nodeID {
					apiNodeStatus = listed.Status
					apiLeaseValidUntil, err = time.Parse(time.RFC3339Nano, listed.LeaseValidUntil)
					if err != nil {
						t.Fatalf("Core returned invalid leaseValidUntil %q: %v", listed.LeaseValidUntil, err)
					}
					break
				}
			}
			apiServerTime, err = time.Parse(time.RFC3339Nano, list.ServerTime)
			if err != nil || apiNodeStatus == "" {
				t.Fatalf("Core omitted the target or serverTime: nodeStatus=%q serverTime=%q err=%v", apiNodeStatus, list.ServerTime, err)
			}
			if !apiServerTime.Before(leaseValidUntil) {
				if apiNodeStatus != "offline" {
					t.Fatalf("Core returned expired lease as online: serverTime=%s validUntil=%s status=%s", apiServerTime, leaseValidUntil, apiNodeStatus)
				}
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if apiNodeStatus != "offline" || apiServerTime.Before(leaseValidUntil) || !apiLeaseValidUntil.Equal(leaseValidUntil) {
			t.Fatalf("real Core API did not report the expired lease: status=%s serverTime=%s validUntil=%s want=%s",
				apiNodeStatus, apiServerTime, apiLeaseValidUntil, leaseValidUntil)
		}

		materializeDeadline := leaseValidUntil.Add(2 * time.Second)
		var offline testNodeLeaseSnapshot
		for time.Now().Before(materializeDeadline) {
			offline = nodeLeaseSnapshot(t, restarted, nodeID)
			if offline.Status == "offline" && offline.Generation > oldGeneration {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if offline.Status != "offline" || offline.Generation <= oldGeneration {
			t.Fatalf("background Core lease materialization did not complete: status=%s generation=%d original=%d", offline.Status, offline.Generation, oldGeneration)
		}
		materializedAt := time.Unix(0, offline.UpdatedAt).UTC()
		materializationDelay := materializedAt.Sub(leaseValidUntil)
		if materializationDelay < 0 {
			t.Fatalf("persisted offline state predates the valid lease deadline: materialized=%s validUntil=%s", materializedAt, leaseValidUntil)
		}
		t.Logf("production timing: heartbeat interval=%s; outage=%s; last valid sequence=%d at %s; effective API status=%s at serverTime=%s validUntil=%s; persisted offline materialized=%s (asynchronous delay=%s)",
			interval, faultAt.Format(time.RFC3339Nano), offline.Sequence, lastValidAt.Format(time.RFC3339Nano),
			apiNodeStatus, apiServerTime.Format(time.RFC3339Nano), apiLeaseValidUntil.Format(time.RFC3339Nano),
			materializedAt.Format(time.RFC3339Nano), materializationDelay)
		proxy.networkDown.Store(false)
		waitForAgentStatusWithin(t, restarted, nodeID, "online", offline.Generation, 35*time.Second)
		for index := 1; index < len(running); index++ {
			generation := generationForNode(t, restarted, running[index].identity.NodeID)
			waitForAgentStatusWithin(t, restarted, running[index].identity.NodeID, "online", generation-1, 35*time.Second)
		}
	})

	runCase("S02-06", func(t *testing.T) {
		// Keep an actual authenticated WSS open while the administrator revokes it.
		running[2].cancel()
		select {
		case <-running[2].done:
			running[2].done = nil
		case <-time.After(3 * time.Second):
			t.Fatal("Agent did not stop before active-socket revocation test")
		}
		activeConn := dialAgentSocket(t, proxyServer.URL, rootPEM, running[2].identity.Credential)
		writeProtocolMessage(t, activeConn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(running[2].identity.AgentID, running[2].identity.NodeID))})
		welcome := readProtocolMessage(t, activeConn)
		if welcome.Type != protocol.TypeWelcome {
			t.Fatalf("active revocation test did not establish Agent WSS: %q", welcome.Type)
		}
		revokeResponse, err := admin(http.MethodPost, "/api/v1/agents/"+running[2].identity.AgentID+"/revoke", nil)
		if err != nil {
			t.Fatal(err)
		}
		if revokeResponse.StatusCode != http.StatusOK {
			_ = revokeResponse.Body.Close()
			t.Fatalf("Agent revocation returned HTTP %d", revokeResponse.StatusCode)
		}
		_ = revokeResponse.Body.Close()
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _, closeErr := activeConn.Read(readCtx)
		cancel()
		if closeErr == nil {
			t.Fatal("revocation did not close the active Agent WebSocket")
		}
		waitForAgentStatus(t, restarted, running[2].identity.NodeID, "revoked", 0)
		unauthorizedConn, response, err := websocket.Dial(context.Background(), agentWebSocketURLForTest(proxyServer.URL), &websocket.DialOptions{
			HTTPClient: tlsHTTPClient(rootPEM),
			HTTPHeader: http.Header{"Authorization": []string{"Bearer " + running[2].identity.Credential}},
		})
		if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
			if err == nil {
				_ = unauthorizedConn.CloseNow()
			}
			t.Fatal("revoked device reconnected to Core")
		}
	})
}

func TestRealAgentLeaseDeadlineOverWSS(t *testing.T) {
	t.Run("S02-SUP-05", func(t *testing.T) {
		workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s02")
		if err := os.MkdirAll(workRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		work, err := os.MkdirTemp(workRoot, "lease-deadline-*")
		if err != nil {
			t.Fatal(err)
		}
		work, err = filepath.Abs(work)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(work, 0o700); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(work)
		certificate, rootPEM, err := makeAgentTestCertificate()
		if err != nil {
			t.Fatal(err)
		}
		logicalClock := atomic.Int64{}
		initial := time.Now().UTC()
		logicalClock.Store(initial.UnixNano())
		now := func() time.Time { return time.Unix(0, logicalClock.Load()).UTC() }
		const leaseTimeout = 30 * time.Second
		core, err := New("lease-deadline", Options{DataDir: filepath.Join(work, "core"), Development: true,
			PublicOrigin: "https://panel.test", AgentOfflineTimeout: leaseTimeout,
			AgentSweepInterval: leaseTimeout, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		defer core.Close()
		webServer := httptest.NewUnstartedServer(core)
		webServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		webServer.StartTLS()
		defer webServer.Close()
		adminSession, adminCSRF, err := installIntegrationAdmin(core)
		if err != nil {
			t.Fatal(err)
		}
		adminClient, err := httpClientForRoots(rootPEM)
		if err != nil {
			t.Fatal(err)
		}
		defer adminClient.CloseIdleConnections()
		fetchAgentList := func() (testAgentListResponse, error) {
			var result testAgentListResponse
			request, err := http.NewRequest(http.MethodGet, webServer.URL+"/api/v1/agents", nil)
			if err != nil {
				return result, err
			}
			request.Header.Set("Origin", "https://panel.test")
			request.Header.Set("Cookie", sessionCookieName+"="+adminSession+"; "+csrfCookieName+"="+adminCSRF)
			response, err := adminClient.Do(request)
			if err != nil {
				return result, err
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				return result, fmt.Errorf("authenticated Agent list returned HTTP %d", response.StatusCode)
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result)
			return result, err
		}

		enrollment, err := core.agents.CreateEnrollment(context.Background(), "lease-deadline", "127.0.0.1", sql.NullInt64{})
		if err != nil {
			t.Fatal(err)
		}
		credential, err := agent.NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		requestID, err := agent.NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token), auth.DigestToken(credential), requestID, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		conn := dialAgentSocket(t, webServer.URL, rootPEM, credential)
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(identity.AgentID, identity.NodeID))})
		welcomeEnvelope := readProtocolMessage(t, conn)
		if welcomeEnvelope.Type != protocol.TypeWelcome {
			t.Fatalf("real Core did not welcome test Agent: %q", welcomeEnvelope.Type)
		}
		var welcome protocol.Welcome
		if err := json.Unmarshal(welcomeEnvelope.Payload, &welcome); err != nil || welcome.Generation == 0 {
			t.Fatalf("real Core returned invalid lease welcome: generation=%d err=%v", welcome.Generation, err)
		}
		logicalClock.Add(int64(time.Millisecond))
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
			Generation: welcome.Generation, Sequence: 1, Payload: marshalAgentPayload(protocol.Heartbeat{})})
		firstAck := readProtocolMessage(t, conn)
		if firstAck.Type != protocol.TypeHeartbeatAck || firstAck.Sequence != 1 {
			t.Fatalf("initial real WSS heartbeat was not acknowledged: type=%s sequence=%d", firstAck.Type, firstAck.Sequence)
		}
		var lastSeen int64
		if err := core.store.DB.QueryRow(`SELECT last_seen_at FROM nodes WHERE id=?`, identity.NodeID).Scan(&lastSeen); err != nil {
			t.Fatal(err)
		}
		logicalClock.Store(lastSeen + int64(leaseTimeout) - int64(time.Millisecond))
		nearDeadlineList, err := fetchAgentList()
		if err != nil {
			t.Fatalf("read Agent status at deadline-1ms: %v", err)
		}
		nearDeadlineServerTime, err := time.Parse(time.RFC3339Nano, nearDeadlineList.ServerTime)
		if err != nil || len(nearDeadlineList.Nodes) != 1 {
			t.Fatalf("invalid authenticated Agent list at deadline-1ms: nodes=%d err=%v", len(nearDeadlineList.Nodes), err)
		}
		nearDeadline, err := time.Parse(time.RFC3339Nano, nearDeadlineList.Nodes[0].LeaseValidUntil)
		if err != nil || nearDeadlineList.Nodes[0].Status != "online" || !nearDeadlineServerTime.Before(nearDeadline) {
			t.Fatalf("Core did not report the lease valid at deadline-1ms: status=%s serverTime=%s validUntil=%s err=%v",
				nearDeadlineList.Nodes[0].Status, nearDeadlineServerTime, nearDeadline, err)
		}
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
			Generation: welcome.Generation, Sequence: 2, Payload: marshalAgentPayload(protocol.Heartbeat{})})
		nearAck := readProtocolMessage(t, conn)
		if nearAck.Type != protocol.TypeHeartbeatAck || nearAck.Sequence != 2 {
			t.Fatalf("heartbeat just before the exact deadline was rejected: type=%s sequence=%d", nearAck.Type, nearAck.Sequence)
		}
		if err := core.store.DB.QueryRow(`SELECT last_seen_at FROM nodes WHERE id=?`, identity.NodeID).Scan(&lastSeen); err != nil {
			t.Fatal(err)
		}
		logicalClock.Store(lastSeen + int64(leaseTimeout))
		if !t.Run("S02-SUP-08", func(t *testing.T) {
			list, err := fetchAgentList()
			if err != nil {
				t.Fatalf("read Agent status at exact lease expiry: %v", err)
			}
			serverTime, err := time.Parse(time.RFC3339Nano, list.ServerTime)
			if err != nil || len(list.Nodes) != 1 {
				t.Fatalf("invalid authenticated Agent list at exact expiry: nodes=%d err=%v", len(list.Nodes), err)
			}
			validUntil, err := time.Parse(time.RFC3339Nano, list.Nodes[0].LeaseValidUntil)
			wantValidUntil := time.Unix(0, lastSeen).UTC().Add(leaseTimeout)
			if err != nil || !validUntil.Equal(wantValidUntil) {
				t.Fatalf("Core returned wrong lease deadline: got=%s want=%s err=%v", validUntil, wantValidUntil, err)
			}
			if !serverTime.Equal(wantValidUntil) || list.Nodes[0].Status != "offline" {
				t.Fatalf("Core returned an expired lease as online: serverTime=%s validUntil=%s status=%s",
					serverTime, validUntil, list.Nodes[0].Status)
			}
			var persistedStatus string
			var persistedGeneration uint64
			if err := core.store.DB.QueryRow(`SELECT status, connection_generation FROM nodes WHERE id=?`, identity.NodeID).
				Scan(&persistedStatus, &persistedGeneration); err != nil {
				t.Fatal(err)
			}
			if persistedStatus != "online" || persistedGeneration != welcome.Generation {
				t.Fatalf("test did not hold asynchronous lease materialization: persisted=%s generation=%d want online/%d",
					persistedStatus, persistedGeneration, welcome.Generation)
			}
			t.Logf("authenticated API reported offline at exact validUntil=%s while persisted status remained online; asynchronous sweeper/watcher was not required for display correctness",
				validUntil.Format(time.RFC3339Nano))
		}) {
			t.FailNow()
		}
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHeartbeat,
			Generation: welcome.Generation, Sequence: 3, Payload: marshalAgentPayload(protocol.Heartbeat{})})
		readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _, lateErr := conn.Read(readCtx)
		cancel()
		if lateErr == nil {
			t.Fatal("Core accepted or acknowledged a heartbeat at the exact lease deadline")
		}
		var status string
		var sequence, generation uint64
		if err := core.store.DB.QueryRow(`SELECT status, heartbeat_sequence, connection_generation FROM nodes WHERE id=?`, identity.NodeID).
			Scan(&status, &sequence, &generation); err != nil {
			t.Fatal(err)
		}
		if status != "offline" || sequence != 2 || generation <= welcome.Generation {
			t.Fatalf("exact-deadline heartbeat changed the lease unexpectedly: status=%s sequence=%d generation=%d welcome=%d", status, sequence, generation, welcome.Generation)
		}
		t.Logf("WSS heartbeat sequence 2 accepted at deadline-1ms; sequence 3 at exact deadline rejected, final status=%s generation=%d", status, generation)
	})
}

func TestRealAgentCoreCloseDrainsAgentProxyTunnels(t *testing.T) {
	t.Run("S02-07", func(t *testing.T) {
		t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s02-close-test-docker.sock")
		workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s02")
		if err := os.MkdirAll(workRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		work, err := os.MkdirTemp(workRoot, "core-close-proxy-tunnels-*")
		if err != nil {
			t.Fatal(err)
		}
		work, err = filepath.Abs(work)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(work, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(work); err != nil {
				t.Errorf("remove Core-close fixture directory: %v", err)
			}
		})
		certificate, rootPEM, err := makeAgentTestCertificate()
		if err != nil {
			t.Fatal(err)
		}
		caPath := filepath.Join(work, "trusted-ca.pem")
		if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		core, err := New("agent-close", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
		if err != nil {
			t.Fatal(err)
		}
		var current atomic.Pointer[Server]
		current.Store(core)
		coreHTTP := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			active := current.Load()
			if active == nil {
				http.Error(w, "Core restarting", http.StatusServiceUnavailable)
				return
			}
			active.ServeHTTP(w, r)
		}))
		coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		coreHTTP.StartTLS()
		proxy := newAgentTestProxy(t, &current, coreHTTP.URL, rootPEM)
		proxyServer := httptest.NewUnstartedServer(proxy)
		proxyServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		proxyServer.StartTLS()
		proxy.proxyURL = proxyServer.URL

		const agentCount = 3
		type runningAgent struct {
			configPath string
			identity   agent.Config
			cancel     context.CancelFunc
			done       chan error
		}
		running := make([]runningAgent, agentCount)
		coreClosed := false
		coreCloseStarted := false
		t.Cleanup(func() {
			current.Store(nil)
			proxy.closeAgentTunnels()
			for index := range running {
				if running[index].cancel != nil {
					running[index].cancel()
				}
			}
			for index := range running {
				if running[index].done == nil {
					continue
				}
				select {
				case <-running[index].done:
				case <-time.After(3 * time.Second):
					t.Errorf("Agent %d did not stop after Core-close fixture cleanup", index+1)
				}
			}
			if !coreClosed && !coreCloseStarted {
				closed := make(chan error, 1)
				go func() { closed <- core.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Errorf("close Core during fixture cleanup: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("Core did not close within the bounded fixture-cleanup deadline")
				}
			}
			closeS02HTTPServerWithin(t, proxyServer, "Agent WSS proxy", 3*time.Second)
			closeS02HTTPServerWithin(t, coreHTTP, "Core HTTPS", 3*time.Second)
		})

		for index := range running {
			enrollment, err := core.agents.CreateEnrollment(context.Background(), fmt.Sprintf("close-test-%d", index), "127.0.0.1", sql.NullInt64{})
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(work, fmt.Sprintf("agent-%d", index), "agent.json")
			if err := agent.Enroll(context.Background(), proxyServer.URL, caPath, false, strings.NewReader(enrollment.Token), configPath); err != nil {
				t.Fatalf("enroll real Agent %d through WSS proxy: %v", index+1, err)
			}
			config, err := agent.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("load real Agent %d identity: %v", index+1, err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			running[index] = runningAgent{configPath: configPath, identity: config, cancel: cancel, done: make(chan error, 1)}
			go func(item *runningAgent) {
				item.done <- agent.Run(ctx, item.configPath, "close-test", &agentTestLog{})
			}(&running[index])
		}
		for index := range running {
			waitForAgentStatus(t, core, running[index].identity.NodeID, "online", 0)
		}
		waitForAgentProxyTunnelCount(t, proxy, agentCount, 5*time.Second, "three established outbound Agent WSS tunnels")
		for index := range running {
			t.Logf("S02-07 Core-close fixture Agent %d id=%s node=%s generation=%d", index+1,
				running[index].identity.AgentID, running[index].identity.NodeID, generationForNode(t, core, running[index].identity.NodeID))
		}
		current.Store(nil)
		coreCloseStarted = true
		closeS02CoreWithin(t, core, proxy, 5*time.Second)
		coreClosed = true
		waitForAgentProxyTunnelCount(t, proxy, 0, 3*time.Second, "all WSS proxy copy handlers after Core.Close")
		t.Logf("S02-07 Core.Close returned and all proxy tunnels/copy handlers exited; three Agent runtimes remained independently cancellable")
	})
}

func closeS02CoreWithin(t *testing.T, core *Server, proxy *agentTestProxy, timeout time.Duration) {
	t.Helper()
	closed := make(chan error, 1)
	started := time.Now()
	go func() { closed <- core.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Core.Close returned an error: %v", err)
		}
	case <-time.After(timeout):
		activeTunnels := proxy.activeTunnelCount()
		proxy.closeAgentTunnels()
		select {
		case err := <-closed:
			t.Fatalf("Core.Close exceeded %s with %d proxy tunnels; it completed with error %v only after forced tunnel cancellation", timeout, activeTunnels, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("Core.Close exceeded %s and remained blocked after canceling %d proxy tunnels", timeout, activeTunnels)
		}
	}
	waitForAgentProxyTunnelCount(t, proxy, 0, 3*time.Second, "Core.Close to drain Agent proxy tunnels")
	t.Logf("Core.Close completed in %s and drained all Agent proxy tunnels", time.Since(started).Round(time.Millisecond))
}

func waitForAgentProxyTunnelCount(t *testing.T, proxy *agentTestProxy, want int, timeout time.Duration, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := proxy.activeTunnelCount(); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s: active Agent proxy tunnels=%d, expected=%d", description, proxy.activeTunnelCount(), want)
}

func closeS02HTTPServerWithin(t *testing.T, server *httptest.Server, description string, timeout time.Duration) {
	t.Helper()
	server.CloseClientConnections()
	closed := make(chan struct{})
	go func() {
		server.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(timeout):
		t.Errorf("%s still had HTTP handlers after %s", description, timeout)
	}
}

func TestRealAgentLeaseWatcherShutdown(t *testing.T) {
	t.Run("S02-SUP-10", func(t *testing.T) {
		workRoot := filepath.Join("..", "..", "..", ".artifacts", "work-s02")
		if err := os.MkdirAll(workRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		work, err := os.MkdirTemp(workRoot, "lease-watcher-shutdown-*")
		if err != nil {
			t.Fatal(err)
		}
		work, err = filepath.Abs(work)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(work, 0o700); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(work)
		certificate, rootPEM, err := makeAgentTestCertificate()
		if err != nil {
			t.Fatal(err)
		}
		core, err := New("watcher-shutdown", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test"})
		if err != nil {
			t.Fatal(err)
		}
		coreClosed := false
		defer func() {
			if !coreClosed {
				_ = core.Close()
			}
		}()
		webServer := httptest.NewUnstartedServer(core)
		webServer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
		webServer.StartTLS()
		defer webServer.Close()

		enrollment, err := core.agents.CreateEnrollment(context.Background(), "watcher-shutdown", "127.0.0.1", sql.NullInt64{})
		if err != nil {
			t.Fatal(err)
		}
		credential, err := agent.NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		requestID, err := agent.NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		identity, err := core.agents.ConsumeEnrollment(context.Background(), auth.DigestToken(enrollment.Token),
			auth.DigestToken(credential), requestID, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		conn := dialAgentSocket(t, webServer.URL, rootPEM, credential)
		writeProtocolMessage(t, conn, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeHello,
			Payload: marshalAgentPayload(makeAgentHello(identity.AgentID, identity.NodeID))})
		welcome := readProtocolMessage(t, conn)
		if welcome.Type != protocol.TypeWelcome {
			_ = conn.CloseNow()
			t.Fatalf("Core did not establish the shutdown-test Agent lease: %q", welcome.Type)
		}
		var payload protocol.Welcome
		if err := json.Unmarshal(welcome.Payload, &payload); err != nil {
			_ = conn.CloseNow()
			t.Fatal("decode shutdown-test Agent generation")
		}
		waitForLeaseWatcherGeneration(t, core, identity.AgentID, payload.Generation, time.Second)
		_ = conn.CloseNow()
		waitForAgentConnectionDetached(t, core, identity.AgentID, time.Second)
		core.agentConnectionsMu.Lock()
		watcher := core.agentLeaseWatchers[identity.AgentID]
		core.agentConnectionsMu.Unlock()
		if watcher == nil || watcher.watchDone == nil {
			t.Fatal("ordinary disconnect removed the watcher before lease expiry")
		}
		watchDone := watcher.watchDone
		select {
		case <-watchDone:
			t.Fatal("lease watcher exited before Core.Close")
		default:
		}

		closed := make(chan error, 1)
		go func() { closed <- core.Close() }()
		select {
		case err := <-closed:
			if err != nil {
				t.Fatalf("Core.Close failed: %v", err)
			}
			coreClosed = true
		case <-time.After(3 * time.Second):
			t.Fatal("Core.Close did not join the disconnected Agent lease watcher")
		}
		select {
		case <-watchDone:
		default:
			t.Fatal("Core.Close returned while the lease watcher goroutine remained active")
		}
		core.agentConnectionsMu.Lock()
		remainingWatchers := len(core.agentLeaseWatchers)
		core.agentConnectionsMu.Unlock()
		if remainingWatchers != 0 {
			t.Fatalf("Core.Close left %d lease watchers registered", remainingWatchers)
		}
		t.Logf("Core.Close joined the detached generation %d watcher and cleared ownership", payload.Generation)
	})
}

type agentTestProxy struct {
	current                         *atomic.Pointer[Server]
	coreURL                         string
	proxyURL                        string
	rootPEM                         []byte
	dropNextEnrollment              atomic.Bool
	droppedEnrollment               atomic.Bool
	holdNextIdentity                atomic.Bool
	holdNextHello                   atomic.Bool
	droppedWelcome                  chan droppedWelcomeEvent
	identityHeld                    chan struct{}
	helloHeld                       chan struct{}
	releaseHello                    chan struct{}
	releaseIdentity                 chan struct{}
	dropWelcomeMu                   sync.Mutex
	dropWelcomeTarget               *dropWelcomeTarget
	networkDown                     atomic.Bool
	connectionAttempts              atomic.Uint64
	dashboardOpened                 atomic.Uint64
	dashboardClosed                 atomic.Uint64
	dropDockerChunk                 atomic.Bool
	blockReconnectAfterDroppedChunk atomic.Bool
	droppedDockerChunk              chan struct{}
	dockerChunks                    atomic.Uint64
	dropDockerMessages              atomic.Bool
	droppedDockerMessages           atomic.Uint64
	replayNextDockerChange          atomic.Bool
	dockerReplayResults             chan dockerReplayEvidence
	tunnelMu                        sync.Mutex
	tunnels                         map[uint64]context.CancelFunc
	nextTunnelID                    uint64
}

type dockerReplayEvidence struct {
	OlderSequence   uint64
	NewerSequence   uint64
	OlderAction     string
	NewerAction     string
	OlderState      string
	NewerState      string
	DuplicateWrites int
}

type dropWelcomeTarget struct {
	agentID         string
	nodeID          string
	afterGeneration uint64
}

type droppedWelcomeEvent struct {
	AgentID                     string
	NodeID                      string
	Generation                  uint64
	CredentialRotationCommitted bool
}

func newAgentTestProxy(t *testing.T, current *atomic.Pointer[Server], coreURL string, rootPEM []byte) *agentTestProxy {
	t.Helper()
	return &agentTestProxy{current: current, coreURL: coreURL, rootPEM: rootPEM,
		droppedWelcome: make(chan droppedWelcomeEvent, 1), identityHeld: make(chan struct{}, 1), helloHeld: make(chan struct{}, 1),
		releaseHello: make(chan struct{}, 1), releaseIdentity: make(chan struct{}), tunnels: make(map[uint64]context.CancelFunc),
		droppedDockerChunk: make(chan struct{}, 1), dockerReplayResults: make(chan dockerReplayEvidence, 1)}
}

func (p *agentTestProxy) dropNextDockerSnapshotChunk() {
	p.dropDockerChunk.Store(true)
}

func (p *agentTestProxy) dropNextDockerSnapshotChunkAndBlockReconnect() {
	p.blockReconnectAfterDroppedChunk.Store(true)
	p.dropDockerChunk.Store(true)
}

func (p *agentTestProxy) replayNextDockerChangeOutOfOrderAndDuplicate() {
	p.replayNextDockerChange.Store(true)
}

func (p *agentTestProxy) dropCommittedRotationWelcome(agentID, nodeID string, afterGeneration uint64) {
	p.dropWelcomeMu.Lock()
	defer p.dropWelcomeMu.Unlock()
	p.dropWelcomeTarget = &dropWelcomeTarget{agentID: agentID, nodeID: nodeID, afterGeneration: afterGeneration}
}

func (p *agentTestProxy) takeCommittedRotationWelcome(data []byte) (droppedWelcomeEvent, bool) {
	var envelope protocol.Envelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Type != protocol.TypeWelcome {
		return droppedWelcomeEvent{}, false
	}
	var welcome protocol.Welcome
	if json.Unmarshal(envelope.Payload, &welcome) != nil || !welcome.CredentialRotationDone || welcome.Generation == 0 {
		return droppedWelcomeEvent{}, false
	}
	p.dropWelcomeMu.Lock()
	defer p.dropWelcomeMu.Unlock()
	target := p.dropWelcomeTarget
	if target == nil || welcome.AgentID != target.agentID || welcome.NodeID != target.nodeID || welcome.Generation <= target.afterGeneration {
		return droppedWelcomeEvent{}, false
	}
	p.dropWelcomeTarget = nil
	return droppedWelcomeEvent{AgentID: welcome.AgentID, NodeID: welcome.NodeID, Generation: welcome.Generation,
		CredentialRotationCommitted: welcome.CredentialRotationDone}, true
}

func (p *agentTestProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.networkDown.Load() && isAgentNetworkPath(r.URL.Path) {
		p.connectionAttempts.Add(1)
		http.Error(w, "injected Agent network outage", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/ws/v1/agent" {
		p.proxyWebSocket(w, r)
		return
	}
	if r.URL.Path == "/ws/v1/dashboard" {
		p.dashboardOpened.Add(1)
		defer p.dashboardClosed.Add(1)
	}
	if r.URL.Path == "/api/v1/agents/identity" && p.holdNextIdentity.CompareAndSwap(true, false) {
		select {
		case p.identityHeld <- struct{}{}:
		default:
		}
		select {
		case <-p.releaseIdentity:
		case <-r.Context().Done():
			return
		}
	}
	active := p.current.Load()
	if active == nil {
		http.Error(w, "Core restarting", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/api/v1/agents/enroll" && p.dropNextEnrollment.CompareAndSwap(true, false) {
		response := httptest.NewRecorder()
		active.ServeHTTP(response, r)
		if response.Code == http.StatusCreated {
			if hijacker, ok := w.(http.Hijacker); ok {
				connection, _, err := hijacker.Hijack()
				if err == nil {
					p.droppedEnrollment.Store(true)
					_ = connection.Close()
					return
				}
			}
		}
		copyRecordedResponse(w, response)
		return
	}
	active.ServeHTTP(w, r)
}

func (p *agentTestProxy) proxyWebSocket(w http.ResponseWriter, r *http.Request) {
	active := p.current.Load()
	if active == nil {
		http.Error(w, "Core restarting", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	tunnelID := p.addTunnel(cancel)
	defer p.removeTunnel(tunnelID)
	upstreamURL := strings.Replace(p.coreURL, "https://", "wss://", 1) + "/ws/v1/agent"
	upstream, response, err := websocket.Dial(ctx, upstreamURL, &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(p.rootPEM),
		HTTPHeader: http.Header{"Authorization": []string{r.Header.Get("Authorization")}},
	})
	if err != nil {
		status := http.StatusBadGateway
		if response != nil && response.StatusCode >= 400 {
			status = response.StatusCode
		}
		http.Error(w, "upstream Agent channel unavailable", status)
		return
	}
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		_ = upstream.CloseNow()
		return
	}
	upstream.SetReadLimit(protocol.MaxMessageBytes + 1024)
	downstream.SetReadLimit(protocol.MaxMessageBytes + 1024)
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		var previousDockerData []byte
		var previousDockerEnvelope protocol.Envelope
		var previousDockerBatch protocol.DockerBatch
		for {
			messageType, data, err := downstream.Read(ctx)
			if err != nil {
				return
			}
			var envelope protocol.Envelope
			if messageType == websocket.MessageText && json.Unmarshal(data, &envelope) == nil && envelope.Type == protocol.TypeDocker {
				if p.dropDockerMessages.Load() {
					p.droppedDockerMessages.Add(1)
					continue
				}
				var batch protocol.DockerBatch
				if json.Unmarshal(envelope.Payload, &batch) == nil && batch.FullSnapshot {
					p.dockerChunks.Add(1)
					if batch.SnapshotIndex == 0 && !batch.SnapshotFinal && p.dropDockerChunk.CompareAndSwap(true, false) {
						if err := upstream.Write(ctx, messageType, data); err != nil {
							return
						}
						if p.blockReconnectAfterDroppedChunk.CompareAndSwap(true, false) {
							p.networkDown.Store(true)
						}
						select {
						case p.droppedDockerChunk <- struct{}{}:
						default:
						}
						return
					}
				}
				if json.Unmarshal(envelope.Payload, &batch) == nil && !batch.FullSnapshot && len(batch.Changes) != 0 {
					if p.replayNextDockerChange.Load() && previousDockerData != nil && envelope.Sequence > previousDockerEnvelope.Sequence &&
						p.replayNextDockerChange.CompareAndSwap(true, false) {
						if err := upstream.Write(ctx, messageType, data); err != nil {
							return
						}
						if err := upstream.Write(ctx, messageType, previousDockerData); err != nil {
							return
						}
						if err := upstream.Write(ctx, messageType, data); err != nil {
							return
						}
						evidence := dockerReplayEvidence{
							OlderSequence: previousDockerEnvelope.Sequence, NewerSequence: envelope.Sequence,
							DuplicateWrites: 1,
						}
						if len(previousDockerBatch.Changes) != 0 {
							evidence.OlderAction = string(previousDockerBatch.Changes[0].Action)
							if previousDockerBatch.Changes[0].Container != nil {
								evidence.OlderState = previousDockerBatch.Changes[0].Container.State
							}
						}
						evidence.NewerAction = string(batch.Changes[0].Action)
						if batch.Changes[0].Container != nil {
							evidence.NewerState = batch.Changes[0].Container.State
						}
						select {
						case p.dockerReplayResults <- evidence:
						default:
						}
						previousDockerData = append(previousDockerData[:0], data...)
						previousDockerEnvelope = envelope
						previousDockerBatch = batch
						continue
					}
					previousDockerData = append(previousDockerData[:0], data...)
					previousDockerEnvelope = envelope
					previousDockerBatch = batch
				}
			}
			if isHelloMessage(data) && p.holdNextHello.CompareAndSwap(true, false) {
				select {
				case p.helloHeld <- struct{}{}:
				default:
				}
				select {
				case <-p.releaseHello:
					return
				case <-ctx.Done():
					return
				}
			}
			if err := upstream.Write(ctx, messageType, data); err != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			messageType, data, err := upstream.Read(ctx)
			if err != nil {
				return
			}
			if dropped, ok := p.takeCommittedRotationWelcome(data); ok {
				p.holdNextIdentity.Store(true)
				select {
				case p.droppedWelcome <- dropped:
				default:
				}
				return
			}
			if err := downstream.Write(ctx, messageType, data); err != nil {
				return
			}
		}
	}()
	<-done
	cancel()
	_ = downstream.CloseNow()
	_ = upstream.CloseNow()
	// The second copier exits because cancel closed both Read contexts.
	<-done
}

func isAgentNetworkPath(path string) bool {
	return path == "/ws/v1/agent" || path == "/api/v1/agents/enroll" || path == "/api/v1/agents/identity"
}

func (p *agentTestProxy) addTunnel(cancel context.CancelFunc) uint64 {
	p.tunnelMu.Lock()
	defer p.tunnelMu.Unlock()
	p.nextTunnelID++
	p.tunnels[p.nextTunnelID] = cancel
	return p.nextTunnelID
}

func (p *agentTestProxy) removeTunnel(id uint64) {
	p.tunnelMu.Lock()
	delete(p.tunnels, id)
	p.tunnelMu.Unlock()
}

func (p *agentTestProxy) closeAgentTunnels() {
	p.tunnelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.tunnels))
	for _, cancel := range p.tunnels {
		cancels = append(cancels, cancel)
	}
	p.tunnelMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (p *agentTestProxy) activeTunnelCount() int {
	p.tunnelMu.Lock()
	defer p.tunnelMu.Unlock()
	return len(p.tunnels)
}

type agentTestLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *agentTestLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(data)
}

func (l *agentTestLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func isWelcomeMessage(data []byte) bool {
	var envelope protocol.Envelope
	return json.Unmarshal(data, &envelope) == nil && envelope.Type == protocol.TypeWelcome
}

func isHelloMessage(data []byte) bool {
	var envelope protocol.Envelope
	return json.Unmarshal(data, &envelope) == nil && envelope.Type == protocol.TypeHello
}

func copyRecordedResponse(w http.ResponseWriter, response *httptest.ResponseRecorder) {
	for name, values := range response.Header() {
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.Code)
	_, _ = w.Write(response.Body.Bytes())
}

func makeAgentTestCertificate() (tls.Certificate, []byte, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "NodeDance local test CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(12 * time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, rootCert, &leafKey.PublicKey, rootKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
	return certificate, rootPEM, err
}

func httpClientForRoots(rootPEM []byte) (*http.Client, error) {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigForRoots(rootPEM)}}, nil
}

func tlsHTTPClient(rootPEM []byte) *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigForRoots(rootPEM)}}
}

func tlsConfigForRoots(rootPEM []byte) *tls.Config {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		panic("invalid test CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
}

func installIntegrationAdmin(core *Server) (string, string, error) {
	now := time.Now().UnixNano()
	if _, err := core.store.DB.Exec(`INSERT INTO admin_user(id, password_hash, password_salt, password_version, display_name, updated_at)
		VALUES(1, ?, ?, ?, 'Integration admin', ?)`, make([]byte, 32), make([]byte, 16), auth.CurrentPasswordVersion, now); err != nil {
		return "", "", err
	}
	sessionRaw, sessionDigest, err := auth.NewToken()
	if err != nil {
		return "", "", err
	}
	csrfRaw, _, err := auth.NewToken()
	if err != nil {
		return "", "", err
	}
	if _, err := core.store.DB.Exec(`INSERT INTO browser_sessions(id, token_digest, csrf_digest, created_at, last_seen_at, user_agent, remote_addr)
		VALUES('0123456789abcdef0123456789abcdef', ?, ?, ?, ?, 'NodeDance S02 integration', '127.0.0.1')`, sessionDigest, auth.DigestToken(csrfRaw), now, now); err != nil {
		return "", "", err
	}
	return sessionRaw, csrfRaw, nil
}

func dialAgentSocket(t *testing.T, serverURL string, rootPEM []byte, credential string) *websocket.Conn {
	t.Helper()
	conn, response, err := websocket.Dial(context.Background(), agentWebSocketURLForTest(serverURL), &websocket.DialOptions{
		HTTPClient: tlsHTTPClient(rootPEM),
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + credential}},
	})
	if err != nil {
		status := ""
		if response != nil {
			status = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		t.Fatalf("Agent WSS test connection failed (%s)", status)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func agentWebSocketURLForTest(serverURL string) string {
	return strings.Replace(serverURL, "https://", "wss://", 1) + "/ws/v1/agent"
}

func makeAgentHello(agentID, nodeID string) protocol.Hello {
	groups, _ := os.Getgroups()
	if len(groups) > 128 {
		groups = groups[:128]
	}
	return protocol.Hello{AgentID: agentID, NodeID: nodeID, AgentVersion: "integration",
		Capabilities: []string{"agent.heartbeat.v1", "agent.rotation.v1", "agent.future.v99"},
		Permissions: protocol.RuntimePermissions{OS: runtime.GOOS, Architecture: runtime.GOARCH,
			EffectiveUID: os.Geteuid(), EffectiveGID: os.Getegid(), SupplementaryGroups: groups}}
}

func writeProtocolMessage(t *testing.T, conn *websocket.Conn, envelope protocol.Envelope) {
	t.Helper()
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal("encode test Agent message")
	}
	writeRawProtocolMessage(t, conn, data)
}

func writeRawProtocolMessage(t *testing.T, conn *websocket.Conn, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal("write test Agent frame")
	}
}

func readProtocolMessage(t *testing.T, conn *websocket.Conn) protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal("read test Agent protocol response")
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal("decode test Agent protocol response")
	}
	return envelope
}

func assertProtocolError(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	envelope := readProtocolMessage(t, conn)
	if envelope.Type != protocol.TypeProtocolError {
		t.Fatalf("expected Core protocol error, got %q", envelope.Type)
	}
}

func waitForAgentStatus(t *testing.T, core *Server, nodeID, status string, generationGreaterThan uint64) {
	waitForAgentStatusWithin(t, core, nodeID, status, generationGreaterThan, 10*time.Second)
}

func waitForAgentStatusWithin(t *testing.T, core *Server, nodeID, status string, generationGreaterThan uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err == nil {
			for _, node := range nodes {
				if node.NodeID == nodeID && node.Status == status && node.ConnectionGeneration > generationGreaterThan {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("NodeDance Core did not report node %s as %s with generation > %d within %s", nodeID, status, generationGreaterThan, timeout)
}

func waitForAgentConnectionDetached(t *testing.T, core *Server, agentID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		core.agentConnectionsMu.Lock()
		connection := core.agentConnections[agentID]
		core.agentConnectionsMu.Unlock()
		if connection == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Core kept an active connection entry for Agent %s after WebSocket close", agentID)
}

func waitForLeaseWatcherGeneration(t *testing.T, core *Server, agentID string, generation uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		core.agentConnectionsMu.Lock()
		watcher := core.agentLeaseWatchers[agentID]
		core.agentConnectionsMu.Unlock()
		if watcher != nil && watcher.generation == generation {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Core did not assign the current lease watcher for Agent %s generation %d", agentID, generation)
}

func leaseWatcherCount(core *Server, agentID string) int {
	core.agentConnectionsMu.Lock()
	defer core.agentConnectionsMu.Unlock()
	if core.agentLeaseWatchers[agentID] != nil {
		return 1
	}
	return 0
}

func lastSeenForNode(t *testing.T, core *Server, nodeID string) time.Time {
	t.Helper()
	nodes, err := core.agents.ListNodes(context.Background())
	if err != nil {
		t.Fatal("list Core nodes")
	}
	for _, node := range nodes {
		if node.NodeID == nodeID && node.HasLastSeen {
			return node.LastSeen
		}
	}
	t.Fatal("Core has no heartbeat timestamp for Agent node")
	return time.Time{}
}

func generationForNode(t *testing.T, core *Server, nodeID string) uint64 {
	t.Helper()
	nodes, err := core.agents.ListNodes(context.Background())
	if err != nil {
		t.Fatal("list Core node generations")
	}
	for _, node := range nodes {
		if node.NodeID == nodeID {
			return node.ConnectionGeneration
		}
	}
	t.Fatal("Core has no Agent node generation")
	return 0
}

func heartbeatSequenceForNode(t *testing.T, core *Server, nodeID string) uint64 {
	t.Helper()
	var sequence uint64
	if err := core.store.DB.QueryRow(`SELECT heartbeat_sequence FROM nodes WHERE id=?`, nodeID).Scan(&sequence); err != nil {
		t.Fatalf("read Core heartbeat sequence: %v", err)
	}
	return sequence
}

type testNodeLeaseSnapshot struct {
	Status     string
	Generation uint64
	Sequence   uint64
	LastSeen   int64
	UpdatedAt  int64
}

type testAgentListResponse struct {
	ServerTime string `json:"serverTime"`
	Nodes      []struct {
		NodeID          string `json:"nodeId"`
		Status          string `json:"status"`
		LeaseValidUntil string `json:"leaseValidUntil"`
	} `json:"nodes"`
}

func nodeLeaseSnapshot(t *testing.T, core *Server, nodeID string) testNodeLeaseSnapshot {
	t.Helper()
	var result testNodeLeaseSnapshot
	if err := core.store.DB.QueryRow(`SELECT status, connection_generation, heartbeat_sequence, last_seen_at, updated_at FROM nodes WHERE id=?`, nodeID).
		Scan(&result.Status, &result.Generation, &result.Sequence, &result.LastSeen, &result.UpdatedAt); err != nil {
		t.Fatalf("read Core node lease sample: %v", err)
	}
	return result
}

func processListenerSockets(pid int) ([]string, error) {
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return nil, err
	}
	owned := make(map[string]struct{})
	for _, fd := range fds {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
			owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = struct{}{}
		}
	}
	var listeners []string
	for _, table := range []struct {
		name   string
		family string
		tcp    bool
	}{{"tcp", "tcp4", true}, {"tcp6", "tcp6", true}, {"udp", "udp4", false}, {"udp6", "udp6", false}} {
		file, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, table.name))
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 || table.tcp && fields[3] != "0A" { // TCP_LISTEN; every owned UDP socket is bound.
				continue
			}
			if _, ok := owned[fields[9]]; ok {
				listeners = append(listeners, table.family+" "+fields[1])
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return listeners, nil
}

func waitForConfig(t *testing.T, path string, condition func(agent.Config) bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		value, err := agent.LoadConfig(path)
		if err == nil && condition(value) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Agent config did not reach the expected persisted state")
}

func errorsForTest(message string) error { return fmt.Errorf("%s", message) }
