package update

// Updates execute through the installed product binary. A transient worker owns
// the transaction so replacing/restarting the application cannot kill it.
import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Invoke(ctx context.Context, component, dataDir string, rpc RPC) (Response, error) {
	if rpc.Request.Component != component {
		return Response{}, errors.New("update component does not match the running product")
	}
	config, container, engine, err := discover(ctx, component, dataDir)
	if err != nil {
		if rpc.Action == "check" {
			release, resolveErr := NewGitHub().Resolve(ctx, component, rpc.Request.Version)
			if resolveErr != nil {
				return Response{}, resolveErr
			}
			raw, _ := json.Marshal(Check{Component: component, Latest: release.Version, Release: release, Reason: err.Error()})
			return Response{Status: 200, Data: raw}, nil
		}
		return Response{}, err
	}
	configRaw, _ := json.Marshal(config)
	rpcRaw, _ := json.Marshal(rpc)
	if container != nil {
		return engine.worker(ctx, container, component, string(configRaw), string(rpcRaw))
	}
	executable, err := os.Executable()
	if err != nil {
		return Response{}, err
	}
	return launchNative(ctx, executable, string(configRaw), string(rpcRaw))
}
func launchNative(ctx context.Context, executable, configRaw, rpcRaw string) (Response, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Response{}, err
	}
	// PID 1 starts the worker independently of the requesting service cgroup.
	// Its reply is a private temporary file rather than a pipe tied to the target.
	reply, err := os.CreateTemp("", "nodedance-update-response-*.json")
	if err != nil {
		return Response{}, err
	}
	path := reply.Name()
	reply.Close()
	defer os.Remove(path)
	cmd := exec.CommandContext(ctx, "systemd-run", "--quiet", "--collect", "--no-block", "--property=UMask=0077", "--property=StandardOutput=file:"+path, "--property=StandardError=journal", "--unit=nodedance-update-"+hex.EncodeToString(nonce[:]), executable, "__update-rpc", configRaw, rpcRaw)
	if raw, err := cmd.CombinedOutput(); err != nil {
		return Response{}, fmt.Errorf("start built-in update worker: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		file, err := os.Open(path)
		if err != nil {
			return Response{}, err
		}
		raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if err != nil {
			return Response{}, err
		}
		if len(raw) > 1<<20 {
			return Response{}, errors.New("worker response exceeds limit")
		}
		var response Response
		if json.Unmarshal(raw, &response) == nil {
			return response, nil
		}
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func discover(ctx context.Context, component, dataDir string) (Config, map[string]any, *Docker, error) {
	if component != "core" && component != "agent" {
		return Config{}, nil, nil, errors.New("invalid component")
	}
	dataDir = filepath.Clean(dataDir)
	if !filepath.IsAbs(dataDir) || dataDir == "/" {
		return Config{}, nil, nil, errors.New("invalid product data directory")
	}
	target := Target{DataDir: dataDir}
	config := Config{StateDir: updateState(dataDir)}
	if component == "agent" {
		engine := NewDocker()
		if err := engine.initialize(ctx); err == nil {
			var containers []struct {
				ID string `json:"Id"`
			}
			if err := engine.call(ctx, "GET", engine.version+"/containers/json", nil, &containers); err != nil {
				return Config{}, nil, nil, err
			}
			var match map[string]any
			for _, c := range containers {
				value, e := engine.inspect(ctx, c.ID)
				if e != nil {
					return Config{}, nil, nil, e
				}
				state, _ := value["State"].(map[string]any)
				cfg, _ := value["Config"].(map[string]any)
				entries, _ := cfg["Entrypoint"].([]any)
				agentEntrypoint := false
				for _, entry := range entries {
					path, _ := entry.(string)
					base := filepath.Base(path)
					if base == "agent-entrypoint" || base == "nodedance-agent" {
						agentEntrypoint = true
					}
				}
				mounted := false
				mounts, _ := value["Mounts"].([]any)
				for _, entry := range mounts {
					mount, _ := entry.(map[string]any)
					if mount["Source"] == dataDir {
						mounted = true
					}
				}
				if intNumber(state["Pid"]) == os.Getpid() || mounted && agentEntrypoint {
					if match != nil {
						return Config{}, nil, nil, errors.New("ambiguous product container")
					}
					match = value
				}
			}
			if match != nil {
				cfg, _ := match["Config"].(map[string]any)
				target.Mode = "docker"
				target.Container = strings.TrimPrefix(match["Name"].(string), "/")
				// Resolve the application's private data mount to its actual host path.
				mounts, _ := match["Mounts"].([]any)
				for _, entry := range mounts {
					m, _ := entry.(map[string]any)
					dest, _ := m["Destination"].(string)
					if dest == dataDir {
						source, _ := m["Source"].(string)
						target.DataDir = source
						break
					}
				}
				labels, _ := cfg["Labels"].(map[string]any)
				if compose, _ := labels["com.docker.compose.project.config_files"].(string); compose != "" && !strings.Contains(compose, ",") {
					target.ComposeFile = compose
				}
				if err := runtimeHealth(&target, dataDir); err != nil {
					return Config{}, nil, nil, err
				}
				target.AgentConfig = runningAgentConfig(intNumber(match["State"].(map[string]any)["Pid"]), target.DataDir)
				config.StateDir = updateState(target.DataDir)
				config.Targets = map[string]Target{component: target}
				return config, match, engine, nil
			}
		}
		if _, err := os.Stat("/.dockerenv"); err == nil {
			return Config{}, nil, nil, errors.New("Agent Docker deployment requires the Docker socket mounted by the standard Compose configuration")
		}
	}
	if os.Geteuid() != 0 {
		return Config{}, nil, nil, errors.New("updating the installed service requires root")
	}
	target.Mode = "systemd"
	target.Unit = "nodedance.service"
	if component == "agent" {
		target.Unit = "nodedance-agent.service"
	}
	output, err := exec.CommandContext(ctx, "systemctl", "show", target.Unit, "--property=MainPID", "--value").Output()
	if err != nil {
		return Config{}, nil, nil, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 1 {
		return Config{}, nil, nil, errors.New(target.Unit + " is not running")
	}
	target.Binary, err = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return Config{}, nil, nil, err
	}
	if component == "core" {
		executable, executableErr := os.Executable()
		if executableErr != nil || filepath.Clean(executable) != filepath.Clean(target.Binary) {
			return Config{}, nil, nil, errors.New("this instance does not run the managed Core service binary")
		}
		target.HealthURL = "http://127.0.0.1:8180/api/v1/health"
	}
	if err := runtimeHealth(&target, dataDir); err != nil {
		return Config{}, nil, nil, err
	}
	if component == "agent" {
		target.AgentConfig = runningAgentConfig(pid, target.DataDir)
		target.DataDir = filepath.Dir(target.AgentConfig)
	}
	config.StateDir = updateState(target.DataDir)
	config.Targets = map[string]Target{component: target}
	return config, nil, nil, nil
}
func intNumber(v any) int { n, _ := v.(float64); return int(n) }

// Internal is entered only by a product-spawned worker, never by API-supplied
// commands. It does not install a binary, register a daemon or change services.
func Internal(args []string, stdout, stderr io.Writer) (bool, error) {
	return internal(args, stdout, stderr, NewGitHub(), New)
}

func internal(args []string, stdout, stderr io.Writer, resolver Resolver, factory func(Config, Resolver) (*Manager, error)) (bool, error) {
	if len(args) == 0 || (args[0] != "__update-rpc" && args[0] != "__update-host") {
		return false, nil
	}
	if os.Geteuid() != 0 || len(args) != 3 {
		return true, errors.New("internal update worker requires root and a fixed request")
	}
	if args[0] == "__update-host" {
		executable, err := os.Open("/proc/self/exe")
		if err != nil {
			return true, err
		}
		defer executable.Close()
		cmd := exec.Command("nsenter", "--target", "1", "--mount", "--uts", "--ipc", "--net", "--pid", "--cgroup", "--root", "--wd", "--no-fork", "--", "/proc/self/fd/3", "__update-rpc", args[1], args[2])
		cmd.ExtraFiles = []*os.File{executable}
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return true, cmd.Run()
	}
	var config Config
	var rpc RPC
	if Decode([]byte(args[1]), &config) != nil || Decode([]byte(args[2]), &rpc) != nil {
		return true, errors.New("invalid internal update request")
	}
	return true, runRPCWithFactory(config, rpc, stdout, resolver, factory)
}
func runRPC(config Config, rpc RPC, stdout io.Writer, resolver Resolver) error {
	return runRPCWithFactory(config, rpc, stdout, resolver, New)
}

func runRPCWithFactory(config Config, rpc RPC, stdout io.Writer, resolver Resolver, factory func(Config, Resolver) (*Manager, error)) error {
	if len(config.Targets) != 1 || config.Targets[rpc.Request.Component].Mode == "" {
		return errors.New("worker must manage its own product")
	}
	if rpc.Action != "apply" && rpc.Action != "rollback" && rpc.Action != "delete-backup" {
		// Observation must remain available while the transaction holds its lock.
		manager := &Manager{Config: config, Resolver: resolver, command: execute, Docker: NewDocker(), jobs: map[string]Job{}}
		entries, err := os.ReadDir(config.StateDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			id := strings.TrimSuffix(entry.Name(), ".json")
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !idPattern.MatchString(id) {
				continue
			}
			raw, e := os.ReadFile(filepath.Join(config.StateDir, entry.Name()))
			if e != nil {
				return e
			}
			var record journal
			if Decode(raw, &record) != nil {
				return errors.New("invalid update journal")
			}
			job := record.job()
			manager.jobs[job.ID] = job
		}
		interrupted := false
		for _, job := range manager.jobs {
			if job.Status == "queued" || job.Status == "running" {
				interrupted = true
			}
		}
		if interrupted {
			recovery, err := factory(config, resolver)
			if err == nil {
				defer recovery.Close()
				defer recovery.workers.Wait()
				manager = recovery
			} else if err.Error() != "an update controller is already running" {
				return err
			}
		}
		return json.NewEncoder(stdout).Encode(manager.Handle(context.Background(), rpc))
	}
	// A repeated submission can observe its durable acceptance while the original
	// worker still owns the lock, without starting another transaction.
	if err := Validate(rpc.Request); err != nil {
		return json.NewEncoder(stdout).Encode(Response{Status: 400, Error: err.Error()})
	}
	if rpc.IdempotencyKey == "" || !rpc.Request.Confirm {
		return json.NewEncoder(stdout).Encode(Response{Status: 400, Error: "confirmation and retry identity are required"})
	}
	entries, _ := os.ReadDir(config.StateDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !idPattern.MatchString(strings.TrimSuffix(entry.Name(), ".json")) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(config.StateDir, entry.Name()))
		if err != nil {
			return err
		}
		var record journal
		if Decode(raw, &record) != nil {
			return errors.New("invalid update journal")
		}
		job := record.job()
		if rpc.Action != "delete-backup" && job.IdempotencyKey == rpc.IdempotencyKey {
			if job.Component != rpc.Request.Component || (rpc.Request.Version != "" && rpc.Request.Version != job.Target) || rpc.Request.JobID != job.Parent {
				return json.NewEncoder(stdout).Encode(Response{Status: 409, Error: "idempotency key was used for another update"})
			}
			raw, _ = json.Marshal(job)
			return json.NewEncoder(stdout).Encode(Response{Status: 202, Data: raw})
		}
	}
	if err := os.MkdirAll(filepath.Dir(config.StateDir), 0700); err != nil {
		return err
	}
	manager, err := factory(config, resolver)
	if err != nil {
		return json.NewEncoder(stdout).Encode(Response{Status: 409, Error: err.Error()})
	}
	response := manager.Handle(context.Background(), rpc)
	err = json.NewEncoder(stdout).Encode(response)
	// Close waits for completion. The worker belongs to a separate service or
	// container and therefore survives the product's stop/restart.
	manager.workers.Wait()
	manager.Close()
	return err
}

// worker starts the fixed update worker container for a Docker Agent. Only the
// Agent still deploys in a container; Core runs as the managed systemd service.
func (d *Docker) worker(ctx context.Context, product map[string]any, component, config, rpc string) (Response, error) {
	if component != "agent" {
		return Response{}, errors.New("only the Docker Agent starts its update worker in a container")
	}
	// Remove only completed workers bearing our exact ownership label.
	var old []struct {
		ID    string `json:"Id"`
		State string `json:"State"`
	}
	filters, _ := json.Marshal(map[string][]string{"label": {"io.nodedance.update.worker=true"}, "status": {"exited", "dead"}})
	if err := d.call(ctx, "GET", d.version+"/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil, &old); err != nil {
		return Response{}, err
	}
	for _, c := range old {
		if c.State == "exited" || c.State == "dead" {
			_ = d.remove(ctx, c.ID)
		}
	}
	image, _ := product["Image"].(string)
	if !strings.HasPrefix(image, "sha256:") {
		return Response{}, errors.New("invalid running product image")
	}
	executable := "/usr/local/bin/nodedance-agent"
	spec := map[string]any{"Image": image, "User": "0:0", "Entrypoint": []string{executable}, "Cmd": []string{"__update-host", config, rpc}, "Labels": map[string]string{"io.nodedance.update.worker": "true"}, "HostConfig": map[string]any{"Privileged": true, "PidMode": "host", "NetworkMode": "host", "Binds": []string{"/:/host:ro"}, "RestartPolicy": map[string]string{"Name": "no"}}}
	var created struct {
		ID string `json:"Id"`
	}
	if err := d.call(ctx, "POST", d.version+"/containers/create", spec, &created); err != nil {
		return Response{}, err
	}
	if err := d.start(ctx, created.ID); err != nil {
		_ = d.remove(context.Background(), created.ID)
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", "http://docker"+d.version+"/containers/"+created.ID+"/logs?stdout=1&stderr=1&follow=1", nil)
	if err != nil {
		return Response{}, err
	}
	response, err := d.HTTP.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return Response{}, fmt.Errorf("worker logs HTTP %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	var output strings.Builder
	for output.Len() < 1<<20 {
		var header [8]byte
		if _, err = io.ReadFull(reader, header[:]); err != nil {
			return Response{}, fmt.Errorf("built-in update worker: %s: %w", output.String(), err)
		}
		length := binary.BigEndian.Uint32(header[4:])
		if length > 1<<20 {
			return Response{}, errors.New("worker response exceeds limit")
		}
		frame := make([]byte, length)
		if _, err = io.ReadFull(reader, frame); err != nil {
			return Response{}, err
		}
		output.Write(frame)
		var result Response
		if json.Unmarshal([]byte(strings.TrimSpace(output.String())), &result) == nil {
			// Cleanup is independent of request cancellation; a later invocation also
			// collects finished workers if the calling product restarted meanwhile.
			go func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
				defer cancel()
				_ = d.call(cleanup, "POST", d.version+"/containers/"+created.ID+"/wait?condition=not-running", nil, nil)
				_ = d.remove(cleanup, created.ID)
			}()
			return result, nil
		}
	}
	return Response{}, errors.New("worker response exceeds limit")
}

func updateState(dataDir string) string {
	return filepath.Join(filepath.Dir(dataDir), "."+filepath.Base(dataDir)+"-updates")
}

// RecordRuntime is written by Core startup, not supplied by update callers.
func RecordRuntime(dataDir string, target Target) error {
	raw, err := json.Marshal(target)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(dataDir, ".nodedance-runtime.json"), raw, 0600)
}
func runtimeHealth(target *Target, metadataDir string) error {
	raw, err := os.ReadFile(filepath.Join(metadataDir, ".nodedance-runtime.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var running Target
	if Decode(raw, &running) != nil {
		return errors.New("invalid Core runtime metadata")
	}
	target.HealthURL = running.HealthURL
	target.HealthCAFile = running.HealthCAFile
	target.HealthServerName = running.HealthServerName
	return nil
}

func runningAgentConfig(pid int, dataDir string) string {
	absolute := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if err != nil {
			return path
		}
		return filepath.Join(cwd, path)
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		args := strings.Split(string(raw), "\x00")
		for i, arg := range args {
			if arg == "--config" && i+1 < len(args) {
				return absolute(args[i+1])
			}
			if strings.HasPrefix(arg, "--config=") {
				return absolute(strings.TrimPrefix(arg, "--config="))
			}
		}
	}
	return filepath.Join(dataDir, "agent.json")
}
