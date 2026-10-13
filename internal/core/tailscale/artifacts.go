package tailscale

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
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
	binary, _, err := readBoundedRegularFile(path, maxAgentArtifactSize)
	if err != nil || len(binary) == 0 {
		return Artifact{}, fmt.Errorf("load bounded regular Agent artifact: %w", errors.Join(err, errors.New("invalid Agent artifact")))
	}
	signature, _, err := readBoundedRegularFile(path+".sig", 4096)
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

// Check and read the same descriptor so replacements cannot bypass the bound
// or turn a validated regular file into a symlink or blocking device.
func readBoundedRegularFile(path string, limit int64) ([]byte, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, nil, errors.New("file must be a bounded regular non-symlink file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > limit {
		return nil, nil, errors.New("file exceeds size limit")
	}
	return data, info, nil
}
