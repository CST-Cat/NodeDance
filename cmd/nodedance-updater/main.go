// nodedance-updater is a narrowly scoped root supervisor, independent of Core/Agent restarts.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/update"
)

var version = "dev"

const configPath = "/etc/nodedance-updater/config.json"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nodedance-updater:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nodedance-updater install|serve|manifest|version")
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Println("NodeDance Updater", version)
		return nil
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "manifest":
		component := flags.String("component", "core", "release component")
		tag := flags.String("version", version, "published component tag")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !update.ValidTag(*component, *tag) {
			return errors.New("invalid component release tag")
		}
		return json.NewEncoder(os.Stdout).Encode(update.Describe(*component, *tag))
	case "serve":
		path := flags.String("config", configPath, "root-owned deployment registry")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if os.Geteuid() != 0 {
			return errors.New("update supervisor requires root")
		}
		info, err := os.Lstat(*path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0 {
			return errors.New("registry must be a root-owned 0600 regular file")
		}
		raw, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		var config update.Config
		if err = update.Decode(raw, &config); err != nil {
			return err
		}
		manager, err := update.New(config, update.NewGitHub())
		if err != nil {
			return err
		}
		defer manager.Close()
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		return update.Serve(ctx, manager)
	case "install":
		return install(flags, args[1:])
	default:
		return errors.New("unknown update supervisor command")
	}
}
func install(flags *flag.FlagSet, args []string) error {
	component := flags.String("component", "core", "managed core or agent")
	mode := flags.String("mode", "docker", "docker or systemd")
	container := flags.String("container", "", "existing managed container")
	binary := flags.String("binary", "", "existing native binary")
	unit := flags.String("unit", "", "managed systemd unit")
	data := flags.String("data-dir", "", "existing protected data directory")
	health := flags.String("health-url", "", "local Core HTTP health endpoint")
	healthCA := flags.String("health-ca-file", "", "additional Core health TLS CA file")
	compose := flags.String("compose-file", "", "private deployment Compose file")
	uid := flags.Uint("core-uid", 10001, "authorized Core process UID; use 0 on Agent-only hosts")
	gid := flags.Int("core-gid", 10001, "Core socket group; use 0 on Agent-only hosts")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected installation arguments")
	}
	if os.Geteuid() != 0 {
		return errors.New("install requires root")
	}
	if *component != "core" && *component != "agent" {
		return errors.New("component must be core or agent")
	}
	target := update.Target{Mode: *mode, Container: *container, Binary: *binary, Unit: *unit, DataDir: *data, HealthURL: *health, HealthCAFile: *healthCA, ComposeFile: *compose}
	config := update.Config{StateDir: "/var/lib/nodedance-updater", Socket: update.Socket, CoreUID: uint32(*uid), CoreGID: *gid, Targets: map[string]update.Target{}}
	if info, err := os.Lstat(configPath); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 0) {
		return errors.New("existing registry must be a root-owned private regular file")
	}
	if _, err := os.Stat(filepath.Dir(configPath)); err == nil {
		if err := registryDirectory(filepath.Dir(configPath)); err != nil {
			return err
		}
	}
	if raw, err := os.ReadFile(configPath); err == nil {
		if err = update.Decode(raw, &config); err != nil {
			return err
		}
	}
	config.SupervisorBinary = "/usr/local/libexec/nodedance-updater"
	if *uid > 4294967295 || *gid < 0 {
		return errors.New("invalid Core UID/GID")
	}
	if _, hasCore := config.Targets["core"]; *component == "core" || !hasCore {
		config.CoreUID = uint32(*uid)
		config.CoreGID = *gid
	}
	config.Targets[*component] = target
	if raw, err := os.ReadFile("/etc/systemd/system/nodedance-updater.service"); err == nil && !strings.Contains(string(raw), "Description=NodeDance update supervisor") {
		return errors.New("refusing to overwrite an unrelated updater service")
	}
	if entries, err := os.ReadDir(config.StateDir); err == nil {
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(config.StateDir, entry.Name()))
			if err != nil {
				return err
			}
			var job update.Job
			if json.Unmarshal(raw, &job) == nil && (job.Status == "queued" || job.Status == "running") {
				return errors.New("wait for the active update before reinstalling the supervisor")
			}
		}
	}
	// Validate using a temporary private workspace, without holding the live daemon lock.
	workspace, err := os.MkdirTemp("", "nodedance-updater-validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	validation := config
	validation.SupervisorBinary = ""
	validation.StateDir = workspace
	manager, err := update.New(validation, update.NewGitHub())
	if err != nil {
		return err
	}
	manager.Close()
	if err = writePrivate(configPath, config); err != nil {
		return err
	}
	installed := "/usr/local/libexec/nodedance-updater"
	raw, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(installed), 0755); err != nil {
		return err
	}
	staging, err := os.CreateTemp(filepath.Dir(installed), ".updater-")
	if err != nil {
		return err
	}
	defer os.Remove(staging.Name())
	if err = staging.Chmod(0755); err == nil {
		_, err = staging.Write(raw)
	}
	if err == nil {
		err = staging.Sync()
	}
	staging.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(staging.Name(), installed); err != nil {
		return err
	}
	service := "[Unit]\nDescription=NodeDance update supervisor\nAfter=network-online.target docker.service\nWants=network-online.target\n\n[Service]\nType=simple\nUser=root\nExecStart=" + installed + " serve --config " + configPath + "\nRestart=on-failure\nRestartSec=3\nUMask=0077\nTimeoutStopSec=300\n\n[Install]\nWantedBy=multi-user.target\n"
	if err = os.WriteFile("/etc/systemd/system/nodedance-updater.service", []byte(service), 0644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "nodedance-updater.service"}, {"restart", "nodedance-updater.service"}} {
		if raw, err = exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, raw)
		}
	}
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readyCancel()
	for {
		attemptCtx, attemptCancel := context.WithTimeout(readyCtx, 2*time.Second)
		response, callErr := update.Call(attemptCtx, config.Socket, update.RPC{Action: "status", Request: update.Request{Component: *component}})
		attemptCancel()
		if callErr == nil && response.Status == 200 {
			break
		}
		select {
		case <-readyCtx.Done():
			return errors.New("supervisor did not become ready; inspect journalctl -u nodedance-updater")
		case <-time.After(250 * time.Millisecond):
		}
	}
	fmt.Println("NodeDance update supervisor installed; deployment state and credentials were preserved.")
	return nil
}
func writePrivate(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := registryDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(raw)
	}
	if err == nil {
		err = file.Sync()
	}
	file.Close()
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func registryDirectory(path string) error {
	info, err := os.Lstat(path)
	real, realErr := filepath.EvalSymlinks(path)
	if err != nil || realErr != nil || real != filepath.Clean(path) || !info.IsDir() || info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("registry directory must be root-owned, private and without symlinks")
	}
	return nil
}
