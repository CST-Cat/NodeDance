package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestPreparedUpdateAckWaitsForCorePersistence(t *testing.T) {
	connection := &agentConnection{generation: 42, commands: make(chan protocol.Envelope, 1)}
	report := protocol.AgentUpdateReport{TaskID: "5f29b72e-2613-4f44-8c88-3754fca3a124", Status: "prepared"}
	persistStarted := make(chan struct{})
	finishPersist := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- persistAgentUpdateReportAndQueueAck(context.Background(), connection, report, func() error {
			close(persistStarted)
			<-finishPersist
			return nil
		})
	}()
	<-persistStarted
	select {
	case ack := <-connection.commands:
		t.Fatalf("ACK queued before Core persistence completed: %#v", ack)
	default:
	}
	close(finishPersist)
	if err := <-result; err != nil {
		t.Fatalf("queue persisted prepared ACK: %v", err)
	}
	select {
	case ack := <-connection.commands:
		if ack.Type != protocol.TypeAgentUpdatePreparedAck || ack.Generation != connection.generation || ack.RequestID != report.TaskID {
			t.Fatalf("unexpected ACK envelope: %#v", ack)
		}
		var payload protocol.AgentUpdatePreparedAck
		if err := decodeAgentPayload(ack.Payload, &payload); err != nil || payload.TaskID != report.TaskID {
			t.Fatalf("ACK payload = %#v, %v", payload, err)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted prepared report did not queue an ACK")
	}
}

func TestPreparedUpdatePersistenceFailureDoesNotQueueAck(t *testing.T) {
	connection := &agentConnection{generation: 7, commands: make(chan protocol.Envelope, 1)}
	report := protocol.AgentUpdateReport{TaskID: "5f29b72e-2613-4f44-8c88-3754fca3a124", Status: "prepared"}
	wantErr := errors.New("injected Core database failure")
	if err := persistAgentUpdateReportAndQueueAck(context.Background(), connection, report, func() error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("persistence error = %v, want %v", err, wantErr)
	}
	select {
	case ack := <-connection.commands:
		t.Fatalf("ACK queued after failed Core persistence: %#v", ack)
	default:
	}
}

func TestStagedHelloRequiresPreparedAckCapability(t *testing.T) {
	base := protocol.Hello{
		AgentID: "11111111-1111-4111-8111-111111111111", NodeID: "22222222-2222-4222-8222-222222222222",
		AgentVersion: "1.0.0", UpdateTaskID: "5f29b72e-2613-4f44-8c88-3754fca3a124", UpdateState: "staged",
		Permissions: protocol.RuntimePermissions{OS: "linux", Architecture: "amd64"},
	}
	legacy := base
	legacy.Capabilities = []string{protocol.CapabilityAgentUpdates}
	if validHello(legacy) {
		t.Fatal("staged Hello with legacy update capability was accepted")
	}
	current := base
	current.Capabilities = []string{protocol.CapabilityAgentUpdatesPreparedAck}
	if !validHello(current) {
		t.Fatal("staged Hello with prepared-ACK capability was rejected")
	}
}
