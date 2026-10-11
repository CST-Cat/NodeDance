package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentruntime "github.com/CST-Cat/NodeDance/internal/agent"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestProductionCoreSupportsPlainHTTPAgentEnrollmentAndConnection(t *testing.T) {
	ctx := context.Background()
	core, err := New("test", Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("start production-mode Core: %v", err)
	}
	t.Cleanup(func() {
		if err := core.Close(); err != nil {
			t.Errorf("stop Core: %v", err)
		}
	})
	coreHTTP := httptest.NewServer(core)
	t.Cleanup(coreHTTP.Close)

	enrollment, err := core.agents.CreateEnrollment(ctx, "direct-http-test", "127.0.0.1", sql.NullInt64{})
	if err != nil {
		t.Fatalf("create one-time enrollment: %v", err)
	}
	credential, err := agentruntime.NewCredential()
	if err != nil {
		t.Fatalf("create device credential: %v", err)
	}
	requestID, err := agentruntime.NewRequestID()
	if err != nil {
		t.Fatalf("create enrollment request ID: %v", err)
	}
	body, err := json.Marshal(consumeAgentEnrollmentRequest{RequestID: requestID, Credential: credential})
	if err != nil {
		t.Fatalf("encode enrollment request: %v", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, coreHTTP.URL+"/api/v1/agents/enroll", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create enrollment request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Enrollment "+enrollment.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send plain-HTTP enrollment: %v", err)
	}
	var identity agentIdentityResponse
	decodeErr := json.NewDecoder(response.Body).Decode(&identity)
	response.Body.Close()
	if decodeErr != nil || response.StatusCode != http.StatusCreated || !validUUID(identity.AgentID) || !validUUID(identity.NodeID) {
		t.Fatalf("plain-HTTP enrollment response: status=%d identity=%#v decodeErr=%v", response.StatusCode, identity, decodeErr)
	}

	identityRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, coreHTTP.URL+"/api/v1/agents/identity", nil)
	if err != nil {
		t.Fatalf("create identity request: %v", err)
	}
	identityRequest.Header.Set("Authorization", "Bearer "+credential)
	identityResponse, err := http.DefaultClient.Do(identityRequest)
	if err != nil {
		t.Fatalf("send plain-HTTP identity request: %v", err)
	}
	var resolved agentIdentityResponse
	decodeErr = json.NewDecoder(identityResponse.Body).Decode(&resolved)
	identityResponse.Body.Close()
	if decodeErr != nil || identityResponse.StatusCode != http.StatusOK || resolved.AgentID != identity.AgentID || resolved.NodeID != identity.NodeID {
		t.Fatalf("plain-HTTP identity response: status=%d identity=%#v decodeErr=%v", identityResponse.StatusCode, resolved, decodeErr)
	}

	conn, welcome := connectPersistedProtocolAgentForTest(t, coreHTTP.URL, agents.Identity{AgentID: identity.AgentID, NodeID: identity.NodeID}, credential,
		[]string{protocol.CapabilityMetrics})
	defer conn.CloseNow()
	if welcome.Generation == 0 || !containsCapability(welcome.Capabilities, protocol.CapabilityMetrics) {
		t.Fatalf("plain-HTTP Agent did not receive its live metrics session: %#v", welcome)
	}
}
