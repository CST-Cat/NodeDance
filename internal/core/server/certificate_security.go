package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const certificateEncryptionKeyName = "certificate-encryption.key"

// loadOrCreateCertificateEncryptionKey returns the 32-byte key that seals DNS
// provider tokens, certificate private keys and ACME account keys at rest. The
// key lives in the private data directory next to the SQLite database, mirroring
// the alert-channel key.
func loadOrCreateCertificateEncryptionKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, certificateEncryptionKeyName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("certificate encryption key must be a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure certificate encryption key: %w", err)
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read certificate encryption key: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("invalid certificate encryption key length")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect certificate encryption key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate certificate encryption key: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".certificate-encryption-key-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary certificate encryption key: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("secure certificate encryption key: %w", err)
	}
	if _, err := tmp.Write(key); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write certificate encryption key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("sync certificate encryption key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close certificate encryption key: %w", err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateCertificateEncryptionKey(dataDir)
		}
		return nil, fmt.Errorf("install certificate encryption key: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil {
		return nil, err
	}
	return key, nil
}
