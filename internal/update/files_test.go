package update

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type repeatedBytes byte

func (r repeatedBytes) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(r)
	}
	return len(buffer), nil
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestBinaryCopyStreamsLargeArtifactsAndProtectsTargets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "release-binary")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	if _, err = io.CopyN(io.MultiWriter(file, hash), repeatedBytes('A'), 16<<20); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	wantHash := hex.EncodeToString(hash.Sum(nil))
	backup := filepath.Join(root, "previous-binary")
	if err = copyBinary(source, backup); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Size() != 16<<20 || info.Mode().Perm() != 0700 || hashFile(t, backup) != wantHash {
		t.Fatalf("backup was not copied exactly with private mode: %v", err)
	}
	installed := filepath.Join(root, "nodedance-agent")
	if err = os.WriteFile(installed, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = installBinary(source, installed); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(installed)
	if err != nil || info.Size() != 16<<20 || info.Mode().Perm() != 0755 || hashFile(t, installed) != wantHash {
		t.Fatalf("installed artifact was not copied exactly: %v", err)
	}
	if err = os.Remove(installed); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(source, installed); err != nil {
		t.Fatal(err)
	}
	if err = installBinary(source, installed); err == nil {
		t.Fatal("symlink target was accepted")
	}
	if err = os.Remove(installed); err != nil {
		t.Fatal(err)
	}
}
