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
