package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"runtime"
	"testing"
)

func TestSignedManifestAuthenticatesMetadataAndCompatibility(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{FormatVersion: 1, Version: "1.0.1", OS: "linux", Architecture: runtime.GOARCH, SHA256: Digest([]byte("fixture")), Size: 7, MinProtocol: 1, MaxProtocol: 1, CoreMin: "1.0.0"}
	signed, err := Sign(m, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.Verify(public); err != nil {
		t.Fatalf("signed manifest did not verify: %v", err)
	}
	if err := RuntimeCompatibility(signed, "1.0.0", "1.0.0", 1); err != nil {
		t.Fatalf("compatible release was rejected: %v", err)
	}
	tampered := signed
	tampered.Version = "1.0.2"
	if err := tampered.Verify(public); err == nil {
		t.Fatal("tampered manifest verified")
	}
	if err := RuntimeCompatibility(signed, "1.0.0", "1.0.0", 2); err == nil {
		t.Fatal("incompatible protocol was accepted")
	}
	wrongArch := signed
	if runtime.GOARCH == "amd64" {
		wrongArch.Architecture = "arm64"
	} else {
		wrongArch.Architecture = "amd64"
	}
	if err := RuntimeCompatibility(wrongArch, "1.0.0", "1.0.0", 1); err == nil {
		t.Fatal("wrong architecture was accepted")
	}
}
