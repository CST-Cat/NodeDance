package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const maxWalkEntries = 100000
const maxWalkDepth = 64

// Browse scans with bounded memory. The opaque continuation is the last path;
// concurrent filesystem edits may change subsequent pages, as with readdir.
func (s *Service) Browse(ctx context.Context, virtual, after, filter string, recursive bool, minSize, maxSize int64) ([]protocol.FileEntry, string, error) {
	if len(filter) > 200 || len(after) > 4096 || minSize < 0 || maxSize < 0 || maxSize > 0 && maxSize < minSize {
		return nil, "", ErrInvalidPath
	}
	items := make([]protocol.FileEntry, 0, protocol.MaxFileEntries+1)
	scanned := 0
	err := s.walk(ctx, virtual, recursive, 0, &scanned, func(entry protocol.FileEntry) error {
		if entry.Path <= after || !strings.Contains(strings.ToLower(entry.Name), strings.ToLower(filter)) {
			return nil
		}
		if entry.Kind != "directory" && (entry.Size < minSize || maxSize > 0 && entry.Size > maxSize) {
			return nil
		}
		index := sort.Search(len(items), func(i int) bool { return items[i].Path >= entry.Path })
		if index > protocol.MaxFileEntries {
			return nil
		}
		items = append(items, protocol.FileEntry{})
		copy(items[index+1:], items[index:])
		items[index] = entry
		if len(items) > protocol.MaxFileEntries+1 {
			items = items[:protocol.MaxFileEntries+1]
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > protocol.MaxFileEntries {
		items = items[:protocol.MaxFileEntries]
		next = items[len(items)-1].Path
	}
	for {
		payload, err := json.Marshal(protocol.FileResponse{Operation: protocol.FileList, Entries: items, NextAfter: next})
		if err != nil {
			return nil, "", err
		}
		if len(payload) <= protocol.MaxFileControlBytes-512 {
			break
		}
		if len(items) <= 1 {
			return nil, "", ErrDirectoryLarge
		}
		items = items[:len(items)-1]
		next = items[len(items)-1].Path
	}

	return items, next, nil
}

func (s *Service) walk(ctx context.Context, virtual string, recursive bool, depth int, scanned *int, visit func(protocol.FileEntry) error) error {
	if depth > maxWalkDepth {
		return ErrLimitExceeded
	}
	name, canonical, err := s.normalize(virtual)
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
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(256)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, item := range entries {
			*scanned++
			if *scanned > maxWalkEntries {
				return ErrLimitExceeded
			}
			child := path.Join(canonical, item.Name())
			childName, _, err := s.normalize(child)
			if errors.Is(err, ErrInvalidPath) {
				continue
			}
			if err != nil {
				return err
			}
			info, err := s.lstat(childName)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrInvalidPath) {
				continue
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				continue
			}
			entry := makeEntry(item.Name(), child, info)
			if info.Mode().IsRegular() {
				entry.Version, err = s.version(childName, info, false)
				if err != nil {
					return err
				}
			}
			if err := visit(entry); err != nil {
				return err
			}
			if recursive && info.IsDir() {
				if err := s.walk(ctx, child, true, depth+1, scanned, visit); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
	}
}
