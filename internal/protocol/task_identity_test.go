package protocol

import (
	"strings"
	"testing"
)

func TestTaskIdentityFieldsRejectPathSyntax(t *testing.T) {
	identity := func(taskID string) bool {
		return validTaskIdentityFields(taskID, "node", strings.Repeat("0", 64), strings.Repeat("a", 64), "key")
	}
	if !identity("ndt_" + strings.Repeat("a", 32)) {
		t.Fatal("generated task identity rejected")
	}
	for _, hostile := range []string{
		"",
		"task",
		"../../etc",
		"a/b",
		"..",
		"ndt_short",
		"ndt_" + strings.Repeat("A", 32),
		"ndt_" + strings.Repeat("g", 32),
		"ndt_" + strings.Repeat("a", 31) + "/.",
	} {
		if identity(hostile) {
			t.Fatalf("accepted hostile task id %q", hostile)
		}
	}
}
