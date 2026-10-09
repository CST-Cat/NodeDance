package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const AgentUnitName = "nodedance-agent.service"

type SystemdInstallOptions struct {
	User                string
	ConfigPath          string
	UnitDir             string
	BinaryPath          string
	FileRoot            string
	FileRootSpecified   bool
	DisableFileRoot     bool
	SupplementaryGroups []string
	Reload              bool
	EnableNow           bool
}

func InstallSystemd(ctx context.Context, options SystemdInstallOptions) (string, error) {
	if strings.TrimSpace(options.User) == "" {
		return "", errors.New("systemd installation requires an explicit --user")
	}
	serviceUser, err := user.Lookup(options.User)
	if err != nil {
		return "", fmt.Errorf("lookup systemd service user %q: %w", options.User, err)
	}
	if !validUnitIdentity(serviceUser.Username) {
		return "", errors.New("systemd service user has an unsupported account name")
	}
	uid, err := strconv.Atoi(serviceUser.Uid)
	if err != nil || uid < 0 {
		return "", errors.New("systemd service user has an invalid UID")
	}
	gid, err := strconv.Atoi(serviceUser.Gid)
	if err != nil || gid < 0 {
		return "", errors.New("systemd service user has an invalid primary GID")
	}
	if options.UnitDir == "" {
		options.UnitDir = "/etc/systemd/system"
	}
	if options.EnableNow && options.UnitDir != "/etc/systemd/system" {
		return "", errors.New("enabling a systemd unit is available only for /etc/systemd/system")
	}
	if options.EnableNow {
		options.Reload = true
	}
	if options.Reload && options.UnitDir != "/etc/systemd/system" {
		return "", errors.New("systemd daemon reload is available only for /etc/systemd/system")
	}
	if options.BinaryPath == "" {
		options.BinaryPath, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("resolve Agent executable: %w", err)
		}
	}
	options.BinaryPath, err = filepath.Abs(options.BinaryPath)
	if err != nil {
		return "", fmt.Errorf("resolve Agent executable path: %w", err)
	}
	if info, err := os.Stat(options.BinaryPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("systemd Agent executable must be an executable regular file")
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "/var/lib/nodedance-agent/agent.json"
	}
	if !filepath.IsAbs(options.ConfigPath) {
		return "", errors.New("systemd Agent config path must be absolute")
	}
	options.ConfigPath = filepath.Clean(options.ConfigPath)
	if err := validateSystemdConfig(options.ConfigPath, uid); err != nil {
		return "", err
	}
	if options.UnitDir == "/etc/systemd/system" && os.Geteuid() != 0 {
		return "", errors.New("installing a system unit requires root privileges")
	}
	stateDir := filepath.Dir(options.ConfigPath)
	unitPath := filepath.Join(options.UnitDir, AgentUnitName)
	fileRoot, err := resolveSystemdFileRoot(options, unitPath, stateDir)
	if err != nil {
		return "", err
	}
	for _, group := range options.SupplementaryGroups {
		if !validSupplementaryGroup(group) {
			return "", errors.New("systemd supplementary group must be a numeric GID or a valid group name")
		}
	}
	if err := os.MkdirAll(options.UnitDir, 0o755); err != nil {
		return "", fmt.Errorf("create systemd unit directory: %w", err)
	}
	unit := renderSystemdUnit(serviceUser.Username, serviceUser.Gid, options.ConfigPath, options.BinaryPath, fileRoot, options.SupplementaryGroups)
	if err := writeSystemdUnit(unitPath, unit); err != nil {
		return "", err
	}
	if options.Reload {
		command := exec.CommandContext(ctx, "systemctl", "daemon-reload")
		if output, err := command.CombinedOutput(); err != nil {
			return unitPath, fmt.Errorf("systemctl daemon-reload failed: %s", strings.TrimSpace(string(output)))
		}
	}
	if options.EnableNow {
		command := exec.CommandContext(ctx, "systemctl", "enable", "--now", AgentUnitName)
		if output, err := command.CombinedOutput(); err != nil {
			return unitPath, fmt.Errorf("systemctl enable --now failed: %s", strings.TrimSpace(string(output)))
		}
	}
	return unitPath, nil
}

func renderSystemdUnit(serviceUser, primaryGroup, configPath, binaryPath, fileRoot string, supplementaryGroups []string) string {
	stateDir := filepath.Dir(configPath)
	groupLine := ""
	if len(supplementaryGroups) > 0 {
		quoted := make([]string, 0, len(supplementaryGroups))
		for _, group := range supplementaryGroups {
			quoted = append(quoted, systemdQuote(group))
		}
		groupLine = "SupplementaryGroups=" + strings.Join(quoted, " ") + "\n"
	}
	fileRootLine := ""
	fileRootMarker := ""
	if fileRoot != "" {
		fileRootLine = "BindPaths=" + systemdQuoteUnitValue(fileRoot) + "\nEnvironment=NODEDANCE_AGENT_FILE_ROOT=" + systemdQuoteUnitValue(fileRoot) + "\n"
		fileRootMarker = "# NodeDanceFileRootBase64=" + base64.RawStdEncoding.EncodeToString([]byte(fileRoot)) + "\n"
	}
	return fmt.Sprintf(`[Unit]
Description=NodeDance Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
%sNoNewPrivileges=true
ProtectSystem=strict
ProtectHome=tmpfs
PrivateTmp=true
ReadWritePaths=%s
%s%s
UMask=0077
ExecStart=/usr/bin/env -- %s run --config %s
Restart=always
RestartSec=3s

[Install]
WantedBy=multi-user.target
`, serviceUser, primaryGroup, groupLine, systemdQuoteUnitValue(stateDir), fileRootLine, fileRootMarker, systemdQuote(binaryPath), systemdQuote(configPath))
}

func systemdQuote(value string) string {
	quoted := strconv.Quote(value)
	quoted = strings.ReplaceAll(quoted, "%", "%%")
	return strings.ReplaceAll(quoted, "$", "$$")
}

// systemdQuoteUnitValue escapes unit specifiers without applying ExecStart's
// dollar-variable escaping. Path-valued directives and Environment= treat a
// dollar sign literally; doubling it here would point to a different path.
func systemdQuoteUnitValue(value string) string {
	return strings.ReplaceAll(strconv.Quote(value), "%", "%%")
}

func validUnitIdentity(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-') {
			return false
		}
	}
	return true
}

func validateSystemdConfig(path string, serviceUID int) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve systemd Agent config path: %w", err)
	}
	path = filepath.Clean(absolute)
	if err := rejectSymlinkPath(path); err != nil {
		return err
	}
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect systemd Agent state directory: %w", err)
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 || directoryInfo.Mode().Perm() != 0o700 {
		return errors.New("systemd Agent config requires a dedicated 0700 directory")
	}
	if err := checkOwnerUID(directoryInfo, serviceUID, "systemd Agent state directory"); err != nil {
		return err
	}
	configInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect systemd Agent config: %w", err)
	}
	if !configInfo.Mode().IsRegular() || configInfo.Mode()&os.ModeSymlink != 0 || configInfo.Mode().Perm() != 0o600 {
		return errors.New("systemd Agent config must be a regular 0600 file")
	}
	if err := checkOwnerUID(configInfo, serviceUID, "systemd Agent config"); err != nil {
		return err
	}
	if _, err := loadConfigForUID(path, serviceUID); err != nil {
		return fmt.Errorf("validate systemd Agent config: %w", err)
	}
	return nil
}

// ValidateFileRoot applies the same path policy to foreground Agents and
// systemd installations. The root must already exist and be a real directory;
// rejecting symlinked path components keeps the path named by the service
// allowlist identical to the directory opened by the Agent.
func ValidateFileRoot(value, stateDir string) (string, error) {
	if value == "" || !filepath.IsAbs(value) || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("Agent file root must be an absolute existing directory")
	}
	clean := filepath.Clean(value)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("resolve Agent file root: %w", err)
	}
	if resolved != clean {
		return "", errors.New("Agent file root may not contain symlinked path components")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("inspect Agent file root: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("Agent file root must be a directory")
	}
	if clean == "/" || clean == "/home" {
		return "", fmt.Errorf("Agent file root %q is too broad or protected", clean)
	}
	for _, root := range []string{"/root", "/proc", "/sys", "/dev", "/run", "/etc/systemd"} {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			return "", fmt.Errorf("Agent file root %q is too broad or protected", clean)
		}
	}
	if stateDir != "" {
		stateDir = filepath.Clean(stateDir)
		if clean == stateDir || isPathAncestor(clean, stateDir) || isPathAncestor(stateDir, clean) {
			return "", errors.New("Agent file root may not include the systemd Agent state directory")
		}
	}
	return clean, nil
}

func isPathAncestor(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validSupplementaryGroup(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' || char == '-') {
			return false
		}
	}
	return true
}

func resolveSystemdFileRoot(options SystemdInstallOptions, unitPath, stateDir string) (string, error) {
	if options.DisableFileRoot && (options.FileRootSpecified || options.FileRoot != "") {
		return "", errors.New("--file-root and --no-file-root are mutually exclusive")
	}
	if options.DisableFileRoot {
		return "", nil
	}
	if options.FileRootSpecified || options.FileRoot != "" {
		return ValidateFileRoot(options.FileRoot, stateDir)
	}
	if configured := os.Getenv("NODEDANCE_AGENT_FILE_ROOT"); configured != "" {
		return ValidateFileRoot(configured, stateDir)
	}
	if previous, err := readPersistedSystemdFileRoot(unitPath); err != nil {
		return "", err
	} else if previous != "" {
		return ValidateFileRoot(previous, stateDir)
	}
	return "", nil
}

func readPersistedSystemdFileRoot(unitPath string) (string, error) {
	data, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read existing Agent systemd unit: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "# NodeDanceFileRootBase64=") {
			continue
		}
		encoded := strings.TrimPrefix(line, "# NodeDanceFileRootBase64=")
		decoded, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			return "", errors.New("existing Agent systemd file-root marker is invalid; use --no-file-root to replace it")
		}
		return string(decoded), nil
	}
	return "", nil
}

func writeSystemdUnit(path, contents string) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("refusing to replace a non-regular systemd unit path")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect systemd unit path: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".nodedance-agent-unit-*")
	if err != nil {
		return fmt.Errorf("create temporary systemd unit: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write systemd unit: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync systemd unit: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install systemd unit: %w", err)
	}
	return nil
}
