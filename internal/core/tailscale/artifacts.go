package tailscale

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxAgentArtifactSize = 128 << 20

type Artifact struct {
	Bytes     []byte
	SHA256    string
	OS        string
	Arch      string
	Signature []byte
}

type ArtifactLoader interface {
	Load(osName, arch string) (Artifact, error)
}

// DirectoryArtifacts loads a release artifact from NODEDANCE_AGENT_ARTIFACT_DIR
// and verifies its detached Ed25519 signature using the public key configured
// in NODEDANCE_AGENT_SIGNING_PUBLIC_KEY (base64-encoded raw 32-byte key).
type DirectoryArtifacts struct{ Directory, PublicKeyBase64 string }

func (l DirectoryArtifacts) Load(osName, arch string) (Artifact, error) {
	if osName != "linux" {
		return Artifact{}, errors.New("only Linux Agent artifacts are supported")
	}
	if arch != "amd64" && arch != "arm64" {
		return Artifact{}, errors.New("unsupported Linux Agent architecture")
	}
	dir := strings.TrimSpace(l.Directory)
	if dir == "" {
		dir = os.Getenv("NODEDANCE_AGENT_ARTIFACT_DIR")
	}
	keyText := strings.TrimSpace(l.PublicKeyBase64)
	if keyText == "" {
		keyText = os.Getenv("NODEDANCE_AGENT_SIGNING_PUBLIC_KEY")
	}
	if dir == "" || keyText == "" {
		return Artifact{}, errors.New("signed Agent artifacts are not configured on this Core")
	}
	publicKey, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		publicKey, err = base64.RawStdEncoding.DecodeString(keyText)
	}
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Artifact{}, errors.New("configured Agent signing public key is invalid")
	}
	path := filepath.Join(dir, "linux-"+arch, "nodedance-agent")
	info, err := os.Lstat(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("load signed Agent artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > maxAgentArtifactSize {
		return Artifact{}, errors.New("Agent artifact must be a bounded regular non-symlink file")
	}
	binary, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("read Agent artifact: %w", err)
	}
	signaturePath := path + ".sig"
	sigInfo, err := os.Lstat(signaturePath)
	if err != nil || !sigInfo.Mode().IsRegular() || sigInfo.Mode()&os.ModeSymlink != 0 {
		return Artifact{}, errors.New("Agent signature must be a regular non-symlink file")
	}
	signature, err := os.ReadFile(signaturePath)
	if err != nil {
		return Artifact{}, fmt.Errorf("read Agent signature: %w", err)
	}
	if decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature))); decodeErr == nil {
		signature = decoded
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(ed25519.PublicKey(publicKey), binary, signature) {
		return Artifact{}, errors.New("Agent artifact signature verification failed")
	}
	digest := sha256.Sum256(binary)
	return Artifact{Bytes: binary, SHA256: hex.EncodeToString(digest[:]), OS: osName, Arch: arch, Signature: append([]byte(nil), signature...)}, nil
}
