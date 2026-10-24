package update

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type releaseWriter struct {
	file    *os.File
	pending int64
	offset  int64
}

func (w *releaseWriter) Write(data []byte) (int, error) {
	written := 0
	for len(data) > 0 {
		size := min(len(data), (1<<20)-int(w.pending))
		n, err := w.file.Write(data[:size])
		written += n
		w.pending += int64(n)
		data = data[n:]
		if err != nil {
			return written, err
		}
		if n != size {
			return written, io.ErrShortWrite
		}
		if w.pending == 1<<20 {
			if err := w.flush(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func (w *releaseWriter) flush() error {
	if err := w.file.Sync(); err != nil {
		return err
	}
	if w.pending > 0 {
		_ = unix.Fadvise(int(w.file.Fd()), w.offset, w.pending, unix.FADV_DONTNEED)
	}
	w.offset += w.pending
	w.pending = 0
	return nil
}

func privateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("state directory must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("update state requires an owner-protected 0700 directory")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != filepath.Clean(path) {
		return errors.New("update state directory must not contain symlinks")
	}
	return nil
}
func atomicFile(path string, raw []byte, mode fs.FileMode) error {
	return atomicWrite(path, mode, func(writer io.Writer) error {
		_, err := writer.Write(raw)
		return err
	})
}
func atomicCopy(source, path string, mode fs.FileMode) error {
	return atomicWrite(path, mode, func(writer io.Writer) error {
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		_, err = io.Copy(writer, struct{ io.Reader }{input})
		return err
	})
}
func atomicWrite(path string, mode fs.FileMode, write func(io.Writer) error) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing non-regular update file")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".update-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	writer := &releaseWriter{file: file}
	if err = write(writer); err == nil {
		err = writer.flush()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("update data contains a symlink or special file")
		}
		if info.IsDir() {
			if err = os.Mkdir(target, info.Mode().Perm()); err != nil {
				return err
			}
		} else {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
			if err != nil {
				in.Close()
				return err
			}
			_, err = io.Copy(out, in)
			in.Close()
			if err == nil {
				err = out.Sync()
			}
			out.Close()
			if err != nil {
				return err
			}
		}
		stat := info.Sys().(*syscall.Stat_t)
		if err := os.Chown(target, int(stat.Uid), int(stat.Gid)); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}
func restoreTree(snapshot, destination, id string) error {
	staging := destination + ".restore-" + id
	failed := destination + ".failed-" + id
	if _, err := os.Stat(failed); err == nil {
		if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
			return os.Rename(staging, destination)
		}
		return nil
	}
	if _, err := os.Stat(staging); err == nil {
		if err := os.RemoveAll(staging); err != nil {
			return err
		}
	}
	if err := copyTree(snapshot, staging); err != nil {
		return err
	}
	if err := os.Rename(destination, failed); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		_ = os.Rename(failed, destination)
		return err
	}
	return nil
}
