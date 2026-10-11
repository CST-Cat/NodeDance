// Package hostcontrol owns Agent settings and durable host command execution.
package hostcontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
)

type Manager struct {
	mu       sync.Mutex
	closed   bool
	cancel   context.CancelFunc
	settings protocol.AgentSettings
	dir      string
	ctx      context.Context
	running  map[string]context.CancelFunc
	wg       sync.WaitGroup
}

func Open(ctx context.Context, dir string) (*Manager, error) {
	if err := localstate.Directory(dir); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	m := &Manager{cancel: cancel, settings: protocol.DefaultAgentSettings(), dir: dir, ctx: ctx, running: map[string]context.CancelFunc{}}
	err := localstate.Read(filepath.Join(dir, "settings.json"), &m.settings)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := protocol.ValidateAgentSettings(m.settings); err != nil {
		return nil, err
	}
	if err := localstate.Directory(filepath.Join(dir, "commands")); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "commands", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		var j protocol.CommandJob
		if err := localstate.Read(f, &j); err != nil {
			return nil, err
		}
		if j.Status == "running" {
			j.Status = "interrupted"
			now := time.Now().UTC()
			j.FinishedAt = &now
			j.Error = "Agent restarted during execution; command was not replayed"
			if err := localstate.Write(f, j); err != nil {
				return nil, err
			}
		}
	}
	return m, nil
}
func (m *Manager) Settings() protocol.AgentSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSettings(m.settings)
}
func cloneSettings(s protocol.AgentSettings) protocol.AgentSettings {
	s.IncludeInterfaces = append([]string{}, s.IncludeInterfaces...)
	s.ExcludeInterfaces = append([]string{}, s.ExcludeInterfaces...)
	s.IncludeMountpoints = append([]string{}, s.IncludeMountpoints...)
	return s
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	for _, cancel := range m.running {
		cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
func (m *Manager) Handle(req protocol.HostRequest) protocol.HostResponse {
	fail := func(status int, e error) protocol.HostResponse {
		return protocol.HostResponse{Status: status, Error: e.Error()}
	}
	switch req.Action {
	case "settings.get":
		s := m.Settings()
		return protocol.HostResponse{Status: 200, Settings: &s}
	case "settings.put":
		if req.Settings == nil {
			return fail(400, errors.New("settings are required"))
		}
		if err := protocol.ValidateAgentSettings(*req.Settings); err != nil {
			return fail(400, err)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := localstate.Write(filepath.Join(m.dir, "settings.json"), req.Settings); err != nil {
			return fail(500, errors.New("could not persist settings"))
		}
		m.settings = cloneSettings(*req.Settings)
		if m.settings.DisableRemoteControl {
			for _, cancel := range m.running {
				cancel()
			}
		}
		saved := cloneSettings(m.settings)
		return protocol.HostResponse{Status: 200, Settings: &saved}
	case "diagnostics":
		s := m.Settings()
		return protocol.HostResponse{Status: 200, Settings: &s, Diagnostics: map[string]any{"uid": os.Geteuid(), "pid": os.Getpid(), "stateDirectory": m.dir, "credential": "[redacted]", "settingsApply": "sampling on next interval; connection settings on reconnect", "updateSupervisorSocket": "/run/nodedance-updater/control.sock"}}
	case "commands.submit":
		j, err := m.submit(req)
		if err != nil {
			return fail(400, err)
		}
		return protocol.HostResponse{Status: 202, Job: &j}
	case "commands.get", "commands.cancel":
		if !validJobID(req.JobID) {
			return fail(400, errors.New("invalid command job ID"))
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		var j protocol.CommandJob
		if e := localstate.Read(m.jobPath(req.JobID), &j); e != nil {
			return fail(404, errors.New("command job not found"))
		}
		if req.Action == "commands.cancel" {
			if cancel := m.running[req.JobID]; cancel != nil {
				cancel()
			}
		}
		return protocol.HostResponse{Status: 200, Job: &j}
	case "commands.list":
		m.mu.Lock()
		defer m.mu.Unlock()
		files, _ := filepath.Glob(filepath.Join(m.dir, "commands", "*.json"))
		jobs := []protocol.CommandJob{}
		for _, f := range files {
			var j protocol.CommandJob
			if localstate.Read(f, &j) == nil {
				j.Output = ""
				jobs = append(jobs, j)
			}
		}
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.After(jobs[j].StartedAt) })
		if len(jobs) > 100 {
			jobs = jobs[:100]
		}
		return protocol.HostResponse{Status: 200, Jobs: jobs}
	default:
		return fail(400, errors.New("unsupported host operation"))
	}
}
func validJobID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil
}
func (m *Manager) jobPath(id string) string { return filepath.Join(m.dir, "commands", id+".json") }
func (m *Manager) submit(req protocol.HostRequest) (protocol.CommandJob, error) {
	if req.Command == nil {
		return protocol.CommandJob{}, errors.New("command is required")
	}
	if e := protocol.ValidateCommand(*req.Command); e != nil {
		return protocol.CommandJob{}, e
	}
	if len(req.IdempotencyKey) < 8 || len(req.IdempotencyKey) > 128 {
		return protocol.CommandJob{}, errors.New("Idempotency-Key must contain 8..128 bytes")
	}
	sum := sha256.Sum256([]byte(req.IdempotencyKey))
	id := hex.EncodeToString(sum[:])
	data, _ := json.Marshal(req.Command)
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.settings.DisableRemoteControl {
		return protocol.CommandJob{}, errors.New("remote script execution disabled by Agent settings")
	}
	if m.closed {
		return protocol.CommandJob{}, errors.New("Agent is shutting down")
	}
	var j protocol.CommandJob
	if err := localstate.Read(m.jobPath(id), &j); err == nil {
		if j.RequestHash != hash {
			return j, errors.New("idempotency key was used for a different command")
		}
		return j, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return j, errors.New("command state unavailable")
	}
	if len(m.running) >= 4 {
		return j, errors.New("Agent command concurrency limit reached")
	}
	files, _ := filepath.Glob(filepath.Join(m.dir, "commands", "*.json"))
	if len(files) >= 10000 {
		return j, errors.New("command history limit reached")
	}
	j = protocol.CommandJob{ID: id, Status: "running", StartedAt: time.Now().UTC(), RequestHash: hash}
	if err := localstate.Write(m.jobPath(id), j); err != nil {
		return j, err
	}
	ctx, cancel := context.WithTimeout(m.ctx, time.Duration(req.Command.TimeoutSeconds)*time.Second)
	m.running[id] = cancel
	m.wg.Add(1)
	script := req.Command.Script
	go func() { defer m.wg.Done(); defer cancel(); m.execute(ctx, j, script) }()
	return j, nil
}

type outputBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := (128 << 10) - len(b.data)
	if len(p) > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (m *Manager) execute(ctx context.Context, j protocol.CommandJob, script string) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = time.Second
	output := &outputBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	code := 0
	j.Status = "succeeded"
	if err != nil {
		code = -1
		j.Status = "failed"
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		j.Error = "command failed"
	}
	if ctx.Err() != nil {
		j.Status = "cancelled"
		j.Error = "command cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			j.Status = "timed_out"
			j.Error = "command timeout"
		}
	}
	now := time.Now().UTC()
	j.FinishedAt = &now
	j.ExitCode = &code
	output.mu.Lock()
	j.Output = strings.ToValidUTF8(string(output.data), "�")
	j.Truncated = output.truncated
	if len(j.Output) > 128<<10 {
		j.Output = j.Output[:128<<10]
		for !utf8.ValidString(j.Output) {
			j.Output = j.Output[:len(j.Output)-1]
		}
		j.Truncated = true
	}
	output.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := localstate.Write(m.jobPath(j.ID), j); err != nil {
		fmt.Fprintln(os.Stderr, "could not persist command result")
	}
	delete(m.running, j.ID)
}

// Automatic updates use the installed supervisor and the published agent tag
// resolver, retaining its preflight, backup, health checks and rollback.
func (m *Manager) RunUpdates() {
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-timer.C:
		}
		s := m.Settings()
		if s.AutoUpdate {
			ctx, cancel := context.WithTimeout(m.ctx, 60*time.Second)
			r := update.Request{Component: "agent"}
			response, err := update.Call(ctx, "", update.RPC{Action: "check", Request: r})
			if err == nil && response.Status == 200 {
				var check update.Check
				if json.Unmarshal(response.Data, &check) == nil && check.Available && check.Compatible && check.Configured {
					r.Confirm = true
					r.Version = check.Release.Version
					key := "auto-agent-" + r.Version
					_, _ = update.Call(ctx, "", update.RPC{Action: "apply", Request: r, IdempotencyKey: key})
				}
			}
			cancel()
		}
		timer.Reset(time.Duration(s.UpdateIntervalHours) * time.Hour)
	}
}
