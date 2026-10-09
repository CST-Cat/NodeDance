package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func newTestService(t *testing.T) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	service, err := New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, root
}

func TestRootConfinedNamesMetadataAndOperations(t *testing.T) {
	service, root := newTestService(t)
	for _, name := range []string{"plain.txt", "中文 文件 [x].txt"} {
		virtual := "/" + name
		data := []byte("NodeDance\n")
		digest := sha256.Sum256(data)
		upload, err := service.BeginUpload(virtual, "", int64(len(data)), hex.EncodeToString(digest[:]))
		if err != nil {
			t.Fatalf("begin upload %q: %v", name, err)
		}
		if err := upload.WriteChunk(data); err != nil {
			t.Fatal(err)
		}
		entry, err := upload.Commit()
		if err != nil {
			t.Fatalf("commit upload %q: %v", name, err)
		}
		if entry.Name != name || entry.Kind != "file" || entry.Path != virtual || entry.Version == "" {
			t.Fatalf("unexpected entry: %#v", entry)
		}
		stored, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(stored) != string(data) {
			t.Fatalf("stored content %q: %v", stored, err)
		}
	}
	if err := service.Mkdir("/folder"); err != nil {
		t.Fatal(err)
	}
	if err := service.Rename("/folder", "/renamed folder"); err != nil {
		t.Fatal(err)
	}
	entries, err := service.List("/")
	if err != nil || len(entries) != 3 {
		t.Fatalf("list entries=%d err=%v", len(entries), err)
	}
	if err := service.Delete("/renamed folder", false); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("unconfirmed delete error=%v", err)
	}
	if err := service.Delete("/renamed folder", true); err != nil {
		t.Fatal(err)
	}
}

func TestTraversalAndSymlinkEscapeRejected(t *testing.T) {
	service, root := newTestService(t)
	if _, _, err := service.ReadText("/../outside"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("traversal error=%v", err)
	}
	if _, err := service.List("/a/../../outside"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("nested traversal error=%v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadText("/escape/secret"); err == nil {
		t.Fatal("read through an escaping symlink succeeded")
	}
	if _, err := service.List("/escape"); err == nil {
		t.Fatal("listing an escaping symlink succeeded")
	}
}

func TestUploadRequiresConfirmationAndPreservesOriginalOnAbortOrFailure(t *testing.T) {
	service, root := newTestService(t)
	path := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(path, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BeginUpload("/existing.txt", "", 3, strings.Repeat("0", 64)); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite without confirmation error=%v", err)
	}
	entry, err := service.Stat("/existing.txt")
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.BeginUpload("/existing.txt", entry.Version, 11, strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := upload.WriteChunk([]byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Commit(); !errors.Is(err, ErrTransferDigest) {
		t.Fatalf("wrong digest error=%v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != "original" {
		t.Fatalf("original changed after bad upload: %q err=%v", stored, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".existing.txt.nodedance-upload-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v err=%v", matches, err)
	}
}

func TestInterruptedUploadLeavesOldFileAndCleansTemporary(t *testing.T) {
	service, root := newTestService(t)
	path := filepath.Join(root, "data")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, _ := service.Stat("/data")
	digest := sha256.Sum256([]byte("new data"))
	upload, err := service.BeginUpload("/data", entry.Version, 8, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := upload.WriteChunk([]byte("new")); err != nil {
		t.Fatal(err)
	}
	upload.Abort()
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != "old" {
		t.Fatalf("original changed: %q err=%v", stored, err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".data.nodedance-upload-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestTextEditDetectsExternalChangeAndPreservesMetadata(t *testing.T) {
	service, root := newTestService(t)
	path := filepath.Join(root, "edit.txt")
	if err := os.WriteFile(path, []byte("before"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := service.Stat("/edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	contents, version, err := service.ReadText("/edit.txt")
	if err != nil || contents != "before" || version != before.Version {
		t.Fatalf("read text %q version=%q err=%v", contents, version, err)
	}
	if err := os.WriteFile(path, []byte("external"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.SaveText("/edit.txt", version, "web edit"); !errors.Is(err, ErrConflict) {
		t.Fatalf("external change error=%v", err)
	}
	latest, latestVersion, err := service.ReadText("/edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	updated, backup, err := service.SaveText("/edit.txt", latestVersion, "saved by web")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == "" || after.Mode().Perm() != 0o640 {
		t.Fatalf("metadata not preserved: %#o entry=%#v", after.Mode().Perm(), updated)
	}
	if runtime.GOOS == "linux" && latest != "external" {
		t.Fatalf("latest pre-save content=%q", latest)
	}
	if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(backup, "/"))); err != nil {
		t.Fatalf("backup missing: %s err=%v", backup, err)
	}
	stored, _ := os.ReadFile(path)
	if string(stored) != "saved by web" {
		t.Fatalf("saved text=%q", stored)
	}
}

func TestPostAtomicReplaceMetadataFailuresReturnUnknown(t *testing.T) {
	t.Run("SaveText", func(t *testing.T) {
		service, root := newTestService(t)
		path := filepath.Join(root, "target.txt")
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		entry, err := service.Stat("/target.txt")
		if err != nil {
			t.Fatal("read initial metadata:", err)
		}
		service.afterAtomicReplace = func() { _ = service.root.Close() }
		if _, _, err := service.SaveText("/target.txt", entry.Version, "after"); !errors.Is(err, ErrMutationResultUnknown) {
			t.Fatalf("post-rename metadata error=%v; want unknown mutation result", err)
		}
		stored, err := os.ReadFile(path)
		if err != nil || string(stored) != "after" {
			t.Fatalf("SaveText did not replace the file before metadata failure: content=%q err=%v", stored, err)
		}
	})

	t.Run("UploadCommit", func(t *testing.T) {
		service, root := newTestService(t)
		path := filepath.Join(root, "target.txt")
		if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		entry, err := service.Stat("/target.txt")
		if err != nil {
			t.Fatal("read initial metadata:", err)
		}
		data := []byte("uploaded")
		digest := sha256.Sum256(data)
		upload, err := service.BeginUpload("/target.txt", entry.Version, int64(len(data)), hex.EncodeToString(digest[:]))
		if err != nil {
			t.Fatal("begin upload:", err)
		}
		if err := upload.WriteChunk(data); err != nil {
			t.Fatal("write upload chunk:", err)
		}
		service.afterAtomicReplace = func() { _ = service.root.Close() }
		if _, err := upload.Commit(); !errors.Is(err, ErrMutationResultUnknown) {
			t.Fatalf("post-rename upload metadata error=%v; want unknown mutation result", err)
		}
		stored, err := os.ReadFile(path)
		if err != nil || string(stored) != string(data) {
			t.Fatalf("UploadCommit did not replace the file before metadata failure: content=%q err=%v", stored, err)
		}
	})
}

func TestTextEditRejectsBinaryAndOversizedText(t *testing.T) {
	service, root := newTestService(t)
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadText("/binary"); !errors.Is(err, ErrNotText) {
		t.Fatalf("binary read error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "large"), make([]byte, protocol.MaxTextFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ReadText("/large"); !errors.Is(err, ErrTextTooLarge) {
		t.Fatalf("large read error=%v", err)
	}
}

func TestDirectoryListingNeverExceedsAgentControlFrameLimit(t *testing.T) {
	entries := make([]protocol.FileEntry, 0, protocol.MaxFileEntries)
	for index := 0; index < protocol.MaxFileEntries; index++ {
		entry := protocol.FileEntry{Name: "item", Path: "/" + strings.Repeat("d", 3600) + "/item", Kind: "file", Version: strings.Repeat("a", 64)}
		var err error
		entries, err = appendBoundedListingEntry(entries, entry)
		if errors.Is(err, ErrDirectoryLarge) {
			if len(entries) != 0 {
				t.Fatal("oversized listing returned a partial directory as if complete")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("worst-case path list did not hit the bounded response size")
}

func TestUploadDetectsExternalModificationAndLimit(t *testing.T) {
	service, root := newTestService(t)
	path := filepath.Join(root, "race")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	entry, _ := service.Stat("/race")
	data := []byte("replacement")
	digest := sha256.Sum256(data)
	upload, err := service.BeginUpload("/race", entry.Version, int64(len(data)), hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := upload.WriteChunk(data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("SSH write"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Commit(); !errors.Is(err, ErrConflict) {
		t.Fatalf("external write conflict=%v", err)
	}
	limited, err := New(t.TempDir(), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if _, err := limited.BeginUpload("/large", "", 5, strings.Repeat("0", 64)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("limit error=%v", err)
	}
}

func TestAbsentUploadTargetCommitPrimitiveNeverReplacesRacingTarget(t *testing.T) {
	service, root := newTestService(t)
	if err := os.WriteFile(filepath.Join(root, "temp"), []byte("new data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("external data"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := linkUploadWithoutReplace(service.root, "temp", "target"); !errors.Is(err, ErrConflict) {
		t.Fatalf("no-replace commit error=%v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "target"))
	if err != nil || string(contents) != "external data" {
		t.Fatalf("racing target was overwritten: %q err=%v", contents, err)
	}
}
