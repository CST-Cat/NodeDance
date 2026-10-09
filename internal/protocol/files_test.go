package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

const testFileRequestID = "01234567-89ab-4cde-8fab-0123456789ac"

func fileEnvelope(messageType string, sequence uint64) Envelope {
	return Envelope{Version: CurrentVersion, Type: messageType, Generation: 7, RequestID: testFileRequestID, Sequence: sequence}
}

func TestFileRequestValidatesOperationAndTransferIdentity(t *testing.T) {
	valid := fileEnvelope(TypeFileRequest, 0)
	request := FileRequest{Operation: FileUploadBegin, Path: "/target", TransferID: testFileRequestID, ExpectedSize: DefaultFileLimit}
	if err := ValidateFileRequest(valid, 7, request); err != nil {
		t.Fatalf("valid upload begin rejected: %v", err)
	}
	wrongType := valid
	wrongType.Type = TypeTaskDispatch
	if err := ValidateFileRequest(wrongType, 7, request); err == nil {
		t.Fatal("wrong envelope type was accepted")
	}
	request.TransferID = "11234567-89ab-4cde-8fab-0123456789ac"
	if err := ValidateFileRequest(valid, 7, request); err == nil {
		t.Fatal("upload transfer ID did not bind to request ID")
	}
	request.TransferID = testFileRequestID
	request.ExpectedSize = MaxFileSize + 1
	if err := ValidateFileRequest(valid, 7, request); err == nil {
		t.Fatal("protocol hard file size limit was not enforced")
	}
}

func TestFileChunksBindGenerationTransferSequenceAndChunkSize(t *testing.T) {
	data := []byte("chunk")
	chunk := FileChunk{TransferID: testFileRequestID, Sequence: 3, Data: base64.StdEncoding.EncodeToString(data)}
	envelope := fileEnvelope(TypeFileChunk, 3)
	if err := ValidateFileChunkEnvelope(envelope, 7, chunk); err != nil {
		t.Fatalf("valid chunk rejected: %v", err)
	}
	chunk.TransferID = "11234567-89ab-4cde-8fab-0123456789ac"
	if err := ValidateFileChunkEnvelope(envelope, 7, chunk); err == nil {
		t.Fatal("chunk with mismatched transfer ID was accepted")
	}
	chunk.TransferID = testFileRequestID
	chunk.Sequence++
	if err := ValidateFileChunkEnvelope(envelope, 7, chunk); err == nil {
		t.Fatal("chunk with mismatched sequence was accepted")
	}
	chunk.Sequence = 3
	chunk.Data = base64.StdEncoding.EncodeToString(make([]byte, MaxFileChunkBytes+1))
	if err := ValidateFileChunkEnvelope(envelope, 7, chunk); err == nil {
		t.Fatal("oversized chunk was accepted")
	}
}

func TestTextEditorLimitFitsControlEnvelope(t *testing.T) {
	request := FileRequest{Operation: FileSaveText, Path: "/text.txt", ExpectedVersion: "version",
		Text: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", MaxTextFileBytes)))}
	envelope := fileEnvelope(TypeFileRequest, 0)
	if err := ValidateFileRequest(envelope, 7, request); err != nil {
		t.Fatalf("maximum text edit request rejected: %v", err)
	}
	request.Text = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", MaxTextFileBytes+1)))
	if err := ValidateFileRequest(envelope, 7, request); err == nil {
		t.Fatal("text edit larger than advertised editor limit was accepted")
	}
}

func TestFileCancelAckBindsGenerationAndTransfer(t *testing.T) {
	envelope := fileEnvelope(TypeFileCancelAck, 0)
	ack := FileCancelAck{TransferID: testFileRequestID, Canceled: true}
	if err := ValidateFileCancelAck(envelope, 7, ack); err != nil {
		t.Fatalf("valid cancel acknowledgment rejected: %v", err)
	}
	ack.TransferID = "11234567-89ab-4cde-8fab-0123456789ac"
	if err := ValidateFileCancelAck(envelope, 7, ack); err == nil {
		t.Fatal("cancel acknowledgment with different transfer ID was accepted")
	}
}
