package protocol

import (
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"testing"
)

func TestComposeBaselineCleanupFailureCanBeReported(t *testing.T) {
	for _, state := range []string{"recovery_required", "deployment_verified"} {
		if err := ValidateTaskResult(taskstate.Unknown, TaskResult{Code: "result_pending", ObservedState: state, ResourceRevision: "baseline_cleanup_failed"}); err != nil {
			t.Fatal(state, err)
		}
	}
	if err := ValidateTaskResult(taskstate.Unknown, TaskResult{Code: "result_pending", ObservedState: "recovery_required", ResourceRevision: "arbitrary_failure"}); err == nil {
		t.Fatal("unregistered recovery code accepted")
	}
}
