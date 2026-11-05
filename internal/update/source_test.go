package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, raw)
	}
	return string(raw)
}

func sourceRepo(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "checkout", "-b", "main")
	git(t, dir, "config", "user.email", "test@example.com")
	git(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/CST-Cat/NodeDance\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\ttrue\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "initial")
	if origin != "" {
		git(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func TestDevChannelValidation(t *testing.T) {
	if err := Validate(Request{Component: "core", Version: ChannelDev}); err != nil {
		t.Fatalf("core dev rejected: %v", err)
	}
	if err := Validate(Request{Component: "agent", Version: ChannelDev}); err != nil {
		t.Fatalf("agent dev rejected: %v", err)
	}
	if err := Validate(Request{Component: "core", Version: "dev-v1"}); err == nil {
		t.Fatal("non-channel dev string accepted as a release tag")
	}
}

func TestSourceEnvDerivesGoPathsWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("GOPATH", "")
	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOCACHE", "")
	t.Setenv("XDG_CACHE_HOME", "")
	values := map[string]string{}
	for _, entry := range sourceEnv(t.TempDir()) {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	for _, key := range []string{"HOME", "GOPATH", "GOMODCACHE", "GOCACHE"} {
		if values[key] == "" || !filepath.IsAbs(values[key]) {
			t.Fatalf("%s = %q, want a non-empty absolute path in %v", key, values[key], values)
		}
	}
	if values["GOMODCACHE"] != filepath.Join(values["GOPATH"], "pkg", "mod") {
		t.Fatalf("GOMODCACHE = %q, want it under GOPATH %q", values["GOMODCACHE"], values["GOPATH"])
	}
}

func TestSourceEnvPreservesExplicitGoPaths(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "gopath")
	t.Setenv("GOPATH", custom)
	t.Setenv("GOMODCACHE", "")
	values := map[string]string{}
	for _, entry := range sourceEnv(t.TempDir()) {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	if values["GOPATH"] != custom {
		t.Fatalf("GOPATH = %q, want preserved %q", values["GOPATH"], custom)
	}
	if values["GOMODCACHE"] != filepath.Join(custom, "pkg", "mod") {
		t.Fatalf("GOMODCACHE = %q, want it derived from the preserved GOPATH", values["GOMODCACHE"])
	}
}

func TestValidSourceDir(t *testing.T) {
	ctx := context.Background()
	repo := sourceRepo(t, "https://github.com/CST-Cat/NodeDance.git")
	if !validSourceDir(ctx, repo) {
		t.Fatal("complete checkout rejected")
	}
	other := sourceRepo(t, "https://example.com/other/project.git")
	if validSourceDir(ctx, other) {
		t.Fatal("foreign checkout accepted")
	}
	plain := t.TempDir()
	if validSourceDir(ctx, plain) {
		t.Fatal("plain directory accepted")
	}
}

func TestFindSourceDirPrefersInstallStamp(t *testing.T) {
	repo := sourceRepo(t, "https://github.com/CST-Cat/NodeDance.git")
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "nodedance")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, sourceStamp), []byte(repo+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if found := findSourceDir(context.Background(), binary, "", 0); found != repo {
		t.Fatalf("stamp lookup = %q, want %q", found, repo)
	}
}

func TestCheckSourceChannel(t *testing.T) {
	upstream := sourceRepo(t, "")
	bare := filepath.Join(t.TempDir(), "remote.git")
	git(t, filepath.Dir(bare), "clone", "--bare", upstream, bare)
	checkout := filepath.Join(t.TempDir(), "checkout")
	git(t, filepath.Dir(checkout), "clone", bare, checkout)
	git(t, checkout, "config", "user.email", "test@example.com")
	git(t, checkout, "config", "user.name", "Test")
	binary := filepath.Join(t.TempDir(), "bin", "nodedance")
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		Config: Config{Targets: map[string]Target{"core": {Mode: "systemd", Unit: "nodedance.service", Binary: binary, SourceDir: checkout}}},
		command: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("dev\n"), nil
		},
		jobs: map[string]Job{},
	}
	check, err := m.Check(context.Background(), Request{Component: "core", Version: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Configured || !check.Available || check.Latest != ChannelDev || check.Current != "dev" {
		t.Fatalf("up-to-date check = %+v", check)
	}
	if err := os.WriteFile(filepath.Join(upstream, "next.txt"), []byte("next\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, upstream, "add", ".")
	git(t, upstream, "commit", "-m", "next")
	git(t, upstream, "push", bare, "main")
	check, err = m.Check(context.Background(), Request{Component: "core", Version: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Configured || !check.Available {
		t.Fatalf("behind check = %+v", check)
	}
	if err := os.WriteFile(filepath.Join(checkout, "local.txt"), []byte("local\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, checkout, "add", ".")
	git(t, checkout, "commit", "-m", "local divergence")
	check, err = m.Check(context.Background(), Request{Component: "core", Version: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Configured || !check.Available {
		t.Fatalf("diverged check = %+v", check)
	}
	remote, err := gitRemoteMain(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitOutput(context.Background(), checkout, "reset", "--hard", remote); err != nil {
		t.Fatal(err)
	}
	head, err := gitHead(context.Background(), checkout)
	if err != nil {
		t.Fatal(err)
	}
	if head != remote {
		t.Fatalf("head after hard reset = %s, want %s", head, remote)
	}
	if status, err := gitOutput(context.Background(), checkout, "status", "--porcelain"); err != nil || status != "" {
		t.Fatalf("status after hard reset = %q, %v", status, err)
	}
	m.Config.Targets["core"] = Target{Mode: "systemd", Unit: "nodedance.service", Binary: binary}
	check, err = m.Check(context.Background(), Request{Component: "core", Version: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if check.Configured || check.Reason == "" {
		t.Fatalf("missing-checkout check = %+v", check)
	}
}

func TestCoreDevBuildInstallAndHealthRollback(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root transaction test")
	}
	for _, failHealth := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "rollback"}[failHealth], func(t *testing.T) {
			upstream := sourceRepo(t, "")
			makefile := "build:\n\tmkdir -p .build\n\tprintf '#!/bin/sh\\necho NodeDance dev\\n' > .build/nodedance\n\tchmod 0755 .build/nodedance\ninstall:\n\tinstall -m 0755 .build/nodedance $(PREFIX)/bin/nodedance\n"
			if err := os.WriteFile(filepath.Join(upstream, "Makefile"), []byte(makefile), 0644); err != nil {
				t.Fatal(err)
			}
			git(t, upstream, "add", "Makefile")
			git(t, upstream, "commit", "-m", "source build fixture")
			bare := filepath.Join(t.TempDir(), "remote.git")
			git(t, filepath.Dir(bare), "clone", "--bare", upstream, bare)
			checkout := filepath.Join(t.TempDir(), "checkout")
			git(t, filepath.Dir(checkout), "clone", bare, checkout)
			root := t.TempDir()
			data := filepath.Join(root, "data")
			if err := os.Mkdir(data, 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(root, "bin", "nodedance")
			if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
				t.Fatal(err)
			}
			previous := "#!/bin/sh\necho NodeDance core-v0.1.0\n"
			if err := os.WriteFile(binary, []byte(previous), 0755); err != nil {
				t.Fatal(err)
			}
			health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := os.ReadFile(binary)
				if err != nil || failHealth && strings.Contains(string(raw), "NodeDance dev") {
					w.WriteHeader(503)
					return
				}
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer health.Close()
			m, err := New(Config{StateDir: filepath.Join(root, "state"), Targets: map[string]Target{"core": {
				Mode: "systemd", Unit: "nodedance.service", Binary: binary, SourceDir: checkout, DataDir: data, HealthURL: health.URL + "/api/v1/health",
			}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.healthTimeout = 150 * time.Millisecond
			m.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "systemctl" {
					return nil, nil
				}
				return execute(ctx, name, args...)
			}
			job, err := m.Apply(context.Background(), Request{Component: "core", Version: ChannelDev, Confirm: true}, "core-dev-source-fixture")
			if err != nil {
				t.Fatal(err)
			}
			m.workers.Wait()
			job, err = m.Get("core", job.ID)
			wantStatus, wantVersion := "succeeded", "dev"
			if failHealth {
				wantStatus, wantVersion = "rolled_back", "core-v0.1.0"
			}
			version, versionErr := m.current(context.Background(), "core")
			if err != nil || versionErr != nil || !job.Source || !job.Switched || job.Target != ChannelDev || job.Status != wantStatus || version != wantVersion {
				t.Fatalf("Core Dev source transaction: %+v version=%q err=%v/%v", job, version, err, versionErr)
			}
		})
	}
}
