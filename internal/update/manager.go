package update

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/tailscale"
	"golang.org/x/sys/unix"
)

type Manager struct {
	tailscaleRunner tailscale.Runner
	Config          Config
	Resolver        Resolver
	command         func(context.Context, string, ...string) ([]byte, error)
	healthTimeout   time.Duration
	Docker          *Docker
	mu              sync.Mutex
	jobs            map[string]Job
	active          bool
	closed          bool
	ctx             context.Context
	cancel          context.CancelFunc
	workers         sync.WaitGroup
	lock            *os.File
}

func New(config Config, resolver Resolver) (*Manager, error) {
	if target, ok := config.Targets["core"]; ok && target.Mode != "systemd" {
		return nil, errors.New("Core updates require the native nodedance.service")
	}
	if os.Geteuid() != 0 {
		return nil, errors.New("update controller requires root")
	}
	if err := privateDirectory(config.StateDir); err != nil {
		return nil, err
	}
	for component, target := range config.Targets {
		if component != "core" && component != "agent" {
			return nil, errors.New("invalid managed component")
		}
		if component == "core" && target.Mode != "systemd" {
			return nil, errors.New("Core updates require the native nodedance.service")
		}
		if target.Mode != "systemd" && !(component == "agent" && target.Mode == "docker") {
			return nil, errors.New("Agent deployment must be docker or systemd")
		}
		if !filepath.IsAbs(target.DataDir) || target.DataDir == "/" || target.DataDir == config.StateDir || strings.HasPrefix(config.StateDir, target.DataDir+"/") || strings.HasPrefix(target.DataDir, config.StateDir+"/") {
			return nil, errors.New("invalid managed data directory")
		}
		real, err := filepath.EvalSymlinks(target.DataDir)
		if err != nil || real != filepath.Clean(target.DataDir) {
			return nil, errors.New("managed data directory must not contain symlinks")
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return nil, errors.New("managed data requires mode 0700")
		}
		if component == "agent" {
			if filepath.Dir(agentConfig(target)) != real {
				return nil, errors.New("Agent credentials must remain inside the private state directory")
			}
			if info.Sys().(*syscall.Stat_t).Uid != 0 {
				return nil, errors.New("Agent state must be root-owned")
			}
			info, err := os.Lstat(agentConfig(target))
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 {
				return nil, errors.New("Agent config requires a regular 0600 file")
			}
		}
		if target.Mode == "docker" && !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`).MatchString(target.Container) {
			return nil, errors.New("invalid managed container name")
		}
		if target.Mode == "systemd" {
			expected := "nodedance.service"
			if component == "agent" {
				expected = "nodedance-agent.service"
			}
			binaryInfo, binaryErr := os.Lstat(target.Binary)
			realBinary, realErr := filepath.EvalSymlinks(target.Binary)
			if target.Unit != expected || !filepath.IsAbs(target.Binary) || binaryErr != nil || realErr != nil || realBinary != filepath.Clean(target.Binary) || !binaryInfo.Mode().IsRegular() || binaryInfo.Sys().(*syscall.Stat_t).Uid != 0 || binaryInfo.Mode().Perm()&0022 != 0 {
				return nil, errors.New("invalid managed systemd service")
			}
		}
		if component == "core" {
			u, err := url.Parse(target.HealthURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Path != "/api/v1/health" || !localAddress(u.Hostname()) || u.Fragment != "" {
				return nil, errors.New("Core requires a registered local HTTP health endpoint")
			}
		}
		if target.ComposeFile != "" {
			info, err := os.Lstat(target.ComposeFile)
			if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0077 != 0 {
				return nil, errors.New("managed Compose file must be regular")
			}
		}
	}
	lock, err := os.OpenFile(filepath.Join(config.StateDir, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("an update controller is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{Config: config, Resolver: resolver, command: execute, healthTimeout: 3 * time.Minute, Docker: NewDocker(), jobs: map[string]Job{}, ctx: ctx, cancel: cancel, lock: lock}
	entries, err := os.ReadDir(config.StateDir)
	if err != nil {
		m.Close()
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") || !idPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(config.StateDir, entry.Name()))
		if err != nil {
			m.Close()
			return nil, err
		}
		var record journal
		if err = json.Unmarshal(raw, &record); err != nil {
			m.Close()
			return nil, errors.New("invalid durable update journal")
		}
		job := record.job()
		m.jobs[job.ID] = job
	}
	for _, job := range m.jobs {
		if job.Status == "running" || job.Status == "queued" {
			m.active = true
			m.workers.Add(1)
			go func(job Job) {
				defer m.workers.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				var err error
				if job.Stage != "queued" && job.Stage != "downloading" {
					recovery := job
					if job.Parent != "" {
						var ok bool
						m.mu.Lock()
						recovery, ok = m.jobs[job.Parent]
						m.mu.Unlock()
						if !ok {
							err = errors.New("rollback parent journal is missing")
						}
					}
					if err == nil && !(job.Source && (job.Stage == "pulling" || job.Stage == "building")) {
						err = m.rollback(ctx, &recovery)
					}
				}
				if err != nil {
					job.Status = "rollback_failed"
					job.Error = err.Error()
				} else {
					job.Status = "rolled_back"
					job.Error = "controller restarted; interrupted update recovered"
				}
				m.finish(&job)
			}(job)
			break
		}
	}
	return m, nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.cancel()
	m.mu.Unlock()
	m.workers.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock != nil {
		unix.Flock(int(m.lock.Fd()), unix.LOCK_UN)
		m.lock.Close()
		m.lock = nil
	}
}
func (m *Manager) persist(job Job) error {
	raw, err := json.Marshal(job.record())
	if err != nil {
		return err
	}
	if err = atomicFile(filepath.Join(m.Config.StateDir, job.ID+".json"), raw, 0600); err != nil {
		return err
	}
	m.jobs[job.ID] = job
	return nil
}
func (m *Manager) save(job *Job, stage string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.Stage = stage
	return m.persist(*job)
}
func (m *Manager) finish(job *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	job.Finished = &now
	if err := m.persist(*job); err != nil {
		job.Status = "journal_failed"
		job.Error = err.Error()
		m.jobs[job.ID] = *job
	}
	m.active = false
}
func execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	raw, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}
func (m *Manager) current(ctx context.Context, component string) (string, error) {
	target, ok := m.Config.Targets[component]
	if !ok {
		return "", errors.New("update controller is not configured for this component")
	}
	var raw []byte
	var err error
	if target.Mode == "docker" {
		// Only the Agent still deploys in a container. Docker exec cannot resolve
		// the original image path after its host-namespace entry, so the version
		// comes from the running container process's executable on the host.
		pidRaw, pidErr := m.command(ctx, "docker", "inspect", "--format", "{{.State.Pid}}", target.Container)
		if pidErr != nil {
			return "", pidErr
		}
		pid, pidErr := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
		if pidErr != nil || pid <= 1 {
			return "", errors.New("managed Agent container has no running process")
		}
		raw, err = m.command(ctx, fmt.Sprintf("/proc/%d/exe", pid), "--version")
	} else {
		raw, err = m.command(ctx, target.Binary, "--version")
	}
	if err != nil {
		return "", err
	}
	for _, word := range strings.Fields(string(raw)) {
		if ValidTag(component, word) {
			return word, nil
		}
	}
	for _, word := range strings.Fields(string(raw)) {
		if word == "dev" {
			return "dev", nil
		}
	}
	return "", errors.New("managed runtime does not report a component version")
}
func (m *Manager) Check(ctx context.Context, r Request) (Check, error) {
	if err := Validate(r); err != nil {
		return Check{}, err
	}
	if r.Version == ChannelBeta {
		return m.checkSource(ctx, r)
	}
	release, err := m.Resolver.Resolve(ctx, r.Component, r.Version)
	if err != nil {
		return Check{}, err
	}
	result := Check{Component: r.Component, Latest: release.Version, Release: release}
	target, configured := m.Config.Targets[r.Component]
	result.Configured = configured
	result.Mode = target.Mode
	if configured {
		result.Current, err = m.current(ctx, r.Component)
		if err != nil {
			return result, err
		}
	}
	result.Available = result.Current != "" && (result.Current == "dev" || Compare(release.Version, result.Current) > 0)
	return result, nil
}
func (m *Manager) checkSource(ctx context.Context, r Request) (Check, error) {
	result := Check{Component: r.Component, Latest: ChannelBeta}
	target, configured := m.Config.Targets[r.Component]
	result.Mode = target.Mode
	if !configured {
		result.Reason = "the beta channel requires the managed Core service"
		return result, nil
	}
	if target.SourceDir == "" {
		result.Reason = "NodeDance source checkout was not found on this host"
		return result, nil
	}
	if filepath.Base(filepath.Dir(target.Binary)) != "bin" {
		result.Reason = "the installed Core binary is not under a bin directory"
		return result, nil
	}
	result.Configured = true
	current, err := m.current(ctx, r.Component)
	if err != nil {
		return result, err
	}
	result.Current = current
	local, err := gitHead(ctx, target.SourceDir)
	if err != nil {
		return result, err
	}
	remote, err := gitRemoteMain(ctx, target.SourceDir)
	if err != nil {
		return result, err
	}
	if local == remote {
		result.Reason = "source checkout already matches origin/main"
		return result, nil
	}
	ancestor, err := gitIsAncestor(ctx, target.SourceDir, local, remote)
	if err != nil {
		return result, err
	}
	if !ancestor {
		result.Reason = "source checkout has diverged from origin/main"
		return result, nil
	}
	result.Available = true
	return result, nil
}
func (m *Manager) Apply(ctx context.Context, r Request, key string) (Job, error) {
	if err := Validate(r); err != nil {
		return Job{}, err
	}
	if !r.Confirm {
		return Job{}, errors.New("update requires explicit confirmation")
	}
	if len(key) < 8 || len(key) > 128 {
		return Job{}, errors.New("Idempotency-Key must contain 8 to 128 bytes")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Job{}, errors.New("update controller is shutting down")
	}
	for _, job := range m.jobs {
		if job.IdempotencyKey == key {
			m.mu.Unlock()
			if job.Component != r.Component || (r.Version != "" && r.Version != job.Target) || r.JobID != job.Parent {
				return Job{}, errors.New("idempotency key was used for another update")
			}
			return job, nil
		}
	}
	if m.active {
		m.mu.Unlock()
		return Job{}, errors.New("another update is running")
	}
	m.active = true
	// Admission includes release lookup and durable acceptance. Shutdown must
	// retain the controller lock until both admission and execution have exited.
	m.workers.Add(1)
	m.mu.Unlock()
	defer m.workers.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	defer func() { stop(); cancel() }()
	accepted := false
	defer func() {
		if !accepted {
			m.mu.Lock()
			m.active = false
			m.mu.Unlock()
		}
	}()
	var check Check
	var parent Job
	var err error
	if r.JobID != "" {
		m.mu.Lock()
		parent = m.jobs[r.JobID]
		m.mu.Unlock()
		if parent.ID == "" || parent.Component != r.Component || parent.Status != "succeeded" || !parent.Switched || parent.BackupDeleted || parent.BackupDeleting {
			return Job{}, errors.New("no successful update is available for rollback")
		}
		current, err := m.current(ctx, r.Component)
		if err != nil || current != parent.Target {
			return Job{}, errors.New("runtime has changed since the selected update")
		}
		check = Check{Current: current, Latest: parent.Current, Mode: parent.Mode}
	} else {
		check, err = m.Check(ctx, r)
		if err != nil {
			return Job{}, err
		}
		if !check.Configured || !check.Available {
			return Job{}, errors.New("no newer release is available")
		}
	}
	var bytes [16]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return Job{}, err
	}
	job := Job{ID: hex.EncodeToString(bytes[:]), Component: r.Component, Mode: check.Mode, Current: check.Current, Target: check.Latest, Status: "queued", Stage: "queued", Started: time.Now().UTC(), Parent: r.JobID, Source: r.JobID == "" && r.Version == ChannelBeta, IdempotencyKey: key}
	if err = privateDirectory(filepath.Join(m.Config.StateDir, job.ID)); err != nil {
		return Job{}, err
	}
	m.mu.Lock()
	if m.closed {
		err = errors.New("update controller is shutting down")
	} else {
		err = m.persist(job)
	}
	m.mu.Unlock()
	if err != nil {
		return Job{}, err
	}
	accepted = true
	m.workers.Add(1)
	go func(job Job, parent Job) {
		defer m.workers.Done()
		if parent.ID != "" {
			m.runRollback(job, parent)
			return
		}
		if job.Source {
			m.runSource(job)
			return
		}
		m.run(job, check.Release)
	}(job, parent)
	return job, nil
}

func (m *Manager) runRollback(job, parent Job) {
	ctx, cancel := context.WithTimeout(m.ctx, 5*time.Minute)
	defer cancel()
	job.Status = "running"
	if err := m.save(&job, "rolling_back"); err != nil {
		job.Status, job.Error = "failed", err.Error()
		m.finish(&job)
		return
	}
	if err := m.rollback(ctx, &parent); err != nil {
		job.Status, job.Error = "rollback_failed", err.Error()
	} else {
		job.Status, job.Stage = "succeeded", "rolled_back"
	}
	m.finish(&job)
}

func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Started.After(jobs[j].Started) })
	return jobs
}
func (m *Manager) Get(component, id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok || job.Component != component {
		return Job{}, errors.New("update job not found")
	}
	return job, nil
}
func (m *Manager) run(job Job, release Release) {
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Minute)
	defer cancel()
	job.Status = "running"
	target := m.Config.Targets[job.Component]
	err := func() error {
		if err := m.save(&job, "downloading"); err != nil {
			return err
		}
		workspace := filepath.Join(m.Config.StateDir, job.ID)
		if job.Component == "agent" {
			generation, ok := m.agentOnline(ctx, target, 0)
			if !ok {
				return errors.New("Agent must be connected before updating")
			}
			job.AgentGeneration = generation
		}

		image := ""
		binary := filepath.Join(workspace, "release-binary")
		if target.Mode == "docker" {
			if err := m.Docker.initialize(ctx); err != nil {
				return err
			}
			var err error
			image, err = m.Docker.pull(ctx, "ghcr.io/cst-cat/nodedance-"+job.Component+":"+release.Version)
			if err != nil {
				return err
			}
			old, err := m.Docker.inspect(ctx, target.Container)
			if err != nil {
				return err
			}
			job.OldContainer, _ = old["Id"].(string)
			if job.OldContainer == "" {
				return errors.New("managed container ID is missing")
			}
			raw, _ := json.Marshal(old)
			if err = atomicFile(filepath.Join(workspace, "container.json"), raw, 0600); err != nil {
				return err
			}
		} else {
			if err := m.Resolver.Download(ctx, release, binary); err != nil {
				return err
			}
			job.OldBinary = filepath.Join(workspace, "previous-binary")
			if err := copyBinary(target.Binary, job.OldBinary); err != nil {
				return err
			}
		}
		if target.ComposeFile != "" {
			raw, err := os.ReadFile(target.ComposeFile)
			if err != nil {
				return err
			}
			job.OldCompose = filepath.Join(workspace, "compose.json")
			if err = atomicFile(job.OldCompose, raw, 0600); err != nil {
				return err
			}
		}
		if err := m.save(&job, "stopping"); err != nil {
			return err
		}
		if err := m.stop(ctx, job); err != nil {
			return err
		}
		if err := m.save(&job, "backing_up"); err != nil {
			return err
		}
		if err := copyTree(target.DataDir, filepath.Join(workspace, "data")); err != nil {
			return err
		}
		job.Snapshot = true
		job.Backup = job.ID + "/data"
		job.Switched = true
		if err := m.save(&job, "switching"); err != nil {
			return err
		}
		if target.Mode == "docker" {
			raw, err := os.ReadFile(filepath.Join(workspace, "container.json"))
			if err != nil {
				return err
			}
			var old map[string]any
			json.Unmarshal(raw, &old)
			if err = m.Docker.rename(ctx, job.OldContainer, target.Container+".rollback-"+job.ID); err != nil {
				return err
			}
			if err = m.Docker.disconnect(ctx, job.OldContainer, old); err != nil {
				return err
			}
			job.NewContainer, err = m.Docker.create(ctx, target.Container, image, job.ID, old)
			if err != nil {
				return err
			}
		} else {
			if err := installBinary(binary, target.Binary); err != nil {
				return err
			}
		}
		job.Switched = true
		if err := m.save(&job, "starting"); err != nil {
			return err
		}
		if err := m.start(ctx, job); err != nil {
			return err
		}
		if err := m.save(&job, "verifying"); err != nil {
			return err
		}
		healthCtx, healthCancel := context.WithTimeout(ctx, m.healthTimeout)
		defer healthCancel()
		if err := m.verify(healthCtx, job.Component, job.Target, job.AgentGeneration); err != nil {
			return err
		}
		if target.ComposeFile != "" {
			if err := m.writeCompose(ctx, target, job.Component, job.Target); err != nil {
				return err
			}
		}

		return nil
	}()
	if err == nil {
		job.Status = "succeeded"
		job.Stage = "complete"
		m.finish(&job)
		return
	}
	job.Error = err.Error()
	rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer rollbackCancel()
	if job.Stage != "downloading" && (job.OldContainer != "" || job.OldBinary != "") {
		m.save(&job, "rolling_back")
		if err := m.rollback(rollbackCtx, &job); err != nil {
			job.Status = "rollback_failed"
			job.Error += "; rollback: " + err.Error()
		} else {
			job.Status = "rolled_back"
			job.Stage = "rolled_back"
		}
	} else {
		job.Status = "failed"
	}
	m.finish(&job)
}
func (m *Manager) runSource(job Job) {
	ctx, cancel := context.WithTimeout(m.ctx, 20*time.Minute)
	defer cancel()
	job.Status = "running"
	target := m.Config.Targets[job.Component]
	source := target.SourceDir
	workspace := filepath.Join(m.Config.StateDir, job.ID)
	err := func() error {
		if err := m.save(&job, "pulling"); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, source, "fetch", "origin", "main"); err != nil {
			return err
		}
		if _, err := gitOutput(ctx, source, "pull", "--ff-only", "origin", "main"); err != nil {
			return err
		}
		job.OldBinary = filepath.Join(workspace, "previous-binary")
		if err := copyBinary(target.Binary, job.OldBinary); err != nil {
			return err
		}
		if err := m.save(&job, "building"); err != nil {
			return err
		}
		if _, err := runSourceCommand(ctx, source, "make", "build"); err != nil {
			return err
		}
		if err := m.save(&job, "installing"); err != nil {
			return err
		}
		prefix := filepath.Dir(filepath.Dir(target.Binary))
		if _, err := runSourceCommand(ctx, source, "make", "install", "PREFIX="+prefix); err != nil {
			return err
		}
		job.Switched = true
		if err := m.save(&job, "restarting"); err != nil {
			return err
		}
		if _, err := m.command(ctx, "systemctl", "restart", target.Unit); err != nil {
			return err
		}
		if err := m.save(&job, "verifying"); err != nil {
			return err
		}
		healthCtx, healthCancel := context.WithTimeout(ctx, m.healthTimeout)
		defer healthCancel()
		return m.verifySource(healthCtx, target)
	}()
	if err == nil {
		job.Status = "succeeded"
		job.Stage = "complete"
		m.finish(&job)
		return
	}
	job.Error = err.Error()
	if job.Switched {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer rollbackCancel()
		m.save(&job, "rolling_back")
		if rollbackErr := m.rollbackSource(rollbackCtx, job, target); rollbackErr != nil {
			job.Status = "rollback_failed"
			job.Error += "; rollback: " + rollbackErr.Error()
		} else {
			job.Status = "rolled_back"
			job.Stage = "rolled_back"
		}
	} else {
		job.Status = "failed"
	}
	m.finish(&job)
}
func (m *Manager) rollbackSource(ctx context.Context, job Job, target Target) error {
	if err := installBinary(job.OldBinary, target.Binary); err != nil {
		return err
	}
	if _, err := m.command(ctx, "systemctl", "restart", target.Unit); err != nil {
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, m.healthTimeout)
	defer cancel()
	return m.verifySource(healthCtx, target)
}
func (m *Manager) verifySource(ctx context.Context, target Target) error {
	for {
		ready := false
		req, reqErr := http.NewRequestWithContext(ctx, "GET", target.HealthURL, nil)
		if reqErr != nil {
			return reqErr
		}
		client, clientErr := healthClient(target.HealthCAFile, target.HealthServerName)
		if clientErr != nil {
			return clientErr
		}
		response, err := client.Do(req)
		client.CloseIdleConnections()
		if err == nil {
			var health struct {
				Status string `json:"status"`
			}
			json.NewDecoder(response.Body).Decode(&health)
			response.Body.Close()
			ready = response.StatusCode == 200 && health.Status == "ok"
		}
		if ready {
			return nil
		}
		if err := pause(ctx, time.Second); err != nil {
			return errors.New("source-built runtime failed health verification")
		}
	}
}
func copyBinary(source, destination string) error {
	return atomicCopy(source, destination, 0700)
}
func installBinary(source, destination string) error {
	return atomicCopy(source, destination, 0755)
}
func (m *Manager) stop(ctx context.Context, job Job) error {
	target := m.Config.Targets[job.Component]
	if target.Mode == "docker" {
		return m.Docker.stop(ctx, job.OldContainer)
	}
	_, err := m.command(ctx, "systemctl", "stop", target.Unit)
	return err
}
func (m *Manager) start(ctx context.Context, job Job) error {
	target := m.Config.Targets[job.Component]
	if target.Mode == "docker" {
		return m.Docker.start(ctx, job.NewContainer)
	}
	_, err := m.command(ctx, "systemctl", "start", target.Unit)
	return err
}
func (m *Manager) verify(ctx context.Context, component, version string, previousGeneration uint64) error {
	target := m.Config.Targets[component]
	for {
		current, err := m.current(ctx, component)
		ready := err == nil && current == version
		if ready && target.Mode == "docker" {
			value, err := m.Docker.inspect(ctx, target.Container)
			ready = err == nil && healthy(value)
		}
		if ready && component == "core" {
			req, _ := http.NewRequestWithContext(ctx, "GET", target.HealthURL, nil)
			client, clientErr := healthClient(target.HealthCAFile, target.HealthServerName)
			if clientErr != nil {
				return clientErr
			}
			response, err := client.Do(req)
			client.CloseIdleConnections()
			ready = false
			if err == nil {
				var health struct {
					Status  string `json:"status"`
					Version string `json:"version"`
				}
				json.NewDecoder(response.Body).Decode(&health)
				response.Body.Close()
				ready = response.StatusCode == 200 && health.Status == "ok" && health.Version == version
			}
		}
		if ready && component == "agent" {
			_, ready = m.agentOnline(ctx, target, previousGeneration)
		}

		if ready {
			return nil
		}
		if err := pause(ctx, time.Second); err != nil {
			return errors.New("updated runtime failed health or Agent reconnection verification")
		}
	}
}
func (m *Manager) rollback(ctx context.Context, job *Job) error {
	target, ok := m.Config.Targets[job.Component]
	if !ok {
		return errors.New("managed deployment is unavailable during recovery")
	}
	workspace := filepath.Join(m.Config.StateDir, job.ID)
	if target.Mode == "docker" {
		if err := m.Docker.initialize(ctx); err != nil {
			return err
		}
		current, err := m.Docker.inspect(ctx, target.Container)
		if err == nil {
			currentID, _ := current["Id"].(string)
			if currentID != job.OldContainer {
				config, _ := current["Config"].(map[string]any)
				labels, _ := config["Labels"].(map[string]any)
				if labels["io.nodedance.update.job"] != job.ID {
					return errors.New("refusing to replace a container changed outside this update")
				}
				_ = m.Docker.stop(ctx, currentID)
				if err = m.Docker.remove(ctx, currentID); err != nil {
					return err
				}
			} else {
				_ = m.Docker.stop(ctx, currentID)
			}
		}
	} else {
		if _, err := m.command(ctx, "systemctl", "stop", target.Unit); err != nil {
			return err
		}
		if job.OldBinary != "" {
			if err := installBinary(job.OldBinary, target.Binary); err != nil {
				return err
			}
		}
	}

	if job.Snapshot && job.Switched && job.Component == "core" {
		if err := restoreTree(filepath.Join(workspace, "data"), target.DataDir, job.ID); err != nil {
			return err
		}
	}
	if target.ComposeFile != "" && job.OldCompose != "" {
		raw, err := os.ReadFile(job.OldCompose)
		if err != nil {
			return err
		}
		if err = atomicFile(target.ComposeFile, raw, 0600); err != nil {
			return err
		}
	}
	if target.Mode == "docker" {
		raw, err := os.ReadFile(filepath.Join(workspace, "container.json"))
		if err != nil {
			return err
		}
		var old map[string]any
		json.Unmarshal(raw, &old)
		if err = m.Docker.rename(ctx, job.OldContainer, target.Container); err != nil {
			return err
		}
		value, err := m.Docker.inspect(ctx, job.OldContainer)
		if err != nil {
			return err
		}
		_ = value
		if err = m.Docker.connect(ctx, job.OldContainer, old); err != nil {
			return err
		}
		if err = m.Docker.start(ctx, job.OldContainer); err != nil {
			return err
		}
	} else {
		if _, err := m.command(ctx, "systemctl", "start", target.Unit); err != nil {
			return err
		}
	}
	return m.verify(ctx, job.Component, job.Current, job.AgentGeneration)
}
func (m *Manager) writeCompose(ctx context.Context, target Target, component, version string) error {
	raw, err := m.command(ctx, "docker", "compose", "-f", target.ComposeFile, "config", "--format", "json")
	if err != nil {
		return err
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		return err
	}
	services, _ := config["services"].(map[string]any)
	found := false
	for name, value := range services {
		service, _ := value.(map[string]any)
		if service["container_name"] == target.Container || name == component {
			service["image"] = "ghcr.io/cst-cat/nodedance-" + component + ":" + version
			delete(service, "build")
			found = true
		}
	}
	if !found {
		return errors.New("managed container is not present in the registered Compose file")
	}
	raw, err = json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(target.ComposeFile, raw, 0600)
}

func (m *Manager) agentOnline(ctx context.Context, target Target, previous uint64) (uint64, bool) {
	raw, err := os.ReadFile(agentConfig(target))
	if err != nil {
		return 0, false
	}
	var config struct {
		Server      string `json:"server"`
		Credential  string `json:"credential"`
		AgentID     string `json:"agentId"`
		NodeID      string `json:"nodeId"`
		CAFile      string `json:"caFile"`
		Development bool   `json:"development"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return 0, false
	}
	address, err := url.Parse(config.Server)
	if err != nil || address.User != nil || address.Host == "" || address.Scheme != "https" && !(address.Scheme == "http" && config.Development && net.ParseIP(address.Hostname()) != nil && net.ParseIP(address.Hostname()).IsLoopback()) {
		return 0, false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	if config.CAFile != "" {
		raw, err := os.ReadFile(config.CAFile)
		if err != nil {
			return 0, false
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return 0, false
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(config.Server, "/")+"/api/v1/agents/identity", nil)
	if err != nil {
		return 0, false
	}
	request.Header.Set("Authorization", "Bearer "+config.Credential)
	request.Header.Set("User-Agent", "NodeDance")
	response, err := client.Do(request)
	if err != nil {
		return 0, false
	}
	defer response.Body.Close()
	var identity struct {
		AgentID    string `json:"agentId"`
		NodeID     string `json:"nodeId"`
		Status     string `json:"status"`
		Generation uint64 `json:"generation"`
	}
	ok := response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&identity) == nil && identity.AgentID == config.AgentID && identity.NodeID == config.NodeID && identity.Status == "online" && identity.Generation != previous
	return identity.Generation, ok
}

func localAddress(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		value, _, err := net.ParseCIDR(address.String())
		if err == nil && value.Equal(ip) {
			return true
		}
	}
	return false
}
func healthClient(caFile, serverName string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(raw) {
			return nil, errors.New("health CA file contains no certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func agentConfig(target Target) string {
	if target.AgentConfig != "" {
		return target.AgentConfig
	}
	return filepath.Join(target.DataDir, "agent.json")
}
