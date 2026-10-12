package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"golang.org/x/sys/unix"
)

func Call(ctx context.Context, socket string, rpc RPC) (Response, error) {
	if socket == "" {
		socket = Socket
	}
	raw, err := json.Marshal(rpc)
	if err != nil {
		return Response{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://updater/rpc", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Response{}, errors.New("update service is unavailable; install and configure nodedance-updater on the deployment host")
	}
	defer response.Body.Close()
	var result Response
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}
func (m *Manager) Handle(ctx context.Context, rpc RPC) Response {
	if rpc.Action == "tailscale-peers" {
		// This fixed read never accepts a command, path, target node or update options.
		if rpc.Request != (Request{Component: "core"}) || rpc.IdempotencyKey != "" {
			return Response{Status: 400, Error: "host discovery requires only component core"}
		}
		if _, ok := m.Config.Targets["core"]; !ok {
			return Response{Status: 403, Error: "Core is not registered on this host"}
		}
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		peers, err := (tailscale.Discovery{Runner: m.tailscaleRunner}).List(ctx)
		if errors.Is(err, tailscale.ErrUnavailable) {
			return Response{Status: 404, Error: "Tailscale is unavailable on the deployment host"}
		}
		if errors.Is(err, tailscale.ErrNotLoggedIn) {
			return Response{Status: 412, Error: "Deployment host is not signed in to Tailscale"}
		}
		if err != nil {
			return Response{Status: 503, Error: "Cannot read deployment host Tailscale status"}
		}
		raw, err := json.Marshal(peers)
		if err != nil || len(raw) > 900<<10 {
			return Response{Status: 502, Error: "Host discovery response exceeds its limit"}
		}
		return Response{Status: 200, Data: raw}
	}
	if err := Validate(rpc.Request); err != nil {
		return Response{Status: 400, Error: err.Error()}
	}
	var data any
	var err error
	status := 200
	switch rpc.Action {
	case "check":
		data, err = m.Check(ctx, rpc.Request)
	case "apply", "rollback":
		if rpc.Action == "apply" && rpc.Request.JobID != "" {
			return Response{Status: 400, Error: "use rollback for a completed update"}
		}
		if rpc.Action == "rollback" && rpc.Request.JobID == "" {
			return Response{Status: 400, Error: "rollback requires jobId"}
		}
		data, err = m.Apply(ctx, rpc.Request, rpc.IdempotencyKey)
		status = 202
	case "status":
		if rpc.Request.JobID == "" {
			jobs := []Job{}
			for _, job := range m.Jobs() {
				if job.Component == rpc.Request.Component {
					jobs = append(jobs, job)
				}
			}
			data = map[string]any{"jobs": jobs}
		} else {
			data, err = m.Get(rpc.Request.Component, rpc.Request.JobID)
		}
	default:
		return Response{Status: 400, Error: "unknown update operation"}
	}
	if err != nil {
		return Response{Status: 409, Error: err.Error()}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Response{Status: 500, Error: "cannot encode update state"}
	}
	return Response{Status: status, Data: raw}
}
func Serve(ctx context.Context, manager *Manager) error {
	path := manager.Config.Socket
	if path == "" {
		path = Socket
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0750); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(directory)
	info, statErr := os.Lstat(directory)
	if err != nil || statErr != nil || real != filepath.Clean(directory) || !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("update socket directory must be root-owned without symlinks or group writes")
	}
	if err := os.Chown(directory, 0, manager.Config.CoreGID); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0750); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("update socket path is not a socket")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chown(path, 0, manager.Config.CoreGID); err != nil {
		return err
	}
	if err = os.Chmod(path, 0660); err != nil {
		return err
	}
	var peers sync.Map
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 65 * time.Second, WriteTimeout: 65 * time.Second, ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
		socket, ok := conn.(*net.UnixConn)
		if !ok {
			return ctx
		}
		raw, err := socket.SyscallConn()
		if err != nil {
			return ctx
		}
		var peer *unix.Ucred
		raw.Control(func(fd uintptr) { peer, _ = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
		if peer != nil {
			peers.Store(conn, peer.Uid)
		}
		return context.WithValue(ctx, connectionKey{}, conn)
	}, ConnState: func(conn net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			peers.Delete(conn)
		}
	}}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := peers.Load(r.Context().Value(connectionKey{}))
		if !ok || (uid.(uint32) != 0 && uid.(uint32) != manager.Config.CoreUID) {
			http.Error(w, "update socket peer rejected", 403)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/rpc" {
			http.Error(w, "method not allowed", 405)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		var rpc RPC
		if err != nil || Decode(raw, &rpc) != nil {
			http.Error(w, "invalid update request", 400)
			return
		}
		result := manager.Handle(r.Context(), rpc)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("update service: %w", err)
	}
}

type connectionKey struct{}
