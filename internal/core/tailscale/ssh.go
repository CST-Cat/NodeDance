package tailscale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshHandshakeTimeout = 8 * time.Second

var ErrOutcomeUnknown = errors.New("remote deployment outcome is unconfirmed")

type SSHCredentials struct {
	User       string `json:"user"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
}

func (c SSHCredentials) authMethods() ([]ssh.AuthMethod, error) {
	if strings.TrimSpace(c.User) == "" || len(c.User) > 128 || strings.ContainsAny(c.User, "\x00\r\n") {
		return nil, errors.New("SSH user is invalid")
	}
	methods := make([]ssh.AuthMethod, 0, 2)
	if c.Password != "" {
		if len(c.Password) > 4096 {
			return nil, errors.New("SSH password is too large")
		}
		methods = append(methods, ssh.Password(c.Password))
	}
	if c.PrivateKey != "" {
		if len(c.PrivateKey) > 64<<10 {
			return nil, errors.New("SSH private key is too large")
		}
		var signer ssh.Signer
		var err error
		if c.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.PrivateKey), []byte(c.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(c.PrivateKey))
		}
		if err != nil {
			return nil, errors.New("SSH private key could not be parsed or decrypted")
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, errors.New("provide a one-time SSH password or private key")
	}
	return methods, nil
}

func firstOnlineIP(peer Peer) (string, error) {
	if peer.Class != "linux" || !peer.Online {
		return "", errors.New("selected Tailscale peer must be an online Linux node")
	}
	for _, candidate := range peer.IPs {
		if ip := net.ParseIP(candidate); ip != nil {
			return ip.String(), nil
		}
	}
	return "", errors.New("selected Tailscale peer has no valid Tailscale IP")
}

func ProbeHostKey(ctx context.Context, peer Peer) (string, error) {
	return probeHostKeyOnPort(ctx, peer, 22)
}

func probeHostKeyOnPort(ctx context.Context, peer Peer, port int) (string, error) {
	if port < 1 || port > 65535 {
		return "", errors.New("SSH port is invalid")
	}
	address, err := firstOnlineIP(peer)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, sshHandshakeTimeout)
	defer cancel()
	remoteAddress := net.JoinHostPort(address, strconv.Itoa(port))
	conn, err := (&net.Dialer{Timeout: sshHandshakeTimeout}).DialContext(ctx, "tcp", remoteAddress)
	if err != nil {
		return "", fmt.Errorf("connect to discovered Tailscale SSH endpoint: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sshHandshakeTimeout))
	var fingerprint string
	config := &ssh.ClientConfig{User: "nodedance-host-key-probe", HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return nil
	}, Timeout: sshHandshakeTimeout}
	_, _, _, handshakeErr := ssh.NewClientConn(conn, remoteAddress, config)
	if fingerprint == "" {
		return "", fmt.Errorf("SSH host key could not be read: %w", handshakeErr)
	}
	return fingerprint, nil
}

type Remote interface {
	Run(context.Context, string, []byte) ([]byte, error)
	Close() error
}

type SSHTransport interface {
	Probe(context.Context, Peer) (string, error)
	Connect(context.Context, Peer, SSHCredentials, string, *StateStore, bool) (Remote, error)
}

type RealSSHTransport struct{}

func (RealSSHTransport) Probe(ctx context.Context, peer Peer) (string, error) {
	return ProbeHostKey(ctx, peer)
}

func (RealSSHTransport) Connect(ctx context.Context, peer Peer, credentials SSHCredentials, fingerprint string, pins *StateStore, reconfirm bool) (Remote, error) {
	return ConnectSSH(ctx, peer, credentials, fingerprint, pins, reconfirm)
}

func ConnectSSH(ctx context.Context, peer Peer, credentials SSHCredentials, expectedFingerprint string, pinStore *StateStore, reconfirmChanged bool) (Remote, error) {
	return connectSSHOnPort(ctx, peer, credentials, expectedFingerprint, pinStore, reconfirmChanged, 22)
}

func connectSSHOnPort(ctx context.Context, peer Peer, credentials SSHCredentials, expectedFingerprint string, pinStore *StateStore, reconfirmChanged bool, port int) (Remote, error) {
	if port < 1 || port > 65535 {
		return nil, errors.New("SSH port is invalid")
	}
	address, err := firstOnlineIP(peer)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(expectedFingerprint, "SHA256:") || len(expectedFingerprint) < 32 {
		return nil, errors.New("confirm the SSH host-key fingerprint shown by NodeDance")
	}
	if err := pinStore.CheckPin(peer.Identity, expectedFingerprint, reconfirmChanged); err != nil {
		return nil, err
	}
	methods, err := credentials.authMethods()
	if err != nil {
		return nil, err
	}
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return verifyHostKey(expectedFingerprint, key)
	}
	config := &ssh.ClientConfig{User: credentials.User, Auth: methods, HostKeyCallback: callback, Timeout: sshHandshakeTimeout}
	dialCtx, cancel := context.WithTimeout(ctx, sshHandshakeTimeout)
	defer cancel()
	remoteAddress := net.JoinHostPort(address, strconv.Itoa(port))
	conn, err := (&net.Dialer{Timeout: sshHandshakeTimeout}).DialContext(dialCtx, "tcp", remoteAddress)
	if err != nil {
		return nil, fmt.Errorf("connect to discovered Tailscale SSH endpoint: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(sshHandshakeTimeout))
	clientConn, channels, requests, err := ssh.NewClientConn(conn, remoteAddress, config)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH authentication or host-key verification failed: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	if err := pinStore.Pin(peer.Identity, expectedFingerprint); err != nil {
		_ = clientConn.Close()
		_ = conn.Close()
		return nil, err
	}
	return &sshRemote{client: ssh.NewClient(clientConn, channels, requests), conn: conn}, nil
}

func verifyHostKey(expected string, key ssh.PublicKey) error {
	if key == nil || expected == "" || ssh.FingerprintSHA256(key) != expected {
		return errors.New("SSH host key does not match the fingerprint confirmed in NodeDance")
	}
	return nil
}

type sshRemote struct {
	client *ssh.Client
	conn   net.Conn
}

func (r *sshRemote) Close() error { return r.client.Close() }

func (r *sshRemote) Run(ctx context.Context, command string, input []byte) ([]byte, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var output boundedOutput
	session.Stdout, session.Stderr = &output, &output
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := session.Start(command); err != nil {
		return nil, err
	}
	if len(input) > 0 {
		if _, err := stdin.Write(input); err != nil {
			_ = stdin.Close()
			_ = r.client.Close()
			return output.Bytes(), fmt.Errorf("%w: SSH stream ended while sending the remote deployment", ErrOutcomeUnknown)
		}
	}
	if err := stdin.Close(); err != nil {
		_ = r.client.Close()
		return output.Bytes(), err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var exitError *ssh.ExitError
			if !errors.As(err, &exitError) {
				return output.Bytes(), fmt.Errorf("%w: remote SSH connection ended before the command result was confirmed: %v", ErrOutcomeUnknown, err)
			}
			return output.Bytes(), fmt.Errorf("remote SSH command failed: %w: %s", err, strings.TrimSpace(string(output.Bytes())))
		}
		return output.Bytes(), nil
	case <-ctx.Done():
		_ = r.client.Close()
		return output.Bytes(), fmt.Errorf("%w: %v", ErrOutcomeUnknown, ctx.Err())
	}
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	original := len(data)
	if b.Len() >= 64<<10 {
		return original, nil
	}
	if len(data) > (64<<10)-b.Len() {
		data = data[:(64<<10)-b.Len()]
	}
	_, _ = b.Buffer.Write(data)
	return original, nil
}

type Preflight struct {
	OS, Arch       string
	UID            int
	UseSudo        bool
	ExistingConfig bool
	NodeID         string
	Docker         DockerSocketInfo
}

type DockerSocketInfo struct {
	Present          bool
	ModeText         string
	Mode             uint32
	OwnerUID         int
	GroupGID         int
	ServiceUID       int
	Access           string
	SupplementaryGID int
}

func AssessDockerSocket(present bool, modeText string, ownerUID, groupGID, serviceUID int) (DockerSocketInfo, error) {
	info := DockerSocketInfo{Present: present, ModeText: modeText, OwnerUID: ownerUID, GroupGID: groupGID, ServiceUID: serviceUID, SupplementaryGID: -1}
	if !present {
		info.Access = "absent"
		return info, nil
	}
	mode, err := strconv.ParseUint(strings.TrimSpace(modeText), 8, 32)
	if err != nil || ownerUID < 0 || groupGID < 0 {
		return info, errors.New("Docker socket permissions or numeric owner are invalid")
	}
	info.Mode = uint32(mode)
	switch {
	case info.Mode&0o060 == 0o060:
		info.Access = "group"
		info.SupplementaryGID = groupGID
	case info.Mode&0o006 == 0o006:
		info.Access = "world"
	case info.Mode&0o600 == 0o600 && serviceUID >= 0 && ownerUID == serviceUID:
		info.Access = "owner"
	default:
		info.Access = "denied"
	}
	return info, nil
}

func InspectRemote(ctx context.Context, remote Remote) (Preflight, error) {
	command := `set -eu
uid="$(id -u)"
if [ "$uid" -ne 0 ]; then command -v sudo >/dev/null && sudo -n true; rootcmd='sudo -n'; else rootcmd=''; fi
printf '%s\n' "$(uname -s)" "$(uname -m)" "$uid"
command -v systemctl >/dev/null
if [ -n "$rootcmd" ]; then
  if sudo -n test -s /var/lib/nodedance-agent/agent.json; then echo existing; sudo -n grep -oE '"nodeId"[[:space:]]*:[[:space:]]*"[^"]+"' /var/lib/nodedance-agent/agent.json | cut -d '"' -f4 | head -n 1; else echo new; echo -; fi
  echo sudo
else
  if test -s /var/lib/nodedance-agent/agent.json; then echo existing; grep -oE '"nodeId"[[:space:]]*:[[:space:]]*"[^"]+"' /var/lib/nodedance-agent/agent.json | cut -d '"' -f4 | head -n 1; else echo new; echo -; fi
  echo root
fi`
	command += `
if [ -S /var/run/docker.sock ]; then
  service_uid="$(id -u nodedance-agent 2>/dev/null || echo -1)"
  printf 'docker|%s|%s|%s|%s\n' "$(stat -c %a /var/run/docker.sock)" "$(stat -c %u /var/run/docker.sock)" "$(stat -c %g /var/run/docker.sock)" "$service_uid"
elif [ -e /var/run/docker.sock ]; then
  echo docker|invalid
else
  echo docker|absent
fi`
	output, err := remote.Run(ctx, "sh -s", []byte(command))
	if err != nil {
		return Preflight{}, fmt.Errorf("SSH preflight failed (check Linux, systemd, and root or passwordless sudo permissions): %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) < 7 {
		return Preflight{}, errors.New("SSH preflight returned incomplete OS, architecture, permission, or identity information")
	}
	arch := normalizeArchitecture(fields[1])
	if fields[0] != "Linux" {
		return Preflight{}, errors.New("SSH deployment supports Linux nodes only")
	}
	if arch == "" {
		return Preflight{}, fmt.Errorf("unsupported SSH node architecture %q", fields[1])
	}
	var uid int
	if _, err := fmt.Sscanf(fields[2], "%d", &uid); err != nil || uid < 0 {
		return Preflight{}, errors.New("SSH preflight returned an invalid effective UID")
	}
	if fields[3] != "existing" && fields[3] != "new" {
		return Preflight{}, errors.New("SSH preflight returned an invalid Agent identity state")
	}
	if fields[5] != "sudo" && fields[5] != "root" {
		return Preflight{}, errors.New("SSH deployment requires root or non-interactive sudo access")
	}
	if uid != 0 && fields[5] != "sudo" {
		return Preflight{}, errors.New("SSH account lacks root privileges or passwordless sudo")
	}
	preflight := Preflight{OS: "linux", Arch: arch, UID: uid, UseSudo: fields[5] == "sudo", ExistingConfig: fields[3] == "existing", NodeID: strings.TrimSpace(fields[4])}
	for _, field := range fields[6:] {
		if field == "docker|absent" {
			preflight.Docker.Access = "absent"
			continue
		}
		if field == "docker|invalid" {
			preflight.Docker = DockerSocketInfo{Present: true, Access: "invalid"}
			continue
		}
		parts := strings.Split(field, "|")
		if len(parts) != 5 || parts[0] != "docker" {
			continue
		}
		mode, modeErr := strconv.ParseUint(parts[1], 8, 32)
		ownerUID, uidErr := strconv.Atoi(parts[2])
		groupGID, gidErr := strconv.Atoi(parts[3])
		serviceUID, serviceErr := strconv.Atoi(parts[4])
		if modeErr != nil || uidErr != nil || gidErr != nil || serviceErr != nil {
			return Preflight{}, errors.New("SSH preflight returned invalid Docker socket metadata")
		}
		info, err := AssessDockerSocket(true, parts[1], ownerUID, groupGID, serviceUID)
		if err != nil {
			return Preflight{}, err
		}
		info.Mode = uint32(mode)
		preflight.Docker = info
	}
	if preflight.Docker.Access == "" {
		return Preflight{}, errors.New("SSH preflight did not report Docker socket state")
	}
	return preflight, nil
}

func normalizeArchitecture(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

func validateCoreURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("Core URL must be an HTTPS origin; TLS verification is always enabled")
	}
	if parsed.Port() != "" {
		if parsed.Port() == "0" {
			return "", errors.New("Core URL port is invalid")
		}
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// SelectReachableCoreURL probes from the target node. curl's normal CA and
// hostname verification stays enabled; the helper never uses an insecure TLS
// flag. A backup is not contacted unless allowFallback is explicitly true.
func SelectReachableCoreURL(ctx context.Context, remote Remote, primary, fallback string, allowFallback bool) (string, error) {
	primary, err := validateCoreURL(primary)
	if err != nil {
		return "", err
	}
	if fallback != "" {
		fallback, err = validateCoreURL(fallback)
		if err != nil {
			return "", fmt.Errorf("backup Core URL rejected: %w", err)
		}
	}
	allow := "0"
	if allowFallback {
		allow = "1"
	}
	script := fmt.Sprintf(`set -eu
probe() { curl --fail --silent --show-error --proto '=https' --tlsv1.2 --connect-timeout 4 --max-time 8 --output /dev/null "$1/api/v1/auth/setup/status"; }
primary="$(printf '%%s' '%s' | base64 -d)"
fallback="$(printf '%%s' '%s' | base64 -d)"
if probe "$primary"; then echo primary; exit 0; fi
if [ '%s' = 1 ] && [ -n "$fallback" ] && probe "$fallback"; then echo fallback; exit 0; fi
echo 'Core HTTPS endpoint is unreachable or failed TLS verification' >&2
exit 31
`, base64.StdEncoding.EncodeToString([]byte(primary)), base64.StdEncoding.EncodeToString([]byte(fallback)), allow)
	output, err := remote.Run(ctx, "sh -s", []byte(script))
	if err != nil {
		if allowFallback && fallback != "" {
			return "", fmt.Errorf("primary and explicitly enabled backup Core HTTPS endpoints failed connectivity or TLS verification: %w", err)
		}
		return "", fmt.Errorf("primary Core HTTPS endpoint failed connectivity or TLS verification (backup was not attempted): %w", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "primary":
		return primary, nil
	case "fallback":
		if allowFallback && fallback != "" {
			return fallback, nil
		}
	}
	return "", errors.New("remote Core reachability probe returned an unknown result")
}

type Enrollment struct{ NodeID, Token string }

// preflightRemoteFileRoot runs the Agent's exact filesystem validator on the
// target before Core creates a one-time enrollment identity. The binary is
// the already signature-verified artifact selected for this target's platform;
// its digest is checked again after transfer. The path is transported as
// base64 data and passed to the validator as one process argument.
func preflightRemoteFileRoot(ctx context.Context, remote Remote, preflight Preflight, artifact Artifact, fileRoot string) error {
	if err := validateFileRootRequest(fileRoot); err != nil {
		return err
	}
	if fileRoot == "" {
		return nil
	}
	if remote == nil {
		return errors.New("SSH target is unavailable for file-root validation")
	}
	if preflight.OS != "linux" || preflight.Arch != artifact.Arch || artifact.OS != "linux" || len(artifact.Bytes) == 0 || len(artifact.Bytes) > maxAgentArtifactSize {
		return errors.New("signed Agent artifact does not match the preflight OS and architecture")
	}
	if HashSignedPayload(artifact.Bytes) != artifact.SHA256 {
		return errors.New("signed Agent artifact digest is inconsistent")
	}
	mode := "/bin/sh -s"
	if preflight.UseSudo {
		mode = "sudo -n /bin/sh -s"
	}
	if preflight.UID != 0 && !preflight.UseSudo {
		return errors.New("root or non-interactive sudo permissions are required")
	}
	encodedArtifact := base64.StdEncoding.EncodeToString(artifact.Bytes)
	encodedFileRoot := base64.StdEncoding.EncodeToString([]byte(fileRoot))
	script := fmt.Sprintf(`set -eu
umask 077
TMP="$(mktemp -d /tmp/nodedance-file-root-check.XXXXXX)"
cleanup() { rm -rf -- "$TMP"; }
trap cleanup EXIT HUP INT TERM
printf '%%s' '%s' | base64 -d > "$TMP/nodedance-agent"
actual="$(sha256sum "$TMP/nodedance-agent" | awk '{print $1}')"
[ "$actual" = '%s' ] || { echo 'Agent SHA-256 mismatch during file-root preflight' >&2; exit 21; }
chmod 0700 "$TMP/nodedance-agent"
FILE_ROOT="$(printf '%%s' '%s' | base64 -d)"
"$TMP/nodedance-agent" validate-file-root --file-root "$FILE_ROOT" --state-dir /var/lib/nodedance-agent
`, encodedArtifact, artifact.SHA256, encodedFileRoot)
	if _, err := remote.Run(ctx, mode, []byte(script)); err != nil {
		return fmt.Errorf("validate the selected file root on the target: %w", err)
	}
	return nil
}

func InstallRemote(ctx context.Context, remote Remote, preflight Preflight, artifact Artifact, coreURL string, enrollment Enrollment) error {
	return InstallRemoteWithFileRootOptions(ctx, remote, preflight, artifact, coreURL, enrollment, "", false)
}

// InstallRemoteWithFileRoot passes an optional admin-selected host directory
// as base64 data through the SSH script and then as one quoted process
// argument. The target Agent installer performs filesystem canonicalization
// and the full file-root policy check before generating the unit.
func InstallRemoteWithFileRoot(ctx context.Context, remote Remote, preflight Preflight, artifact Artifact, coreURL string, enrollment Enrollment, fileRoot string) error {
	return InstallRemoteWithFileRootOptions(ctx, remote, preflight, artifact, coreURL, enrollment, fileRoot, false)
}

func InstallRemoteWithFileRootOptions(ctx context.Context, remote Remote, preflight Preflight, artifact Artifact, coreURL string, enrollment Enrollment, fileRoot string, disableFileRoot bool) error {
	coreURL, err := validateCoreURL(coreURL)
	if err != nil {
		return err
	}
	if err := validateFileRootRequest(fileRoot); err != nil {
		return err
	}
	if disableFileRoot && fileRoot != "" {
		return errors.New("fileRoot and disableFileRoot cannot be used together")
	}
	if preflight.OS != "linux" || preflight.Arch != artifact.Arch || artifact.OS != "linux" || len(artifact.Bytes) == 0 {
		return errors.New("signed Agent artifact does not match the preflight OS and architecture")
	}
	if !preflight.ExistingConfig && (enrollment.NodeID == "" || enrollment.Token == "") {
		return errors.New("one-time Agent enrollment is required for a new installation")
	}
	mode := "/bin/sh -s"
	if preflight.UseSudo {
		mode = "sudo -n /bin/sh -s"
	}
	if preflight.UID != 0 && !preflight.UseSudo {
		return errors.New("root or non-interactive sudo permissions are required")
	}
	if preflight.Docker.Present && preflight.Docker.Access != "group" && preflight.Docker.Access != "world" && preflight.Docker.Access != "owner" {
		return errors.New("Docker socket is present but the Agent service user cannot be granted safe read/write access; refusing to start a partially authorized Agent")
	}
	encodedArtifact := base64.StdEncoding.EncodeToString(artifact.Bytes)
	encodedURL := base64.StdEncoding.EncodeToString([]byte(coreURL))
	encodedToken := base64.StdEncoding.EncodeToString([]byte(enrollment.Token))
	encodedFileRoot := base64.StdEncoding.EncodeToString([]byte(fileRoot))
	fileRootInstall := `/usr/local/bin/nodedance-agent install-systemd --user nodedance-agent --config /var/lib/nodedance-agent/agent.json`
	if fileRoot != "" {
		fileRootInstall = fmt.Sprintf(`FILE_ROOT="$(printf '%%s' '%s' | base64 -d)"
/usr/local/bin/nodedance-agent install-systemd --user nodedance-agent --config /var/lib/nodedance-agent/agent.json --file-root "$FILE_ROOT"`, encodedFileRoot)
	} else if disableFileRoot {
		fileRootInstall = "/usr/local/bin/nodedance-agent install-systemd --user nodedance-agent --config /var/lib/nodedance-agent/agent.json --no-file-root"
	} else {
		fileRootInstall = "env -u NODEDANCE_AGENT_FILE_ROOT " + fileRootInstall
	}
	dockerSupplementaryGroup := ""
	if preflight.Docker.Access == "group" {
		dockerSupplementaryGroup = fmt.Sprintf("--supplementary-group '%d'", preflight.Docker.SupplementaryGID)
	}
	dockerSocketRecheck := ""
	dockerOwnerRecheck := ""
	if preflight.Docker.Present {
		dockerSocketRecheck = fmt.Sprintf(`[ -S /var/run/docker.sock ] || { echo 'Docker socket changed after preflight' >&2; exit 23; }
[ "$(stat -c '%%a|%%u|%%g' /var/run/docker.sock)" = '%s|%d|%d' ] || { echo 'Docker socket permissions changed after preflight' >&2; exit 24; }
`, preflight.Docker.ModeText, preflight.Docker.OwnerUID, preflight.Docker.GroupGID)
	}
	if preflight.Docker.Access == "owner" {
		dockerOwnerRecheck = fmt.Sprintf(`[ "$service_uid" = '%d' ] || { echo 'Agent service UID changed after Docker socket preflight' >&2; exit 25; }
`, preflight.Docker.OwnerUID)
	}
	script := fmt.Sprintf(`set -eu
umask 077
TMP="$(mktemp -d /tmp/nodedance-deploy.XXXXXX)"
NEW_CONFIG=0
HAD_BINARY=0
HAD_AGENT_BIN=0
HAD_UNIT=0
WAS_ACTIVE=0
rollback() {
  result="$1"
  trap - EXIT HUP INT TERM
  if [ "$result" -ne 0 ]; then
    if [ "$HAD_BINARY" -eq 1 ]; then cp -a "$TMP/old-agent" /usr/local/bin/nodedance-agent; else rm -f /usr/local/bin/nodedance-agent; fi
    if [ "$HAD_AGENT_BIN" -eq 1 ]; then rm -rf /var/lib/nodedance-agent/bin; cp -a "$TMP/old-agent-bin" /var/lib/nodedance-agent/bin; else rm -rf /var/lib/nodedance-agent/bin; fi
    if [ "$HAD_UNIT" -eq 1 ]; then cp -a "$TMP/old-unit" /etc/systemd/system/nodedance-agent.service; else rm -f /etc/systemd/system/nodedance-agent.service; fi
    if [ "$NEW_CONFIG" -eq 1 ]; then systemctl disable --now nodedance-agent.service >/dev/null 2>&1 || true; rm -f /var/lib/nodedance-agent/agent.json; fi
    systemctl daemon-reload >/dev/null 2>&1 || true
    if [ "$WAS_ACTIVE" -eq 1 ]; then systemctl restart nodedance-agent.service >/dev/null 2>&1 || true; fi
  fi
  rm -rf -- "$TMP"
  exit "$result"
}
trap 'rollback "$?"' EXIT
trap 'rollback 129' HUP
trap 'rollback 130' INT
trap 'rollback 143' TERM
if [ -x /usr/local/bin/nodedance-agent ]; then cp -a /usr/local/bin/nodedance-agent "$TMP/old-agent"; HAD_BINARY=1; fi
if [ -d /var/lib/nodedance-agent/bin ]; then cp -a /var/lib/nodedance-agent/bin "$TMP/old-agent-bin"; HAD_AGENT_BIN=1; fi
if [ -f /etc/systemd/system/nodedance-agent.service ]; then cp -a /etc/systemd/system/nodedance-agent.service "$TMP/old-unit"; HAD_UNIT=1; fi
if systemctl is-active --quiet nodedance-agent.service; then WAS_ACTIVE=1; fi
%s
printf '%%s' '%s' | base64 -d > "$TMP/nodedance-agent"
actual="$(sha256sum "$TMP/nodedance-agent" | awk '{print $1}')"
[ "$actual" = '%s' ] || { echo 'Agent SHA-256 mismatch' >&2; exit 21; }
chmod 0755 "$TMP/nodedance-agent"
if ! getent passwd nodedance-agent >/dev/null; then useradd --system --home-dir /var/lib/nodedance-agent --shell /usr/sbin/nologin nodedance-agent; fi
service_uid="$(id -u nodedance-agent)"
[ "$service_uid" -ne 0 ] || { echo 'refusing to run Agent as root service account' >&2; exit 22; }
%s
install -m 0755 "$TMP/nodedance-agent" /usr/local/bin/.nodedance-agent.new
mv -f /usr/local/bin/.nodedance-agent.new /usr/local/bin/nodedance-agent
mkdir -p /var/lib/nodedance-agent
chmod 0700 /var/lib/nodedance-agent
chown nodedance-agent:nodedance-agent /var/lib/nodedance-agent
if [ ! -s /var/lib/nodedance-agent/agent.json ]; then
  NEW_CONFIG=1
  printf '%%s' '%s' | base64 -d | runuser -u nodedance-agent -- /usr/local/bin/nodedance-agent enroll --server "$(printf '%%s' '%s' | base64 -d)" --token-stdin --config /var/lib/nodedance-agent/agent.json
fi
chmod 0600 /var/lib/nodedance-agent/agent.json
chown nodedance-agent:nodedance-agent /var/lib/nodedance-agent/agent.json
%s %s --enable
/usr/local/bin/nodedance-agent version >/dev/null
`, dockerSocketRecheck, encodedArtifact, artifact.SHA256, dockerOwnerRecheck, encodedToken, encodedURL, fileRootInstall, dockerSupplementaryGroup)
	if _, err := remote.Run(ctx, mode, []byte(script)); err != nil {
		return fmt.Errorf("remote Agent installation failed; temporary artifacts are removed and prior binary/service are restored where possible: %w", err)
	}
	return nil
}

func validateFileRootRequest(value string) error {
	if value == "" {
		return nil
	}
	if !filepath.IsAbs(value) || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("Agent file root must be an absolute directory path without control characters")
	}
	clean := filepath.Clean(value)
	if clean == "/" || clean == "/home" {
		return fmt.Errorf("Agent file root %q is too broad or protected", clean)
	}
	for _, root := range []string{"/root", "/proc", "/sys", "/dev", "/run", "/etc/systemd", "/var/lib/nodedance-agent"} {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return fmt.Errorf("Agent file root %q is too broad or protected", clean)
		}
	}
	privateState := "/var/lib/nodedance-agent"
	if clean == privateState || pathContains(clean, privateState) || pathContains(privateState, clean) {
		return errors.New("Agent file root may not include the private systemd state directory")
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// HashSignedPayload is exported for release tooling and deterministic tests.
func HashSignedPayload(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}
