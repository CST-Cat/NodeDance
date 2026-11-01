//go:build linux

// Package rsyncbundle supplies the complete upstream rsync executable,
// trimmed at build time to the transfer feature's needs (see
// scripts/build-rsync.sh). Assets are embedded per architecture, including
// the upstream license.
package rsyncbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const Version = "3.4.1"

//go:embed licenses
var licenses embed.FS

func Install(parentDirectory string) (string, error) {
	info, err := os.Lstat(parentDirectory)
	if err != nil || !info.IsDir() {
		return "", errors.New("rsync requires an existing parent directory")
	}
	if len(archive) == 0 {
		return "", errors.New("rsync is unavailable on this architecture")
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != archiveSHA256 {
		return "", errors.New("embedded rsync checksum mismatch")
	}
	directory, err := os.MkdirTemp(parentDirectory, "rsync-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	if err = os.Chmod(directory, 0700); err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil || header.Name != "rsync" || header.Typeflag != tar.TypeReg || header.Size > 16<<20 {
		return "", errors.New("invalid embedded rsync archive")
	}
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return "", err
	}
	binary := filepath.Join(directory, "rsync")
	if err = os.WriteFile(binary, data, 0700); err != nil {
		return "", err
	}
	err = fs.WalkDir(licenses, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := licenses.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		target := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0600)
	})
	if err != nil {
		return "", err
	}
	return binary, nil
}

func Available() bool {
	return len(archive) > 0
}
