package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
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
	FilePunchProbeRoute   = "/ws/v1/files/punch"
	FileStreamLifetime    = time.Minute
	MaxFileStreamMessage  = 1 << 20
	MaxFilePunchMessage   = 512
	FilePunchUnobservable = "unobservable"
)

const (
	FileTransferModeRelay  = "relay"
	FileTransferModeDirect = "direct"
)

func ValidFileTransferMode(value string) bool {
	return value == FileTransferModeRelay || value == FileTransferModeDirect
}

type FileTransferSpec struct {
	Directory      bool    `json:"directory,omitempty"`
	Mode           string  `json:"mode"`
	SourceNodeID   string  `json:"sourceNodeId"`
	SourcePath     string  `json:"sourcePath"`
	SourceVersion  string  `json:"sourceVersion"`
	BandwidthLimit float64 `json:"bandwidthLimit,omitempty"`
	RetryOfTaskID  string  `json:"retryOfTaskId,omitempty"`
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

func ValidFileBandwidth(value float64) bool {
	return value == 0 || !math.IsInf(value, 0) && value >= 1024.0/1000000 && value <= float64(MaxFileSize)/1000000
}

func FileBandwidthBytes(value float64) int64 {
	return int64(math.Round(value * 1000000))
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
	Directory       bool   `json:"directory,omitempty"`
	Operation       string `json:"operation"`
	Path            string `json:"path"`
	TaskID          string `json:"taskId,omitempty"`
	ExpectedVersion string `json:"expectedVersion,omitempty"`
	Size            int64  `json:"size,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
}

func (value FileDataRequest) FileRequest(id string) FileRequest {
	request := FileRequest{Directory: value.Directory, Operation: value.Operation, Path: value.Path, TaskID: value.TaskID, ExpectedVersion: value.ExpectedVersion, Size: value.Size, SHA256: value.SHA256}
	if value.Operation == FileDownload || value.Operation == FileUploadBegin {
		request.TransferID = id
	}
	return request
}

func FileDataRequestFor(value FileRequest) FileDataRequest {
	return FileDataRequest{Directory: value.Directory, Operation: value.Operation, Path: value.Path, TaskID: value.TaskID, ExpectedVersion: value.ExpectedVersion, Size: value.Size, SHA256: value.SHA256}
}

type FileRsyncRequest struct {
	Operation   string                 `json:"operation"`
	SessionID   string                 `json:"sessionId"`
	Descriptor  FileTransferDescriptor `json:"descriptor"`
	Grant       FileStreamGrant        `json:"grant"`
	Transport   string                 `json:"transport,omitempty"`
	PeerMode    string                 `json:"peerMode,omitempty"`
	SDP         string                 `json:"sdp,omitempty"`
	ListenPort  int                    `json:"listenPort,omitempty"`
	Endpoints   []string               `json:"endpoints,omitempty"`
	Fingerprint string                 `json:"fingerprint,omitempty"`
	Completed   uint64                 `json:"completed,omitempty"`
}

type FileRsyncResponse struct {
	SDP         string   `json:"sdp,omitempty"`
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

type FilePunchObservation struct {
	RemoteAddr string `json:"remoteAddr,omitempty"`
	Error      string `json:"error,omitempty"`
}

func ValidFilePunchObservation(value FilePunchObservation) bool {
	if value.Error != "" {
		return value.RemoteAddr == "" && value.Error == FilePunchUnobservable
	}
	peer, err := netip.ParseAddrPort(value.RemoteAddr)
	if err != nil {
		return false
	}
	addr := peer.Addr()
	return !addr.IsUnspecified() && !addr.IsMulticast() && peer.Port() > 0
}

func ValidateFileRsyncRequest(envelope Envelope, generation uint64, request FileRsyncRequest) error {
	if envelope.Type != TypeFileRsyncRequest || envelope.Version != CurrentVersion || generation == 0 || envelope.Generation != generation ||
		envelope.Sequence != 0 || !fileRequestID.MatchString(envelope.RequestID) || !fileRequestID.MatchString(request.SessionID) || !ValidFileTransferDescriptor(request.Descriptor) ||
		len(request.Endpoints) > 64 || request.Completed > uint64(request.Descriptor.File.Size) ||
		request.ListenPort < 0 || request.ListenPort > 65535 || len(request.SDP) > 64<<10 {
		return errors.New("invalid rsync control request")
	}
	for _, endpoint := range request.Endpoints {
		if !ValidFilePeerEndpoint(endpoint) {
			return errors.New("invalid rsync endpoint")
		}
	}
	switch request.Operation {
	case "prepare", "send", "connect", "start":
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
		if request.Transport == "direct" && !validRsyncPeerFields(request) {
			return errors.New("invalid direct rsync offer")
		}
		if request.Completed != 0 || request.Operation == "prepare" && (len(request.Endpoints) != 0 || request.Fingerprint != "") || request.Transport == "relay" && (len(request.Endpoints) != 0 || request.Fingerprint != "") {
			return errors.New("unexpected rsync offer fields")
		}
		if request.Transport == "relay" && (request.PeerMode != "" || request.SDP != "" || (request.Operation == "connect" || request.Operation == "start")) {
			return errors.New("unexpected rsync peer negotiation")
		}
		if request.ListenPort != 0 && (request.Transport != "direct" || !(request.Operation == "prepare" && request.PeerMode == "tcp") && !(request.Operation == "send" && request.PeerMode == "reverse")) {
			return errors.New("unexpected rsync listen port")
		}
	case "commit":
		if request.Transport != "direct" && request.Transport != "relay" || request.Grant != (FileStreamGrant{}) || len(request.Endpoints) != 0 || request.Fingerprint != "" || request.Completed != 0 || request.ListenPort != 0 || request.PeerMode != "" || request.SDP != "" {
			return errors.New("invalid rsync commit")
		}
	case "abort", "progress":
		if request.Transport != "" || request.Grant != (FileStreamGrant{}) || len(request.Endpoints) != 0 || request.Fingerprint != "" || request.ListenPort != 0 || request.PeerMode != "" || request.SDP != "" || request.Operation == "abort" && request.Completed != 0 {
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
		response.Fingerprint != "" && !validFileDigest(response.Fingerprint) || len(response.SDP) > 64<<10 {
		return errors.New("invalid rsync control response")
	}
	switch response.State {
	case "offer", "signaled", "ready", "progress", "received", "sent", "aborted", "committed", "unavailable", "error":
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

func validRsyncPeerFields(r FileRsyncRequest) bool {
	if r.PeerMode != "tcp" && r.PeerMode != "reverse" && r.PeerMode != "ice" && r.PeerMode != "tcp_punch" {
		return false
	}
	if r.Operation == "start" {
		return r.PeerMode == "tcp_punch" && r.SDP == "" && len(r.Endpoints) == 0 && r.Fingerprint == "" && r.ListenPort == 0
	}
	if r.Operation == "prepare" {
		return r.SDP == "" && len(r.Endpoints) == 0 && r.Fingerprint == ""
	}
	if r.PeerMode == "ice" {
		return r.SDP != "" && len(r.Endpoints) == 0 && r.Fingerprint == "" && r.ListenPort == 0
	}
	if r.SDP != "" {
		return false
	}
	if r.Operation == "send" && r.PeerMode == "reverse" {
		return len(r.Endpoints) == 0 && r.Fingerprint == ""
	}
	return len(r.Endpoints) > 0 && validFileDigest(r.Fingerprint) && (r.Operation != "connect" || r.PeerMode == "reverse" || r.PeerMode == "tcp_punch")
}

func ValidFileRsyncOffer(mode string, endpoints []string, fingerprint, sdp string) bool {
	if mode == "ice" {
		return sdp != "" && len(sdp) <= 64<<10 && len(endpoints) == 0 && fingerprint == ""
	}
	if mode != "tcp" && mode != "reverse" && mode != "tcp_punch" {
		return false
	}
	if len(endpoints) == 0 || len(endpoints) > 64 || !validFileDigest(fingerprint) || sdp != "" {
		return false
	}
	for _, endpoint := range endpoints {
		if !ValidFilePeerEndpoint(endpoint) {
			return false
		}
	}
	return true
}
