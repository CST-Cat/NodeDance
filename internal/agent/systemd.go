package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const AgentUnitName = "nodedance-agent.service"

const systemdStateConfigMaxBytes = 1 << 20

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
	commandRunner       func(context.Context, ...string) ([]byte, error)
}

// ValidateSystemdStatePath checks the Agent state directory and config without
// following any existing symlink. It performs no filesystem mutations, so an
// SSH installer can reject unsafe paths before creating the service account or
// replacing the installed binary.
func ValidateSystemdStatePath(configPath string) error {
	dirFD, missing, err := openSystemdStateDirectory(configPath, false)
	if err != nil || missing {
		return err
	}
	defer unix.Close(dirFD)

	configFD, exists, err := openSystemdConfig(dirFD, filepath.Base(configPath))
	if err != nil || !exists {
		return err
	}
	defer unix.Close(configFD)
	_, err = readSystemdConfig(configFD)
	return err
}

// PrepareSystemdStateForUser safely prepares the dedicated root Agent state
// path. Directory and file ownership/mode changes are applied through
// already-open, no-follow file descriptors.
//
// It returns true when a valid config already existed and was preserved.
func PrepareSystemdStateForUser(serviceUser, configPath string) (bool, error) {
	return prepareSystemdStateForUser(serviceUser, configPath, false)
}

// PrepareNewSystemdStateForUser safely creates the state directory only when
// its final path component is absent. It is used by fresh installs that must
// refuse to adopt an existing Agent state directory. It returns the device and
// inode observed through the opened directory descriptor, so callers can
// identify exactly the directory created even if the path is concurrently
// replaced.
func PrepareNewSystemdStateForUser(serviceUser, configPath string) (string, error) {
	uid, gid, err := systemdServiceUIDGID(serviceUser)
	if err != nil {
		return "", err
	}
	_, identity, err := prepareSystemdStateDetailed(configPath, uid, gid, true)
	return identity, err
}

func prepareSystemdStateForUser(serviceUser, configPath string, requireNew bool) (bool, error) {
	uid, gid, err := systemdServiceUIDGID(serviceUser)
	if err != nil {
		return false, err
	}
	return prepareSystemdStateWithRequirement(configPath, uid, gid, requireNew)
}

func systemdServiceUIDGID(serviceUser string) (int, int, error) {
	if os.Geteuid() != 0 {
		return 0, 0, errors.New("preparing systemd Agent state requires root privileges")
	}
	serviceUser = strings.TrimSpace(serviceUser)
	if serviceUser != "" && serviceUser != "root" {
		return 0, 0, errors.New("NodeDance Agent systemd service only supports the root account")
	}
	account, err := user.Lookup("root")
	if err != nil {
		return 0, 0, fmt.Errorf("lookup root systemd service account: %w", err)
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, errors.New("systemd service user has an invalid UID")
	}
	if err := validateSystemdServiceUID(account.Username, uid); err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid < 0 {
		return 0, 0, errors.New("systemd service user has an invalid primary GID")
	}
	return uid, gid, nil
}

func prepareSystemdState(configPath string, serviceUID, serviceGID int) (bool, error) {
	return prepareSystemdStateWithRequirement(configPath, serviceUID, serviceGID, false)
}

func prepareSystemdStateWithRequirement(configPath string, serviceUID, serviceGID int, requireNew bool) (bool, error) {
	exists, _, err := prepareSystemdStateDetailed(configPath, serviceUID, serviceGID, requireNew)
	return exists, err
}

func prepareSystemdStateDetailed(configPath string, serviceUID, serviceGID int, requireNew bool) (bool, string, error) {
	dirFD, missing, created, err := openSystemdStateDirectoryDetailed(configPath, true)
	if err != nil {
		return false, "", err
	}
	if missing {
		return false, "", errors.New("internal error: Agent state directory was not created")
	}
	defer unix.Close(dirFD)
	if requireNew && !created {
		return false, "", errors.New("Agent state directory already exists; refusing to adopt it")
	}

	configFD, exists, err := openSystemdConfig(dirFD, filepath.Base(configPath))
	if err != nil {
		return false, "", err
	}
	if exists {
		defer unix.Close(configFD)
		if _, err := readSystemdConfig(configFD); err != nil {
			return false, "", err
		}
	}
	if requireNew && exists {
		return false, "", errors.New("Agent config already exists; refusing to adopt it")
	}

	// Validate and open the config before touching either path's metadata. In
	// particular, a config symlink must not cause even the state directory to
	// be chmod/chowned as a side effect of a rejected installation.
	if err := setSystemdStateMetadata(dirFD, serviceUID, serviceGID, 0o700, "Agent state directory"); err != nil {
		return false, "", err
	}
	if exists {
		if err := setSystemdStateMetadata(configFD, serviceUID, serviceGID, 0o600, "Agent config"); err != nil {
			return false, "", err
		}
	}
	if !created {
		return exists, "", nil
	}
	var state unix.Stat_t
	if err := unix.Fstat(dirFD, &state); err != nil {
		return false, "", fmt.Errorf("inspect newly created Agent state directory: %w", err)
	}
	return exists, fmt.Sprintf("%d:%d", state.Dev, state.Ino), nil
}

func openSystemdStateDirectory(configPath string, createFinal bool) (int, bool, error) {
	fd, missing, _, err := openSystemdStateDirectoryDetailed(configPath, createFinal)
	return fd, missing, err
}

func openSystemdStateDirectoryDetailed(configPath string, createFinal bool) (int, bool, bool, error) {
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath || strings.ContainsRune(configPath, '\x00') {
		return -1, false, false, errors.New("systemd Agent config path must be a clean absolute path")
	}
	if filepath.Base(configPath) == "." || filepath.Base(configPath) == string(filepath.Separator) {
		return -1, false, false, errors.New("systemd Agent config path must name a file")
	}
	directory := filepath.Dir(configPath)
	if directory == string(filepath.Separator) {
		return -1, false, false, errors.New("systemd Agent config requires a dedicated state directory")
	}
	components := strings.Split(strings.TrimPrefix(directory, string(filepath.Separator)), string(filepath.Separator))
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, false, false, fmt.Errorf("open filesystem root: %w", err)
	}
	currentFD := rootFD
	created := false
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			if currentFD != rootFD {
				unix.Close(currentFD)
			}
			unix.Close(rootFD)
			return -1, false, false, errors.New("systemd Agent state path has an invalid component")
		}
		final := index == len(components)-1
		nextFD, openErr := unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) && final {
			if !createFinal {
				if currentFD == rootFD {
					unix.Close(rootFD)
				} else {
					unix.Close(currentFD)
					unix.Close(rootFD)
				}
				return -1, true, false, nil
			}
			if err := unix.Mkdirat(currentFD, component, 0o700); err == nil {
				created = true
			} else if !errors.Is(err, unix.EEXIST) {
				if currentFD != rootFD {
					unix.Close(currentFD)
				}
				unix.Close(rootFD)
				return -1, false, false, fmt.Errorf("create Agent state directory: %w", err)
			}
			nextFD, openErr = unix.Openat(currentFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			if currentFD != rootFD {
				unix.Close(currentFD)
			}
			unix.Close(rootFD)
			return -1, false, false, fmt.Errorf("open Agent state path component %q without following symlinks: %w", component, openErr)
		}
		if currentFD != rootFD {
			unix.Close(currentFD)
		}
		currentFD = nextFD
	}
	unix.Close(rootFD)
	return currentFD, false, created, nil
}

func openSystemdConfig(dirFD int, name string) (int, bool, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return -1, false, errors.New("systemd Agent config path must name a file")
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return -1, false, nil
	}
	if err != nil {
		return -1, false, fmt.Errorf("open Agent config without following symlinks: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, false, fmt.Errorf("inspect Agent config: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return -1, false, errors.New("Agent config must be a regular file")
	}
	if stat.Nlink != 1 {
		unix.Close(fd)
		return -1, false, errors.New("Agent config must not have hard links")
	}
	return fd, true, nil
}

func readSystemdConfig(fd int) (Config, error) {
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return Config{}, fmt.Errorf("rewind Agent config: %w", err)
	}
	data := make([]byte, 0, 4096)
	buffer := make([]byte, 4096)
	for {
		count, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("read Agent config: %w", err)
		}
		if count == 0 {
			break
		}
		data = append(data, buffer[:count]...)
		if len(data) > systemdStateConfigMaxBytes {
			return Config{}, errors.New("Agent config is too large")
		}
	}
	config, err := decodeConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("validate existing Agent config: %w", err)
	}
	return config, nil
}

func setSystemdStateMetadata(fd, uid, gid int, mode uint32, label string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if label == "Agent config" && stat.Nlink != 1 {
		return errors.New("Agent config must not have hard links")
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return fmt.Errorf("set %s owner: %w", label, err)
		}
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		return fmt.Errorf("set %s permissions: %w", label, err)
	}
	return nil
}

func InstallSystemd(ctx context.Context, options SystemdInstallOptions) (string, error) {
	if options.UnitDir == "" {
		options.UnitDir = "/etc/systemd/system"
	}
	options.UnitDir = filepath.Clean(options.UnitDir)
	unitPath := filepath.Join(options.UnitDir, AgentUnitName)
	initialUnitState, initialUnitDigest, err := inspectNodeDanceSystemdUnit(unitPath)
	if err != nil {
		return "", err
	}
	serviceName, err := resolveSystemdServiceName(options.User, unitPath)
	if err != nil {
		return "", err
	}
	serviceUser, err := user.Lookup(serviceName)
	if err != nil {
		return "", fmt.Errorf("lookup systemd service user %q: %w", serviceName, err)
	}
	if !validUnitIdentity(serviceUser.Username) {
		return "", errors.New("systemd service user has an unsupported account name")
	}
	uid, err := strconv.Atoi(serviceUser.Uid)
	if err != nil {
		return "", errors.New("systemd service user has an invalid UID")
	}
	if err := validateSystemdServiceUID(serviceUser.Username, uid); err != nil {
		return "", err
	}
	gid, err := strconv.Atoi(serviceUser.Gid)
	if err != nil || gid < 0 {
		return "", errors.New("systemd service user has an invalid primary GID")
	}
	if options.EnableNow && options.UnitDir != "/etc/systemd/system" && options.commandRunner == nil {
		return "", errors.New("enabling a systemd unit is available only for /etc/systemd/system")
	}
	if options.EnableNow {
		options.Reload = true
	}
	if options.Reload && options.UnitDir != "/etc/systemd/system" && options.commandRunner == nil {
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
	if options.UnitDir == "/etc/systemd/system" && os.Geteuid() != 0 {
		return "", errors.New("installing a system unit requires root privileges")
	}
	stateDir := filepath.Dir(options.ConfigPath)
	fileRootOptions := options
	fileRootOptions.User = serviceUser.Username
	fileRoot, err := resolveSystemdFileRoot(fileRootOptions, unitPath, stateDir)
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
	unit := renderSystemdUnit(options.ConfigPath, options.BinaryPath, fileRoot, options.SupplementaryGroups)
	if options.ExpectedUnitState != "" && (initialUnitState != options.ExpectedUnitState || initialUnitDigest != options.ExpectedUnitSHA256) {
		return "", errors.New("systemd unit state changed since deployment preflight; refusing to replace it")
	}
	unitBackup, err := captureSystemdUnitBackup(unitPath)
	if err != nil {
		return "", err
	}
	configBackup, err := captureAgentConfigBackup(options.ConfigPath, uid)
	if err != nil {
		return "", err
	}
	defer configBackup.close()
	var baselineIdentity assignedIdentity
	if options.EnableNow {
		baselineIdentity, err = lookupIdentityFromConfig(ctx, configBackup.config)
		if err != nil {
			return "", fmt.Errorf("verify Agent credential with Core before activation: %w", err)
		}
		if baselineIdentity.CredentialState != "active" || baselineIdentity.AgentID != configBackup.config.AgentID || baselineIdentity.NodeID != configBackup.config.NodeID {
			return "", errors.New("Agent credential does not match an active Core identity; refusing activation")
		}
	}
	var priorServiceState managedServiceState
	if options.EnableNow && initialUnitState == "managed" {
		priorServiceState, err = readManagedServiceState(ctx, options)
		if err != nil {
			return "", err
		}
	}
	if err := configBackup.prepare(uid, gid); err != nil {
		if restoreErr := configBackup.restore(); restoreErr != nil {
			return "", fmt.Errorf("prepare root Agent credentials: %w; rollback incomplete: %v", err, restoreErr)
		}
		return "", err
	}
	installedDigest := sha256.Sum256([]byte(unit))
	installedDigestHex := hex.EncodeToString(installedDigest[:])
	unitInstalled := false
	rollback := func(cause error) error {
		var rollbackErrors []string
		if restoreErr := configBackup.restore(); restoreErr != nil {
			rollbackErrors = append(rollbackErrors, restoreErr.Error())
		}
		if unitInstalled {
			unitOwned, inspectErr := systemdUnitMatchesDigest(unitPath, installedDigestHex)
			if inspectErr != nil {
				rollbackErrors = append(rollbackErrors, "verify installed Agent unit before rollback: "+inspectErr.Error())
			} else if !unitOwned {
				rollbackErrors = append(rollbackErrors, "Agent unit changed after this install wrote it; refusing to stop, disable, or replace the current service")
			} else {
				if options.EnableNow {
					stillOwned, verifyErr := systemdUnitMatchesDigest(unitPath, installedDigestHex)
					if verifyErr != nil {
						rollbackErrors = append(rollbackErrors, "verify Agent unit before stopping it: "+verifyErr.Error())
					} else if !stillOwned {
						rollbackErrors = append(rollbackErrors, "Agent unit changed before rollback stop; refusing to stop or disable the current service")
					} else if _, stopErr := runSystemdCommand(ctx, options, "disable", "--now", AgentUnitName); stopErr != nil {
						rollbackErrors = append(rollbackErrors, "stop new Agent unit: "+strings.TrimSpace(stopErr.Error()))
					}
				}
				stillOwned, verifyErr := systemdUnitMatchesDigest(unitPath, installedDigestHex)
				if verifyErr != nil {
					rollbackErrors = append(rollbackErrors, "verify Agent unit before restoring it: "+verifyErr.Error())
				} else if !stillOwned {
					rollbackErrors = append(rollbackErrors, "Agent unit changed during rollback; refusing to replace the current service")
				} else if restoreErr := restoreSystemdUnitBackup(unitPath, unitBackup, installedDigestHex); restoreErr != nil {
					rollbackErrors = append(rollbackErrors, restoreErr.Error())
				} else {
					if options.Reload {
						if _, reloadErr := runSystemdCommand(ctx, options, "daemon-reload"); reloadErr != nil {
							rollbackErrors = append(rollbackErrors, "reload restored Agent unit: "+strings.TrimSpace(reloadErr.Error()))
						}
					}
					if options.EnableNow && initialUnitState == "managed" {
						if stateErr := restoreManagedServiceState(ctx, options, priorServiceState); stateErr != nil {
							rollbackErrors = append(rollbackErrors, stateErr.Error())
						}
					}
				}
			}
		}
		if options.ResultSHA256Path != "" {
			_ = os.Remove(options.ResultSHA256Path)
		}
		if len(rollbackErrors) > 0 {
			return fmt.Errorf("%w; rollback incomplete: %s", cause, strings.Join(rollbackErrors, "; "))
		}
		return cause
	}
	installed, writeErr := writeSystemdUnitExpected(unitPath, unit, initialUnitState, initialUnitDigest)
	unitInstalled = installed
	if writeErr != nil {
		return "", rollback(writeErr)
	}
	if err := configBackup.pathStillNamesOriginal(); err != nil {
		return "", rollback(err)
	}
	if options.Reload {
		if _, err := runSystemdCommand(ctx, options, "daemon-reload"); err != nil {
			return unitPath, rollback(fmt.Errorf("systemctl daemon-reload failed: %s", strings.TrimSpace(err.Error())))
		}
	}
	if options.EnableNow {
		unitOwned, err := systemdUnitMatchesDigest(unitPath, installedDigestHex)
		if err != nil {
			return unitPath, rollback(fmt.Errorf("verify installed Agent unit before activation: %w", err))
		}
		if !unitOwned {
			return unitPath, rollback(errors.New("Agent systemd unit changed after installation; refusing to enable or start it"))
		}
		if _, err := runSystemdCommand(ctx, options, "enable", "--now", AgentUnitName); err != nil {
			return unitPath, rollback(fmt.Errorf("systemctl enable --now failed: %s", strings.TrimSpace(err.Error())))
		}
		if _, err := runSystemdCommand(ctx, options, "is-active", "--quiet", AgentUnitName); err != nil {
			return unitPath, rollback(errors.New("systemd Agent service did not remain active after enable/start"))
		}
		if err := waitForAgentCoreReconnect(ctx, configBackup.config, baselineIdentity.Generation); err != nil {
			return unitPath, rollback(fmt.Errorf("Agent service did not reconnect to Core: %w", err))
		}
		if _, err := runSystemdCommand(ctx, options, "is-active", "--quiet", AgentUnitName); err != nil {
			return unitPath, rollback(errors.New("systemd Agent service stopped before Core reconnect was confirmed"))
		}
	}
	if options.ResultSHA256Path != "" {
		if err := writeSystemdInstallResult(options.ResultSHA256Path, installedDigestHex); err != nil {
			return unitPath, rollback(fmt.Errorf("write installed systemd unit fingerprint: %w", err))
		}
	}
	if err := configBackup.pathStillNamesOriginal(); err != nil {
		return unitPath, rollback(err)
	}
	return unitPath, nil
}

type agentConfigBackup struct {
	dirFD      int
	configFD   int
	configName string
	dirStat    unix.Stat_t
	configStat unix.Stat_t
	contents   []byte
	config     Config
}

func captureAgentConfigBackup(configPath string, expectedUID int) (*agentConfigBackup, error) {
	dirFD, missing, err := openSystemdStateDirectory(configPath, false)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, errors.New("Agent state directory is missing")
	}
	configFD, exists, err := openSystemdConfig(dirFD, filepath.Base(configPath))
	if err != nil {
		unix.Close(dirFD)
		return nil, err
	}
	if !exists {
		unix.Close(dirFD)
		return nil, errors.New("Agent config is missing; enroll the Agent before installing its systemd unit")
	}
	backup := &agentConfigBackup{dirFD: dirFD, configFD: configFD, configName: filepath.Base(configPath)}
	if err := unix.Fstat(dirFD, &backup.dirStat); err != nil {
		backup.close()
		return nil, fmt.Errorf("inspect Agent state directory: %w", err)
	}
	if err := unix.Fstat(configFD, &backup.configStat); err != nil {
		backup.close()
		return nil, fmt.Errorf("inspect Agent config: %w", err)
	}
	if backup.dirStat.Mode&unix.S_IFMT != unix.S_IFDIR || int(backup.dirStat.Uid) != expectedUID || backup.dirStat.Mode&0o777 != 0o700 {
		backup.close()
		return nil, errors.New("Agent state directory owner or permissions do not match the root service identity")
	}
	if backup.configStat.Mode&unix.S_IFMT != unix.S_IFREG || backup.configStat.Nlink != 1 || int(backup.configStat.Uid) != expectedUID || backup.configStat.Mode&0o777 != 0o600 {
		backup.close()
		return nil, errors.New("Agent config owner or permissions do not match the root service identity")
	}
	backup.contents, backup.config, err = readSystemdConfigSnapshot(configFD)
	if err != nil {
		backup.close()
		return nil, err
	}
	if err := backup.pathStillNamesOriginal(); err != nil {
		backup.close()
		return nil, err
	}
	return backup, nil
}

func readSystemdConfigSnapshot(fd int) ([]byte, Config, error) {
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		return nil, Config{}, fmt.Errorf("rewind Agent config: %w", err)
	}
	data := make([]byte, 0, 4096)
	buffer := make([]byte, 4096)
	for {
		count, err := unix.Read(fd, buffer)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, Config{}, fmt.Errorf("read Agent config: %w", err)
		}
		if count == 0 {
			break
		}
		data = append(data, buffer[:count]...)
		if len(data) > systemdStateConfigMaxBytes {
			return nil, Config{}, errors.New("Agent config is too large")
		}
	}
	config, err := decodeConfig(data)
	if err != nil {
		return nil, Config{}, fmt.Errorf("validate existing Agent config: %w", err)
	}
	return data, config, nil
}

func (b *agentConfigBackup) prepare(uid, gid int) error {
	if err := b.pathStillNamesOriginal(); err != nil {
		return err
	}
	if err := setSystemdStateMetadata(b.dirFD, uid, gid, 0o700, "Agent state directory"); err != nil {
		return err
	}
	if err := setSystemdStateMetadata(b.configFD, uid, gid, 0o600, "Agent config"); err != nil {
		return err
	}
	return b.pathStillNamesOriginal()
}

func (b *agentConfigBackup) pathStillNamesOriginal() error {
	var current unix.Stat_t
	if err := unix.Fstatat(b.dirFD, b.configName, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("verify Agent config path identity: %w", err)
	}
	if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Dev != b.configStat.Dev || current.Ino != b.configStat.Ino {
		return errors.New("Agent config path changed during systemd installation")
	}
	return nil
}

func (b *agentConfigBackup) restore() error {
	if b == nil || b.configFD < 0 || b.dirFD < 0 {
		return nil
	}
	if err := b.pathStillNamesOriginal(); err != nil {
		return fmt.Errorf("cannot safely restore Agent config: %w", err)
	}
	if err := unix.Ftruncate(b.configFD, 0); err != nil {
		return fmt.Errorf("restore Agent config contents: %w", err)
	}
	for offset := 0; offset < len(b.contents); {
		written, err := unix.Pwrite(b.configFD, b.contents[offset:], int64(offset))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("restore Agent config contents: %w", err)
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		offset += written
	}
	if err := unix.Fchown(b.dirFD, int(b.dirStat.Uid), int(b.dirStat.Gid)); err != nil {
		return fmt.Errorf("restore Agent state directory owner: %w", err)
	}
	if err := unix.Fchmod(b.dirFD, b.dirStat.Mode&0o777); err != nil {
		return fmt.Errorf("restore Agent state directory permissions: %w", err)
	}
	if err := unix.Fchown(b.configFD, int(b.configStat.Uid), int(b.configStat.Gid)); err != nil {
		return fmt.Errorf("restore Agent config owner: %w", err)
	}
	if err := unix.Fchmod(b.configFD, b.configStat.Mode&0o777); err != nil {
		return fmt.Errorf("restore Agent config permissions: %w", err)
	}
	if err := unix.Fsync(b.configFD); err != nil {
		return fmt.Errorf("sync restored Agent config: %w", err)
	}
	return nil
}

func (b *agentConfigBackup) close() {
	if b == nil {
		return
	}
	if b.configFD >= 0 {
		_ = unix.Close(b.configFD)
		b.configFD = -1
	}
	if b.dirFD >= 0 {
		_ = unix.Close(b.dirFD)
		b.dirFD = -1
	}
}

type systemdUnitBackup struct {
	exists   bool
	contents []byte
	mode     os.FileMode
	uid      int
	gid      int
	digest   string
}

func captureSystemdUnitBackup(unitPath string) (systemdUnitBackup, error) {
	contents, info, absent, err := readSystemdUnitFile(unitPath)
	if absent {
		return systemdUnitBackup{}, nil
	}
	if err != nil {
		return systemdUnitBackup{}, fmt.Errorf("inspect existing systemd Agent unit: %w", err)
	}
	if !isNodeDanceSystemdUnit(string(contents)) {
		return systemdUnitBackup{}, errors.New("refusing to replace an existing systemd unit not marked as NodeDance-managed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return systemdUnitBackup{}, errors.New("existing systemd unit ownership is unavailable")
	}
	digest := sha256.Sum256(contents)
	return systemdUnitBackup{exists: true, contents: contents, mode: info.Mode().Perm(), uid: int(stat.Uid), gid: int(stat.Gid), digest: hex.EncodeToString(digest[:])}, nil
}

func restoreSystemdUnitBackup(unitPath string, backup systemdUnitBackup, installedDigest string) error {
	state, currentDigest, err := inspectNodeDanceSystemdUnit(unitPath)
	if err != nil {
		return err
	}
	if backup.exists && state == "managed" && currentDigest == backup.digest {
		_, info, absent, readErr := readSystemdUnitFile(unitPath)
		if readErr != nil || absent {
			return errors.New("cannot verify the previous systemd Agent unit metadata during rollback")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("cannot verify the previous systemd Agent unit owner during rollback")
		}
		if info.Mode().Perm() == backup.mode && int(stat.Uid) == backup.uid && int(stat.Gid) == backup.gid {
			return nil
		}
	}
	if !backup.exists && state == "absent" {
		return nil
	}
	if state != "managed" || currentDigest != installedDigest {
		return errors.New("systemd Agent unit changed during rollback; refusing to overwrite it")
	}
	if !backup.exists {
		if err := os.Remove(unitPath); err != nil {
			return fmt.Errorf("remove newly installed Agent unit during rollback: %w", err)
		}
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(unitPath), ".nodedance-agent-restore-*")
	if err != nil {
		return fmt.Errorf("create systemd Agent unit rollback file: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if _, err := file.Write(backup.contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("write systemd Agent unit rollback: %w", err)
	}
	if err := file.Chown(backup.uid, backup.gid); err != nil {
		_ = file.Close()
		return fmt.Errorf("restore systemd Agent unit owner: %w", err)
	}
	if err := file.Chmod(backup.mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("restore systemd Agent unit permissions: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync systemd Agent unit rollback: %w", err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	state, currentDigest, err = inspectNodeDanceSystemdUnit(unitPath)
	if err != nil || state != "managed" || currentDigest != installedDigest {
		return errors.New("systemd Agent unit changed during rollback; refusing to overwrite it")
	}
	if err := os.Rename(temporaryPath, unitPath); err != nil {
		return fmt.Errorf("restore previous systemd Agent unit: %w", err)
	}
	return nil
}

type managedServiceState struct {
	active  bool
	enabled bool
}

func runSystemdCommand(ctx context.Context, options SystemdInstallOptions, args ...string) ([]byte, error) {
	if options.commandRunner != nil {
		return options.commandRunner(ctx, args...)
	}
	command := exec.CommandContext(ctx, "systemctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return output, errors.New(message)
	}
	return output, nil
}

func readManagedServiceState(ctx context.Context, options SystemdInstallOptions) (managedServiceState, error) {
	activeOutput, activeErr := runSystemdCommand(ctx, options, "is-active", AgentUnitName)
	enabledOutput, enabledErr := runSystemdCommand(ctx, options, "is-enabled", AgentUnitName)
	activeState := strings.TrimSpace(string(activeOutput))
	enabledState := strings.TrimSpace(string(enabledOutput))
	if activeErr != nil && activeState != "inactive" || enabledErr != nil && enabledState != "disabled" {
		return managedServiceState{}, errors.New("cannot determine existing Agent systemd enable/active state safely")
	}
	if activeState != "active" && activeState != "inactive" || enabledState != "enabled" && enabledState != "disabled" {
		return managedServiceState{}, errors.New("existing Agent systemd state is not a restorable enabled/disabled and active/inactive state")
	}
	return managedServiceState{active: activeState == "active", enabled: enabledState == "enabled"}, nil
}

func restoreManagedServiceState(ctx context.Context, options SystemdInstallOptions, prior managedServiceState) error {
	var failures []string
	verb := "disable"
	if prior.enabled {
		verb = "enable"
	}
	if _, err := runSystemdCommand(ctx, options, verb, AgentUnitName); err != nil {
		failures = append(failures, "restore service enablement: "+err.Error())
	}
	verb = "stop"
	if prior.active {
		verb = "start"
	}
	if _, err := runSystemdCommand(ctx, options, verb, AgentUnitName); err != nil {
		failures = append(failures, "restore service activity: "+err.Error())
	}
	activeOutput, activeErr := runSystemdCommand(ctx, options, "is-active", AgentUnitName)
	enabledOutput, enabledErr := runSystemdCommand(ctx, options, "is-enabled", AgentUnitName)
	if activeErr != nil && strings.TrimSpace(string(activeOutput)) != "inactive" || enabledErr != nil && strings.TrimSpace(string(enabledOutput)) != "disabled" {
		failures = append(failures, "could not verify restored service state")
	} else if (strings.TrimSpace(string(activeOutput)) == "active") != prior.active || (strings.TrimSpace(string(enabledOutput)) == "enabled") != prior.enabled {
		failures = append(failures, "restored service state does not match the previous state")
	}
	if len(failures) != 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func waitForAgentCoreReconnect(ctx context.Context, config Config, previousGeneration uint64) error {
	checkCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client, err := newHTTPClient(config.CAFile)
	if err != nil {
		return fmt.Errorf("prepare Core identity check: %w", err)
	}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		identity, lookupErr := lookupIdentity(checkCtx, client, config, config.Credential)
		if lookupErr == nil && identity.CredentialState == "active" && identity.AgentID == config.AgentID && identity.NodeID == config.NodeID && identity.Status == "online" && identity.Generation > previousGeneration {
			return nil
		}
		if lookupErr != nil {
			lastErr = lookupErr
		} else {
			lastErr = errors.New("Core has not observed a new online Agent connection generation")
		}
		select {
		case <-checkCtx.Done():
			return fmt.Errorf("%w (last check: %v)", checkCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func lookupIdentityFromConfig(ctx context.Context, config Config) (assignedIdentity, error) {
	client, err := newHTTPClient(config.CAFile)
	if err != nil {
		return assignedIdentity{}, err
	}
	defer client.CloseIdleConnections()
	return lookupIdentity(ctx, client, config, config.Credential)
}

// UninstallSystemd stops and removes only a unit that still matches a
// NodeDance-managed root unit. It deliberately leaves the Agent
// binary, service account, credentials, and journal in place for recovery or
// later reinstall.
func UninstallSystemd(ctx context.Context, unitDir string) error {
	return uninstallSystemd(ctx, unitDir, nil, nil)
}

func uninstallSystemd(ctx context.Context, unitDir string, commandRunner func(context.Context, ...string) ([]byte, error), beforeDisable func() error) error {
	if unitDir == "" {
		unitDir = "/etc/systemd/system"
	}
	unitDir = filepath.Clean(unitDir)
	if unitDir == "/etc/systemd/system" && os.Geteuid() != 0 {
		return errors.New("uninstalling a system unit requires root privileges")
	}
	unitPath := filepath.Join(unitDir, AgentUnitName)
	if unitDir == "/etc/systemd/system" {
		if err := refuseSystemdUnitShadow(ctx, unitPath); err != nil {
			return err
		}
	}
	state, digest, err := inspectNodeDanceSystemdUnit(unitPath)
	if err != nil {
		return err
	}
	if state == "absent" {
		return nil
	}
	if state != "managed" {
		return errors.New("refusing to uninstall a systemd unit not marked as NodeDance-managed")
	}
	usesSystemd := unitDir == "/etc/systemd/system" || commandRunner != nil
	if usesSystemd {
		if beforeDisable != nil {
			if err := beforeDisable(); err != nil {
				return err
			}
		}
		stillManaged, currentDigest, verifyErr := inspectNodeDanceSystemdUnit(unitPath)
		if verifyErr != nil {
			return fmt.Errorf("verify NodeDance Agent unit before stopping it: %w", verifyErr)
		}
		if stillManaged != "managed" || currentDigest != digest {
			return errors.New("NodeDance systemd unit changed before uninstall; refusing to stop or disable it")
		}
		if _, err := runUninstallSystemdCommand(ctx, commandRunner, "disable", "--now", AgentUnitName); err != nil {
			return fmt.Errorf("systemctl disable --now failed: %s", strings.TrimSpace(err.Error()))
		}
	}
	if err := removeManagedSystemdUnit(unitPath, digest); err != nil {
		return err
	}
	if usesSystemd {
		if _, err := runUninstallSystemdCommand(ctx, commandRunner, "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl daemon-reload failed after removing NodeDance unit: %s", strings.TrimSpace(err.Error()))
		}
	}
	return nil
}

func runUninstallSystemdCommand(ctx context.Context, runner func(context.Context, ...string) ([]byte, error), args ...string) ([]byte, error) {
	if runner != nil {
		return runner(ctx, args...)
	}
	command := exec.CommandContext(ctx, "systemctl", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s", strings.TrimSpace(string(output)))
	}
	return output, nil
}

func removeManagedSystemdUnit(path, expectedSHA256 string) error {
	state, digest, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return err
	}
	if state != "managed" || expectedSHA256 == "" || digest != expectedSHA256 {
		return errors.New("NodeDance systemd unit changed before uninstall; refusing to remove it")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove NodeDance systemd unit: %w", err)
	}
	return nil
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

func renderSystemdUnit(configPath, binaryPath, fileRoot string, supplementaryGroups []string) string {
	groupLine := ""
	if len(supplementaryGroups) > 0 {
		groupLine = "SupplementaryGroups=" + strings.Join(supplementaryGroups, " ") + "\n"
	}
	fileRootLine := ""
	fileRootMarker := ""
	if fileRoot != "" {
		fileRootLine += "Environment=NODEDANCE_AGENT_FILE_ROOT=" + systemdQuoteUnitValue(fileRoot) + "\n"
		fileRootMarker = "# NodeDanceFileRootBase64=" + base64.RawStdEncoding.EncodeToString([]byte(fileRoot)) + "\n"
	} else {
		fileRootLine = "Environment=NODEDANCE_AGENT_FILE_ACCESS=disabled\n"
	}
	return fmt.Sprintf(`# NodeDanceAgentUnit=1
[Unit]
Description=NodeDance Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
Group=root
%s
%s%s
UMask=0077
ExecStart=/usr/bin/env -- %s run --config %s
Restart=always
RestartSec=3s

[Install]
WantedBy=multi-user.target
`, groupLine, fileRootLine, fileRootMarker, systemdQuote(binaryPath), systemdQuote(configPath))
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

func validateSystemdServiceUID(username string, uid int) error {
	if uid < 0 {
		return errors.New("systemd service user has an invalid UID")
	}
	if uid != 0 || username != "root" {
		return errors.New("NodeDance Agent systemd service only supports the literal root account")
	}
	return nil
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
		if filepath.Clean(options.FileRoot) == string(filepath.Separator) {
			return string(filepath.Separator), nil
		}
		return ValidateFileRoot(options.FileRoot, stateDir)
	}
	if configured := os.Getenv("NODEDANCE_AGENT_FILE_ROOT"); configured != "" {
		if filepath.Clean(configured) == string(filepath.Separator) {
			return string(filepath.Separator), nil
		}
		return ValidateFileRoot(configured, stateDir)
	}
	if previous, disabled, found, err := readPersistedSystemdFilePolicy(unitPath); err != nil {
		return "", err
	} else if disabled {
		return "", nil
	} else if found && previous != "" {
		if filepath.Clean(previous) == string(filepath.Separator) {
			return string(filepath.Separator), nil
		}
		return ValidateFileRoot(previous, stateDir)
	}
	return string(filepath.Separator), nil
}

func readPersistedSystemdFilePolicy(unitPath string) (root string, disabled, found bool, err error) {
	data, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("read existing Agent systemd unit: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "Environment=NODEDANCE_AGENT_FILE_ACCESS=disabled" {
			return "", true, true, nil
		}
		if !strings.HasPrefix(line, "# NodeDanceFileRootBase64=") {
			continue
		}
		encoded := strings.TrimPrefix(line, "# NodeDanceFileRootBase64=")
		decoded, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) == 0 {
			return "", false, false, errors.New("existing Agent systemd file-root marker is invalid; use --no-file-root to replace it")
		}
		return string(decoded), false, true, nil
	}
	return "", false, false, nil
}

func writeSystemdUnit(path, contents string) error {
	_, err := writeSystemdUnitExpected(path, contents, "", "")
	return err
}

func writeSystemdUnitExpected(path, contents, expectedState, expectedSHA256 string) (bool, error) {
	initialState, initialSHA256, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return false, err
	}
	if expectedState != "" && (initialState != expectedState || initialSHA256 != expectedSHA256) {
		return false, errors.New("systemd unit state changed since deployment preflight; refusing to replace it")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".nodedance-agent-unit-*")
	if err != nil {
		return false, fmt.Errorf("create temporary systemd unit: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.WriteString(contents); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("write systemd unit: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, fmt.Errorf("sync systemd unit: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	currentState, currentSHA256, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return false, err
	}
	if currentState != initialState || currentSHA256 != initialSHA256 {
		return false, errors.New("systemd unit changed while preparing the update; refusing to replace it")
	}
	if currentState == "absent" {
		if err := os.Link(temporaryPath, path); err != nil {
			return false, fmt.Errorf("install new systemd unit without replacing a concurrent file: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return true, fmt.Errorf("remove temporary systemd unit: %w", err)
		}
	} else if err := os.Rename(temporaryPath, path); err != nil {
		return false, fmt.Errorf("install systemd unit: %w", err)
	}
	return true, nil
}

func systemdUnitMatchesDigest(path, expectedDigest string) (bool, error) {
	state, digest, err := inspectNodeDanceSystemdUnit(path)
	if err != nil {
		return false, err
	}
	return state == "managed" && digest == expectedDigest, nil
}

func inspectNodeDanceSystemdUnit(path string) (string, string, error) {
	contents, _, absent, err := readSystemdUnitFile(path)
	if absent {
		return "absent", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("inspect systemd unit path: %w", err)
	}
	if !isNodeDanceSystemdUnit(string(contents)) {
		return "", "", errors.New("refusing to replace an existing systemd unit not marked as NodeDance-managed")
	}
	digest := sha256.Sum256(contents)
	return "managed", hex.EncodeToString(digest[:]), nil
}

func readSystemdUnitFile(path string) ([]byte, os.FileInfo, bool, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil, true, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, false, errors.New("systemd Agent unit must be a regular file")
	}
	contents, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, nil, false, fmt.Errorf("read systemd Agent unit: %w", err)
	}
	if len(contents) > 1<<20 {
		return nil, nil, false, errors.New("systemd Agent unit is too large")
	}
	return contents, info, false, nil
}

func resolveSystemdServiceName(requested, _ string) (string, error) {
	if requested = strings.TrimSpace(requested); requested != "" {
		if requested != "root" {
			return "", errors.New("NodeDance Agent systemd service only supports the root account")
		}
	}
	return "root", nil
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
	hasRootUser, hasRootGroup, hasRestart, hasInstallTarget := false, false, false, false
	hasInstallSection, hasUMask := false, false
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
		case line == "User=root":
			hasRootUser = true
		case line == "Group=root":
			hasRootGroup = true
		case line == "Restart=always":
			hasRestart = true
		case line == "WantedBy=multi-user.target":
			hasInstallTarget = true
		case line == "UMask=0077":
			hasUMask = true
		}
	}
	generatedShape := hasUnit && hasService && hasDescription && hasSimpleType && hasExecStart && hasRestart && hasInstallSection && hasInstallTarget && hasUMask && hasRootUser && hasRootGroup
	return hasMarker && generatedShape
}
