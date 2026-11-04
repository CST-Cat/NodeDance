package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	TypeFileRsyncRequest  = "file_rsync_request"
	TypeFileRsyncResponse = "file_rsync_response"
	FileOpenData          = "open_data"
	FileRsyncDataRoute    = "/ws/v1/files/rsync"
	FileChunkDataRoute    = "/ws/v1/files/chunks"
	FileStreamLifetime    = time.Minute
	MaxFileStreamMessage  = 1 << 20
)

const (
	FileTransferModeRelay  = "relay"
	FileTransferModeDirect = "direct"
)

func ValidFileTransferMode(value string) bool {
	return value == FileTransferModeRelay || value == FileTransferModeDirect
}

type FileTransferSpec struct {
	Mode           string `json:"mode"`
	SourceNodeID   string `json:"sourceNodeId"`
	SourcePath     string `json:"sourcePath"`
	SourceVersion  string `json:"sourceVersion"`
	BandwidthLimit int64  `json:"bandwidthLimit,omitempty"`
	RetryOfTaskID  string `json:"retryOfTaskId,omitempty"`
}

type FileTransferDescriptor struct {
	TaskID       string       `json:"taskId"`
	TargetNodeID string       `json:"targetNodeId"`
	File         FileTaskSpec `json:"file"`
}

type FileStreamGrant struct {
	SessionID string    `json:"sessionId"`
	TaskID    string    `json:"taskId,omitempty"`
	NodeID    string    `json:"nodeId"`
	Role      string    `json:"role"`
	Path      string    `json:"path"`
	Binding   string    `json:"binding"`
	ExpiresAt time.Time `json:"expiresAt"`
	Secret    string    `json:"secret"`
}

func FileStreamDigest(value any) string {
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidFileBandwidth(value int64) bool {
	return value == 0 || value >= 1024 && value <= MaxFileSize
}

func ValidFileTransferDescriptor(value FileTransferDescriptor) bool {
	return validFileTaskID(value.TaskID) && fileRequestID.MatchString(value.TargetNodeID) && value.File.Transfer != nil &&
		ValidateTaskIntent(TaskIntent{Action: TaskFileUpload, ContainerID: FileTargetKey(value.File.Path), File: &value.File}) == nil
}

func ValidFileStreamGrant(grant FileStreamGrant, now time.Time) bool {
	return fileRequestID.MatchString(grant.SessionID) && (grant.TaskID == "" || validFileTaskID(grant.TaskID)) &&
		fileRequestID.MatchString(grant.NodeID) && (grant.Role == "sender" || grant.Role == "receiver") &&
		ValidVirtualFilePath(grant.Path) && validFileDigest(grant.Binding) && validFileDigest(grant.Secret) &&
		grant.ExpiresAt.After(now) && !grant.ExpiresAt.After(now.Add(2*FileStreamLifetime))
}

func (grant FileStreamGrant) Headers() http.Header {
	return http.Header{
		"Authorization":         {"Task " + grant.Secret},
		"X-Nodedance-Stream":    {grant.SessionID},
		"X-Nodedance-Task":      {grant.TaskID},
		"X-Nodedance-Node":      {grant.NodeID},
		"X-Nodedance-Direction": {grant.Role},
		"X-Nodedance-Binding":   {grant.Binding},
	}
}

func (grant FileStreamGrant) MatchesHeaders(headers http.Header) bool {
	return headers.Get("X-NodeDance-Stream") == grant.SessionID && headers.Get("X-NodeDance-Task") == grant.TaskID &&
		headers.Get("X-NodeDance-Node") == grant.NodeID && headers.Get("X-NodeDance-Direction") == grant.Role &&
		headers.Get("X-NodeDance-Binding") == grant.Binding
}

type FileDataOpen struct {
	Grant   FileStreamGrant `json:"grant"`
	Request FileDataRequest `json:"request"`
}

type FileDataRequest struct {
	Operation       string `json:"operation"`
	Path            string `json:"path"`
	TaskID          string `json:"taskId,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	Size            int64  `json:"size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

func (value FileDataRequest) FileRequest(id string) FileRequest {
	request := FileRequest{Operation: value.Operation, Path: value.Path, TaskID: value.TaskID, ExpectedVersion: value.ExpectedVersion, Size: value.Size, SHA256: value.SHA256}
	if value.Operation == FileDownload || value.Operation == FileUploadBegin {
		request.TransferID = id
	}
	return request
}

func FileDataRequestFor(value FileRequest) FileDataRequest {
	return FileDataRequest{Operation: value.Operation, Path: value.Path, TaskID: value.TaskID, ExpectedVersion: value.ExpectedVersion, Size: value.Size, SHA256: value.SHA256}
}

type FileRsyncRequest struct {
	Operation   string                 `json:"operation"`
	SessionID   string                 `json:"sessionId"`
	Descriptor  FileTransferDescriptor `json:"descriptor"`
	Grant       FileStreamGrant        `json:"grant"`
	Transport   string                 `json:"transport,omitempty"`
	Endpoints   []string               `json:"endpoints,omitempty"`
	Fingerprint string                 `json:"fingerprint,omitempty"`
	Completed   uint64                 `json:"completed,omitempty"`
}

type FileRsyncResponse struct {
	Operation   string   `json:"operation"`
	SessionID   string   `json:"sessionId"`
	State       string   `json:"state"`
	Code        string   `json:"code,omitempty"`
	Endpoints   []string `json:"endpoints,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Completed   uint64   `json:"completed,omitempty"`
}

func ValidFilePeerEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "wss" || parsed.Path != FileRsyncDataRoute || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	port, err := strconv.Atoi(parsed.Port())
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && err == nil && port > 0 && port <= 65535
}

func ValidateFileRsyncRequest(envelope Envelope, generation uint64, request FileRsyncRequest) error {
	if envelope.Type != TypeFileRsyncRequest || envelope.Version != CurrentVersion || generation == 0 || envelope.Generation != generation ||
		envelope.Sequence != 0 || !fileRequestID.MatchString(envelope.RequestID) || !fileRequestID.MatchString(request.SessionID) || !ValidFileTransferDescriptor(request.Descriptor) ||
		len(request.Endpoints) > 64 || request.Completed > uint64(request.Descriptor.File.Size) {
		return errors.New("invalid rsync control request")
	}
	for _, endpoint := range request.Endpoints {
		if !ValidFilePeerEndpoint(endpoint) {
			return errors.New("invalid rsync endpoint")
		}
	}
	switch request.Operation {
	case "prepare", "send":
		if request.Transport != "direct" && request.Transport != "relay" || !ValidFileStreamGrant(request.Grant, time.Now()) ||
			request.Grant.SessionID != request.SessionID || request.Grant.TaskID != request.Descriptor.TaskID || request.Grant.Binding != FileStreamDigest(request.Descriptor) ||
			request.Grant.Path != request.Descriptor.File.Path {
			return errors.New("invalid rsync task credential")
		}
		if request.Operation == "prepare" && request.Transport == "relay" {
			if request.Grant.Role != "receiver" || request.Grant.NodeID != request.Descriptor.TargetNodeID {
				return errors.New("invalid rsync receiver credential")
			}
		} else if request.Grant.Role != "sender" || request.Grant.NodeID != request.Descriptor.File.Transfer.SourceNodeID {
			return errors.New("invalid rsync sender credential")
		}
		if request.Operation == "send" && request.Transport == "direct" && (len(request.Endpoints) == 0 || !validFileDigest(request.Fingerprint)) {
			return errors.New("invalid direct rsync offer")
		}
		if request.Completed != 0 || request.Operation == "prepare" && (len(request.Endpoints) != 0 || request.Fingerprint != "") || request.Transport == "relay" && (len(request.Endpoints) != 0 || request.Fingerprint != "") {
			return errors.New("unexpected rsync offer fields")
		}
	case "commit":
		if request.Transport != "direct" && request.Transport != "relay" || request.Grant != (FileStreamGrant{}) || len(request.Endpoints) != 0 || request.Fingerprint != "" || request.Completed != 0 {
			return errors.New("invalid rsync commit")
		}
	case "abort", "progress":
		if request.Transport != "" || request.Grant != (FileStreamGrant{}) || len(request.Endpoints) != 0 || request.Fingerprint != "" || request.Operation == "abort" && request.Completed != 0 {
			return errors.New("unexpected rsync control fields")
		}
	default:
		return errors.New("unsupported rsync control request")
	}
	return nil
}

func ValidateFileRsyncResponse(envelope Envelope, generation uint64, response FileRsyncResponse) error {
	if envelope.Type != TypeFileRsyncResponse || envelope.Version != CurrentVersion || generation == 0 || envelope.Generation != generation || envelope.Sequence != 0 ||
		!fileRequestID.MatchString(envelope.RequestID) || !fileRequestID.MatchString(response.SessionID) || len(response.Endpoints) > 64 ||
		response.Fingerprint != "" && !validFileDigest(response.Fingerprint) {
		return errors.New("invalid rsync control response")
	}
	switch response.State {
	case "ready", "progress", "received", "sent", "aborted", "committed", "unavailable", "error":
	default:
		return errors.New("invalid rsync response state")
	}
	if len(response.Code) > 64 {
		return errors.New("invalid rsync error")
	}
	for _, endpoint := range response.Endpoints {
		if !ValidFilePeerEndpoint(endpoint) {
			return errors.New("invalid rsync response endpoint")
		}
	}
	return nil
}
