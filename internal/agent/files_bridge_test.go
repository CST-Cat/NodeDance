package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	agentfiles "github.com/CST-Cat/NodeDance/internal/agent/files"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type fileBridgeTestWriter struct {
	messages chan protocol.Envelope
}

func (w *fileBridgeTestWriter) send(ctx context.Context, envelope protocol.Envelope) error {
	select {
	case w.messages <- envelope:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *fileBridgeTestWriter) offerFile(ctx context.Context, envelope protocol.Envelope) error {
	return w.send(ctx, envelope)
}

func TestFileBridgeAcknowledgesUploadCancellationAndMapsUnknownMutation(t *testing.T) {
	root := t.TempDir()
	service, err := agentfiles.New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	writer := &fileBridgeTestWriter{messages: make(chan protocol.Envelope, 8)}
	bridge := newAgentFileBridge(service, 1, writer)
	ctx, cancel := context.WithCancel(context.Background())
	commands := make(chan protocol.Envelope, 4)
	done := make(chan error, 1)
	go func() { done <- bridge.run(ctx, commands) }()

	data := []byte("temp")
	digest := sha256.Sum256(data)
	transferID := "77777777-7777-4777-8777-777777777777"
	begin := protocol.FileRequest{Operation: protocol.FileUploadBegin, Path: "/target.bin", TransferID: transferID,
		ExpectedSize: int64(len(data)), ExpectedSHA256: hex.EncodeToString(digest[:])}
	beginPayload, _ := json.Marshal(begin)
	commands <- protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRequest, Generation: 1, RequestID: transferID, Payload: beginPayload}
	if response := receiveFileBridgeFrame(t, writer.messages); response.Type != protocol.TypeFileResponse {
		t.Fatalf("upload begin response type=%q", response.Type)
	}
	if len(mustGlob(t, filepath.Join(root, ".target.bin.nodedance-upload-*"))) != 1 {
		t.Fatal("Agent did not create the expected temporary upload before cancellation")
	}

	cancelPayload, _ := json.Marshal(protocol.FileCancel{TransferID: transferID})
	commands <- protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileCancel, Generation: 1, RequestID: transferID, Payload: cancelPayload}
	ackEnvelope := receiveFileBridgeFrame(t, writer.messages)
	var ack protocol.FileCancelAck
	if err := json.Unmarshal(ackEnvelope.Payload, &ack); err != nil || ackEnvelope.Type != protocol.TypeFileCancelAck || !ack.Canceled {
		t.Fatalf("Agent cancellation ACK=%+v envelope=%+v err=%v; want confirmed temporary cleanup", ack, ackEnvelope, err)
	}
	if err := protocol.ValidateFileCancelAck(ackEnvelope, 1, ack); err != nil {
		t.Fatalf("Agent cancellation ACK failed protocol validation: %v", err)
	}
	if len(mustGlob(t, filepath.Join(root, ".target.bin.nodedance-upload-*"))) != 0 {
		t.Fatal("confirmed cancellation left a temporary upload file")
	}

	unknownID := "88888888-8888-4888-8888-888888888888"
	unknownCancel, _ := json.Marshal(protocol.FileCancel{TransferID: unknownID})
	commands <- protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileCancel, Generation: 1, RequestID: unknownID, Payload: unknownCancel}
	unknownEnvelope := receiveFileBridgeFrame(t, writer.messages)
	var unknownAck protocol.FileCancelAck
	if err := json.Unmarshal(unknownEnvelope.Payload, &unknownAck); err != nil || unknownAck.Canceled {
		t.Fatalf("Agent unknown-transfer cancellation ACK=%+v err=%v; want unconfirmed", unknownAck, err)
	}

	mutationErr := fmt.Errorf("verification after atomic rename: %w", agentfiles.ErrMutationResultUnknown)
	if got := fileErrorCode(mutationErr); got != "result_unknown" {
		t.Fatalf("post-mutation verification error mapped to %q; want result_unknown", got)
	}
	if got := fileErrorCode(agentfiles.ErrConflict); got != "conflict" {
		t.Fatalf("pre-mutation version rejection mapped to %q; want conflict", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Agent file bridge did not stop")
	}
}

func receiveFileBridgeFrame(t *testing.T, frames <-chan protocol.Envelope) protocol.Envelope {
	t.Helper()
	select {
	case envelope := <-frames:
		return envelope
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Agent file frame")
		return protocol.Envelope{}
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}
