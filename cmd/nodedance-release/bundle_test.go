package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundleManifestSignatureBindsBothBinariesAndArchitecture(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	core, agent := []byte("core bytes"), []byte("agent bytes")
	manifest := bundleManifest{
		FormatVersion: 1,
		Version:       "v1.2.3",
		OS:            "linux",
		Architecture:  "arm64",
		CoreSHA256:    digestHex(core),
		CoreSize:      int64(len(core)),
		AgentSHA256:   digestHex(agent),
		AgentSize:     int64(len(agent)),
		Signature:     base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
	}
	message, err := manifest.signingBytes()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, message))
	if err := manifest.verify(public); err != nil {
		t.Fatalf("valid signed bundle manifest rejected: %v", err)
	}
	manifest.AgentSHA256 = digestHex([]byte("substituted Agent"))
	if err := manifest.verify(public); err == nil {
		t.Fatal("manifest accepted a changed Agent digest")
	}
	manifest.AgentSHA256 = digestHex(agent)
	manifest.Architecture = "386"
	if err := manifest.verify(public); err == nil {
		t.Fatal("manifest accepted an unsupported architecture")
	}
}

func TestValidateReleaseELFChecksExecutableMachine(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hostArchitecture := runtime.GOARCH
	otherArchitecture := "amd64"
	if hostArchitecture == "amd64" {
		otherArchitecture = "arm64"
	}
	if err := validateReleaseELF(data, hostArchitecture); err != nil {
		t.Fatalf("current test executable rejected for %s: %v", hostArchitecture, err)
	}
	if err := validateReleaseELF(data, otherArchitecture); err == nil {
		t.Fatalf("accepted current test executable as %s", otherArchitecture)
	}
	if err := validateReleaseELF([]byte("not ELF"), hostArchitecture); err == nil {
		t.Fatal("accepted a non-ELF binary")
	}
}

func TestWriteBundleUsesOnlyExpectedRegularEntriesAndNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.tar.gz")
	manifest, core, agent := []byte(`{"formatVersion":1}`), []byte("core"), []byte("agent")
	if err := writeBundle(path, manifest, core, agent); err != nil {
		t.Fatal(err)
	}
	if err := writeBundle(path, manifest, core, agent); err == nil {
		t.Fatal("bundle writer replaced an existing output")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	want := []struct {
		name string
		data []byte
	}{{"manifest.json", manifest}, {"nodedance", core}, {"nodedance-agent", agent}}
	for _, item := range want {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read %s header: %v", item.name, err)
		}
		if header.Name != item.name || header.Typeflag != tar.TypeReg || header.Size != int64(len(item.data)) {
			t.Fatalf("unexpected archive entry: %#v", header)
		}
		actual, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read %s content: %v", item.name, err)
		}
		if !bytes.Equal(actual, item.data) {
			t.Fatalf("%s content changed", item.name)
		}
	}
	if _, err := reader.Next(); err != io.EOF {
		t.Fatalf("bundle contains an extra entry: %v", err)
	}
}
