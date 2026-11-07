package files

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

// FuzzValidateArchiveItemMatchesGate asserts archive entry validation is never
// weaker than the normalize traversal gate: any entry it accepts must also pass
// normalize against the extraction target, and rejections may only be the two
// documented archive errors.
func FuzzValidateArchiveItemMatchesGate(f *testing.F) {
	for _, seed := range []struct {
		name string
		mode uint32
		size int64
	}{
		{"top.txt", 0644, 10}, {"nested/dir/file.bin", 0600, 4096}, {"dir", 0755 | uint32(os.ModeDir), 0},
		{"../escape", 0644, 1}, {"/absolute", 0644, 1}, {"back\\slash", 0644, 1},
		{"nul\x00byte", 0644, 1}, {"./dot", 0644, 1}, {"a//b", 0644, 1}, {"trailing/", 0644, 1},
		{"link", 0644 | uint32(os.ModeSymlink), 1}, {"", 0644, 1}, {"negative", 0644, -5},
		{"huge", 0644, 1 << 40}, {"deep/" + strings.Repeat("x/", 40) + "end", 0644, 1},
	} {
		f.Add(seed.name, seed.mode, seed.size)
	}
	root := f.TempDir()
	service, err := New(root, protocol.DefaultFileLimit, filepath.Join(root, "secret"))
	if err != nil {
		f.Skip("openat2 file service is unavailable:", err)
	}
	defer service.Close()
	target := "/stage"
	f.Fuzz(func(t *testing.T, name string, rawMode uint32, size int64) {
		item := archiveItem{name: name, size: size, mode: os.FileMode(rawMode)}
		seen := map[string]archiveItem{}
		var total int64
		err := service.validateArchiveItem(item, target, seen, &total)
		if err != nil {
			if !errors.Is(err, ErrInvalidPath) && !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("unexpected rejection error for %q mode %#x: %v", name, rawMode, err)
			}
			return
		}
		if name == "" || path.Clean(name) != name || path.IsAbs(name) {
			t.Fatalf("unclean or absolute name %q was accepted", name)
		}
		for _, part := range strings.Split(name, "/") {
			if part == "" || part == "." || part == ".." {
				t.Fatalf("unsafe component in accepted name %q", name)
			}
		}
		if !item.mode.IsRegular() && !item.directory() {
			t.Fatalf("special-file mode %#x was accepted for %q", rawMode, name)
		}
		if size < 0 {
			t.Fatalf("negative size %d was accepted for %q", size, name)
		}
		if _, _, err := service.normalize(path.Join(target, name)); err != nil {
			t.Fatalf("archive validation accepted %q but the traversal gate rejects it: %v", name, err)
		}
	})
}
