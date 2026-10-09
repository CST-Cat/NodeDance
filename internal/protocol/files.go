package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	CapabilityFiles = "agent.files.v1"

	TypeFileRequest  = "file_request"
	TypeFileResponse = "file_response"
	TypeFileChunk    = "file_chunk"
	TypeFileCancel   = "file_cancel"

	FileList         = "list"
	FileStat         = "stat"
	FileMkdir        = "mkdir"
	FileRename       = "rename"
	FileDelete       = "delete"
	FileReadText     = "read_text"
	FileSaveText     = "save_text"
	FileUploadBegin  = "upload_begin"
	FileUploadChunk  = "upload_chunk"
	FileUploadCommit = "upload_commit"
	FileUploadAck    = "upload_ack"
	FileDownload     = "download"
	FileDownloadAck  = "download_ack"

	MaxFileControlBytes = 64 << 10
	MaxFileChunkBytes   = 32 << 10
	DefaultFileLimit    = int64(1 << 30)
	MaxFileSize         = int64(16 << 30)
	MaxFileEntries      = 100
	MaxTextFileBytes    = 32 << 10
)

var fileRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// FileRequest is a typed, read-only request sent by Core after authenticating
// the administrator Session and binding the transfer to a node generation.
type FileRequest struct {
	Operation       string `json:"operation"`
	Path            string `json:"path,omitempty"`
	TransferID      string `json:"transferId,omitempty"`
	Sequence        uint64 `json:"sequence,omitempty"`
	TaskID          string `json:"taskId,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	Size            int64  `json:"size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

type FileEntry struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Size       int64  `json:"size"`
	Mode       uint32 `json:"mode"`
	OwnerUID   int    `json:"ownerUid"`
	OwnerGID   int    `json:"ownerGid"`
	ModifiedAt int64  `json:"modifiedAt"`
	Version    string `json:"version,omitempty"`
}

type FileResponse struct {
	Operation   string      `json:"operation"`
	Code        string      `json:"code,omitempty"`
	Message     string      `json:"message,omitempty"`
	Version     string      `json:"version,omitempty"`
	Size        int64       `json:"size,omitempty"`
	Entries     []FileEntry `json:"entries,omitempty"`
	Entry       *FileEntry  `json:"entry,omitempty"`
	Text        string      `json:"text,omitempty"`
	AckSequence uint64      `json:"ackSequence,omitempty"`
}

type FileChunk struct {
	TransferID string `json:"transferId"`
	Sequence   uint64 `json:"sequence"`
	Data       string `json:"data,omitempty"`
	Final      bool   `json:"final,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type FileCancel struct {
	TransferID string `json:"transferId"`
}

func DecodeFileChunk(chunk FileChunk) ([]byte, error) {
	if chunk.TransferID == "" || chunk.Sequence == 0 || len(chunk.Data) > base64.StdEncoding.EncodedLen(MaxFileChunkBytes) {
		return nil, errors.New("file chunk metadata is invalid")
	}
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil || len(data) > MaxFileChunkBytes {
		return nil, errors.New("file chunk data is invalid")
	}
	if chunk.Final && len(data) != 0 {
		return nil, errors.New("final file chunk must not contain data")
	}
	return data, nil
}

func ValidateFileRequest(envelope Envelope, generation uint64, request FileRequest) error {
	if envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation || !fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence != 0 {
		return errors.New("file request envelope is invalid")
	}
	if envelope.Type != TypeFileRequest {
		return errors.New("file request message type is invalid")
	}
	if strings.ContainsRune(request.Path, '\x00') || len(request.Path) > 4096 {
		return errors.New("file request path is invalid")
	}
	switch request.Operation {
	case FileList, FileStat, FileReadText, FileDownload:
		if request.Path == "" {
			return errors.New("file path is required")
		}
		if request.Operation == FileDownload && request.TransferID != envelope.RequestID {
			return errors.New("download transfer identity is invalid")
		}
	case FileDownloadAck:
		if request.TransferID != envelope.RequestID || request.Sequence == 0 {
			return errors.New("download acknowledgement is invalid")
		}
	case FileUploadBegin:
		if request.TransferID != envelope.RequestID || !validFileTaskID(request.TaskID) || request.Path == "" ||
			request.Size < 0 || request.Size > MaxFileSize || !validFileDigest(request.SHA256) || len(request.ExpectedVersion) > 128 {
			return errors.New("upload initialization is invalid")
		}
	case FileUploadAck:
		if request.TransferID != envelope.RequestID || request.Sequence == 0 {
			return errors.New("upload acknowledgement is invalid")
		}
	default:
		return fmt.Errorf("unsupported file operation %q", request.Operation)
	}
	return nil
}

func validFileTaskID(value string) bool {
	if len(value) != len("ndt_")+32 || !strings.HasPrefix(value, "ndt_") {
		return false
	}
	for _, character := range value[len("ndt_"):] {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func ValidateFileResponse(envelope Envelope, generation uint64, response FileResponse) error {
	if envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation || !fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence == 0 {
		return errors.New("file response envelope is invalid")
	}
	if envelope.Type != TypeFileResponse || len(response.Message) > 256 || len(response.Entries) > MaxFileEntries || response.Size < 0 || len(response.Text) > base64.StdEncoding.EncodedLen(MaxTextFileBytes) {
		return errors.New("file response is outside its bound")
	}
	switch response.Operation {
	case FileList, FileStat, FileReadText, FileDownload, FileUploadBegin, FileUploadChunk:
	default:
		return errors.New("file response operation is invalid")
	}
	if response.Code != "" {
		switch response.Code {
		case "conflict", "destination_exists", "invalid_path", "limit_exceeded", "directory_too_large", "not_text", "confirmation_required", "not_found", "permission_denied", "digest_mismatch", "transfer_failed", "read_failed", "unavailable", "result_unknown":
		default:
			return errors.New("file response error code is invalid")
		}
	}
	if response.Operation == FileUploadChunk && response.AckSequence == 0 && response.Code == "" || response.Operation != FileUploadChunk && response.AckSequence != 0 {
		return errors.New("file response acknowledgement is invalid")
	}
	if response.Text != "" {
		text, err := base64.StdEncoding.DecodeString(response.Text)
		if err != nil || len(text) > MaxTextFileBytes {
			return errors.New("file response text is invalid")
		}
	}
	return nil
}

func ValidateFileChunkEnvelope(envelope Envelope, generation uint64, chunk FileChunk) error {
	if envelope.Type != TypeFileChunk || envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation || !fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence == 0 || chunk.Sequence != envelope.Sequence || chunk.TransferID != envelope.RequestID {
		return errors.New("file chunk envelope is invalid")
	}
	_, err := DecodeFileChunk(chunk)
	if err != nil {
		return err
	}
	if chunk.Final {
		if !validFileDigest(chunk.SHA256) {
			return errors.New("final file chunk digest is invalid")
		}
	} else if chunk.SHA256 != "" {
		return errors.New("non-final file chunk includes a digest")
	}
	return nil
}

func ValidateFileCancel(envelope Envelope, generation uint64, cancel FileCancel) error {
	if envelope.Type != TypeFileCancel || envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation || !fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence != 0 || cancel.TransferID != envelope.RequestID {
		return errors.New("file cancellation is invalid")
	}
	return nil
}

func validFileDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
