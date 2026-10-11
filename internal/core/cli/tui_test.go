package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestTUILoginAndReturn(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/csrf" {
			http.SetCookie(w, &http.Cookie{Name: "nodedance_csrf", Value: "csrf", Path: "/"})
			io.WriteString(w, `{"token":"csrf"}`)
			return
		}
		if r.URL.Path == "/api/v1/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "nodedance_session", Value: "session", Path: "/"})
			io.WriteString(w, `{"user":{"displayName":"admin"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer host.Close()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	pty.Setsize(slave, &pty.Winsize{Rows: 24, Cols: 120})
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- runTUI(ctx, []string{"--server", host.URL, "--dev", "--state-dir", filepath.Join(t.TempDir(), "client")}, func(string) (string, bool) { return "", false }, slave, slave, io.Discard, "test")
	}()
	chunks := make(chan string, 100)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	var transcript strings.Builder
	await := func(text string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case part := <-chunks:
				transcript.WriteString(part)
				if strings.Contains(transcript.String(), text) {
					return
				}
			case <-timer.C:
				t.Fatalf("did not see %q: %s", text, transcript.String())
			case err := <-done:
				t.Fatalf("TUI exited early: %v", err)
			}
		}
	}
	await("Select:")
	io.WriteString(master, "l\r")
	await("选择:")
	io.WriteString(master, "l\r")
	io.WriteString(master, "/auth.login\r")
	await("Filter: auth.login")
	io.WriteString(master, "1\r")
	await("Administrator username")
	io.WriteString(master, "test-admin\r")
	await("Password (hidden)")
	io.WriteString(master, "tui-password\r")
	await("Enter to return:")
	io.WriteString(master, "\r")
	io.WriteString(master, "q\r")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TUI failed to exit")
	}
	if strings.Contains(transcript.String(), "tui-password") {
		t.Fatal("TUI echoed password")
	}
}
