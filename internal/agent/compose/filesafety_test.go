package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
)

func TestOpenSafeWorkingRootRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	safe, err := openSafeWorkingRoot(root)
	if err != nil {
		t.Fatalf("a real absolute directory was rejected: %v", err)
	}
	defer safe.Close()

	for name, path := range map[string]string{
		"relative":           "compose/root",
		"filesystem root":    string(filepath.Separator),
		"uncleaned":          root + string(filepath.Separator) + ".",
		"missing":            filepath.Join(root, "absent"),
		"trailing separator": root + string(filepath.Separator),
	} {
		if _, err := openSafeWorkingRoot(path); err == nil {
			t.Fatalf("unsafe working root %q (%s) was accepted", path, name)
		}
	}
	// A symlinked directory must be refused even though it resolves inside.
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := openSafeWorkingRoot(link); err == nil {
		t.Fatal("a symlinked working root was accepted")
	}
}

func TestWriteNewComposeCandidateCreatesPrivateExhaustiveNames(t *testing.T) {
	safe, err := openSafeWorkingRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer safe.Close()
	name, full, err := writeNewComposeCandidate(safe, "/srv/compose", []byte("services: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, ".nodedance-compose-") || !strings.HasSuffix(name, ".tmp") || !filepath.IsAbs(full) {
		t.Fatalf("unexpected candidate naming: %q %q", name, full)
	}
	info, err := os.Stat(filepath.Join(safe.Name(), name))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("compose candidate must be private: %v %v", info, err)
	}
}

func TestWriteComposeCandidateAtRefusesOverwrite(t *testing.T) {
	directory := t.TempDir()
	original := filepath.Join(directory, "compose.yml")
	if err := os.WriteFile(original, []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	safe, err := openSafeWorkingRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer safe.Close()
	candidate, err := writeComposeCandidateAt(safe, filepath.Base(original), []byte("services: {web: {}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if candidate == original {
		t.Fatal("candidate must never replace the original path")
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatal("original compose file was disturbed")
	}
	kept, err := os.ReadFile(original)
	if err != nil || string(kept) != "services: {}\n" {
		t.Fatalf("original content changed: %q %v", kept, err)
	}
}

func TestServiceInstanceCounts(t *testing.T) {
	counts := serviceInstanceCounts([]agentdocker.Container{
		{Compose: &agentdocker.ComposeIdentity{Service: "web"}},
		{Compose: &agentdocker.ComposeIdentity{Service: "web"}},
		{Compose: &agentdocker.ComposeIdentity{Service: "db"}},
		{Compose: nil},
	})
	if counts["web"] != 2 || counts["db"] != 1 || len(counts) != 2 {
		t.Fatalf("instance counts: %v", counts)
	}
	if len(serviceInstanceCounts(nil)) != 0 {
		t.Fatal("empty inventory must produce empty counts")
	}
}
