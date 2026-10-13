package containerrebuild

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRejectsSidecarSymlinkBeforeSQLiteWrites(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			root := t.TempDir()
			private := filepath.Join(root, "state")
			if err := os.Mkdir(private, 0700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(root, "victim")
			if err := os.WriteFile(victim, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			database := filepath.Join(private, "rebuild.sqlite")
			if err := os.Symlink(victim, database+suffix); err != nil {
				t.Fatal(err)
			}
			store, err := OpenStore(context.Background(), database)
			if store != nil {
				store.Close()
			}
			if err == nil {
				t.Fatal("unsafe sidecar accepted")
			}
			contents, err := os.ReadFile(victim)
			if err != nil || string(contents) != "unchanged" {
				t.Fatal("SQLite touched sidecar target", string(contents), err)
			}
		})
	}
}
