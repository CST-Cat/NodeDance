package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const maxBundleBinaryBytes = 256 << 20

var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type bundleManifest struct {
	FormatVersion int    `json:"formatVersion"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	CoreSHA256    string `json:"coreSha256"`
	CoreSize      int64  `json:"coreSize"`
	AgentSHA256   string `json:"agentSha256"`
	AgentSize     int64  `json:"agentSize"`
	Signature     string `json:"signature"`
}

type bundleSignedFields struct {
	FormatVersion int    `json:"formatVersion"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Architecture  string `json:"architecture"`
	CoreSHA256    string `json:"coreSha256"`
	CoreSize      int64  `json:"coreSize"`
	AgentSHA256   string `json:"agentSha256"`
	AgentSize     int64  `json:"agentSize"`
}

func (m bundleManifest) signingBytes() ([]byte, error) {
	return json.Marshal(bundleSignedFields{m.FormatVersion, m.Version, m.OS, m.Architecture,
		m.CoreSHA256, m.CoreSize, m.AgentSHA256, m.AgentSize})
}

func (m bundleManifest) validate() error {
	if m.FormatVersion != 1 || !releaseVersionPattern.MatchString(m.Version) {
		return errors.New("bundle version or format is invalid")
	}
	if m.OS != "linux" || (m.Architecture != "amd64" && m.Architecture != "arm64") {
		return errors.New("release bundle must target linux/amd64 or linux/arm64")
	}
	for _, item := range []struct {
		name   string
		digest string
		size   int64
	}{{"Core", m.CoreSHA256, m.CoreSize}, {"Agent", m.AgentSHA256, m.AgentSize}} {
		if len(item.digest) != sha256.Size*2 || item.size < 1 || item.size > maxBundleBinaryBytes {
			return fmt.Errorf("%s bundle digest or size is invalid", item.name)
		}
		if _, err := hex.DecodeString(item.digest); err != nil || strings.ToLower(item.digest) != item.digest {
			return fmt.Errorf("%s bundle digest is invalid", item.name)
		}
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("bundle signature is malformed")
	}
	return nil
}

func (m bundleManifest) verify(key ed25519.PublicKey) error {
	if err := m.validate(); err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize {
		return errors.New("bundle trust key has an invalid size")
	}
	signature, _ := base64.StdEncoding.DecodeString(m.Signature)
	message, err := m.signingBytes()
	if err != nil || !ed25519.Verify(key, message, signature) {
		return errors.New("bundle signature verification failed")
	}
	return nil
}

func bundle(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("bundle", flag.ContinueOnError)
	f.SetOutput(stderr)
	corePath := f.String("core", "", "Linux Core binary path")
	agentPath := f.String("agent", "", "Linux Agent binary path")
	keyPath := f.String("private-key-file", "", "base64 Ed25519 private key path")
	output := f.String("output", "", "new .tar.gz bundle output path")
	version := f.String("version", "", "release version")
	architecture := f.String("architecture", "", "linux/amd64 or linux/arm64")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *corePath == "" || *agentPath == "" || *keyPath == "" || *output == "" || *version == "" || *architecture == "" {
		return errors.New("bundle requires --core, --agent, --private-key-file, --output, --version, and --architecture")
	}
	if *architecture != "amd64" && *architecture != "arm64" {
		return errors.New("architecture must be amd64 or arm64")
	}
	if !releaseVersionPattern.MatchString(*version) {
		return errors.New("version must be a semantic release version, optionally prefixed with v")
	}
	private, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	core, err := readReleaseBinary(*corePath)
	if err != nil {
		return fmt.Errorf("read Core binary: %w", err)
	}
	agent, err := readReleaseBinary(*agentPath)
	if err != nil {
		return fmt.Errorf("read Agent binary: %w", err)
	}
	if err := validateReleaseELF(core, *architecture); err != nil {
		return fmt.Errorf("Core binary does not match linux/%s: %w", *architecture, err)
	}
	if err := validateReleaseELF(agent, *architecture); err != nil {
		return fmt.Errorf("Agent binary does not match linux/%s: %w", *architecture, err)
	}
	manifest := bundleManifest{
		FormatVersion: 1, Version: *version, OS: "linux", Architecture: *architecture,
		CoreSHA256: digestHex(core), CoreSize: int64(len(core)), AgentSHA256: digestHex(agent), AgentSize: int64(len(agent)),
		Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
	}
	if err := manifest.validate(); err != nil {
		return err
	}
	message, err := manifest.signingBytes()
	if err != nil {
		return err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, message))
	encodedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encodedManifest = append(encodedManifest, '\n')
	if err := writeBundle(*output, encodedManifest, core, agent); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Signed NodeDance %s linux/%s release bundle (%d bytes of binaries).\n", *version, *architecture, len(core)+len(agent))
	return nil
}

func validateReleaseELF(data []byte, architecture string) error {
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return errors.New("binary is not a valid ELF executable")
	}
	defer file.Close()
	if file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN {
		return errors.New("ELF file is not executable or position-independent")
	}
	want := elf.EM_X86_64
	if architecture == "arm64" {
		want = elf.EM_AARCH64
	}
	if file.Machine != want {
		return fmt.Errorf("ELF machine is %s, expected %s", file.Machine, want)
	}
	return nil
}

func readReleaseBinary(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maxBundleBinaryBytes {
		return nil, errors.New("binary must be a regular non-symlink file within the supported size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBundleBinaryBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() || len(data) > maxBundleBinaryBytes {
		return nil, errors.New("binary changed while being read or exceeded the supported size limit")
	}
	return data, nil
}

func digestHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func writeBundle(path string, manifest, core, agent []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	gz := gzip.NewWriter(file)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tarWriter := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"manifest.json", manifest}, {"nodedance", core}, {"nodedance-agent", agent}} {
		header := &tar.Header{Name: entry.name, Mode: 0o755, Size: int64(len(entry.data)), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR, ModTime: time.Unix(0, 0).UTC()}
		if entry.name == "manifest.json" {
			header.Mode = 0o644
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = tarWriter.Close()
			_ = gz.Close()
			_ = file.Close()
			return err
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			_ = tarWriter.Close()
			_ = gz.Close()
			_ = file.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gz.Close()
		_ = file.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	removeOnError = false
	return nil
}
