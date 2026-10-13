package compose

import (
	"os"
	"testing"
)

func TestTaskOverridePropagatesWriteFailure(t *testing.T) {
	root, err := os.OpenRoot("/dev")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// /dev/full deterministically rejects writes with ENOSPC without changing a file.
	override := taskComposeOverride{root: root, name: "full"}
	if err := override.write(map[string]map[string]string{"app": {"task": "fixture"}}); err == nil {
		t.Fatal("failed override write reported success")
	}
}
