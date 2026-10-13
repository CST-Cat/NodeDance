// Package localstate stores root-owned private Agent runtime state.
package localstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

const maxStateBytes = 4 << 20

func Directory(path string) error {
	if os.Geteuid() != 0 {
		return errors.New("Agent state requires root")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("state path must be absolute and clean")
	}
	current := "/"
	for _, part := range splitPath(path) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(current, 0700); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		stat, owned := info.Sys().(*syscall.Stat_t)
		if !owned || stat.Uid != 0 || (info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return errors.New("Agent state ancestors must be root-owned and protected from replacement")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("state directory must not contain symlinks")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode().Perm() != 0700 {
		return errors.New("Agent state directory must be root-owned 0700")
	}
	return nil
}
func splitPath(path string) []string {
	var out []string
	for path != "/" {
		out = append([]string{filepath.Base(path)}, out...)
		path = filepath.Dir(path)
	}
	return out
}
func Read(path string, v any) error {
	if err := Directory(filepath.Dir(path)); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != 0 || info.Mode().Perm() != 0600 {
		return errors.New("Agent state file must be root-owned regular 0600")
	}
	// Read one byte beyond the limit so a truncated reader cannot disguise
	// trailing data as EOF. Apply the same bound before replacing state.
	data, err := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return errors.New("Agent state exceeds 4 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("trailing state data")
	}
	return nil
}
func Write(path string, v any) error {
	if err := Directory(filepath.Dir(path)); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || st.Uid != 0 || info.Mode().Perm() != 0600 {
			return errors.New("unsafe Agent state file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxStateBytes {
		return errors.New("Agent state exceeds 4 MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
