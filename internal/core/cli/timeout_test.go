package cli

import (
	"testing"

	"github.com/CST-Cat/NodeDance/internal/control"
)

func TestDefaultTimeoutAppliesOnlyToFiniteRequests(t *testing.T) {
	operation := func(transport string) control.Operation {
		return control.Operation{Name: "op", Transport: transport}
	}
	for _, transport := range []string{"json", "local"} {
		if !defaultTimeoutApplies(operation(transport), false, false) {
			t.Fatalf("finite %s request lost its default timeout", transport)
		}
	}
	for _, transport := range []string{"websocket", "terminal", "download", "upload", "multipart"} {
		if defaultTimeoutApplies(operation(transport), false, false) {
			t.Fatalf("%s transport must not inherit the default timeout", transport)
		}
	}
	for _, transport := range []string{"json", "local", "websocket", "terminal"} {
		if defaultTimeoutApplies(operation(transport), true, false) {
			t.Fatalf("interactive %s must not inherit the default timeout", transport)
		}
		if defaultTimeoutApplies(operation(transport), false, true) {
			t.Fatalf("observed %s must not inherit the default timeout", transport)
		}
	}
}
