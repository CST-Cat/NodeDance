// nodedance-release creates signed Agent release metadata. It never uploads
// releases or receives a signing key through a command-line argument.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	agentupdate "github.com/CST-Cat/NodeDance/internal/agent/update"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nodedance-release:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("expected keygen or sign")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], stdout, stderr)
	case "sign":
		return sign(args[1:], stdout, stderr)
	case "bundle":
		return bundle(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (expected keygen, sign, or bundle)", args[0])
	}
}

func keygen(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("keygen", flag.ContinueOnError)
	f.SetOutput(stderr)
	privatePath := f.String("private-file", "", "private key output path")
	publicPath := f.String("public-file", "", "public key output path")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *privatePath == "" || *publicPath == "" {
		return errors.New("keygen requires --private-file and --public-file")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateData := []byte(base64.StdEncoding.EncodeToString(private))
	publicData := []byte(base64.StdEncoding.EncodeToString(public))
	if err := writeNew(*privatePath, privateData, 0600); err != nil {
		return err
	}
	if err := writeNew(*publicPath, publicData, 0644); err != nil {
		_ = os.Remove(*privatePath)
		return err
	}
	fmt.Fprintf(stdout, "Public key written to %s; protect %s and keep it outside source control.\n", *publicPath, *privatePath)
	return nil
}

func sign(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("sign", flag.ContinueOnError)
	f.SetOutput(stderr)
	binaryPath := f.String("binary", "", "Agent binary path")
	keyPath := f.String("private-key-file", "", "base64 Ed25519 private key path")
	output := f.String("output", "", "manifest JSON output path")
	version := f.String("version", "", "release version")
	architecture := f.String("architecture", "", "linux/amd64 or linux/arm64")
	minProtocol := f.Int("min-protocol", 1, "minimum Agent protocol")
	maxProtocol := f.Int("max-protocol", 1, "maximum Agent protocol")
	coreMin := f.String("core-min", "", "minimum supported Core version")
	coreMax := f.String("core-max", "", "maximum supported Core version")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *binaryPath == "" || *keyPath == "" || *output == "" || *version == "" || *architecture == "" {
		return errors.New("sign requires --binary, --private-key-file, --output, --version, and --architecture")
	}
	if *architecture != "amd64" && *architecture != "arm64" {
		return errors.New("architecture must be amd64 or arm64")
	}
	private, err := readPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*binaryPath)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > agentupdate.MaxArtifactBytes {
		return errors.New("Agent binary size is outside the supported release range")
	}
	manifest, err := agentupdate.Sign(agentupdate.Manifest{FormatVersion: 1, Version: *version, OS: "linux", Architecture: *architecture, SHA256: agentupdate.Digest(data), Size: int64(len(data)), MinProtocol: *minProtocol, MaxProtocol: *maxProtocol, CoreMin: *coreMin, CoreMax: *coreMax}, private)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := writeNew(*output, encoded, 0644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Signed manifest %s for %s (%d bytes).\n", filepath.Base(*output), manifest.Architecture, manifest.Size)
	return nil
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, errors.New("private signing key must be a regular file with mode 0600")
	}
	rawKey, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, err
	}
	if len(rawKey) > 4096 {
		return nil, errors.New("private signing key file is oversized")
	}
	privateBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(rawKey)))
	if err != nil {
		return nil, errors.New("private signing key file must contain base64 Ed25519 key material")
	}
	if len(privateBytes) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(privateBytes), nil
	}
	if len(privateBytes) == ed25519.PrivateKeySize {
		return ed25519.PrivateKey(privateBytes), nil
	}
	return nil, errors.New("private signing key has an invalid size")
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
