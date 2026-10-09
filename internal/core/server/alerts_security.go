package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const alertEncryptionKeyName = "alert-channel-encryption.key"

func loadOrCreateAlertEncryptionKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, alertEncryptionKeyName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("alert encryption key must be a regular file")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure alert encryption key: %w", err)
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read alert encryption key: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("invalid alert encryption key length")
		}
		return key, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect alert encryption key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate alert encryption key: %w", err)
	}
	tmp, err := os.CreateTemp(dataDir, ".alert-encryption-key-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary alert encryption key: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return loadOrCreateAlertEncryptionKey(dataDir)
		}
		return nil, fmt.Errorf("install alert encryption key: %w", err)
	}
	if err := syncDirectory(dataDir); err != nil {
		return nil, err
	}
	return key, nil
}
