package tailscale

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSignedArtifactAcceptsValidAndRejectsTamperedOrWrongArchitecture(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "artifacts", "linux-amd64")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := []byte("signed Agent fixture")
	path := filepath.Join(dir, "nodedance-agent")
	if err := os.WriteFile(path, binary, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".sig", ed25519.Sign(private, binary), 0o600); err != nil {
		t.Fatal(err)
	}
	loader := DirectoryArtifacts{Directory: filepath.Dir(dir), PublicKeyBase64: encodeBase64(public)}
	artifact, err := loader.Load("linux", "amd64")
	if err != nil || artifact.SHA256 != HashSignedPayload(binary) {
		t.Fatalf("valid artifact=%+v error=%v", artifact, err)
	}
	if _, err := loader.Load("linux", "arm64"); err == nil {
		t.Fatal("missing arm64 binary accepted")
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Load("linux", "amd64"); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered artifact error=%v", err)
	}
}

func TestStatePinsRejectChangedHostKeyUntilExplicitReconfirmation(t *testing.T) {
	store := NewStateStore(t.TempDir())
	if err := store.Pin("nodekey:stable", "SHA256:firstFingerprintExample123456"); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckPin("nodekey:stable", "SHA256:changedFingerprintExample123456", false); !errors.Is(err, ErrChangedHostKey) {
		t.Fatalf("changed fingerprint error=%v", err)
	}
	if err := store.CheckPin("nodekey:stable", "SHA256:changedFingerprintExample123456", true); err != nil {
		t.Fatalf("explicitly reconfirmed fingerprint rejected: %v", err)
	}
	wrongKey, err := ssh.NewPublicKey(mustPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyHostKey("SHA256:some-other-fingerprint", wrongKey); err == nil {
		t.Fatal("SSH host key different from user-confirmed fingerprint was accepted")
	}
	actual := ssh.FingerprintSHA256(wrongKey)
	if err := verifyHostKey(actual, wrongKey); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}
}

func TestStatePersistsIdentityAssociationsAndPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewStateStore(dir)
	if err := store.Pin("nodekey:one", "SHA256:fingerprint"); err != nil {
		t.Fatal(err)
	}
	if err := store.Associate("nodekey:one", "12345678-1234-4234-8234-123456789abc"); err != nil {
		t.Fatal(err)
	}
	state, err := NewStateStore(dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pins["nodekey:one"] != "SHA256:fingerprint" || state.Managed["nodekey:one"] == "" {
		t.Fatalf("persisted state=%+v", state)
	}
	info, err := os.Stat(filepath.Join(dir, "tailscale-deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode=%#o", info.Mode().Perm())
	}
}

func TestInstallScriptCleansTemporaryArtifactsAndPreservesExistingIdentity(t *testing.T) {
	remote := &recordingRemote{err: errors.New("simulated ssh interruption")}
	artifact := Artifact{Bytes: []byte("binary"), SHA256: HashSignedPayload([]byte("binary")), OS: "linux", Arch: "amd64"}
	err := InstallRemote(context.Background(), remote, Preflight{OS: "linux", Arch: "amd64", UID: 0, ExistingConfig: true}, artifact, "https://core.example", Enrollment{NodeID: "existing-node"})
	if err == nil {
		t.Fatal("interrupted install unexpectedly succeeded")
	}
	script := string(remote.input)
	for _, required := range []string{"trap 'rollback \"$?\"' EXIT", "trap 'rollback 129' HUP", "trap 'rollback 130' INT", "trap 'rollback 143' TERM", `rm -rf -- "$TMP"`, `if [ ! -s /var/lib/nodedance-agent/agent.json ]; then`, "HAD_BINARY", "old-unit"} {
		if !strings.Contains(script, required) {
			t.Errorf("remote rollback/identity-preservation script lacks %q", required)
		}
	}
	guard := strings.Index(script, "if [ ! -s /var/lib/nodedance-agent/agent.json ]; then")
	enroll := strings.Index(script, "runuser -u nodedance-agent -- /usr/local/bin/nodedance-agent enroll")
	if guard < 0 || enroll <= guard {
		t.Fatal("existing Agent config is not guarded from repeated enrollment")
	}
}

func TestInstallScriptPassesAdminFileRootAsDataAndQuotedArgument(t *testing.T) {
	fileRoot := `/srv/node dance/$(touch /tmp/not-executed);%"目录`
	artifactBytes := []byte("test-agent")
	artifact := Artifact{Bytes: artifactBytes, SHA256: HashSignedPayload(artifactBytes), OS: "linux", Arch: "amd64"}
	remote := &recordingRemote{err: errors.New("capture only")}
	err := InstallRemoteWithFileRoot(context.Background(), remote,
		Preflight{OS: "linux", Arch: "amd64", UID: 0, Docker: DockerSocketInfo{Access: "absent"}},
		artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, fileRoot)
	if err == nil {
		t.Fatal("recording remote unexpectedly succeeded")
	}
	script := string(remote.input)
	encoded := base64.StdEncoding.EncodeToString([]byte(fileRoot))
	if !strings.Contains(script, "printf '%s' '"+encoded+"' | base64 -d") || !strings.Contains(script, `--file-root "$FILE_ROOT"`) || !strings.Contains(script, "--enable") {
		t.Fatalf("SSH script did not pass the file root as base64 data and a single quoted CLI argument:\n%s", script)
	}
	if strings.Contains(script, fileRoot) {
		t.Fatal("SSH script interpolated the raw administrator path into shell source")
	}
	for _, rejected := range []string{"relative/path", "/", "/home", "/root/private", "/proc/1", "/var/lib/nodedance-agent/files", "/srv/root\nEnvironment=BAD=1"} {
		before := remote.calls
		if err := InstallRemoteWithFileRoot(context.Background(), remote,
			Preflight{OS: "linux", Arch: "amd64", UID: 0, Docker: DockerSocketInfo{Access: "absent"}},
			artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, rejected); err == nil {
			t.Errorf("unsafe file-root request %q was accepted", rejected)
		}
		if remote.calls != before {
			t.Errorf("unsafe file-root request %q contacted SSH target", rejected)
		}
	}
	disabledRemote := &recordingRemote{err: errors.New("capture only")}
	_ = InstallRemoteWithFileRootOptions(context.Background(), disabledRemote,
		Preflight{OS: "linux", Arch: "amd64", UID: 0, Docker: DockerSocketInfo{Access: "absent"}},
		artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, "", true)
	if !strings.Contains(string(disabledRemote.input), "--no-file-root") || !strings.Contains(string(disabledRemote.input), "--enable") {
		t.Fatal("explicit SSH disable did not reach the Agent systemd installer")
	}
	if err := InstallRemoteWithFileRootOptions(context.Background(), &recordingRemote{},
		Preflight{OS: "linux", Arch: "amd64", UID: 0, Docker: DockerSocketInfo{Access: "absent"}},
		artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"}, fileRoot, true); err == nil {
		t.Fatal("SSH install accepted conflicting file-root and explicit-disable options")
	}
}

func TestInspectRemoteRejectsPermissionAndUnknownArchitecture(t *testing.T) {
	for name, output := range map[string]string{
		"no sudo":                  "Linux\nx86_64\n1000\nnew\n-\nrootless\ndocker|absent\n",
		"unsupported architecture": "Linux\nriscv64\n0\nnew\n-\nroot\ndocker|absent\n",
		"non Linux":                "Darwin\nx86_64\n0\nnew\n-\nroot\ndocker|absent\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := InspectRemote(context.Background(), &recordingRemote{output: []byte(output)}); err == nil {
				t.Fatal("invalid SSH target passed preflight")
			}
		})
	}
}

func TestDockerSocketAccessRejectsOwnerOnlyAndSelectsSupplementaryGroup(t *testing.T) {
	denied, err := AssessDockerSocket(true, "600", 0, 0, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Access != "denied" {
		t.Fatalf("owner-only socket access=%q", denied.Access)
	}
	group, err := AssessDockerSocket(true, "660", 0, 989, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if group.Access != "group" || group.SupplementaryGID != 989 {
		t.Fatalf("group access=%+v", group)
	}
	owner, err := AssessDockerSocket(true, "600", 1001, 1001, 1001)
	if err != nil || owner.Access != "owner" {
		t.Fatalf("owner access=%+v err=%v", owner, err)
	}
	absent, err := AssessDockerSocket(false, "", -1, -1, -1)
	if err != nil || absent.Access != "absent" {
		t.Fatalf("absent socket=%+v err=%v", absent, err)
	}
}

func TestFreshRemoteWithoutAgentUserUsesSupplementaryDockerGroup(t *testing.T) {
	// This is the exact preflight shape for a new host: id -u nodedance-agent
	// exits non-zero, so the remote command must emit -1 instead of an empty UID.
	remote := &recordingRemote{output: []byte("Linux\nx86_64\n0\nnew\n-\nroot\ndocker|660|0|989|-1\n")}
	preflight, err := InspectRemote(context.Background(), remote)
	if err != nil {
		t.Fatalf("fresh target preflight: %v", err)
	}
	if preflight.ExistingConfig || preflight.NodeID != "-" {
		t.Fatalf("unexpected existing Agent identity: %+v", preflight)
	}
	if preflight.Docker.ServiceUID != -1 || preflight.Docker.Access != "group" || preflight.Docker.SupplementaryGID != 989 {
		t.Fatalf("fresh target did not select docker supplementary group: %+v", preflight.Docker)
	}
	if !strings.Contains(string(remote.input), `service_uid="$(id -u nodedance-agent 2>/dev/null || echo -1)"`) {
		t.Fatal("fresh-target preflight does not explicitly report a missing Agent user as UID -1")
	}

	artifactBytes := []byte("test-agent")
	artifact := Artifact{Bytes: artifactBytes, SHA256: HashSignedPayload(artifactBytes), OS: "linux", Arch: "amd64"}
	install := &recordingRemote{err: errors.New("simulated SSH interruption")}
	err = InstallRemote(context.Background(), install, preflight, artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"})
	if err == nil {
		t.Fatal("expected recording SSH transport to interrupt installation")
	}
	if !strings.Contains(string(install.input), "--supplementary-group '989' --enable") {
		t.Fatal("Agent systemd installer does not receive the Docker socket's numeric supplementary GID")
	}
	if !strings.Contains(string(install.input), `[ "$(stat -c '%a|%u|%g' /var/run/docker.sock)" = '660|0|989' ]`) {
		t.Fatal("installation script does not recheck the preflight Docker socket metadata")
	}
}

func TestInstallRefusesDockerSocketThatCannotBeSafelyAuthorized(t *testing.T) {
	remote := &recordingRemote{}
	artifactBytes := []byte("test-agent")
	artifact := Artifact{Bytes: artifactBytes, SHA256: HashSignedPayload(artifactBytes), OS: "linux", Arch: "amd64"}
	preflight := Preflight{OS: "linux", Arch: "amd64", UID: 0, Docker: DockerSocketInfo{Present: true, Access: "denied"}}
	err := InstallRemote(context.Background(), remote, preflight, artifact, "https://core.example", Enrollment{NodeID: "node-1", Token: "one-time-token"})
	if err == nil || !strings.Contains(err.Error(), "safe read/write access") {
		t.Fatalf("unsafe Docker socket install error=%v", err)
	}
	if remote.calls != 0 {
		t.Fatal("installer contacted the target despite denied Docker socket permissions")
	}
}

func TestDockerSocketOwnerAccessRequiresExistingMatchingServiceUID(t *testing.T) {
	for _, serviceUID := range []int{-1, 1002} {
		info, err := AssessDockerSocket(true, "600", 1001, 1001, serviceUID)
		if err != nil {
			t.Fatal(err)
		}
		if info.Access != "denied" {
			t.Fatalf("service UID %d unexpectedly received owner access: %+v", serviceUID, info)
		}
	}
}

func TestRealDockerEngineConnectsWithAuthorizedSupplementaryGroup(t *testing.T) {
	socket := os.Getenv("NODEDANCE_S13_TEST_DOCKER_SOCKET")
	if socket == "" {
		t.Skip("set NODEDANCE_S13_TEST_DOCKER_SOCKET to exercise a real Docker Engine socket")
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("inspect Docker socket: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("Docker socket does not expose numeric ownership")
	}
	modeText := strconv.FormatUint(uint64(info.Mode().Perm()), 8)
	access, err := AssessDockerSocket(true, modeText, int(stat.Uid), int(stat.Gid), -1)
	if err != nil {
		t.Fatal(err)
	}
	if access.Access != "group" {
		t.Fatalf("expected a group-authorized Docker socket for this integration test, got %+v", access)
	}
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	groupPresent := false
	for _, gid := range groups {
		if gid == access.SupplementaryGID {
			groupPresent = true
		}
	}
	if !groupPresent {
		t.Fatalf("test process lacks Docker socket's authorized supplementary GID %d; groups=%v", access.SupplementaryGID, groups)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	response, err := client.Get("http://docker/version")
	if err != nil {
		t.Fatalf("connect to real Docker Engine via authorized socket group: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Docker Engine /version returned %d", response.StatusCode)
	}
}

type shellRemote struct {
	path    string
	env     []string
	command string
	input   []byte
	calls   int
}

func (r *shellRemote) Run(ctx context.Context, command string, input []byte) ([]byte, error) {
	r.calls++
	r.command = command
	r.input = append([]byte(nil), input...)
	cmd := exec.CommandContext(ctx, "sh", "-s")
	cmd.Env = append(os.Environ(), "PATH="+r.path+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, r.env...)
	cmd.Stdin = strings.NewReader(string(input))
	output, err := cmd.CombinedOutput()
	return output, err
}

func (*shellRemote) Close() error { return nil }

func TestBackupCoreIsNeverTriedUnlessExplicitlyEnabledAndTLSFailureIsFatal(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "curl.log")
	curl := `#!/bin/sh
printf '%s\n' "$@" >> "$ND_CURL_LOG"
url=""
for arg do case "$arg" in https://*) url="$arg";; esac; done
case "$url" in *primary*) exit 22;; *backup*) exit "${ND_BACKUP_EXIT:-0}";; esac
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(curl), 0o700); err != nil {
		t.Fatal(err)
	}
	remote := &shellRemote{path: bin, env: []string{"ND_CURL_LOG=" + logPath}}
	if _, err := SelectReachableCoreURL(context.Background(), remote, "https://primary.example", "https://backup.example", false); err == nil {
		t.Fatal("unreachable primary unexpectedly selected")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "backup.example") {
		t.Fatalf("disabled backup was contacted: %s", data)
	}

	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	remote.env = append(remote.env, "ND_BACKUP_EXIT=0")
	selected, err := SelectReachableCoreURL(context.Background(), remote, "https://primary.example", "https://backup.example", true)
	if err != nil || selected != "https://backup.example" {
		t.Fatalf("explicit backup selection=%q err=%v", selected, err)
	}
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "primary.example") || !strings.Contains(string(data), "backup.example") {
		t.Fatalf("explicit backup attempts=%s", data)
	}
	if strings.Contains(string(data), "--insecure") || strings.Contains(string(data), "-k\n") {
		t.Fatalf("TLS verification was disabled: %s", data)
	}

	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	remote.env = []string{"ND_CURL_LOG=" + logPath, "ND_BACKUP_EXIT=60"}
	if _, err := SelectReachableCoreURL(context.Background(), remote, "https://primary.example", "https://backup.example", true); err == nil {
		t.Fatal("backup with TLS certificate failure was accepted")
	}
}
