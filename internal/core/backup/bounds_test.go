package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func fixtureArchive(t *testing.T, keySize int) []byte {
	t.Helper()
	files := map[string][]byte{databaseName: bytes.Repeat([]byte("d"), 256<<10), alertKeyName: bytes.Repeat([]byte("k"), keySize), csrfKeyName: bytes.Repeat([]byte("c"), 32)}
	m := manifest{Format: archiveFormat, Version: archiveVersion}
	for _, name := range requiredFiles {
		data := files[name]
		digest := sha256.Sum256(data)
		m.Files = append(m.Files, manifestFile{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])})
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0600, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write(manifestName, raw)
	for _, name := range requiredFiles {
		write(name, files[name])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type cancelingArchiveReader struct {
	r      *bytes.Reader
	cancel context.CancelFunc
	total  int
}

func (r *cancelingArchiveReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.total += n
	if r.total >= 4096 {
		r.cancel()
	}
	return n, err
}
func TestRestoreStopsDuringEntryCopy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := &cancelingArchiveReader{r: bytes.NewReader(fixtureArchive(t, 32)), cancel: cancel}
	stage := t.TempDir()
	if err := extractAndVerify(ctx, input, stage); err == nil {
		t.Fatal("canceled extraction accepted")
	}
	if input.total > 64<<10 {
		t.Fatalf("cancelled restore consumed %d bytes before stopping", input.total)
	}
}
func TestRestoreRejectsInvalidKeysBeforeExtraction(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		stage := t.TempDir()
		err := extractAndVerify(context.Background(), bytes.NewReader(fixtureArchive(t, size)), stage)
		if err == nil {
			t.Errorf("%d-byte key accepted by archive validation", size)
		}
		entries, readErr := os.ReadDir(stage)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatal("invalid key manifest was rejected only after extracting files")
		}
	}
}
func TestPrivateKeySizeLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), alertKeyName)
	if err := os.WriteFile(p, bytes.Repeat([]byte("k"), 33), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openPrivateFile(p, -1)
	if err == nil {
		io.Copy(io.Discard, f)
		f.Close()
		t.Fatal("oversized private key bypassed size limit")
	}
}
