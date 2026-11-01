package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type failingFinalFileWriter struct{ responses []protocol.Envelope }

func (w *failingFinalFileWriter) send(_ context.Context, e protocol.Envelope) error {
	w.responses = append(w.responses, e)
	return nil
}
func (w *failingFinalFileWriter) offerFile(context.Context, protocol.Envelope) error {
	return errors.New("queue full")
}
func TestDownloadFinalQueueFailureIsReported(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "empty")
	if err != nil {
		t.Fatal(err)
	}
	writer := &failingFinalFileWriter{}
	b := newAgentFileBridge(nil, 1, writer, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transfer := &agentFileTransfer{id: "01234567-89ab-4cde-8123-456789abcdef", download: file, cancel: cancel, sequence: 1}
	if !b.addTransfer(transfer) {
		t.Fatal("admission failed")
	}
	b.streamDownload(ctx, transfer)
	if len(writer.responses) != 1 {
		t.Fatalf("lost EOF silently: %d failure responses", len(writer.responses))
	}
	var response protocol.FileResponse
	if err := json.Unmarshal(writer.responses[0].Payload, &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "transfer_failed" {
		t.Fatalf("failure not reported: %+v", response)
	}
	if b.getTransfer(transfer.id) != nil || ctx.Err() == nil {
		t.Fatal("transfer resources retained")
	}
}
