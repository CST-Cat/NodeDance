package files

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func archiveFormat(name string) string {
	name = strings.ToLower(name)
	if strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz") {
		return "tar.gz"
	}
	if strings.HasSuffix(name, ".tar") {
		return "tar"
	}
	return "zip"
}

func (s *Service) createArchive(ctx context.Context, stage string, spec protocol.FileTaskSpec) error {
	format := spec.Format
	if format == "" {
		format = archiveFormat(spec.Path)
	}
	if !protocol.ValidArchiveFormat(format) {
		return ErrInvalidPath
	}
	return s.writeNew(ctx, stage, 0600, func(out io.Writer) error {
		var closeArchive func() error
		var add func(os.FileInfo, string) (io.Writer, error)
		switch format {
		case "zip":
			writer := zip.NewWriter(out)
			closeArchive = writer.Close
			add = func(info os.FileInfo, name string) (io.Writer, error) {
				header, err := zip.FileInfoHeader(info)
				if err != nil {
					return nil, err
				}
				header.Name = name
				header.Method = zip.Deflate
				if info.IsDir() {
					header.Name += "/"
				}
				return writer.CreateHeader(header)
			}
		case "tar", "tar.gz":
			var compressed *gzip.Writer
			if format == "tar.gz" {
				compressed = gzip.NewWriter(out)
				out = compressed
			}
			writer := tar.NewWriter(out)
			closeArchive = func() error {
				err := writer.Close()
				if compressed != nil {
					err = errors.Join(err, compressed.Close())
				}
				return err
			}
			add = func(info os.FileInfo, name string) (io.Writer, error) {
				header, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return nil, err
				}
				header.Name = name
				header.Uid = 0
				header.Gid = 0
				header.Uname = ""
				header.Gname = ""
				if info.IsDir() {
					header.Name += "/"
				}
				if err = writer.WriteHeader(header); err != nil {
					return nil, err
				}
				return writer, nil
			}
		default:
			return ErrInvalidPath
		}
		count := 0
		var total int64
		for _, source := range spec.Paths {
			if err := s.archiveTree(ctx, add, source, path.Base(source), 0, &count, &total); err != nil {
				_ = closeArchive()
				return err
			}
		}
		return closeArchive()
	})
}

type archiveItem struct {
	name string
	size int64
	mode os.FileMode
}

func (item archiveItem) directory() bool { return item.mode.IsDir() }

// One path/type/size policy is shared by every supported archive format.
func (s *Service) validateArchiveItem(item archiveItem, target string, seen map[string]archiveItem, total *int64) error {
	name := item.name
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name || len(strings.Split(name, "/")) > maxWalkDepth {
		return ErrInvalidPath
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || part == "" {
			return ErrInvalidPath
		}
	}
	if _, ok := seen[name]; ok {
		return ErrInvalidPath
	}
	if !item.mode.IsRegular() && !item.directory() {
		return ErrInvalidPath
	}
	if _, _, err := s.normalize(path.Join(target, name)); err != nil {
		return err
	}
	if item.size < 0 || item.size > s.limit-*total {
		return ErrLimitExceeded
	}
	*total += item.size
	seen[name] = item
	if len(seen) > maxWalkEntries {
		return ErrLimitExceeded
	}
	return nil
}

type archiveReadBound struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
}

func (reader *archiveReadBound) Read(data []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if reader.remaining <= 0 {
		var probe [1]byte
		n, err := reader.reader.Read(probe[:])
		if n > 0 {
			return 0, ErrLimitExceeded
		}
		return 0, err
	}
	if int64(len(data)) > reader.remaining {
		data = data[:reader.remaining]
	}
	n, err := reader.reader.Read(data)
	reader.remaining -= int64(n)
	return n, err
}

func (s *Service) extractArchive(ctx context.Context, source *os.File, size int64, stage, target string) error {
	magic := make([]byte, 4)
	_, _ = source.ReadAt(magic, 0)
	var scan func(func(archiveItem, io.Reader) error) error
	if string(magic[:2]) == "PK" {
		reader, err := zip.NewReader(source, size)
		if err != nil {
			return err
		}
		if len(reader.File) > maxWalkEntries {
			return ErrLimitExceeded
		}
		scan = func(visit func(archiveItem, io.Reader) error) error {
			for _, file := range reader.File {
				if err := ctx.Err(); err != nil {
					return err
				}
				if file.UncompressedSize64 > uint64(s.limit) {
					return ErrLimitExceeded
				}
				item := archiveItem{strings.TrimSuffix(file.Name, "/"), int64(file.UncompressedSize64), file.Mode()}
				input, err := file.Open()
				if err != nil {
					return err
				}
				err = visit(item, input)
				closeErr := input.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			}
			return nil
		}
	} else {
		scan = func(visit func(archiveItem, io.Reader) error) error {
			if _, err := source.Seek(0, io.SeekStart); err != nil {
				return err
			}
			var input io.Reader = source
			if magic[0] == 0x1f && magic[1] == 0x8b {
				reader, err := gzip.NewReader(source)
				if err != nil {
					return err
				}
				defer reader.Close()
				input = reader
			} else if string(magic[:3]) == "BZh" {
				input = bzip2.NewReader(source)
			}
			// Bound expanded payload plus archive headers, padding and PAX metadata.
			bound := &archiveReadBound{ctx: ctx, reader: input, remaining: s.limit + int64(maxWalkEntries)*4096}
			reader := tar.NewReader(bound)
			count := 0
			for {
				header, err := reader.Next()
				if errors.Is(err, io.EOF) {
					_, err = io.Copy(io.Discard, bound)
					return err
				}
				if err != nil {
					return err
				}
				count++
				if count > maxWalkEntries {
					return ErrLimitExceeded
				}
				if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
					return ErrInvalidPath
				}
				item := archiveItem{strings.TrimSuffix(header.Name, "/"), header.Size, header.FileInfo().Mode()}
				if err := visit(item, reader); err != nil {
					return err
				}
			}
		}
	}
	seen := map[string]archiveItem{}
	var total int64
	manifest := []archiveItem{}
	if err := scan(func(item archiveItem, _ io.Reader) error {
		if err := s.validateArchiveItem(item, target, seen, &total); err != nil {
			return err
		}
		manifest = append(manifest, item)
		return nil
	}); err != nil {
		return err
	}
	if len(manifest) == 0 {
		return ErrInvalidPath
	}
	tops := map[string]bool{}
	for _, item := range manifest {
		tops[strings.Split(item.name, "/")[0]] = true
		for parent := path.Dir(item.name); parent != "."; parent = path.Dir(parent) {
			if ancestor, ok := seen[parent]; ok && !ancestor.directory() {
				return ErrInvalidPath
			}
		}
	}
	for top := range tops {
		if _, err := s.Stat(path.Join(target, top)); err == nil {
			return ErrExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := s.Mkdir(stage); err != nil {
		return err
	}
	index := 0
	if err := scan(func(item archiveItem, input io.Reader) error {
		if index >= len(manifest) || item != manifest[index] {
			return ErrConflict
		}
		index++
		dest := path.Join(stage, item.name)
		if err := s.ensureDirs(path.Dir(dest), stage); err != nil {
			return err
		}
		if item.directory() {
			if err := s.Mkdir(dest); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			return nil
		}
		return s.writeNew(ctx, dest, item.mode, func(out io.Writer) error {
			n, err := io.Copy(out, io.LimitReader(input, item.size+1))
			if err == nil && n != item.size {
				return ErrConflict
			}
			return err
		})
	}); err != nil {
		return err
	}
	if index != len(manifest) {
		return ErrConflict
	}
	sort.Slice(manifest, func(i, j int) bool {
		return strings.Count(manifest[i].name, "/") > strings.Count(manifest[j].name, "/")
	})
	for _, item := range manifest {
		if item.directory() {
			if err := s.directoryMode(path.Join(stage, item.name), item.mode); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(tops))
	for name := range tops {
		names = append(names, name)
	}
	sort.Strings(names)
	moved := 0
	for _, name := range names {
		err := ctx.Err()
		if err == nil {
			err = s.Rename(path.Join(stage, name), path.Join(target, name))
		}
		if err != nil {
			if moved > 0 {
				return ErrMutationResultUnknown
			}
			return err
		}
		moved++
	}
	if err := s.Delete(stage, true); err != nil {
		return ErrMutationResultUnknown
	}
	return nil
}
