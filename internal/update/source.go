package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func getentHome(uid string) string {
	raw, err := exec.Command("getent", "passwd", uid).Output()
	if err != nil {
		return ""
	}
	fields := strings.Split(strings.TrimSpace(string(raw)), ":")
	if len(fields) != 7 {
		return ""
	}
	return fields[5]
}

func passwdHome(uid uint32) string {
	id := strconv.FormatUint(uint64(uid), 10)
	if owner, err := user.LookupId(id); err == nil && owner.HomeDir != "" {
		return owner.HomeDir
	}
	return getentHome(id)
}

func sourceHomes(dir string) []string {
	homes := []string{}
	add := func(home string) {
		if home == "" || !filepath.IsAbs(home) {
			return
		}
		home = filepath.Clean(home)
		if info, err := os.Stat(home); err != nil || !info.IsDir() {
			return
		}
		for _, existing := range homes {
			if existing == home {
				return
			}
		}
		homes = append(homes, home)
	}
	if info, err := os.Stat(dir); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			add(passwdHome(stat.Uid))
		}
	}
	if current, err := user.Current(); err == nil {
		add(current.HomeDir)
	} else {
		add(passwdHome(uint32(os.Getuid())))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(home)
	}
	return homes
}

func sourceEnv(dir string) []string {
	paths := []string{}
	if current := os.Getenv("PATH"); current != "" {
		paths = append(paths, filepath.SplitList(current)...)
	}
	homes := sourceHomes(dir)
	candidates := []string{"/usr/local/go/bin", "/usr/local/bin", "/opt/go/bin", "/opt/node/bin", "/usr/lib/go/bin", "/home/linuxbrew/.linuxbrew/bin", "/snap/bin", "/nix/var/nix/profiles/default/bin"}
	for _, home := range homes {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "pnpm"), filepath.Join(home, ".local", "bin"), filepath.Join(home, ".volta", "bin"), filepath.Join(home, "go", "bin"), filepath.Join(home, ".asdf", "shims"), filepath.Join(home, ".local", "share", "mise", "shims"), filepath.Join(home, ".nix-profile", "bin"))
		if nodeVersions, err := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); err == nil {
			candidates = append(candidates, nodeVersions...)
		}
	}
	seen := map[string]bool{}
	merged := []string{}
	for _, candidate := range append(paths, candidates...) {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			merged = append(merged, candidate)
		}
	}
	home := ""
	if len(homes) > 0 {
		home = homes[0]
	}
	base := home
	if base == "" {
		base = os.TempDir()
	}
	goPath := os.Getenv("GOPATH")
	if goPath == "" {
		goPath = filepath.Join(base, "go")
	}
	modCache := os.Getenv("GOMODCACHE")
	if modCache == "" {
		modCache = filepath.Join(filepath.SplitList(goPath)[0], "pkg", "mod")
	}
	goCache := os.Getenv("GOCACHE")
	if goCache == "" {
		cacheHome := os.Getenv("XDG_CACHE_HOME")
		if cacheHome == "" || !filepath.IsAbs(cacheHome) {
			cacheHome = filepath.Join(base, ".cache")
		}
		goCache = filepath.Join(cacheHome, "go-build")
	}
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "PATH" || key == "HOME" || key == "GOPATH" || key == "GOMODCACHE" || key == "GOCACHE" {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "PATH="+strings.Join(merged, string(filepath.ListSeparator)))
	if home != "" {
		env = append(env, "HOME="+home)
	}
	env = append(env, "GOPATH="+goPath, "GOMODCACHE="+modCache, "GOCACHE="+goCache)
	return env
}

func runSourceCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = sourceEnv(dir)
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
