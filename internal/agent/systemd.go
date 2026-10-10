package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	ExpectedUnitState   string
	ExpectedUnitSHA256  string
	ResultSHA256Path    string
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
	if err != nil || uid <= 0 {
		return "", errors.New("systemd service user must resolve to a non-root UID")
	}
	gid, err := strconv.Atoi(serviceUser.Gid)
	if err != nil || gid < 0 {
		return "", errors.New("systemd service user has an invalid primary GID")
	}
	if options.UnitDir == "" {
		options.UnitDir = "/etc/systemd/system"
	}
	options.UnitDir = filepath.Clean(options.UnitDir)
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
	if options.UnitDir == "/etc/systemd/system" {
		if err := refuseSystemdUnitShadow(ctx, unitPath); err != nil {
			return "", err
		}
	}
	if options.ExpectedUnitState != "" && options.ExpectedUnitState != "absent" && options.ExpectedUnitState != "managed" {
		return "", errors.New("expected systemd unit state must be absent or managed")
	}
	if options.ExpectedUnitSHA256 != "" {
		decoded, err := hex.DecodeString(options.ExpectedUnitSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return "", errors.New("expected systemd unit fingerprint is invalid")
		}
	}
	if options.ExpectedUnitState == "absent" && options.ExpectedUnitSHA256 != "" || options.ExpectedUnitState == "managed" && options.ExpectedUnitSHA256 == "" {
		return "", errors.New("expected systemd unit state and fingerprint do not match")
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
	if err := writeSystemdUnitExpected(unitPath, unit, options.ExpectedUnitState, options.ExpectedUnitSHA256); err != nil {
		return "", err
	}
	if options.ResultSHA256Path != "" {
		unitDigest := sha256.Sum256([]byte(unit))
		if err := writeSystemdInstallResult(options.ResultSHA256Path, hex.EncodeToString(unitDigest[:])); err != nil {
			return unitPath, fmt.Errorf("write installed systemd unit fingerprint: %w", err)
		}
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

func writeSystemdInstallResult(path, digest string) error {
	if !filepath.IsAbs(path) || len(digest) != sha256.Size*2 {
		return errors.New("systemd unit result path or fingerprint is invalid")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("systemd unit result fingerprint is invalid")
	}
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("inspect systemd unit result directory: %w", err)
	}
	if !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 || directory.Mode().Perm()&0o077 != 0 {
		return errors.New("systemd unit result directory must be a private real directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create systemd unit result file: %w", err)
	}
	if _, err := file.WriteString(digest + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write systemd unit result file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("sync systemd unit result file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close systemd unit result file: %w", err)
	}
	return nil
}

func renderSystemdUnit(serviceUser, primaryGroup, configPath, binaryPath, fileRoot string, supplementaryGroups []string) string {
	stateDir := filepath.Dir(configPath)
	groupLine := ""
	if len(supplementaryGroups) > 0 {
		groupLine = "SupplementaryGroups=" + strings.Join(supplementaryGroups, " ") + "\n"
	}
	fileRootLine := ""
	fileRootMarker := ""
	if fileRoot != "" {
		fileRootLine = "BindPaths=" + systemdQuoteUnitValue(fileRoot) + "\nEnvironment=NODEDANCE_AGENT_FILE_ROOT=" + systemdQuoteUnitValue(fileRoot) + "\n"
		fileRootMarker = "# NodeDanceFileRootBase64=" + base64.RawStdEncoding.EncodeToString([]byte(fileRoot)) + "\n"
	}
	return fmt.Sprintf(`# NodeDanceAgentUnit=1
[Unit]
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
	return writeSystemdUnitExpected(path, contents, "", "")
}

func writeSystemdUnitExpected(path, contents, expectedState, expectedSHA256 string) error {
	initialState, initialSHA256, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return err
	}
	if expectedState != "" && (initialState != expectedState || initialSHA256 != expectedSHA256) {
		return errors.New("systemd unit state changed since deployment preflight; refusing to replace it")
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
	currentState, currentSHA256, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return err
	}
	if currentState != initialState || currentSHA256 != initialSHA256 {
		return errors.New("systemd unit changed while preparing the update; refusing to replace it")
	}
	if currentState == "absent" {
		if err := os.Link(temporaryPath, path); err != nil {
			return fmt.Errorf("install new systemd unit without replacing a concurrent file: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return fmt.Errorf("remove temporary systemd unit: %w", err)
		}
	} else if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install systemd unit: %w", err)
	}
	return nil
}

func inspectNodeDanceSystemdUnit(path string) (string, string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("inspect systemd unit path: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("refusing to replace a non-regular systemd unit path")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read existing systemd unit: %w", err)
	}
	if !isNodeDanceSystemdUnit(string(contents)) {
		return "", "", errors.New("refusing to replace an existing systemd unit not marked as NodeDance-managed")
	}
	digest := sha256.Sum256(contents)
	return "managed", hex.EncodeToString(digest[:]), nil
}

func refuseSystemdUnitShadow(ctx context.Context, target string) error {
	command := exec.CommandContext(ctx, "systemctl", "show", "--property=FragmentPath", "--value", AgentUnitName)
	output, err := command.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("inspect effective systemd Agent unit: %w", ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("inspect effective systemd Agent unit: %w", err)
	}
	fragment := strings.TrimSpace(string(output))
	if fragment != "" {
		if filepath.Clean(fragment) != filepath.Clean(target) {
			return fmt.Errorf("refusing to shadow an existing Agent systemd unit at %s", fragment)
		}
		return nil
	}
	stateCommand := exec.CommandContext(ctx, "systemctl", "show", "--property=LoadState", "--value", AgentUnitName)
	stateOutput, stateErr := stateCommand.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("confirm absent systemd Agent unit: %w", ctx.Err())
	}
	if stateErr != nil {
		return fmt.Errorf("confirm absent systemd Agent unit: %w", stateErr)
	}
	if strings.TrimSpace(string(stateOutput)) != "not-found" {
		return errors.New("refusing to install without proving the effective Agent systemd unit is absent")
	}
	for _, directory := range []string{
		"/run/systemd/system",
		"/usr/local/lib/systemd/system",
		"/usr/lib/systemd/system",
		"/lib/systemd/system",
	} {
		path := filepath.Join(directory, AgentUnitName)
		if filepath.Clean(path) == filepath.Clean(target) {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to shadow an existing Agent systemd unit at %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect possible systemd Agent unit %s: %w", path, err)
		}
	}
	return nil
}

func isNodeDanceSystemdUnit(contents string) bool {
	hasMarker, hasUnit, hasService, hasDescription, hasSimpleType, hasExecStart := false, false, false, false, false, false
	hasNonRootUser, hasRootUser, hasRestart, hasInstallTarget := false, false, false, false
	hasInstallSection, hasNoNewPrivileges, hasProtectSystem, hasProtectHome := false, false, false, false
	hasPrivateTmp, hasReadWritePaths, hasUMask := false, false, false
	for _, rawLine := range strings.Split(contents, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case line == "# NodeDanceAgentUnit=1":
			hasMarker = true
		case line == "[Unit]":
			hasUnit = true
		case line == "[Service]":
			hasService = true
		case line == "[Install]":
			hasInstallSection = true
		case line == "Description=NodeDance Agent":
			hasDescription = true
		case line == "Type=simple":
			hasSimpleType = true
		case strings.HasPrefix(line, "ExecStart=/usr/bin/env -- ") && strings.Contains(line, "nodedance-agent") && strings.Contains(line, "run --config "):
			hasExecStart = true
		case strings.HasPrefix(line, "User=") && strings.TrimPrefix(line, "User=") != "root" && strings.TrimPrefix(line, "User=") != "":
			hasNonRootUser = true
		case line == "User=root":
			hasRootUser = true
		case line == "Restart=always":
			hasRestart = true
		case line == "WantedBy=multi-user.target":
			hasInstallTarget = true
		case line == "NoNewPrivileges=true":
			hasNoNewPrivileges = true
		case line == "ProtectSystem=strict":
			hasProtectSystem = true
		case line == "ProtectHome=tmpfs":
			hasProtectHome = true
		case line == "PrivateTmp=true":
			hasPrivateTmp = true
		case strings.HasPrefix(line, "ReadWritePaths="):
			hasReadWritePaths = true
		case line == "UMask=0077":
			hasUMask = true
		}
	}
	generatedShape := hasUnit && hasService && hasDescription && hasSimpleType && hasExecStart && hasNonRootUser && !hasRootUser && hasRestart &&
		hasInstallSection && hasInstallTarget && hasNoNewPrivileges && hasProtectSystem && hasProtectHome && hasPrivateTmp && hasReadWritePaths && hasUMask
	return hasMarker && generatedShape
}
