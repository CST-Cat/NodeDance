//go:build linux

package rsyncbundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInstallProvidesOnlyProtectedBundledRsync(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bundled rsync requires the root Agent")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent")
	binary, cleanup, err := Install(state)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.HasPrefix(binary, state+string(filepath.Separator)) {
		t.Fatal("executable is outside private Agent state")
	}
	info, err := os.Lstat(binary)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 {
		t.Fatal("bundled executable is not protected")
	}
	copying, err := os.Stat(filepath.Join(filepath.Dir(binary), "licenses", "COPYING"))
	if err != nil || copying.Mode().Perm() != 0600 || copying.Size() == 0 {
		t.Fatal("embedded COPYING is missing or unprotected", err)
	}
	cmd := exec.Command(binary, "--version")
	cmd.Env = []string{"PATH=", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "rsync  version "+Version) {
		t.Fatalf("bundled version: %s: %v", out, err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(binary)); !os.IsNotExist(err) {
		t.Fatal("completed bundle workspace was not removed", err)
	}
}

func TestInstallRejectsUnsafeStateAndAlteredArchive(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("bundled rsync requires the root Agent")
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(state); err == nil {
		t.Fatal("writable Agent state was accepted")
	}
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink(state, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Install(link); err == nil {
		t.Fatal("symlinked Agent state was accepted")
	}
	original := archive
	archive = append([]byte(nil), archive...)
	archive[0] ^= 1
	defer func() { archive = original }()
	if _, _, err := Install(state); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatal("altered embedded archive was accepted", err)
	}
}
