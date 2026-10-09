package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

type fileRemoteError struct{ code string }

func (e fileRemoteError) Error() string { return "Agent file operation failed: " + e.code }

func (s *Server) handleFilesAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	nodeID, operation, ok := filesRoute(r.URL.Path)
	if !ok {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	filePath := r.URL.Query().Get("path")
	if operation == "list" && filePath == "" {
		filePath = "/"
	}
	if operation == "download" && filePath == "" {
		http.Error(w, "file path is required", http.StatusBadRequest)
		return true
	}
	if operation == "download" {
		s.handleFileDownload(w, r, current, nodeID)
		return true
	}
	requestOperation := protocol.FileList
	switch operation {
	case "stat":
		requestOperation = protocol.FileStat
	case "text":
		requestOperation = protocol.FileReadText
	}
	response, err := s.runFileOperation(r.Context(), current, nodeID, protocol.FileRequest{Operation: requestOperation, Path: filePath})
	if err != nil {
		s.writeFileAPIError(w, err)
		return true
	}
	switch operation {
	case "list":
		writeJSON(w, http.StatusOK, map[string]any{"entries": response.Entries, "path": filePath})
	case "stat":
		if response.Entry == nil {
			http.Error(w, "Agent did not return file metadata", http.StatusBadGateway)
		} else {
			writeJSON(w, http.StatusOK, response.Entry)
		}
	case "text":
		data, err := base64DecodeText(response.Text)
		if err != nil {
			http.Error(w, "Agent returned invalid text content", http.StatusBadGateway)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"path": filePath, "text": string(data), "version": response.Version, "size": len(data)})
	}
	return true
}

func filesRoute(requestPath string) (nodeID, operation string, ok bool) {
	if !strings.HasPrefix(requestPath, "/api/v1/nodes/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(requestPath, "/api/v1/nodes/"), "/")
	if len(parts) < 2 || !validUUID(parts[0]) || parts[1] != "files" {
		return "", "", false
	}
	if len(parts) == 2 {
		return parts[0], "list", true
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "stat", "text", "download":
			return parts[0], parts[2], true
		}
	}
	return "", "", false
}

func (s *Server) runFileOperation(ctx context.Context, current *session, nodeID string, request protocol.FileRequest) (protocol.FileResponse, error) {
	connection, transfer, err := s.newCoreFileTransfer(ctx, nodeID, current, []string{request.Operation}, false)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	defer s.closeCoreFileTransfer(connection, transfer, false, nil)
	payload, err := json.Marshal(request)
	if err != nil || len(payload) > protocol.MaxFileControlBytes {
		return protocol.FileResponse{}, errors.New("file request exceeds its protocol bound")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileRequest, Generation: transfer.generation,
		RequestID: transfer.requestID, Payload: payload}
	if protocol.ValidateFileRequest(envelope, transfer.generation, request) != nil {
		return protocol.FileResponse{}, errors.New("file request is invalid")
	}
	if err := s.sendFileRequest(ctx, connection, transfer, request); err != nil {
		return protocol.FileResponse{}, err
	}
	envelope, err = s.waitFileMessage(ctx, connection, transfer)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if envelope.Type != protocol.TypeFileResponse {
		return protocol.FileResponse{}, errors.New("Agent returned an unexpected file frame")
	}
	var response protocol.FileResponse
	if decodeAgentPayload(envelope.Payload, &response) != nil || response.Operation != request.Operation {
		return protocol.FileResponse{}, errors.New("Agent file response does not match request")
	}
	if response.Code != "" {
		return response, fileRemoteError{code: response.Code}
	}
	return response, nil
}

func base64DecodeText(value string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(data) > protocol.MaxTextFileBytes || !utf8.Valid(data) {
		return nil, errors.New("Agent returned invalid text content")
	}
	return data, nil
}

func (s *Server) writeFileAPIError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var remote fileRemoteError
	if errors.As(err, &remote) {
		switch remote.code {
		case "invalid_path":
			status = http.StatusBadRequest
		case "not_found":
			status = http.StatusNotFound
		case "permission_denied":
			status = http.StatusForbidden
		case "limit_exceeded":
			status = http.StatusRequestEntityTooLarge
		case "not_text":
			status = http.StatusUnsupportedMediaType
		case "conflict":
			status = http.StatusConflict
		case "unavailable":
			status = http.StatusServiceUnavailable
		}
	} else if errors.Is(err, errContainerStreamUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, errContainerStreamNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, context.Canceled) {
		status = http.StatusRequestTimeout
	}
	http.Error(w, "file operation unavailable", status)
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request, current *session, nodeID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		http.Error(w, "file path is required", http.StatusBadRequest)
		return
	}
	connection, transfer, err := s.newCoreFileTransfer(r.Context(), nodeID, current, []string{protocol.FileDownload}, true)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	defer s.closeCoreFileTransfer(connection, transfer, true, nil)
	request := protocol.FileRequest{Operation: protocol.FileDownload, Path: filePath, TransferID: transfer.requestID}
	if err := s.sendFileRequest(r.Context(), connection, transfer, request); err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	first, err := s.waitFileMessage(r.Context(), connection, transfer)
	if err != nil {
		s.writeFileAPIError(w, err)
		return
	}
	var response protocol.FileResponse
	if decodeAgentPayload(first.Payload, &response) != nil || response.Entry == nil {
		http.Error(w, "Agent download metadata is invalid", http.StatusBadGateway)
		return
	}
	if response.Code != "" {
		s.writeFileAPIError(w, fileRemoteError{code: response.Code})
		return
	}
	if response.Operation != protocol.FileDownload || response.Size != response.Entry.Size || response.Size < 0 || response.Size > s.fileTransferLimit {
		http.Error(w, "Agent download metadata is inconsistent", http.StatusBadGateway)
		return
	}
	spool, err := os.CreateTemp(s.dataDir, ".nodedance-download-*")
	if err != nil {
		http.Error(w, "temporary download storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	spoolName := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolName)
	}()
	if err := spool.Chmod(0o600); err != nil {
		http.Error(w, "temporary download storage is unavailable", http.StatusInsufficientStorage)
		return
	}
	name := path.Base(response.Entry.Path)
	if name == "." || name == "/" || name == "" {
		name = "download"
	}
	hash := sha256.New()
	var total int64
	var finalDigest string
	for {
		envelope, waitErr := s.waitFileMessage(r.Context(), connection, transfer)
		if waitErr != nil {
			s.writeFileAPIError(w, waitErr)
			return
		}
		if envelope.Type == protocol.TypeFileResponse {
			var failure protocol.FileResponse
			if decodeAgentPayload(envelope.Payload, &failure) == nil && failure.Code != "" {
				s.writeFileAPIError(w, fileRemoteError{code: failure.Code})
			} else {
				http.Error(w, "Agent download stream failed", http.StatusBadGateway)
			}
			return
		}
		var chunk protocol.FileChunk
		if decodeAgentPayload(envelope.Payload, &chunk) != nil || protocol.ValidateFileChunkEnvelope(envelope, transfer.generation, chunk) != nil {
			http.Error(w, "Agent download chunk is invalid", http.StatusBadGateway)
			return
		}
		data, decodeErr := protocol.DecodeFileChunk(chunk)
		if decodeErr != nil {
			http.Error(w, "Agent download chunk is invalid", http.StatusBadGateway)
			return
		}
		if chunk.Final {
			if total != response.Size || hex.EncodeToString(hash.Sum(nil)) != chunk.SHA256 {
				http.Error(w, "download integrity verification failed", http.StatusBadGateway)
				return
			}
			finalDigest = chunk.SHA256
			break
		}
		if len(data) == 0 || total+int64(len(data)) > response.Size {
			http.Error(w, "Agent download length is inconsistent", http.StatusBadGateway)
			return
		}
		if !s.dashboardSessionStillValid(r.Context(), current.ID) {
			s.closeCoreFileTransfer(connection, transfer, true, errors.New("browser Session was revoked or expired"))
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		n, writeErr := spool.Write(data)
		if writeErr != nil || n != len(data) {
			http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
			return
		}
		_, _ = hash.Write(data)
		total += int64(n)
		ack := protocol.FileRequest{Operation: protocol.FileDownloadAck, TransferID: transfer.requestID, Sequence: chunk.Sequence}
		if err := s.sendFileRequest(r.Context(), connection, transfer, ack); err != nil {
			s.writeFileAPIError(w, err)
			return
		}
	}
	if finalDigest == "" || !s.dashboardSessionStillValid(r.Context(), current.ID) {
		if r.Context().Err() == nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
		}
		return
	}
	if err := spool.Sync(); err != nil {
		http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
		return
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "temporary download storage failed", http.StatusInsufficientStorage)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(response.Size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("X-File-Version", response.Entry.Version)
	w.Header().Set("X-File-SHA256", finalDigest)
	w.Header().Set("X-NodeDance-Transfer-ID", transfer.requestID)
	w.WriteHeader(http.StatusOK)
	if _, err := io.CopyN(w, spool, response.Size); err != nil {
		return
	}
}
