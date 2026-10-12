//go:build linux

// Package tmuxbundle supplies the complete upstream static tmux executable.
// Assets are embedded in each architecture's Agent, including dependency notices.
package tmuxbundle

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
	"syscall"
)

const Version = "3.7c"

//go:embed assets/terminfo licenses
var support embed.FS

// Install creates an isolated, root-only runtime workspace. No system tmux or
// user configuration is modified. The caller owns cleanup after killing servers.
func Install(stateDirectory string) (directory string, err error) {
	if os.Geteuid() != 0 {
		return "", errors.New("Agent terminal requires root")
	}
	info, statErr := os.Lstat(stateDirectory)
	if statErr != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", errors.New("tmux requires the private Agent state directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return "", errors.New("tmux state directory must be owned by root")
	}
	directory, err = os.MkdirTemp(stateDirectory, "terminal-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != archiveSHA256 {
		return directory, errors.New("embedded tmux checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return directory, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil || header.Name != "tmux" || header.Typeflag != tar.TypeReg || header.Size > 16<<20 {
		return directory, errors.New("invalid embedded tmux archive")
	}
	data, err := io.ReadAll(io.LimitReader(reader, 16<<20))
	if err != nil {
		return directory, err
	}
	if err = os.WriteFile(filepath.Join(directory, "tmux"), data, 0700); err != nil {
		return directory, err
	}
	err = fs.WalkDir(support, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := support.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		target := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
	return directory, err
}
