package localstate

import (
	"bytes"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestStateSizeBoundary(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("Agent state is root-only")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "state.json")
	const limit = 4 << 20
	data := append([]byte(`{"value":7}`), bytes.Repeat([]byte(" "), limit-len(`{"value":7}`))...)
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := Read(p, &got); err != nil || got["value"] != 7 {
		t.Fatalf("exact boundary: %v", err)
	}
	if err := os.WriteFile(p, append(data, []byte(`{"value":8}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Read(p, &got); err == nil {
		t.Error("oversized file hid trailing JSON behind reader limit")
	}
	if err := Write(p, map[string]int{"value": 9}); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, string(bytes.Repeat([]byte("x"), limit))); err == nil {
		t.Error("accepted state too large to read back")
	}
	if err := Read(p, &got); err != nil || got["value"] != 9 {
		t.Fatalf("rejected write replaced previous state: %v", err)
	}
}

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
