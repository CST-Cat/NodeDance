package files

import (
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// FuzzNormalizeContainsPaths asserts the traversal gate: any input normalize
// accepts must resolve to a cleaned path inside the file root and outside the
// protected trees, and any rejected input must return no partial result.
func FuzzNormalizeContainsPaths(f *testing.F) {
	for _, seed := range []string{
		"/", "/data", "/data/file.txt", "/data/../escape", "/../escape", "/./data",
		"/data/../../etc/passwd", "/proc/self/mem", "/sys", "/dev", "/run", "/secret",
		"/secret/inner/credential", "relative/path", "", "/double//slash", "/trailing/",
		"/unicode/中文", "/space name", "/data\x00file", "/data/..", "/..", "/.", "/a/./b/../c",
		"/" + strings.Repeat("a/", 2500) + "deep",
	} {
		f.Add(seed)
	}
	root := f.TempDir()
	service, err := New(root, protocol.DefaultFileLimit, filepath.Join(root, "secret"))
	if err != nil {
		f.Skip("openat2 file service is unavailable:", err)
	}
	defer service.Close()
	f.Fuzz(func(t *testing.T, virtual string) {
		name, canonical, err := service.normalize(virtual)
		if err != nil {
			if name != "" || canonical != "" {
				t.Fatalf("rejected path %q returned partial result %q %q", virtual, name, canonical)
			}
			return
		}
		if virtual == "" || !strings.HasPrefix(virtual, "/") || strings.ContainsRune(virtual, '\x00') || len(virtual) > 4096 {
			t.Fatalf("invalid input %q was accepted", virtual)
		}
		for _, part := range strings.Split(virtual, "/") {
			if part == ".." {
				t.Fatalf("path with .. component %q was accepted", virtual)
			}
		}
		if canonical != path.Clean(virtual) {
			t.Fatalf("canonical %q is not the cleaned path of %q", canonical, virtual)
		}
		wantName := strings.TrimPrefix(canonical, "/")
		if wantName == "" {
			wantName = "."
		}
		if name != wantName {
			t.Fatalf("name %q does not match canonical %q", name, canonical)
		}
		if service.isBlocked(canonical) {
			t.Fatalf("blocked path %q was accepted", canonical)
		}
	})
}
