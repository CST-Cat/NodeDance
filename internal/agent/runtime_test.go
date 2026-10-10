package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRootRequirement(t *testing.T) {
	for _, operation := range []string{"enrollment", "recovery", "runtime"} {
		if err := agentRootRequirement(0, operation); err != nil {
			t.Errorf("root %s was rejected: %v", operation, err)
		}
		if err := agentRootRequirement(1000, operation); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
			t.Errorf("non-root %s was accepted: %v", operation, err)
		}
	}
}

func TestAgentHostManagementEntrypointsRejectNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root entrypoint checks require a non-root test process")
	}
	configPath := filepath.Join(t.TempDir(), "missing-agent.json")
	cases := []struct {
		name string
		call func() error
	}{
		{
			name: "enroll",
			call: func() error {
				return Enroll(context.Background(), "http://127.0.0.1:8180", "", true, strings.NewReader(""), configPath)
			},
		},
		{
			name: "recover",
			call: func() error { return Recover(context.Background(), configPath) },
		},
		{
			name: "run",
			call: func() error { return Run(context.Background(), configPath, "test", io.Discard) },
		},
		{
			name: "file service",
			call: func() error {
				service, err := openAgentFileService(configPath)
				if service != nil {
					_ = service.Close()
				}
				return err
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); err == nil || !strings.Contains(err.Error(), "requires root privileges") {
				t.Fatalf("non-root %s entrypoint was accepted: %v", testCase.name, err)
			}
		})
	}
}
