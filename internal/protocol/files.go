package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	CapabilityFiles       = "agent.files.v1"
	CapabilityFileJournal = "agent.file-journal.v1"

	TypeFileRequest      = "file_request"
	TypeFileResponse     = "file_response"
	TypeFileChunk        = "file_chunk"
	TypeFileCancel       = "file_cancel"
	TypeFileCancelAck    = "file_cancel_ack"
	TypeFileJournalQuery = "file_journal_query"
	TypeFileJournalReply = "file_journal_reply"

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
	FileDownload     = "download"
	FileDownloadAck  = "download_ack"

	MaxFileControlBytes = 64 << 10
	MaxFileJournalTasks = 128
	MaxFileChunkBytes   = 32 << 10
	DefaultFileLimit    = int64(1 << 30)
	MaxFileSize         = int64(16 << 30)
	MaxFileEntries      = 100
	MaxTextFileBytes    = 32 << 10
)

var fileRequestID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// FileRequest is a typed request sent by Core after authenticating the
// administrator Session and binding the transfer to a node generation.
// Data is present only for bounded file chunks, never for a whole file.
type FileRequest struct {
	Operation       string `json:"operation"`
	Path            string `json:"path,omitempty"`
	NewPath         string `json:"newPath,omitempty"`
	TransferID      string `json:"transferId,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	Confirmed       bool   `json:"confirmed,omitempty"`
	ExpectedSize    int64  `json:"expectedSize,omitempty"`
	ExpectedSHA256  string `json:"expectedSha256,omitempty"`
	Sequence        uint64 `json:"sequence,omitempty"`
	Data            string `json:"data,omitempty"`
	Text            string `json:"text,omitempty"`
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
	Operation  string      `json:"operation"`
	Code       string      `json:"code,omitempty"`
	Message    string      `json:"message,omitempty"`
	Path       string      `json:"path,omitempty"`
	Version    string      `json:"version,omitempty"`
	Size       int64       `json:"size,omitempty"`
	SHA256     string      `json:"sha256,omitempty"`
	Entries    []FileEntry `json:"entries,omitempty"`
	Entry      *FileEntry  `json:"entry,omitempty"`
	Text       string      `json:"text,omitempty"`
	BackupPath string      `json:"backupPath,omitempty"`
	Completed  int64       `json:"completed,omitempty"`
	Final      bool        `json:"final,omitempty"`
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

type FileCancelAck struct {
	TransferID string `json:"transferId"`
	Canceled   bool   `json:"canceled"`
}

// FileJournalQuery asks an authenticated Core to reconcile only the listed
// durable write IDs. It contains no paths or file content.
type FileJournalQuery struct {
	TaskIDs []string `json:"taskIds"`
}

type FileJournalResult struct {
	TaskID     string `json:"taskId"`
	Status     string `json:"status"`
	ResultCode string `json:"resultCode"`
}

type FileJournalReply struct {
	Results []FileJournalResult `json:"results"`
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
	if strings.ContainsRune(request.Path, '\x00') || strings.ContainsRune(request.NewPath, '\x00') || len(request.Path) > 4096 || len(request.NewPath) > 4096 || len(request.ExpectedVersion) > 128 {
		return errors.New("file request path or version is invalid")
	}
	switch request.Operation {
	case FileList, FileStat, FileReadText, FileDownload:
		if request.Path == "" {
			return errors.New("file path is required")
		}
		if request.Operation == FileDownload && request.TransferID != envelope.RequestID {
			return errors.New("download transfer identity is invalid")
		}
	case FileMkdir:
		if request.Path == "" {
			return errors.New("directory path is required")
		}
	case FileRename:
		if request.Path == "" || request.NewPath == "" {
			return errors.New("source and destination paths are required")
		}
	case FileDelete:
		if request.Path == "" || !request.Confirmed {
			return errors.New("file deletion requires exact-target confirmation")
		}
	case FileSaveText:
		if request.Path == "" || request.ExpectedVersion == "" || len(request.Text) > base64.StdEncoding.EncodedLen(MaxTextFileBytes) || request.TransferID != "" {
			return errors.New("text save request is invalid")
		}
		decoded, err := base64.StdEncoding.DecodeString(request.Text)
		if err != nil || len(decoded) > MaxTextFileBytes {
			return errors.New("text save content is invalid")
		}
	case FileUploadBegin:
		if request.Path == "" || request.TransferID != envelope.RequestID || request.ExpectedSize < 0 || request.ExpectedSize > MaxFileSize || request.ExpectedSHA256 != "" && !validFileDigest(request.ExpectedSHA256) {
			return errors.New("upload request is invalid")
		}
	case FileUploadChunk:
		if request.TransferID != envelope.RequestID || request.Sequence == 0 || len(request.Data) > base64.StdEncoding.EncodedLen(MaxFileChunkBytes) {
			return errors.New("upload chunk is invalid")
		}
		data, err := base64.StdEncoding.DecodeString(request.Data)
		if err != nil || len(data) > MaxFileChunkBytes {
			return errors.New("upload chunk data is invalid")
		}
	case FileUploadCommit:
		if request.TransferID != envelope.RequestID || request.ExpectedSize < 0 || !validFileDigest(request.ExpectedSHA256) {
			return errors.New("upload commit request is invalid")
		}
	case FileDownloadAck:
		if request.TransferID != envelope.RequestID || request.Sequence == 0 {
			return errors.New("download acknowledgement is invalid")
		}
	default:
		return fmt.Errorf("unsupported file operation %q", request.Operation)
	}
	return nil
}

func ValidateFileResponse(envelope Envelope, generation uint64, response FileResponse) error {
	if envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation || !fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence == 0 {
		return errors.New("file response envelope is invalid")
	}
	if envelope.Type != TypeFileResponse || len(response.Message) > 256 || len(response.Entries) > MaxFileEntries || response.Size < 0 || response.Completed < 0 || len(response.Text) > base64.StdEncoding.EncodedLen(MaxTextFileBytes) || len(response.BackupPath) > 4096 {
		return errors.New("file response is outside its bound")
	}
	switch response.Operation {
	case FileList, FileStat, FileMkdir, FileRename, FileDelete, FileReadText, FileSaveText, FileUploadBegin, FileUploadChunk, FileUploadCommit, FileDownload:
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

func ValidateFileCancelAck(envelope Envelope, generation uint64, ack FileCancelAck) error {
	if envelope.Type != TypeFileCancelAck || envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation ||
		!fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence != 0 || ack.TransferID != envelope.RequestID {
		return errors.New("file cancellation acknowledgment is invalid")
	}
	return nil
}

func ValidateFileJournalQuery(envelope Envelope, generation uint64, query FileJournalQuery) error {
	if envelope.Type != TypeFileJournalQuery || envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation ||
		!fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence != 0 || len(query.TaskIDs) == 0 || len(query.TaskIDs) > MaxFileJournalTasks {
		return errors.New("file journal query envelope is invalid")
	}
	seen := make(map[string]struct{}, len(query.TaskIDs))
	for _, id := range query.TaskIDs {
		if !fileRequestID.MatchString(id) {
			return errors.New("file journal query contains an invalid task ID")
		}
		if _, ok := seen[id]; ok {
			return errors.New("file journal query contains duplicate task IDs")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func ValidateFileJournalReply(envelope Envelope, generation uint64, reply FileJournalReply) error {
	if envelope.Type != TypeFileJournalReply || envelope.Version != CurrentVersion || envelope.Generation == 0 || envelope.Generation != generation ||
		!fileRequestID.MatchString(envelope.RequestID) || envelope.Sequence != 0 || len(reply.Results) > MaxFileJournalTasks {
		return errors.New("file journal reply envelope is invalid")
	}
	seen := make(map[string]struct{}, len(reply.Results))
	for _, result := range reply.Results {
		if !fileRequestID.MatchString(result.TaskID) {
			return errors.New("file journal reply contains an invalid task ID")
		}
		if _, ok := seen[result.TaskID]; ok {
			return errors.New("file journal reply contains duplicate task IDs")
		}
		seen[result.TaskID] = struct{}{}
		switch result.Status {
		case "succeeded":
			if result.ResultCode != "verified" && result.ResultCode != "state_verified" {
				return errors.New("file journal success result code is invalid")
			}
		case "failed":
			if result.ResultCode != "agent_rejected" && result.ResultCode != "not_committed" {
				return errors.New("file journal failure result code is invalid")
			}
		case "canceled":
			if result.ResultCode != "cancel_confirmed" {
				return errors.New("file journal cancellation result code is invalid")
			}
		case "unknown":
			if result.ResultCode != "result_pending" && result.ResultCode != "mutation_uncertain" {
				return errors.New("file journal pending result code is invalid")
			}
		default:
			return errors.New("file journal reply status is invalid")
		}
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
