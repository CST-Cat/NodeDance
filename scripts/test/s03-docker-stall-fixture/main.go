// Command s03-docker-stall-fixture serves a private Docker Engine HTTP fixture
// over one guest-local Unix socket. The first container-list request hangs
// until its real Moby SDK client cancels it; health and event requests remain
// independent so the Agent's host metrics and heartbeat paths can be observed.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const dockerAPIVersion = "1.44"

type dockerFixture struct {
	stateDir string
	listSeen atomic.Bool
}

func (f *dockerFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/_ping" || strings.HasSuffix(r.URL.Path, "/_ping")):
		w.Header().Set("API-Version", dockerAPIVersion)
		w.Header().Set("Docker-Experimental", "false")
		w.Header().Set("OSType", "linux")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
		if !f.listSeen.CompareAndSwap(false, true) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		if err := f.writeMarker("list-started"); err != nil {
			http.Error(w, "fixture marker failed", http.StatusInternalServerError)
			return
		}
		select {
		case <-r.Context().Done():
			_ = f.writeMarker("list-cancelled")
		case <-time.After(45 * time.Second):
			_ = f.writeMarker("list-timeout")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *dockerFixture) writeMarker(name string) error {
	if name == "" || strings.ContainsAny(name, `/\\`) || name == "." || name == ".." {
		return errors.New("invalid fixture marker name")
	}
	file, err := os.OpenFile(filepath.Join(f.stateDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "%d\n", time.Now().UnixNano())
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func serve(socketPath, stateDir string) error {
	if !filepath.IsAbs(socketPath) || !filepath.IsAbs(stateDir) {
		return errors.New("fixture socket and state paths must be absolute")
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return fmt.Errorf("refusing to replace existing fixture socket path %s", socketPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect fixture socket path: %w", err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create private fixture state directory: %w", err)
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fixture state path must be a real directory: %s", stateDir)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("protect fixture state directory: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on guest-local Docker fixture socket: %w", err)
	}
	defer func() { _ = listener.Close(); _ = os.Remove(socketPath) }()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("protect Docker fixture socket: %w", err)
	}
	if err := os.Chown(socketPath, 65534, 65534); err != nil {
		return fmt.Errorf("assign Docker fixture socket to Agent service UID: %w", err)
	}
	server := &http.Server{Handler: &dockerFixture{stateDir: stateDir}, ReadHeaderTimeout: 3 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case sig := <-signals:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("shut down fixture after %s: %w", sig, err)
		}
		return nil
	case err := <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Docker fixture: %w", err)
	}
}

func main() {
	socketPath := flag.String("socket", "", "private guest-local Unix socket path")
	stateDir := flag.String("state-dir", "", "private fixture marker directory")
	flag.Parse()
	if *socketPath == "" || *stateDir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: s03-docker-stall-fixture --socket ABSOLUTE --state-dir ABSOLUTE")
		os.Exit(2)
	}
	if err := serve(*socketPath, *stateDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
