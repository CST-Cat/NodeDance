package localstate

import (
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStateRejectsUnsafeFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		if Directory(t.TempDir()) == nil {
			t.Fatal("non-root state accepted")
		}
		return
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "state.json")
	v := map[string]int{"value": 7}
	if err := Write(p, v); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := Read(p, &got); err != nil || got["value"] != 7 {
		t.Fatalf("read %v", err)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if Read(p, &got) == nil || Write(p, v) == nil {
		t.Fatal("public state accepted")
	}
	os.Remove(p)
	os.Symlink(filepath.Join(dir, "target"), p)
	if Read(p, &got) == nil || Write(p, v) == nil {
		t.Fatal("symlink accepted")
	}
	os.Remove(p)
	if err := unix.Mkfifo(p, 0600); err != nil {
		t.Fatal(err)
	}
	if Read(p, &got) == nil {
		t.Fatal("FIFO accepted")
	}
}
