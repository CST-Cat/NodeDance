package tailscale

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealOpenSSHPreflightHostKeyAndCommand(t *testing.T) {
	portText := os.Getenv("NODEDANCE_S13_TEST_SSH_PORT")
	if portText == "" {
		t.Skip("set NODEDANCE_S13_TEST_SSH_PORT and NODEDANCE_S13_TEST_SSH_USER/PRIVATE_KEY_FILE for an isolated OpenSSH fixture")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		t.Fatalf("invalid OpenSSH fixture port %q", portText)
	}
	user := strings.TrimSpace(os.Getenv("NODEDANCE_S13_TEST_SSH_USER"))
	if user == "" {
		t.Fatal("NODEDANCE_S13_TEST_SSH_USER is required with an OpenSSH fixture")
	}
	keyPath := os.Getenv("NODEDANCE_S13_TEST_SSH_PRIVATE_KEY_FILE")
	privateKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read private key for isolated OpenSSH fixture: %v", err)
	}
	peer := Peer{Identity: "ssh-fixture", Class: "linux", Online: true, IPs: []string{"127.0.0.1"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fingerprint, err := probeHostKeyOnPort(ctx, peer, port)
	if err != nil {
		t.Fatalf("read real OpenSSH host key: %v", err)
	}
	state := NewStateStore(t.TempDir())
	remote, err := connectSSHOnPort(ctx, peer, SSHCredentials{User: user, PrivateKey: string(privateKey)}, fingerprint, state, false, port)
	if err != nil {
		t.Fatalf("authenticate to real OpenSSH fixture with confirmed host key: %v", err)
	}
	defer remote.Close()

	output, err := remote.Run(ctx, "printf 'nodedance-s13-ssh-ok\\n'", nil)
	if err != nil || strings.TrimSpace(string(output)) != "nodedance-s13-ssh-ok" {
		t.Fatalf("real SSH command output=%q err=%v", output, err)
	}
	preflight, err := InspectRemote(ctx, remote)
	if err != nil {
		t.Fatalf("run real SSH target preflight: %v", err)
	}
	if preflight.OS != "linux" || preflight.Arch == "" || !preflight.UseSudo || preflight.UID == 0 {
		t.Fatalf("unexpected OpenSSH target preflight: %+v", preflight)
	}
	if preflight.Docker.Access != "absent" && preflight.Docker.Access != "group" && preflight.Docker.Access != "world" && preflight.Docker.Access != "owner" && preflight.Docker.Access != "denied" {
		t.Fatalf("preflight did not classify the real Docker socket: %+v", preflight.Docker)
	}

	if _, err := connectSSHOnPort(ctx, peer, SSHCredentials{User: user, Password: "intentionally-wrong-password"}, fingerprint, state, false, port); err == nil {
		t.Fatal("real OpenSSH fixture accepted an invalid credential")
	}
	if err := state.CheckPin(peer.Identity, "SHA256:changed-fixture-host-key-0123456789", false); err != ErrChangedHostKey {
		t.Fatalf("changed real host-key pin error=%v", err)
	}
}
