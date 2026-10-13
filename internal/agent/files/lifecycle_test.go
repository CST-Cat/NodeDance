package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestCopyAndArchiveRejectDestinationInsideRootSource(t *testing.T) {
	for _, action := range []protocol.TaskAction{protocol.TaskFileCopy, protocol.TaskFileArchive} {
		s, root := extendedService(t)
		if err := os.WriteFile(filepath.Join(root, "kept.txt"), []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		spec := protocol.FileTaskSpec{Path: "/", NewPath: "/copy"}
		if action == protocol.TaskFileArchive {
			spec.Path = "/bundle.zip"
			spec.Paths = []string{"/"}
		}
		err := s.ExecuteExtended(context.Background(), action, spec, "root-source")
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf("root source descended into its own staging destination: %v", err)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 1 {
			t.Fatal("staging residue", entries, err)
		}
	}
}

func TestUploadCancellationInterruptsIdlePipe(t *testing.T) {
	s, root := extendedService(t)
	_ = root
	content := []byte("x")
	sum := sha256.Sum256(content)
	spec := protocol.FileTaskSpec{Path: "/target", Size: 1, SHA256: hex.EncodeToString(sum[:])}
	executor := &TaskExecutor{service: s, uploads: make(map[string]*pendingUpload)}
	if err := executor.prepareUpload("task", spec); err != nil {
		t.Fatal(err)
	}
	defer executor.removePending("task")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer, err := executor.AttachUpload(ctx, "task", protocol.FileRequest{Operation: protocol.FileUploadBegin, Path: spec.Path, Size: 1, SHA256: spec.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- executor.receiveUpload(ctx, "task", spec) }()
	// Let the worker reach the idle pipe read; no remote writer supplies bytes.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		writer.Close()
		<-done
		t.Fatal("cancelled upload remained blocked on its pipe")
	}
}

func TestReadTextVersionBindsReturnedContentDuringReplacement(t *testing.T) {
	s, root := extendedService(t)
	target := filepath.Join(root, "edit.txt")
	next := filepath.Join(root, "next.txt")
	at := time.Unix(1700000000, 0)
	for name, data := range map[string]string{target: "AAAA", next: "BBBB"} {
		if err := os.WriteFile(name, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, at, at); err != nil {
			t.Fatal(err)
		}
	}
	original, err := s.Stat("/edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	s.afterTextRead = func() {
		if err := os.Rename(next, target); err != nil {
			t.Fatal(err)
		}
	}
	text, version, err := s.ReadText("/edit.txt")
	if err != nil || text != "AAAA" {
		t.Fatal(text, err)
	}
	if version != original.Version {
		t.Error("returned content was paired with the replacement's version")
	}
	if _, err := s.SaveText("/edit.txt", version, "edited"); !errors.Is(err, ErrConflict) {
		t.Errorf("unseen replacement was overwritten: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "BBBB" {
		t.Fatal("replacement was not preserved", string(data), err)
	}
}

func TestSaveTextCreatesNewFileAtEditorLimit(t *testing.T) {
	root := t.TempDir()
	s, err := New(root, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := strings.Repeat("x", protocol.MaxTextFileBytes)
	if _, err := s.SaveText("/new.txt", "", text); err != nil {
		t.Fatal("valid new editor file rejected", err)
	}
	content, _, err := s.ReadText("/new.txt")
	if err != nil || content != text {
		t.Fatal("new content lost", len(content), err)
	}
}
