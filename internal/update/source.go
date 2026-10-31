package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const sourceStamp = ".nodedance-source"

func validSourceDir(ctx context.Context, dir string) bool {
	if dir == "" || !filepath.IsAbs(dir) {
		return false
	}
	dir = filepath.Clean(dir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	module, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	moduleLine := strings.TrimSpace(strings.SplitN(string(module), "\n", 2)[0])
	if moduleLine != "module github.com/"+Repository {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remote, err := gitOutput(checkCtx, dir, "remote", "get-url", "origin")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(remote), "cst-cat/nodedance")
}

func stampedSourceDir(ctx context.Context, path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(raw))
	if validSourceDir(ctx, dir) {
		return filepath.Clean(dir)
	}
	return ""
}

func findSourceDir(ctx context.Context, binary, dataDir string, mainPID int) string {
	if binary != "" {
		if dir := stampedSourceDir(ctx, filepath.Join(filepath.Dir(binary), sourceStamp)); dir != "" {
			return dir
		}
	}
	if dataDir != "" {
		if dir := stampedSourceDir(ctx, filepath.Join(dataDir, sourceStamp)); dir != "" {
			return dir
		}
	}
	var candidates []string
	add := func(dir string) {
		if dir != "" {
			candidates = append(candidates, dir)
		}
	}
	addAncestors := func(dir string) {
		for dir != "" && dir != "/" {
			add(dir)
			dir = filepath.Dir(dir)
		}
	}
	if mainPID > 1 {
		if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", mainPID)); err == nil {
			addAncestors(cwd)
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		addAncestors(cwd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, "NodeDance"))
	}
	add("/opt/NodeDance")
	add("/srv/NodeDance")
	add("/usr/local/src/NodeDance")
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if validSourceDir(ctx, candidate) {
			if dataDir != "" {
				_ = atomicFile(filepath.Join(dataDir, sourceStamp), []byte(candidate+"\n"), 0600)
			}
			return candidate
		}
	}
	return ""
}

func sourceEnv() []string {
	paths := []string{}
	if current := os.Getenv("PATH"); current != "" {
		paths = append(paths, filepath.SplitList(current)...)
	}
	home, _ := os.UserHomeDir()
	candidates := []string{"/usr/local/go/bin", "/usr/local/bin", "/opt/go/bin", "/opt/node/bin", "/usr/lib/go/bin"}
	if home != "" {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "pnpm"), filepath.Join(home, ".volta", "bin"), filepath.Join(home, "go", "bin"))
		if nodeVersions, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); err == nil {
			candidates = append(candidates, nodeVersions...)
		}
	}
	seen := map[string]bool{}
	merged := []string{}
	for _, dir := range append(paths, candidates...) {
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			merged = append(merged, dir)
		}
	}
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "HOME=") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+strings.Join(merged, string(filepath.ListSeparator)))
	if home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

func runSourceCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = sourceEnv()
	raw, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(raw))
	if err != nil {
		if len(output) > 2048 {
			output = output[len(output)-2048:]
		}
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, output)
	}
	return output, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-c", "safe.directory=" + dir, "-C", dir}, args...)
	return runSourceCommand(ctx, dir, "git", full...)
}

func gitHead(ctx context.Context, dir string) (string, error) {
	return gitOutput(ctx, dir, "rev-parse", "HEAD")
}

func gitRemoteMain(ctx context.Context, dir string) (string, error) {
	if _, err := gitOutput(ctx, dir, "fetch", "origin", "main"); err != nil {
		return "", err
	}
	return gitOutput(ctx, dir, "rev-parse", "FETCH_HEAD")
}

func gitIsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	_, err := gitOutput(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
