package compose

import (
	"context"
	"encoding/json"
	"testing"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type bridgeTestWriter struct{ messages chan protocol.Envelope }

func (writer bridgeTestWriter) Send(_ context.Context, envelope protocol.Envelope) error {
	writer.messages <- envelope
	return nil
}

func TestBridgeReportsBusyWithoutDroppingAgentConnection(t *testing.T) {
	ref := testProject(t, "bridge-project", "bridge", []string{"compose.yaml"})
	manager := testManager(t, &fakeEngine{items: map[string]agentdocker.Container{}}, &fakeRunner{}, Options{})
	bridge, err := NewBridge(manager)
	if err != nil {
		t.Fatal(err)
	}
	bridge.requests["active-one"] = struct{}{}
	bridge.requests["active-two"] = struct{}{}

	request := protocol.ComposeRequest{OperationID: "busy-operation", Action: protocol.ComposeUp, Project: ref}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	commands := make(chan protocol.Envelope, 1)
	commands <- protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeComposeRequest,
		Generation: 9, RequestID: request.OperationID, Payload: payload}
	writer := bridgeTestWriter{messages: make(chan protocol.Envelope, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- bridge.Run(ctx, writer, commands, 9) }()
	responseEnvelope := <-writer.messages
	var response protocol.ComposeResponse
	if err := json.Unmarshal(responseEnvelope.Payload, &response); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateComposeResponse(response, request); err != nil || response.Status != "failed" || response.ErrorCode != "agent_busy" {
		t.Fatalf("busy result was not correlated: response=%+v err=%v", response, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("busy Compose request closed the Agent bridge: %v", err)
	}
}
