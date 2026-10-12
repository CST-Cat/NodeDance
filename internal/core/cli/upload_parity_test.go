package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/CST-Cat/NodeDance/internal/control"
	"golang.org/x/sys/unix"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestCLIStdinAndTUIFIFOTransferIdenticalBinaryContent(t *testing.T) {
	content := []byte{0, 255, 1, 2, 3}
	var submits atomic.Int32
	o := control.Operation{Name: "future.upload", Method: "POST", Path: "/api/v1/new-upload", Transport: "upload", Upload: &control.UploadContract{ContentType: "application/octet-stream", MaxBytes: 100}}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/operations":
			json.NewEncoder(w).Encode(control.Catalog{Version: 1, Operations: []control.Operation{o}})
		case "/api/v1/auth/csrf":
			io.WriteString(w, `{"token":"csrf"}`)
		case o.Path:
			raw, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(raw, content) {
				t.Error("binary stream changed", raw, err)
			}
			submits.Add(1)
			io.WriteString(w, `{"accepted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer host.Close()
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	writer.Write(content)
	writer.Close()
	args := []string{"call", o.Name, "--input", "-", "--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}
	if err := RunWithInput(context.Background(), args, func(string) (string, bool) { return "", false }, input, io.Discard, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(t.TempDir(), "upload.fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(content)
			f.Close()
		}
		done <- err
	}()
	driveTerminal(t, host.URL, []tuiStep{{"Select:", "/future.upload"}, {"Filter: future.upload", "1"}, {"Input file or FIFO path", fifo}, {"Expected file version", ""}, {"Enter to return:", ""}, {"Select:", "q"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if submits.Load() != 2 {
		t.Fatal("interfaces did not execute identical streams", submits.Load())
	}
}
