package tailscale

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/agent"
)

type fileRootPreflightRemote struct {
	fileRoot string
	commands []string
}

func (r *fileRootPreflightRemote) Run(_ context.Context, _ string, input []byte) ([]byte, error) {
	command := string(input)
	r.commands = append(r.commands, command)
	switch {
	case strings.Contains(command, `"$(uname -s)"`):
		return []byte("Linux\nx86_64\n0\nnew\n-\nroot\ndocker|absent\n"), nil
	case strings.Contains(command, "probe() {"):
		return []byte("primary\n"), nil
	case strings.Contains(command, "validate-file-root"):
		_, err := agent.ValidateFileRoot(r.fileRoot, "/var/lib/nodedance-agent")
		return nil, err
	default:
		return nil, fmt.Errorf("unexpected SSH command in failed-preflight test")
	}
}

func (*fileRootPreflightRemote) Close() error { return nil }

type fileRootPreflightTransport struct{ remote Remote }

func (t fileRootPreflightTransport) Probe(context.Context, Peer) (string, error) {
	return "SHA256:fake-host-key-fingerprint", nil
}

func (t fileRootPreflightTransport) Connect(context.Context, Peer, SSHCredentials, string, *StateStore, bool) (Remote, error) {
	return t.remote, nil
}

type fileRootArtifactLoader struct{ artifact Artifact }

func (l fileRootArtifactLoader) Load(string, string) (Artifact, error) { return l.artifact, nil }

func TestDeployRejectsMissingOrSymlinkFileRootBeforeEnrollment(t *testing.T) {
	base := t.TempDir()
	validTarget := base + "/real-directory"
	if err := os.Mkdir(validTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := base + "/linked-directory"
	if err := os.Symlink(validTarget, symlink); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		fileRoot string
	}{
		{name: "missing directory", fileRoot: base + "/does-not-exist"},
		{name: "symlink directory", fileRoot: symlink},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifactBytes := []byte("verified agent fixture")
			artifact := Artifact{Bytes: artifactBytes, SHA256: HashSignedPayload(artifactBytes), OS: "linux", Arch: "amd64"}
			remote := &fileRootPreflightRemote{fileRoot: test.fileRoot}
			runner := &staticRunner{result: runnerResult{output: []byte(`{"BackendState":"Running","Peer":{"nodekey:target":{"PublicKey":"nodekey:target","HostName":"target","OS":"linux","TailscaleIPs":["100.64.0.9"],"Online":true}}}`)}}
			createCalls := 0
			service := Service{
				State: NewStateStore(t.TempDir()),
				Dependencies: Dependencies{
					Discovery: Discovery{Runner: runner},
					Artifacts: fileRootArtifactLoader{artifact: artifact},
					Transport: fileRootPreflightTransport{remote: remote},
					Create: func(context.Context, string) (Enrollment, error) {
						createCalls++
						return Enrollment{NodeID: "node-id", Token: "one-time-token"}, nil
					},
				},
			}
			_, err := service.Deploy(context.Background(), DeployRequest{
				PeerIdentity:    "nodekey:target",
				DisplayName:     "Target",
				FileRoot:        test.fileRoot,
				CoreURL:         "https://core.example",
				HostFingerprint: "SHA256:fake-host-key-fingerprint",
				ConfirmHostKey:  true,
			})
			if err == nil || !strings.Contains(err.Error(), "target file-root preflight failed before Agent enrollment") {
				t.Fatalf("Deploy error=%v, want target validation failure", err)
			}
			if createCalls != 0 {
				t.Fatalf("Core enrollment was attempted %d times before target path validation", createCalls)
			}
			if len(remote.commands) != 3 || !strings.Contains(remote.commands[2], "validate-file-root") {
				t.Fatalf("expected target validator after platform/reachability preflight, got %d SSH commands", len(remote.commands))
			}
			preflightCommand := remote.commands[2]
			encodedRoot := base64.StdEncoding.EncodeToString([]byte(test.fileRoot))
			if strings.Contains(preflightCommand, test.fileRoot) || !strings.Contains(preflightCommand, encodedRoot) {
				t.Fatalf("target path was not passed as encoded data: %s", preflightCommand)
			}
			if strings.Contains(preflightCommand, "enroll") || !strings.Contains(preflightCommand, "--state-dir /var/lib/nodedance-agent") || !strings.Contains(preflightCommand, artifact.SHA256) {
				t.Fatalf("file-root preflight command includes enrollment or misses actual state dir: %s", preflightCommand)
			}
			for _, command := range remote.commands {
				if strings.Contains(command, "install-systemd") || strings.Contains(command, "--token-stdin") {
					t.Fatalf("invalid root reached installation/enrollment command: %s", command)
				}
			}
		})
	}
}
