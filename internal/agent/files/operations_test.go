package files

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func extendedService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

// Directory browsing must never fingerprint file contents. Content-bound
// versions remain exclusive to explicit stat/read and mutation conflict checks.
func TestBrowseUsesMetadataVersion(t *testing.T) {
	s, root := extendedService(t)
	if err := os.WriteFile(filepath.Join(root, "text.txt"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, _, err := s.Browse(context.Background(), "/", "", "", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.List("/")
	if err != nil {
		t.Fatal(err)
	}
	full, err := s.Stat("/text.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || len(listed) != 1 || entries[0].Version == "" || entries[0].Version != listed[0].Version {
		t.Fatalf("browse metadata versions: %#v / %#v", entries, listed)
	}
	if entries[0].Version == full.Version {
		t.Fatal("browse read contents to compute a content-bound version")
	}
}

func TestBrowsePagesAndSearch(t *testing.T) {
	s, root := extendedService(t)
	for i := 0; i < 245; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("item-%03d.txt", i)), []byte("text"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("item-000.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	var names []string
	after := ""
	for {
		items, next, err := s.Browse(context.Background(), "/", after, "", false, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 100 {
			t.Fatal("unbounded page")
		}
		for _, item := range items {
			names = append(names, item.Name)
		}
		if next == "" {
			break
		}
		if next == after {
			t.Fatal("stalled cursor")
		}
		after = next
	}
	if len(names) != 245 || names[0] != "item-000.txt" || names[244] != "item-244.txt" {
		t.Fatal("lost entries", len(names))
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "needle.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	items, _, err := s.Browse(context.Background(), "/", "", "needle", true, 5, 9)
	if err != nil || len(items) != 1 || items[0].Path != "/sub/needle.txt" {
		t.Fatal(items, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Browse(ctx, "/", "", "", false, 0, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestExtendedCopyArchiveExtractAndConflict(t *testing.T) {
	s, root := extendedService(t)
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "data.txt"), []byte("original"), 0640); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.ExecuteExtended(ctx, protocol.TaskFileCopy, protocol.FileTaskSpec{Path: "/source", NewPath: "/copy"}, "copy-job"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "copy")); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("copy widened directory permissions", info, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "copy", "data.txt")); err != nil || string(data) != "original" {
		t.Fatal(string(data), err)
	}
	if err := s.ExecuteExtended(ctx, protocol.TaskFileCopy, protocol.FileTaskSpec{Path: "/source", NewPath: "/copy"}, "copy-conflict"); !errors.Is(err, ErrExists) {
		t.Fatal("overwrite", err)
	}
	if err := s.ExecuteExtended(ctx, protocol.TaskFileArchive, protocol.FileTaskSpec{Path: "/bundle.zip", Paths: []string{"/source"}}, "archive-job"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "extract"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := s.ExecuteExtended(ctx, protocol.TaskFileExtract, protocol.FileTaskSpec{Path: "/bundle.zip", NewPath: "/extract"}, "extract-job"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, "extract", "source")); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("extract widened directory permissions", info, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "extract", "source", "data.txt")); err != nil || string(data) != "original" {
		t.Fatal(string(data), err)
	}
	if err := s.ExecuteExtended(ctx, protocol.TaskFileExtract, protocol.FileTaskSpec{Path: "/bundle.zip", NewPath: "/extract"}, "extract-conflict"); !errors.Is(err, ErrExists) {
		t.Fatal("extract overwrite", err)
	}
	if err := os.Symlink("data.txt", filepath.Join(root, "source", "link")); err != nil {
		t.Fatal(err)
	}
	if err := s.ExecuteExtended(ctx, protocol.TaskFileCopy, protocol.FileTaskSpec{Path: "/source", NewPath: "/unsafe"}, "symlink-job"); !errors.Is(err, ErrInvalidPath) {
		t.Fatal("symlink followed", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unsafe")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial published")
	}
}
func TestExtractRejectsTraversalLinksAndExpandedLimit(t *testing.T) {
	for _, kind := range []string{"traversal", "symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			s, root := extendedService(t)
			var data bytes.Buffer
			z := zip.NewWriter(&data)
			header := &zip.FileHeader{Name: "safe", Method: zip.Deflate}
			content := []byte("data")
			if kind == "traversal" {
				header.Name = "../escape"
			}
			if kind == "symlink" {
				header.SetMode(os.ModeSymlink | 0777)
			}
			if kind == "oversize" {
				content = make([]byte, (1<<20)+1)
			}
			file, err := z.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write(content); err != nil {
				t.Fatal(err)
			}
			if err = z.Close(); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "bad.zip"), data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Mkdir(filepath.Join(root, "dest"), 0755); err != nil {
				t.Fatal(err)
			}
			if err = s.ExecuteExtended(context.Background(), protocol.TaskFileExtract, protocol.FileTaskSpec{Path: "/bad.zip", NewPath: "/dest"}, "bad-job"); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			items, err := os.ReadDir(filepath.Join(root, "dest"))
			if err != nil || len(items) != 0 {
				t.Fatal("partial extraction", items, err)
			}
		})
	}
}

func TestFileMutationsRejectProtectedAncestors(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(private, "credentials")
	if err := os.WriteFile(secret, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root, 1<<20, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Rename("/private", "/moved"); !errors.Is(err, ErrInvalidPath) {
		t.Fatal("protected ancestor renamed", err)
	}
	if err := s.Delete("/private", true); !errors.Is(err, ErrInvalidPath) {
		t.Fatal("protected ancestor deleted", err)
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "protected" {
		t.Fatal("credentials changed", err)
	}
}

func TestBrowseLongEscapedPathsFitEnvelopeWithVersions(t *testing.T) {
	s, root := extendedService(t)
	virtual := "/" + strings.TrimSuffix(strings.Repeat(strings.Repeat("p", 100)+"/", 20), "/")
	directory := filepath.Join(root, strings.TrimPrefix(virtual, "/"))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 210; i++ {
		name := fmt.Sprintf("entry-%03d-", i) + strings.Repeat("<", 150)
		if err := os.WriteFile(filepath.Join(directory, name), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	after, total := "", 0
	for {
		entries, next, err := s.Browse(context.Background(), virtual, after, "", false, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(protocol.FileResponse{Operation: protocol.FileList, Entries: entries, NextAfter: next})
		if err != nil || len(payload) > protocol.MaxFileControlBytes-512 {
			t.Fatal("oversized control page", len(payload), err)
		}
		for _, entry := range entries {
			if entry.Version == "" {
				t.Fatal("missing version")
			}
		}
		total += len(entries)
		if next == "" {
			break
		}
		if next <= after {
			t.Fatal("cursor did not advance")
		}
		after = next
	}
	if total != 210 {
		t.Fatal("lost entries", total)
	}
}
