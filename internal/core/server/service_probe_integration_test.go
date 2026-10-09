package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent"
	coreprobes "github.com/CST-Cat/NodeDance/internal/core/probes"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestRealAgentServiceProbeExecutesFromEnrolledNode(t *testing.T) {
	work := t.TempDir()
	certificate, rootPEM, err := makeAgentTestCertificate()
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(work, "trusted-ca.pem")
	if err := os.WriteFile(caPath, rootPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	core, err := New("s14-real-agent", Options{DataDir: filepath.Join(work, "core"), PublicOrigin: "https://panel.test", Development: true})
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
			case <-agentDone:
			case <-time.After(7 * time.Second):
				t.Error("real Agent did not stop")
			}
		}
		coreHTTP.Close()
		_ = core.Close()
	})

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer fixture.Close()
	sessionToken, csrfToken, err := installIntegrationAdmin(core)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := core.agents.CreateEnrollment(context.Background(), "S14 service probe Agent", "127.0.0.1", sql.NullInt64{Int64: 1, Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	agentConfigPath := filepath.Join(work, "agent", "agent.json")
	if err := agent.Enroll(context.Background(), coreHTTP.URL, caPath, false, strings.NewReader(enrollment.Token+"\n"), agentConfigPath); err != nil {
		t.Fatalf("real Agent enrollment failed: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	stopAgent = cancel
	agentDone = make(chan error, 1)
	go func() { agentDone <- agent.Run(runCtx, agentConfigPath, "s14-real-agent", nil) }()

	deadline := time.Now().Add(8 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		nodes, err := core.agents.ListNodes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, node := range nodes {
			if node.NodeID == enrollment.NodeID && node.Status == "online" && hasCapability(node.Capabilities, protocol.CapabilityProbes) {
				connected = true
				break
			}
		}
		if connected {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !connected {
		t.Fatal("real enrolled Agent did not negotiate service probe capability")
	}
	payload, err := json.Marshal(map[string]any{"nodeId": enrollment.NodeID, "name": "Loopback readiness", "kind": "http",
		"target": fixture.URL + "/ready", "expectedHttpStatus": http.StatusNoContent, "intervalSeconds": 30, "timeoutSeconds": 3, "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://panel.test/api/v1/probes", bytes.NewReader(payload))
	request.Header.Set("Origin", "https://panel.test")
	request.Header.Set(csrfHeaderName, csrfToken)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	response := httptest.NewRecorder()
	core.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create probe status=%d body=%s", response.Code, response.Body.String())
	}
	var config coreprobes.Config
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := core.probes.History(context.Background(), config.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Status == protocol.ProbeResultHealthy && run.HTTPStatus != nil && *run.HTTPStatus == http.StatusNoContent {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	runs, _ := core.probes.History(context.Background(), config.ID, 10)
	t.Fatalf("real Agent did not execute HTTP probe from its managed node: runs=%+v", runs)
}
