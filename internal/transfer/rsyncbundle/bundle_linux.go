//go:build linux

package rsyncbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"embed"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
)

const Version = "3.4.1"

//go:embed licenses
var licenses embed.FS

func Install(stateDirectory string) (binary string, cleanup func(), err error) {
	if err = localstate.Directory(stateDirectory); err != nil {
		return "", nil, err
	}
	if len(archive) == 0 {
		return "", nil, errors.New("bundled rsync is unavailable on this architecture")
	}
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != archiveSHA256 {
		return "", nil, errors.New("embedded rsync checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", nil, err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	header, err := reader.Next()
	if err != nil || header.Name != "rsync" || header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Size <= 0 || header.Size > 16<<20 {
		return "", nil, errors.New("invalid embedded rsync archive")
	}
	data, err := io.ReadAll(io.LimitReader(reader, (16<<20)+1))
	if err != nil || int64(len(data)) != header.Size {
		return "", nil, errors.New("invalid embedded rsync executable size")
	}
	if _, err = reader.Next(); err != io.EOF {
		return "", nil, errors.New("unexpected embedded rsync archive entry")
	}
	digest = sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != binarySHA256 {
		return "", nil, errors.New("embedded rsync executable checksum mismatch")
	}
	executable, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return "", nil, err
	}
	defer executable.Close()
	if executable.Class != elf.ELFCLASS64 || executable.Data != elf.ELFDATA2LSB || int(executable.Machine) != archiveMachine {
		return "", nil, errors.New("embedded rsync executable architecture mismatch")
	}
	for _, program := range executable.Progs {
		if program.Type == elf.PT_INTERP {
			return "", nil, errors.New("embedded rsync must be statically linked")
		}
	}
	libraries, err := executable.ImportedLibraries()
	if err != nil || len(libraries) != 0 {
		return "", nil, errors.New("embedded rsync must not require external libraries")
	}
	directory, err := os.MkdirTemp(stateDirectory, "rsync-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(directory) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	binary = filepath.Join(directory, "rsync")
	if err = os.WriteFile(binary, data, 0700); err != nil {
		return "", cleanup, err
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
	return binary, cleanup, err
}
