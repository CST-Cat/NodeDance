package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

func TestBetaChannelValidation(t *testing.T) {
	if err := Validate(Request{Component: "core", Version: ChannelBeta}); err != nil {
		t.Fatalf("core beta rejected: %v", err)
	}
	if err := Validate(Request{Component: "agent", Version: ChannelBeta}); err == nil {
		t.Fatal("agent beta accepted")
	}
	if err := Validate(Request{Component: "core", Version: "beta-v1"}); err == nil {
		t.Fatal("non-channel beta string accepted as a release tag")
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
	check, err := m.Check(context.Background(), Request{Component: "core", Version: ChannelBeta})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Configured || check.Available || check.Latest != ChannelBeta || check.Current != "dev" {
		t.Fatalf("up-to-date check = %+v", check)
	}
	if err := os.WriteFile(filepath.Join(upstream, "next.txt"), []byte("next\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, upstream, "add", ".")
	git(t, upstream, "commit", "-m", "next")
	git(t, upstream, "push", bare, "main")
	check, err = m.Check(context.Background(), Request{Component: "core", Version: ChannelBeta})
	if err != nil {
		t.Fatal(err)
	}
	if !check.Configured || !check.Available {
		t.Fatalf("behind check = %+v", check)
	}
	m.Config.Targets["core"] = Target{Mode: "systemd", Unit: "nodedance.service", Binary: binary}
	check, err = m.Check(context.Background(), Request{Component: "core", Version: ChannelBeta})
	if err != nil {
		t.Fatal(err)
	}
	if check.Configured || check.Reason == "" {
		t.Fatalf("missing-checkout check = %+v", check)
	}
}
