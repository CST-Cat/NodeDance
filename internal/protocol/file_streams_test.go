package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestFileStreamCredentialsBindTaskNodePathDirectionAndDescriptor(t *testing.T) {
	id := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	node := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	initial := FileDataRequest{Operation: FileDownload, Path: "/source"}
	grant := FileStreamGrant{SessionID: id, NodeID: node, Role: "sender", Path: initial.Path, Binding: FileStreamDigest(initial), ExpiresAt: time.Now().Add(FileStreamLifetime), Secret: strings.Repeat("c", 64)}
	envelope := Envelope{Version: CurrentVersion, Type: TypeFileRequest, Generation: 1, RequestID: id}
	request := FileRequest{Operation: FileOpenData, Data: &FileDataOpen{Grant: grant, Request: initial}}
	if err := ValidateFileRequest(envelope, 1, request); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FileDataOpen){
		func(v *FileDataOpen) { v.Grant.Role = "receiver" },
		func(v *FileDataOpen) { v.Grant.Path = "/different" },
		func(v *FileDataOpen) { v.Request.Path = "/different" },
		func(v *FileDataOpen) { v.Grant.SessionID = node },
		func(v *FileDataOpen) { v.Grant.ExpiresAt = time.Now().Add(-time.Second) },
		func(v *FileDataOpen) { v.Grant.Binding = strings.Repeat("d", 64) },
	} {
		modified := *request.Data
		change(&modified)
		if ValidateFileRequest(envelope, 1, FileRequest{Operation: FileOpenData, Data: &modified}) == nil {
			t.Fatalf("accepted changed file data binding: %+v", modified)
		}
	}
	headers := grant.Headers()
	if !grant.MatchesHeaders(headers) {
		t.Fatal("generated headers do not match the grant")
	}
	for _, name := range []string{"X-NodeDance-Stream", "X-NodeDance-Task", "X-NodeDance-Node", "X-NodeDance-Direction", "X-NodeDance-Binding"} {
		changed := headers.Clone()
		changed.Set(name, "forged")
		if grant.MatchesHeaders(changed) {
			t.Fatal("accepted forged header", name)
		}
	}
}

func TestFilePeerOffersRejectRedirectsCredentialsAndPathEscapes(t *testing.T) {
	if !ValidFilePeerEndpoint("wss://127.0.0.1:12345"+FileRsyncDataRoute) || !ValidFilePeerEndpoint("wss://[::1]:23456"+FileRsyncDataRoute) {
		t.Fatal("literal peer endpoints were rejected")
	}
	for _, endpoint := range []string{
		"ws://127.0.0.1:12345" + FileRsyncDataRoute,
		"wss://example.invalid:12345" + FileRsyncDataRoute,
		"wss://127.0.0.1:12345" + FileRsyncDataRoute + "?ticket=secret",
		"wss://secret@127.0.0.1:12345" + FileRsyncDataRoute,
		"wss://127.0.0.1:12345/../" + FileRsyncDataRoute,
		"wss://0.0.0.0:12345" + FileRsyncDataRoute,
	} {
		if ValidFilePeerEndpoint(endpoint) {
			t.Fatal("accepted unsafe endpoint", endpoint)
		}
	}
}

func TestRsyncPeerNegotiationRemainsBoundToTask(t *testing.T) {
	source := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	target := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	descriptor := FileTransferDescriptor{TaskID: "ndt_" + strings.Repeat("c", 32), TargetNodeID: target, File: FileTaskSpec{Path: "/out/data", Size: 42, SHA256: strings.Repeat("a", 64), Transfer: &FileTransferSpec{Mode: "direct", SourceNodeID: source, SourcePath: "/data", SourceVersion: "original"}}}
	grant := FileStreamGrant{SessionID: source, TaskID: descriptor.TaskID, NodeID: source, Role: "sender", Path: descriptor.File.Path, Binding: FileStreamDigest(descriptor), ExpiresAt: time.Now().Add(FileStreamLifetime), Secret: strings.Repeat("d", 64)}
	request := FileRsyncRequest{Operation: "connect", SessionID: source, Descriptor: descriptor, Grant: grant, Transport: "direct", PeerMode: "ice", SDP: "v=0\r\n"}
	envelope := Envelope{Version: CurrentVersion, Type: TypeFileRsyncRequest, Generation: 1, RequestID: target}
	if err := ValidateFileRsyncRequest(envelope, 1, request); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*FileRsyncRequest){
		func(r *FileRsyncRequest) { r.Descriptor.File.Size++ },
		func(r *FileRsyncRequest) { r.Grant.Role = "receiver" },
		func(r *FileRsyncRequest) { r.SDP = strings.Repeat("x", (64<<10)+1) },
		func(r *FileRsyncRequest) { r.Operation = "commit" },
		func(r *FileRsyncRequest) { r.Transport = "relay" },
		func(r *FileRsyncRequest) { r.SessionID = target },
	} {
		modified := request
		change(&modified)
		if ValidateFileRsyncRequest(envelope, 1, modified) == nil {
			t.Fatal("accepted unbound peer negotiation")
		}
	}
	if ValidateFileRsyncRequest(envelope, 2, request) == nil {
		t.Fatal("accepted obsolete Agent generation")
	}
	request.PeerMode, request.SDP = "tcp_punch", ""
	request.Endpoints = []string{"wss://127.0.0.1:23456" + FileRsyncDataRoute}
	request.Fingerprint = strings.Repeat("e", 64)
	if err := ValidateFileRsyncRequest(envelope, 1, request); err != nil {
		t.Fatal("TCP punch signaling rejected", err)
	}
	for _, change := range []func(*FileRsyncRequest){
		func(r *FileRsyncRequest) { r.Fingerprint = "" },
		func(r *FileRsyncRequest) { r.Endpoints = nil },
		func(r *FileRsyncRequest) { r.Endpoints = []string{"wss://example.invalid:1234" + FileRsyncDataRoute} },
		func(r *FileRsyncRequest) { r.Grant.Secret = "" },
		func(r *FileRsyncRequest) { r.Descriptor.File.Size++ },
		func(r *FileRsyncRequest) { r.ListenPort = 22 },
		func(r *FileRsyncRequest) { r.SDP = "unexpected" },
	} {
		modified := request
		change(&modified)
		if ValidateFileRsyncRequest(envelope, 1, modified) == nil {
			t.Fatal("accepted unbound TCP punch signaling")
		}
	}
}
