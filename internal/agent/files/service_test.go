package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func TestRootServiceProvidesHostFilesAndProtectsSpecialPaths(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "agent-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "agent.json"), []byte("private identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(string(filepath.Separator), protocol.DefaultFileLimit, stateDir)
	if err != nil {
		t.Fatalf("open full-host file service: %v", err)
	}
	defer service.Close()

	for _, blocked := range []string{"/proc", "/sys", "/dev", "/run", stateDir} {
		if _, err := service.List(blocked); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("protected path %q was not rejected: %v", blocked, err)
		}
	}
	if _, err := service.Stat("../etc/passwd"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("path traversal was not rejected: %v", err)
	}

	rootEntries, err := service.List("/")
	if err != nil {
		t.Fatalf("list host root: %v", err)
	}
	for _, entry := range rootEntries {
		if entry.Path == "/proc" || entry.Path == "/sys" || entry.Path == "/dev" || entry.Path == "/run" || entry.Path == filepath.ToSlash(stateDir) {
			t.Errorf("protected path was exposed in root listing: %s", entry.Path)
		}
	}

	workingDir := filepath.Join(t.TempDir(), "managed")
	if err := os.Mkdir(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	virtualDir := filepath.ToSlash(workingDir)
	if _, err := service.List(virtualDir); err != nil {
		t.Fatalf("browse host directory: %v", err)
	}
	content := []byte("root managed file")
	digest := sha256.Sum256(content)
	virtualFile := filepath.ToSlash(filepath.Join(workingDir, "config.txt"))
	upload, err := service.BeginUpload(virtualFile, "", int64(len(content)), hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatalf("begin host file upload: %v", err)
	}
	if err := upload.WriteChunk(content); err != nil {
		upload.Abort()
		t.Fatalf("write host file: %v", err)
	}
	if _, err := upload.Commit(); err != nil {
		t.Fatalf("commit host file: %v", err)
	}
	got, _, err := service.ReadText(virtualFile)
	if err != nil || got != string(content) {
		t.Fatalf("read host file: content=%q err=%v", got, err)
	}
	if err := service.Rename(virtualFile, filepath.ToSlash(filepath.Join(workingDir, "renamed.txt"))); err != nil {
		t.Fatalf("rename host file: %v", err)
	}
	if err := service.Delete(filepath.ToSlash(filepath.Join(workingDir, "renamed.txt")), true); err != nil {
		t.Fatalf("delete host file: %v", err)
	}
}

func TestRootServiceRejectsSymlinkAliasesIntoProtectedPaths(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "agent-state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linksDir := t.TempDir()
	stateAlias := filepath.Join(linksDir, "identity")
	if err := os.Symlink(stateDir, stateAlias); err != nil {
		t.Fatal(err)
	}
	procAlias := filepath.Join(linksDir, "proc")
	if err := os.Symlink("/proc", procAlias); err != nil {
		t.Fatal(err)
	}
	service, err := New(string(filepath.Separator), protocol.DefaultFileLimit, stateDir)
	if err != nil {
		t.Fatalf("open full-host file service: %v", err)
	}
	defer service.Close()
	for _, alias := range []string{stateAlias, procAlias} {
		if _, err := service.List(filepath.ToSlash(alias)); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("protected symlink alias %q was not rejected: %v", alias, err)
		}
	}
}

func TestRootServiceFailsClosedDuringProtectedSymlinkReplacement(t *testing.T) {
	base := t.TempDir()
	stateDir := filepath.Join(base, "state")
	allowedDir := filepath.Join(base, "allowed")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(allowedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(stateDir, "marker")
	if err := os.WriteFile(marker, []byte("private-state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(allowedDir, "marker"), []byte("allowed"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "moving")
	if err := os.Symlink(allowedDir, alias); err != nil {
		t.Fatal(err)
	}
	service, err := New(string(filepath.Separator), protocol.DefaultFileLimit, stateDir)
	if err != nil {
		t.Fatalf("open full-host file service: %v", err)
	}
	defer service.Close()
	virtualAlias := filepath.ToSlash(alias)
	virtualMarker := filepath.ToSlash(filepath.Join(alias, "marker"))
	virtualCreate := filepath.ToSlash(filepath.Join(alias, "created"))
	renameTarget := filepath.ToSlash(filepath.Join(allowedDir, "renamed-marker"))

	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 400; i++ {
			target := stateDir
			if i%2 == 0 {
				target = allowedDir
			}
			temporary := filepath.Join(base, "moving-next")
			_ = os.Remove(temporary)
			if os.Symlink(target, temporary) == nil {
				_ = os.Rename(temporary, alias)
			}
			runtime.Gosched()
		}
		close(stop)
	}()

	for i := 0; i < 400; i++ {
		if text, _, readErr := service.ReadText(virtualMarker); readErr == nil && text == "private-state" {
			t.Fatal("read followed a concurrently replaced symlink into Agent state")
		}
		if _, writeErr := service.SaveText(virtualCreate, "", "should-not-escape"); writeErr == nil {
			t.Fatal("write followed a concurrently replaced symlink into Agent state")
		}
		if renameErr := service.Rename(virtualMarker, renameTarget); renameErr == nil {
			t.Fatal("rename followed a concurrently replaced symlink into Agent state")
		}
		if deleteErr := service.Delete(virtualMarker, true); deleteErr == nil {
			t.Fatal("delete followed a concurrently replaced symlink into Agent state")
		}
		runtime.Gosched()
	}
	<-stop
	workers.Wait()
	if data, err := os.ReadFile(marker); err != nil || string(data) != "private-state" {
		t.Fatalf("protected Agent state changed during symlink race: content=%q err=%v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(stateDir, "created")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write created a file in protected Agent state: %v", err)
	}
	if _, err := os.Lstat(renameTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rename moved a protected Agent file: %v", err)
	}
	if _, err := os.Lstat(virtualAlias); err != nil {
		t.Fatalf("the concurrent symlink fixture disappeared: %v", err)
	}
}

func TestConfiguredRootRemainsConfinedByOsRoot(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "allowed")
	if err := os.Mkdir(fileRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(fileRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	service, err := New(fileRoot, protocol.DefaultFileLimit)
	if err != nil {
		t.Fatalf("open configured file root: %v", err)
	}
	defer service.Close()
	if _, err := service.List("/"); err != nil {
		t.Fatalf("list configured root: %v", err)
	}
	if _, _, err := service.ReadText("/escape/secret.txt"); err == nil {
		t.Fatal("configured file root followed a symlink outside its os.Root boundary")
	}
	if _, err := service.Stat("/../../etc/passwd"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("configured file root accepted traversal: %v", err)
	}
}
