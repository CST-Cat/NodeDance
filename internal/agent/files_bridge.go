package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"

	agentfiles "github.com/CST-Cat/NodeDance/internal/agent/files"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxAgentFileTransfers = 4

type fileEnvelopeWriter interface {
	send(context.Context, protocol.Envelope) error
	offerFile(context.Context, protocol.Envelope) error
}

type agentFileTransfer struct {
	id                string
	download          *os.File
	ack               chan uint64
	cancel            context.CancelFunc
	sequence          uint64
	lastDownloadChunk uint64
	lastDownloadAck   uint64
	upload            *io.PipeWriter
	nextUploadSeq     uint64
}

type agentFileBridge struct {
	service     *agentfiles.Service
	tasks       *agentfiles.TaskExecutor
	generation  uint64
	writer      fileEnvelopeWriter
	mu          sync.Mutex
	transfers   map[string]*agentFileTransfer
	canceled    map[string]struct{}
	cancelOrder []string
	closed      bool
	wg          sync.WaitGroup
}

func newAgentFileBridge(service *agentfiles.Service, generation uint64, writer fileEnvelopeWriter, tasks *agentfiles.TaskExecutor) *agentFileBridge {
	return &agentFileBridge{service: service, tasks: tasks, generation: generation, writer: writer,
		transfers: make(map[string]*agentFileTransfer), canceled: make(map[string]struct{})}
}

func (b *agentFileBridge) run(ctx context.Context, messages <-chan protocol.Envelope) error {
	defer b.closeAll()
	for {
		select {
		case <-ctx.Done():
			return nil
		case envelope, ok := <-messages:
			if !ok {
				return errors.New("file control channel closed")
			}
			if envelope.Type == protocol.TypeFileCancel {
				var cancel protocol.FileCancel
				if decodeSocketPayload(envelope.Payload, &cancel) != nil || protocol.ValidateFileCancel(envelope, b.generation, cancel) != nil {
					return errors.New("Core file cancellation is invalid")
				}
				b.cancel(cancel.TransferID)
				continue
			}
			if envelope.Type == protocol.TypeFileChunk {
				var chunk protocol.FileChunk
				if decodeSocketPayload(envelope.Payload, &chunk) != nil || protocol.ValidateFileChunkEnvelope(envelope, b.generation, chunk) != nil {
					return errors.New("Core file chunk is invalid")
				}
				if err := b.acceptUploadChunk(ctx, chunk); err != nil {
					return err
				}
				continue
			}
			if envelope.Type != protocol.TypeFileRequest {
				return errors.New("Core sent a non-control file message")
			}
			var request protocol.FileRequest
			if decodeSocketPayload(envelope.Payload, &request) != nil || protocol.ValidateFileRequest(envelope, b.generation, request) != nil {
				return errors.New("Core file request is invalid")
			}
			if request.Operation == protocol.FileDownloadAck {
				if !b.acknowledge(envelope.RequestID, request.Sequence) {
					return errors.New("Core file download acknowledgement is invalid")
				}
				continue
			}
			if err := b.handle(ctx, envelope.RequestID, request); err != nil {
				return err
			}
		}
	}
}

func (b *agentFileBridge) handle(ctx context.Context, id string, request protocol.FileRequest) error {
	switch request.Operation {
	case protocol.FileList:
		entries, err := b.service.List(request.Path)
		return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation, Entries: entries}, err)
	case protocol.FileStat:
		entry, err := b.service.Stat(request.Path)
		return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation, Entry: &entry}, err)
	case protocol.FileReadText:
		text, version, err := b.service.ReadText(request.Path)
		response := protocol.FileResponse{Operation: request.Operation, Version: version}
		if err == nil {
			response.Text = base64.StdEncoding.EncodeToString([]byte(text))
		}
		return b.respond(ctx, id, response, err)
	case protocol.FileDownload:
		file, entry, err := b.service.OpenDownload(request.Path)
		if err != nil {
			return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, err)
		}
		transferCtx, cancel := context.WithCancel(ctx)
		transfer := &agentFileTransfer{id: id, download: file, ack: make(chan uint64, 1), cancel: cancel, lastDownloadAck: 1}
		if !b.addTransfer(transfer) {
			cancel()
			_ = file.Close()
			return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, errors.New("file transfer limit reached"))
		}
		if err := b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation, Entry: &entry, Size: entry.Size}, nil); err != nil {
			b.cancel(id)
			return err
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.streamDownload(transferCtx, transfer)
		}()
		return nil
	case protocol.FileUploadBegin:
		if b.tasks == nil {
			return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, errors.New("durable file task runner is unavailable"))
		}
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.beginUpload(ctx, id, request)
		}()
		return nil
	default:
		return errors.New("unsupported file operation")
	}
}

func (b *agentFileBridge) beginUpload(ctx context.Context, id string, request protocol.FileRequest) {
	pipe, err := b.tasks.AttachUpload(ctx, request.TaskID, request)
	if err != nil {
		if !b.isCanceled(id) {
			b.sendFailure(context.Background(), id, protocol.FileUploadBegin, fileErrorCode(err))
		}
		return
	}
	transferCtx, cancel := context.WithCancel(ctx)
	transfer := &agentFileTransfer{id: id, upload: pipe, cancel: cancel, nextUploadSeq: 1}
	if !b.addTransfer(transfer) {
		cancel()
		_ = pipe.CloseWithError(errors.New("file transfer was canceled"))
		return
	}
	if err := b.respond(transferCtx, id, protocol.FileResponse{Operation: protocol.FileUploadBegin, Size: request.Size}, nil); err != nil {
		b.cancel(id)
	}
}

func (b *agentFileBridge) acceptUploadChunk(ctx context.Context, chunk protocol.FileChunk) error {
	transfer := b.getTransfer(chunk.TransferID)
	if transfer == nil || transfer.upload == nil {
		return errors.New("Core sent a chunk for an inactive upload")
	}
	if chunk.Sequence != transfer.nextUploadSeq {
		return b.failUpload(ctx, chunk.TransferID, protocol.FileUploadChunk, errors.New("Core file upload sequence is invalid"))
	}
	data, err := protocol.DecodeFileChunk(chunk)
	if err != nil {
		return b.failUpload(ctx, chunk.TransferID, protocol.FileUploadChunk, err)
	}
	if len(data) > 0 {
		if _, err := transfer.upload.Write(data); err != nil {
			return b.failUpload(ctx, chunk.TransferID, protocol.FileUploadChunk, err)
		}
	}
	transfer.nextUploadSeq++
	if chunk.Final {
		if err := transfer.upload.Close(); err != nil {
			return b.failUpload(ctx, chunk.TransferID, protocol.FileUploadChunk, err)
		}
	}
	if err := b.respond(ctx, chunk.TransferID, protocol.FileResponse{Operation: protocol.FileUploadChunk, AckSequence: chunk.Sequence}, nil); err != nil {
		b.cancel(chunk.TransferID)
		return nil
	}
	if chunk.Final {
		b.removeTransfer(chunk.TransferID)
	}
	return nil
}

func (b *agentFileBridge) failUpload(ctx context.Context, id, operation string, err error) error {
	b.sendFailure(ctx, id, operation, fileErrorCode(err))
	b.cancel(id)
	return nil
}

func (b *agentFileBridge) streamDownload(ctx context.Context, transfer *agentFileTransfer) {
	defer b.removeTransfer(transfer.id)
	defer transfer.download.Close()
	hash := sha256.New()
	buffer := make([]byte, protocol.MaxFileChunkBytes)
	for {
		n, readErr := transfer.download.Read(buffer)
		if n > 0 {
			_, _ = hash.Write(buffer[:n])
			sequence := b.nextSequence(transfer)
			b.mu.Lock()
			transfer.lastDownloadChunk = sequence
			b.mu.Unlock()
			chunk := protocol.FileChunk{TransferID: transfer.id, Sequence: sequence, Data: base64.StdEncoding.EncodeToString(buffer[:n])}
			if err := b.offerChunk(ctx, transfer, chunk); err != nil {
				b.sendFailure(context.Background(), transfer.id, protocol.FileDownload, "transfer_failed")
				return
			}
			if !b.waitAck(ctx, transfer, sequence) {
				return
			}
		}
		if errors.Is(readErr, io.EOF) {
			sequence := b.nextSequence(transfer)
			chunk := protocol.FileChunk{TransferID: transfer.id, Sequence: sequence, Final: true, SHA256: hex.EncodeToString(hash.Sum(nil))}
			_ = b.offerChunk(ctx, transfer, chunk)
			return
		}
		if readErr != nil {
			b.sendFailure(context.Background(), transfer.id, protocol.FileDownload, "read_failed")
			return
		}
	}
}

func (b *agentFileBridge) offerChunk(ctx context.Context, transfer *agentFileTransfer, chunk protocol.FileChunk) error {
	payload, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileChunk, Generation: b.generation,
		RequestID: transfer.id, Sequence: chunk.Sequence, Payload: payload}
	if protocol.ValidateFileChunkEnvelope(envelope, b.generation, chunk) != nil {
		return errors.New("Agent produced an invalid file chunk")
	}
	return b.writer.offerFile(ctx, envelope)
}

func (b *agentFileBridge) waitAck(ctx context.Context, transfer *agentFileTransfer, sequence uint64) bool {
	select {
	case <-ctx.Done():
		return false
	case acknowledged := <-transfer.ack:
		return acknowledged == sequence
	}
}

func (b *agentFileBridge) acknowledge(id string, sequence uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	transfer := b.transfers[id]
	if transfer == nil || transfer.download == nil || sequence != transfer.lastDownloadAck+1 || sequence != transfer.lastDownloadChunk {
		return false
	}
	select {
	case transfer.ack <- sequence:
		transfer.lastDownloadAck = sequence
		return true
	default:
		return false
	}
}

func (b *agentFileBridge) respond(ctx context.Context, id string, response protocol.FileResponse, operationErr error) error {
	transfer := b.getTransfer(id)
	sequence := uint64(1)
	if transfer != nil {
		sequence = b.nextSequence(transfer)
	}
	if operationErr != nil {
		response.Code = fileErrorCode(operationErr)
		response.Message = "file operation failed"
	}
	payload, err := json.Marshal(response)
	if err != nil || len(payload) > protocol.MaxFileControlBytes {
		return errors.New("Agent file response exceeds its bound")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileResponse, Generation: b.generation,
		RequestID: id, Sequence: sequence, Payload: payload}
	if protocol.ValidateFileResponse(envelope, b.generation, response) != nil {
		return errors.New("Agent file response is invalid")
	}
	return b.writer.send(ctx, envelope)
}

func (b *agentFileBridge) sendFailure(ctx context.Context, id, operation, code string) {
	transfer := b.getTransfer(id)
	sequence := uint64(1)
	if transfer != nil {
		sequence = b.nextSequence(transfer)
	}
	payload, _ := json.Marshal(protocol.FileResponse{Operation: operation, Code: code, Message: "file transfer failed"})
	_ = b.writer.send(ctx, protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileResponse,
		Generation: b.generation, RequestID: id, Sequence: sequence, Payload: payload})
}

func fileErrorCode(err error) string {
	switch {
	case errors.Is(err, agentfiles.ErrInvalidPath):
		return "invalid_path"
	case errors.Is(err, agentfiles.ErrLimitExceeded), errors.Is(err, agentfiles.ErrDirectoryLarge), errors.Is(err, agentfiles.ErrTextTooLarge):
		return "limit_exceeded"
	case errors.Is(err, agentfiles.ErrNotText):
		return "not_text"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	default:
		return "unavailable"
	}
}

func (b *agentFileBridge) addTransfer(transfer *agentFileTransfer) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(b.transfers) >= maxAgentFileTransfers || b.transfers[transfer.id] != nil {
		return false
	}
	if _, canceled := b.canceled[transfer.id]; canceled {
		return false
	}
	b.transfers[transfer.id] = transfer
	return true
}

func (b *agentFileBridge) getTransfer(id string) *agentFileTransfer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.transfers[id]
}

func (b *agentFileBridge) nextSequence(transfer *agentFileTransfer) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	transfer.sequence++
	return transfer.sequence
}

func (b *agentFileBridge) removeTransfer(id string) *agentFileTransfer {
	b.mu.Lock()
	transfer := b.transfers[id]
	delete(b.transfers, id)
	b.mu.Unlock()
	if transfer != nil && transfer.cancel != nil {
		transfer.cancel()
	}
	return transfer
}

func (b *agentFileBridge) cancel(id string) bool {
	b.mu.Lock()
	if _, exists := b.canceled[id]; !exists {
		b.canceled[id] = struct{}{}
		b.cancelOrder = append(b.cancelOrder, id)
		if len(b.cancelOrder) > 64 {
			oldest := b.cancelOrder[0]
			b.cancelOrder = b.cancelOrder[1:]
			delete(b.canceled, oldest)
		}
	}
	transfer := b.transfers[id]
	delete(b.transfers, id)
	b.mu.Unlock()
	if transfer != nil {
		if transfer.cancel != nil {
			transfer.cancel()
		}
		if transfer.upload != nil {
			_ = transfer.upload.CloseWithError(errors.New("file transfer was canceled"))
		}
		if transfer.download != nil {
			_ = transfer.download.Close()
		}
		return true
	}
	return false
}

func (b *agentFileBridge) isCanceled(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.canceled[id]
	return ok
}

func (b *agentFileBridge) closeAll() {
	b.mu.Lock()
	b.closed = true
	ids := make([]string, 0, len(b.transfers))
	for id := range b.transfers {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	for _, id := range ids {
		b.cancel(id)
	}
	b.wg.Wait()
}
