package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// Every generated destination uses the same protected-path/openat2 policy as
// ordinary file operations. Work is staged; existing destinations are not replaced.
func (s *Service) ExecuteExtended(ctx context.Context, action protocol.TaskAction, spec protocol.FileTaskSpec, taskID string) (retErr error) {
	target := spec.NewPath
	if action == protocol.TaskFileArchive {
		target = spec.Path
	}
	if _, _, err := s.normalize(target); err != nil {
		return err
	}
	if target == "/" && action != protocol.TaskFileExtract {
		return ErrInvalidPath
	}
	stage := path.Join(path.Dir(target), "."+path.Base(target)+".nodedance-file-"+taskID)
	if action == protocol.TaskFileExtract {
		stage = path.Join(target, ".nodedance-file-"+taskID)
	}
	if _, err := s.Stat(stage); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.Delete(stage, true)
		}
	}()
	switch action {
	case protocol.TaskFileCopy:
		if target == spec.Path || strings.HasPrefix(target, spec.Path+"/") {
			return ErrInvalidPath
		}
		count := 0
		var bytes int64
		if err := s.copyTree(ctx, spec.Path, stage, 0, &count, &bytes); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Rename(stage, target); err != nil {
			return err
		}
		committed = true
	case protocol.TaskFileArchive:
		for _, source := range spec.Paths {
			if source == target || strings.HasPrefix(target, source+"/") {
				return ErrInvalidPath
			}
		}
		if err := s.createArchive(ctx, stage, spec); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Rename(stage, target); err != nil {
			return err
		}
		committed = true
	case protocol.TaskFileExtract:
		source, entry, err := s.OpenDownload(spec.Path)
		if err != nil {
			return err
		}
		defer source.Close()
		if err := s.extractArchive(ctx, source, entry.Size, stage, target); err != nil {
			return err
		}
		committed = true
	default:
		return ErrInvalidPath
	}
	return nil
}

func (s *Service) ensureDirs(virtual, stop string) error {
	if virtual == stop {
		return nil
	}
	if !strings.HasPrefix(virtual, stop+"/") {
		return ErrInvalidPath
	}
	if err := s.ensureDirs(path.Dir(virtual), stop); err != nil {
		return err
	}
	if err := s.Mkdir(virtual); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

type boundedFileWriter struct {
	ctx       context.Context
	out       io.Writer
	remaining int64
}

func (w *boundedFileWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrLimitExceeded
	}
	n, err := w.out.Write(p)
	w.remaining -= int64(n)
	return n, err
}
func (s *Service) writeNew(ctx context.Context, virtual string, mode os.FileMode, write func(io.Writer) error) error {
	name, _, err := s.normalize(virtual)
	if err != nil {
		return err
	}
	out, err := s.openFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if err := write(&boundedFileWriter{ctx: ctx, out: out, remaining: s.limit}); err != nil {
		return err
	}
	if err := out.Chmod(mode.Perm()); err != nil {
		return err
	}
	return out.Sync()
}
func (s *Service) copyTree(ctx context.Context, source, target string, depth int, count *int, total *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*count++
	if *count > maxWalkEntries || depth > maxWalkDepth {
		return ErrLimitExceeded
	}
	name, _, err := s.normalize(source)
	if err != nil {
		return err
	}
	info, err := s.lstat(name)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := s.Mkdir(target); err != nil {
			return err
		}
		dir, err := s.open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			items, err := dir.ReadDir(256)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, item := range items {
				if err := s.copyTree(ctx, path.Join(source, item.Name()), path.Join(target, item.Name()), depth+1, count, total); err != nil {
					return err
				}
			}
			if errors.Is(err, io.EOF) {
				return s.directoryMode(target, info.Mode())
			}
		}
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidPath
	}
	if info.Size() > s.limit-*total {
		return ErrLimitExceeded
	}
	*total += info.Size()
	input, err := s.open(name)
	if err != nil {
		return err
	}
	defer input.Close()
	return s.writeNew(ctx, target, info.Mode().Perm(), func(out io.Writer) error {
		n, err := io.Copy(out, io.LimitReader(input, info.Size()+1))
		if err == nil && n != info.Size() {
			return ErrConflict
		}
		return err
	})
}
func (s *Service) archiveTree(ctx context.Context, add func(os.FileInfo, string) (io.Writer, error), source, relative string, depth int, count *int, total *int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*count++
	if *count > maxWalkEntries || depth > maxWalkDepth {
		return ErrLimitExceeded
	}
	name, _, err := s.normalize(source)
	if err != nil {
		return err
	}
	info, err := s.lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return ErrInvalidPath
	}
	writer, err := add(info, relative)
	if err != nil {
		return err
	}
	if info.IsDir() {
		dir, err := s.open(name)
		if err != nil {
			return err
		}
		defer dir.Close()
		for {
			items, err := dir.ReadDir(256)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, item := range items {
				if err := s.archiveTree(ctx, add, path.Join(source, item.Name()), path.Join(relative, item.Name()), depth+1, count, total); err != nil {
					return err
				}
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
		}
	}
	if info.Size() > s.limit-*total {
		return ErrLimitExceeded
	}
	*total += info.Size()
	file, err := s.open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	n, err := io.Copy(writer, io.LimitReader(file, info.Size()+1))
	if err == nil && n != info.Size() {
		return ErrConflict
	}
	return err
}

func (s *Service) directoryMode(virtual string, mode os.FileMode) error {
	name, _, err := s.normalize(virtual)
	if err != nil {
		return err
	}
	directory, err := s.open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return ErrInvalidPath
	}
	return directory.Chmod(mode.Perm())
}
