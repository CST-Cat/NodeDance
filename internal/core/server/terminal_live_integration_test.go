//go:build linux

package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type s09TerminalAuthorization struct {
	StreamID  string `json:"streamId"`
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expiresAt"`
}

type s09TerminalMessage struct {
	Type  string                 `json:"type"`
	Frame protocol.TerminalFrame `json:"frame"`
}

// TestRealAgentBrowserTerminalLifecycle verifies the complete host PTY data
// path through Core's protected API and browser WebSocket, using a real
// enrolled Agent runtime. It also verifies both ordinary browser close and
// administrator Session revocation reap their host shell processes.
func TestRealAgentBrowserTerminalLifecycle(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/nodedance-s09-terminal-docker.sock")
	work := t.TempDir()
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s09-live-terminal", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", Development: true})
	if err != nil {
		t.Fatal(err)
	}
	coreHTTP := httptest.NewUnstartedServer(core)
	coreHTTP.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	coreHTTP.StartTLS()

	var agentDone chan error
	var stopAgent context.CancelFunc
	t.Cleanup(func() {
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
	})

	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S09 live terminal Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
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
	runCtx, cancel := context.WithCancel(context.Background())
	stopAgent = cancel
	agentDone = make(chan error, 1)
	go func() { agentDone <- agent.Run(runCtx, configPath, "s09-live-terminal-test", nil) }()
	waitForS09TerminalAgent(t, core, enrollment.NodeID)

	client, err := httpClientForRoots(rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()

	openTerminal := func() (*websocket.Conn, s09TerminalAuthorization) {
		t.Helper()
		body := strings.NewReader(`{"targetKind":"host"}`)
		request, err := http.NewRequest(http.MethodPost, coreHTTP.URL+"/api/v1/nodes/"+enrollment.NodeID+"/terminals", body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", "https://panel.test")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(csrfHeaderName, csrfToken)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("authorize host terminal: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			data := make([]byte, 2048)
			count, _ := response.Body.Read(data)
			t.Fatalf("authorize host terminal: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data[:count])))
		}
		var authorization s09TerminalAuthorization
		if err := json.NewDecoder(response.Body).Decode(&authorization); err != nil || authorization.StreamID == "" || authorization.Ticket == "" {
			t.Fatalf("decode host terminal authorization: value=%+v err=%v", authorization, err)
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
			HTTPClient: client,
			HTTPHeader: http.Header{
				"Cookie": []string{sessionCookieName + "=" + sessionToken},
				"Origin": []string{"https://panel.test"},
			},
		})
		if err != nil {
			status := ""
			if handshake != nil {
				status = fmt.Sprintf(" (HTTP %d)", handshake.StatusCode)
			}
			t.Fatalf("connect authenticated browser terminal WebSocket%s: %v", status, err)
		}
		t.Cleanup(func() { _ = browser.CloseNow() })
		frame := readS09TerminalFrame(t, browser, 5*time.Second)
		if frame.StreamID != authorization.StreamID || frame.Action != protocol.TerminalActionReady {
			t.Fatalf("first browser terminal frame=%+v, want ready for stream %s", frame, authorization.StreamID)
		}
		return browser, authorization
	}

	writeBrowserFrame := func(browser *websocket.Conn, frame protocol.TerminalFrame) {
		t.Helper()
		data, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := browser.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatalf("write browser terminal frame %q: %v", frame.Action, err)
		}
	}

	// First stream verifies actual PTY input/output and terminal-size changes,
	// then exercises the user's ordinary close action.
	normalBrowser, normalAuth := openTerminal()
	writeBrowserFrame(normalBrowser, protocol.TerminalFrame{StreamID: normalAuth.StreamID, Action: protocol.TerminalActionResize, Rows: 42, Columns: 111})
	command := `printf 'ND_S09_CLOSE_PID=%s\n' "$$"; stty size; printf 'ND_S09_UTF8_你好\n'` + "\r"
	writeBrowserFrame(normalBrowser, protocol.TerminalFrame{StreamID: normalAuth.StreamID, Action: protocol.TerminalActionInput, Data: []byte(command)})
	output := readS09TerminalOutputUntil(t, normalBrowser, normalAuth.StreamID, 8*time.Second, func(text string) bool {
		return strings.Contains(text, "42 111") && strings.Contains(text, "ND_S09_UTF8_你好")
	})
	pid := s09TerminalPID(t, output, "ND_S09_CLOSE_PID")
	writeBrowserFrame(normalBrowser, protocol.TerminalFrame{StreamID: normalAuth.StreamID, Action: protocol.TerminalActionClose})
	readUntilBrowserClose(t, normalBrowser, 5*time.Second)
	waitForTerminalPTYReaped(t, pid)
	waitForCoreTerminalGone(t, core, normalAuth.StreamID)
	_ = normalBrowser.CloseNow()

	// A second live stream is cancelled by deleting its real authenticated
	// browser Session. Both Core's browser WebSocket and Agent's host PTY must
	// terminate as a result of that API action.
	revokedBrowser, revokedAuth := openTerminal()
	writeBrowserFrame(revokedBrowser, protocol.TerminalFrame{StreamID: revokedAuth.StreamID, Action: protocol.TerminalActionInput, Data: []byte("stty -echo\r")})
	writeBrowserFrame(revokedBrowser, protocol.TerminalFrame{StreamID: revokedAuth.StreamID, Action: protocol.TerminalActionInput,
		Data: []byte(`printf 'ND_S09_REVOKE_PID=%s\n' "$$"; sleep 300` + "\r")})
	revokedPIDPattern := regexp.MustCompile(`ND_S09_REVOKE_PID=[0-9]+`)
	revokedOutput := readS09TerminalOutputUntil(t, revokedBrowser, revokedAuth.StreamID, 8*time.Second, func(text string) bool {
		return revokedPIDPattern.MatchString(text)
	})
	revokedPID := s09TerminalPID(t, revokedOutput, "ND_S09_REVOKE_PID")

	var sessions struct {
		Sessions []browserSessionView `json:"sessions"`
	}
	listRequest, err := http.NewRequest(http.MethodGet, coreHTTP.URL+"/api/v1/auth/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	listRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	listResponse, err := client.Do(listRequest)
	if err != nil {
		t.Fatalf("list browser sessions before terminal cancellation: %v", err)
	}
	decodeErr := json.NewDecoder(listResponse.Body).Decode(&sessions)
	listResponse.Body.Close()
	if listResponse.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("list browser sessions: status=%d decode=%v", listResponse.StatusCode, decodeErr)
	}
	sessionID := ""
	for _, item := range sessions.Sessions {
		if item.Current {
			sessionID = item.ID
			break
		}
	}
	if sessionID == "" {
		t.Fatal("session list did not identify the current authenticated Session")
	}
	revokeRequest, err := http.NewRequest(http.MethodDelete, coreHTTP.URL+"/api/v1/auth/sessions/"+sessionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	revokeRequest.Header.Set("Origin", "https://panel.test")
	revokeRequest.Header.Set(csrfHeaderName, csrfToken)
	revokeRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	revokeRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	revokeResponse, err := client.Do(revokeRequest)
	if err != nil {
		t.Fatalf("revoke browser Session through live Core API: %v", err)
	}
	revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke browser Session: HTTP %d", revokeResponse.StatusCode)
	}
	readUntilBrowserClose(t, revokedBrowser, 5*time.Second)
	waitForTerminalPTYReaped(t, revokedPID)
	waitForCoreTerminalGone(t, core, revokedAuth.StreamID)
	_ = revokedBrowser.CloseNow()
	t.Log("S09 live Core→enrolled Agent→browser WebSocket passed: shell input/output, 42x111 resize, normal close, Session-revoke stream close, and both PTYs reaped")
}

func waitForS09TerminalAgent(t *testing.T, core *Server, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err == nil {
			for _, node := range nodes {
				if node.NodeID == nodeID && node.Status == "online" && hasCapability(node.Capabilities, protocol.CapabilityTerminal) {
					return
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("real enrolled Agent %s did not negotiate terminal capability", nodeID)
}

func readS09TerminalFrame(t *testing.T, browser *websocket.Conn, timeout time.Duration) protocol.TerminalFrame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	messageType, data, err := browser.Read(ctx)
	if err != nil {
		t.Fatalf("read browser terminal stream: %v", err)
	}
	if messageType != websocket.MessageText {
		t.Fatalf("browser terminal message type=%v, want text", messageType)
	}
	var message s09TerminalMessage
	if err := json.Unmarshal(data, &message); err != nil || message.Type != "terminal" {
		t.Fatalf("decode browser terminal message %q: type=%q err=%v", data, message.Type, err)
	}
	return message.Frame
}

func readS09TerminalOutputUntil(t *testing.T, browser *websocket.Conn, streamID string, timeout time.Duration, complete func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var output strings.Builder
	for time.Now().Before(deadline) {
		frame := readS09TerminalFrame(t, browser, time.Until(deadline))
		if frame.StreamID != streamID {
			t.Fatalf("terminal frame stream ID=%q, want %q", frame.StreamID, streamID)
		}
		switch frame.Action {
		case protocol.TerminalActionOutput:
			output.Write(frame.Data)
		case protocol.TerminalActionError:
			t.Fatalf("Agent rejected host terminal: %s", frame.Message)
		case protocol.TerminalActionClosed:
			t.Fatalf("host terminal closed before expected output: %q", output.String())
		}
		if complete(output.String()) {
			return output.String()
		}
	}
	t.Fatalf("terminal output did not meet completion predicate before timeout: %q", output.String())
	return ""
}

func readUntilBrowserClose(t *testing.T, browser *websocket.Conn, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, _, err := browser.Read(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("browser terminal WebSocket did not close before timeout")
			}
			return
		}
	}
}

func s09TerminalPID(t *testing.T, output, marker string) int {
	t.Helper()
	pattern := regexp.MustCompile(regexp.QuoteMeta(marker) + `=([0-9]+)`)
	match := pattern.FindStringSubmatch(output)
	if len(match) != 2 {
		t.Fatalf("terminal output did not contain process marker %s: %q", marker, output)
	}
	pid, err := strconv.Atoi(match[1])
	if err != nil || pid <= 1 {
		t.Fatalf("terminal reported invalid shell PID %q: %v", match[1], err)
	}
	return pid
}

func waitForTerminalPTYReaped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect host PTY shell PID %d: %v", pid, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("host PTY shell PID %d remained alive after browser stream ended", pid)
}

func waitForCoreTerminalGone(t *testing.T, core *Server, streamID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if core.terminals.find(streamID) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Core retained terminal stream %s after close", streamID)
}
