package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const (
	maxComposeCommandOutput = 8192
	composeStatePoll        = 500 * time.Millisecond
	composeValidationLimit  = 30 * time.Second
	composeOperationLimit   = 15 * time.Minute
)

var (
	ErrProjectUnavailable = errors.New("Compose project is no longer present in Docker inventory")
	ErrProjectConflict    = errors.New("Compose project name or config path is already in use")
	ErrCreateFailed       = errors.New("Compose project creation failed and was rolled back")
	ErrConfigUnsafe       = errors.New("Compose config file is outside the supported safe edit scope")
	ErrConfigChanged      = errors.New("Compose config file changed since it was opened")
	ErrConfigInvalid      = errors.New("Compose config is invalid")
	ErrDeployRejected     = errors.New("Compose deployment was rejected before changing services")
	ErrDeployPortConflict = fmt.Errorf("%w: host port conflicts with another project", ErrDeployRejected)
	ErrDeployImageMissing = fmt.Errorf("%w: image could not be prepared", ErrDeployRejected)
	ErrOperationUncertain = errors.New("Compose operation result could not be verified")
)

type deploymentResultError struct {
	state   string
	code    string
	unknown bool
}

func (e *deploymentResultError) Error() string { return "Compose deployment did not complete" }

type ProjectConfigSnapshot struct{ Files [][]byte }

// CreateProject stages a new config without replacing any existing path,
// validates it with Docker Compose, commits it atomically, and verifies the
// resulting project through Docker Engine. Rollback never removes volumes.
func (m *Manager) CreateProject(ctx context.Context, ref protocol.ComposeProjectRef, content []byte, digest string) ([]agentdocker.Container, error) {
	m.createMu.Lock()
	defer m.createMu.Unlock()
	if err := validateCreateProjectRef(ref); err != nil || len(content) == 0 || len(content) > protocol.MaxComposeFileBytes {
		return nil, ErrConfigUnsafe
	}
	actualDigest := sha256.Sum256(content)
	if hex.EncodeToString(actualDigest[:]) != digest {
		return nil, ErrConfigChanged
	}
	root, err := openSafeWorkingRoot(ref.WorkingDirectory)
	if err != nil {
		return nil, ErrConfigUnsafe
	}
	defer root.Close()
	if _, err := root.Lstat("compose.yaml"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return nil, ErrProjectConflict
	}
	if err := m.assertProjectNameAvailable(ctx, ref); err != nil {
		return nil, err
	}
	temporary, temporaryPath, err := writeNewComposeCandidate(root, ref.WorkingDirectory, content)
	if err != nil {
		return nil, ErrConfigUnsafe
	}
	defer root.Remove(temporary)
	_, err = m.runComposeOutput(ctx, ref, map[int]string{0: temporaryPath}, "config", "--quiet")
	if err != nil {
		return nil, ErrConfigInvalid
	}
	services, err := m.runComposeOutput(ctx, ref, map[int]string{0: temporaryPath}, "config", "--services")
	if err != nil || strings.TrimSpace(services) == "" {
		return nil, ErrConfigInvalid
	}
	if _, err := root.Lstat("compose.yaml"); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return nil, ErrProjectConflict
	}
	if err := root.Link(temporary, "compose.yaml"); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrProjectConflict
		}
		return nil, ErrConfigUnsafe
	}
	removeCreatedConfig := func() bool {
		if err := m.verifyConfigDigest(ref, 0, digest); err != nil {
			return false
		}
		if err := root.Remove("compose.yaml"); err != nil {
			return false
		}
		return syncComposeRoot(root) == nil
	}
	if err := syncComposeRoot(root); err != nil {
		if removeCreatedConfig() {
			return nil, ErrCreateFailed
		}
		return nil, ErrOperationUncertain
	}
	if err := m.verifyConfigDigest(ref, 0, digest); err != nil || m.runCompose(ctx, ref, nil, "config", "--quiet") != nil {
		if !removeCreatedConfig() {
			return nil, ErrOperationUncertain
		}
		return nil, ErrConfigInvalid
	}
	if err := m.assertProjectNameAvailable(ctx, ref); err != nil {
		if removeCreatedConfig() {
			return nil, err
		}
		return nil, ErrOperationUncertain
	}
	if err := m.runCompose(ctx, ref, nil, "up", "-d"); err != nil {
		if m.rollbackCreatedProject(ctx, ref, root, digest) {
			return nil, ErrCreateFailed
		}
		return nil, ErrOperationUncertain
	}
	instances, err := m.waitProjectInstances(ctx, ref)
	if err != nil || len(instances) == 0 {
		if m.rollbackCreatedProject(ctx, ref, root, digest) {
			return nil, ErrCreateFailed
		}
		return nil, ErrOperationUncertain
	}
	if err := m.verifyConfigDigest(ref, 0, digest); err != nil {
		return nil, ErrOperationUncertain
	}
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return nil, ErrOperationUncertain
	}
	return instances, nil
}

func validateCreateProjectRef(ref protocol.ComposeProjectRef) error {
	if protocol.ValidateComposeProjectRef(ref) != nil || len(ref.ConfigFiles) != 1 ||
		filepath.Clean(ref.WorkingDirectory) != ref.WorkingDirectory || ref.WorkingDirectory == string(filepath.Separator) ||
		ref.ConfigFiles[0] != filepath.Join(ref.WorkingDirectory, "compose.yaml") {
		return ErrConfigUnsafe
	}
	return nil
}

func (m *Manager) assertProjectNameAvailable(ctx context.Context, ref protocol.ComposeProjectRef) error {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	for _, id := range ids {
		container, err := m.engine.Inspect(ctx, id)
		if err != nil {
			return ErrProjectUnavailable
		}
		if container.Compose != nil && container.Compose.Project == ref.Name {
			return ErrProjectConflict
		}
	}
	return nil
}

func (m *Manager) assertProjectNameOwned(ctx context.Context, ref protocol.ComposeProjectRef) error {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	for _, id := range ids {
		container, err := m.engine.Inspect(ctx, id)
		if err != nil {
			return ErrProjectUnavailable
		}
		if container.Compose == nil || container.Compose.Project != ref.Name {
			continue
		}
		identity, err := reference(container.Compose)
		if err != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
	}
	return nil
}

func openSafeWorkingRoot(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || filepath.Clean(path) != path {
		return nil, ErrConfigUnsafe
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, ErrConfigUnsafe
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrConfigUnsafe
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrConfigUnsafe
	}
	openedInfo, openErr := root.Stat(".")
	currentInfo, statErr := os.Stat(path)
	if openErr != nil || statErr != nil || !os.SameFile(openedInfo, currentInfo) {
		root.Close()
		return nil, ErrConfigUnsafe
	}
	return root, nil
}

func writeNewComposeCandidate(root *os.Root, directory string, content []byte) (string, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", "", err
		}
		name := ".nodedance-compose-" + hex.EncodeToString(nonce[:]) + ".tmp"
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if _, err = file.Write(content); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = root.Remove(name)
			return "", "", err
		}
		return name, filepath.Join(directory, name), nil
	}
	return "", "", ErrConfigUnsafe
}

func syncComposeRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (m *Manager) rollbackCreatedProject(ctx context.Context, ref protocol.ComposeProjectRef, root *os.Root, digest string) bool {
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return false
	}
	instances, err := m.projectInstances(ctx, ref)
	if err != nil {
		return false
	}
	if len(instances) > 0 {
		for _, instance := range instances {
			if instance.Compose == nil || instance.Compose.Project != ref.Name {
				return false
			}
			identity, identityErr := reference(instance.Compose)
			if identityErr != nil || identity.Key != ref.Key {
				return false
			}
		}
		if err := m.runCompose(ctx, ref, nil, "down", "--remove-orphans"); err != nil {
			return false
		}
	}
	remaining, err := m.projectInstances(ctx, ref)
	if err != nil || len(remaining) != 0 || m.verifyConfigDigest(ref, 0, digest) != nil {
		return false
	}
	if err := root.Remove("compose.yaml"); err != nil {
		return false
	}
	return syncComposeRoot(root) == nil
}

type ConfigDocument struct {
	Text   string
	SHA256 string
	Path   string
}

func (m *Manager) CaptureProjectConfig(ctx context.Context, ref protocol.ComposeProjectRef) (ProjectConfigSnapshot, error) {
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return ProjectConfigSnapshot{}, err
	}
	snapshot := ProjectConfigSnapshot{Files: make([][]byte, len(ref.ConfigFiles))}
	total := 0
	for index := range ref.ConfigFiles {
		path, err := m.safeConfigPath(ref, index)
		if err != nil {
			return ProjectConfigSnapshot{}, err
		}
		content, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
		if err != nil {
			return ProjectConfigSnapshot{}, ErrConfigUnsafe
		}
		total += len(content)
		if total > 2<<20 {
			return ProjectConfigSnapshot{}, ErrConfigInvalid
		}
		snapshot.Files[index] = content
	}
	return snapshot, nil
}

// restoreProjectConfig replaces registered source files only when they still
// match the exact contents observed immediately before deployment. This avoids
// overwriting a concurrent operator edit while recovering an interrupted up.
func (m *Manager) restoreProjectConfig(ctx context.Context, ref protocol.ComposeProjectRef, prior, attempted ProjectConfigSnapshot) error {
	if len(prior.Files) != len(ref.ConfigFiles) || len(attempted.Files) != len(ref.ConfigFiles) || protocol.ValidateComposeProjectRef(ref) != nil {
		return ErrConfigUnsafe
	}
	for index := range ref.ConfigFiles {
		path, err := m.safeConfigPath(ref, index)
		if err != nil {
			return ErrConfigUnsafe
		}
		current, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
		if err != nil || !bytes.Equal(current, attempted.Files[index]) {
			return ErrConfigChanged
		}
	}
	type replacement struct {
		root *os.Root
		base string
		temp string
	}
	replacements := make([]replacement, 0, len(ref.ConfigFiles))
	cleanup := func() {
		for _, item := range replacements {
			if item.temp != "" {
				_ = item.root.Remove(item.temp)
			}
			_ = item.root.Close()
		}
	}
	for index, path := range ref.ConfigFiles {
		parentRoot, base, err := m.openComposeFileParent(ref, index)
		if err != nil {
			cleanup()
			return ErrConfigUnsafe
		}
		temp, err := writeComposeCandidateAt(parentRoot, base, prior.Files[index])
		if err != nil {
			_ = parentRoot.Close()
			cleanup()
			return ErrConfigUnsafe
		}
		replacements = append(replacements, replacement{root: parentRoot, base: filepath.Base(path), temp: temp})
	}
	for index := range replacements {
		item := &replacements[index]
		if err := item.root.Rename(item.temp, item.base); err != nil {
			cleanup()
			return ErrOperationUncertain
		}
		item.temp = ""
		directory, err := item.root.Open(".")
		if err != nil {
			cleanup()
			return ErrOperationUncertain
		}
		err = directory.Sync()
		_ = directory.Close()
		if err != nil {
			cleanup()
			return ErrOperationUncertain
		}
	}
	cleanup()
	verified, err := m.CaptureProjectConfig(ctx, ref)
	if err != nil || !sameConfigSnapshot(verified, prior) {
		return ErrOperationUncertain
	}
	return nil
}

func (m *Manager) openComposeFileParent(ref protocol.ComposeProjectRef, index int) (*os.Root, string, error) {
	if _, err := m.safeConfigPath(ref, index); err != nil {
		return nil, "", err
	}
	root, err := openSafeWorkingRoot(ref.WorkingDirectory)
	if err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(ref.WorkingDirectory, ref.ConfigFiles[index])
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		_ = root.Close()
		return nil, "", ErrConfigUnsafe
	}
	parent := filepath.Dir(relative)
	if parent == "." {
		return root, filepath.Base(relative), nil
	}
	child, err := root.OpenRoot(parent)
	_ = root.Close()
	if err != nil {
		return nil, "", ErrConfigUnsafe
	}
	return child, filepath.Base(relative), nil
}

func writeComposeCandidateAt(root *os.Root, original string, content []byte) (string, error) {
	info, err := root.Lstat(original)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrConfigUnsafe
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	name := ".nodedance-compose-restore-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, info.Mode().Perm())
	if err != nil {
		return "", err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && (stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid())) {
		if err := root.Chown(name, int(stat.Uid), int(stat.Gid)); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return "", err
		}
	}
	if _, err := file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(name)
		return "", err
	}
	return name, nil
}

func sameConfigSnapshot(left, right ProjectConfigSnapshot) bool {
	if len(left.Files) != len(right.Files) {
		return false
	}
	for index := range left.Files {
		if !bytes.Equal(left.Files[index], right.Files[index]) {
			return false
		}
	}
	return true
}

func (m *Manager) ReadConfig(ctx context.Context, ref protocol.ComposeProjectRef, index int) (ConfigDocument, error) {
	path, err := m.safeConfigPath(ref, index)
	if err != nil {
		return ConfigDocument{}, err
	}
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return ConfigDocument{}, err
	}
	content, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
	if err != nil {
		return ConfigDocument{}, err
	}
	digest := sha256.Sum256(content)
	return ConfigDocument{Text: string(content), SHA256: hex.EncodeToString(digest[:]), Path: path}, nil
}

func (m *Manager) ValidateConfig(ctx context.Context, ref protocol.ComposeProjectRef, index int, content string) error {
	if len(content) > protocol.MaxComposeFileBytes {
		return ErrConfigInvalid
	}
	path, err := m.safeConfigPath(ref, index)
	if err != nil {
		return err
	}
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return err
	}
	temporary, err := writeComposeCandidate(path, []byte(content))
	if err != nil {
		return ErrConfigInvalid
	}
	defer os.Remove(temporary)
	if err := m.runCompose(ctx, ref, map[int]string{index: temporary}, "config", "--quiet"); err != nil {
		return ErrConfigInvalid
	}
	return nil
}

// SaveConfig validates a same-directory candidate before an atomic replacement.
// committed is true once rename has replaced the original file, even if a
// later durability check fails and the task must be marked unknown.
func (m *Manager) SaveConfig(ctx context.Context, ref protocol.ComposeProjectRef, index int, baseSHA256, content string) (committed bool, err error) {
	if len(content) > protocol.MaxComposeFileBytes {
		return false, ErrConfigInvalid
	}
	path, err := m.safeConfigPath(ref, index)
	if err != nil {
		return false, err
	}
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return false, err
	}
	old, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
	if err != nil {
		return false, ErrConfigUnsafe
	}
	oldHash := sha256.Sum256(old)
	if hex.EncodeToString(oldHash[:]) != baseSHA256 {
		return false, ErrConfigChanged
	}
	temporary, err := writeComposeCandidate(path, []byte(content))
	if err != nil {
		return false, ErrConfigInvalid
	}
	defer os.Remove(temporary)
	if err := m.runCompose(ctx, ref, map[int]string{index: temporary}, "config", "--quiet"); err != nil {
		return false, ErrConfigInvalid
	}
	// Compose validation may take time. Re-read the target immediately before
	// replacement so an editor or external process cannot be silently clobbered
	// during that interval.
	latest, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
	if err != nil {
		return false, ErrConfigUnsafe
	}
	latestHash := sha256.Sum256(latest)
	if latestHash != oldHash {
		return false, ErrConfigChanged
	}
	if err := os.Rename(temporary, path); err != nil {
		return false, ErrConfigUnsafe
	}
	committed = true
	restoreOriginal := func() bool {
		restoreTemp, restoreErr := writeComposeCandidate(path, old)
		if restoreErr != nil {
			return false
		}
		defer os.Remove(restoreTemp)
		if restoreErr = os.Rename(restoreTemp, path); restoreErr != nil {
			return false
		}
		parent, openErr := os.Open(filepath.Dir(path))
		if openErr != nil {
			return false
		}
		defer parent.Close()
		return parent.Sync() == nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		if restoreOriginal() {
			return false, ErrConfigUnsafe
		}
		return committed, ErrOperationUncertain
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		if restoreOriginal() {
			return false, ErrConfigUnsafe
		}
		return committed, ErrOperationUncertain
	}
	verified, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
	if err != nil || !bytes.Equal(verified, []byte(content)) {
		if restoreOriginal() {
			return false, ErrConfigUnsafe
		}
		return committed, ErrOperationUncertain
	}
	return committed, nil
}

func (m *Manager) ExecuteProject(ctx context.Context, ref protocol.ComposeProjectRef, action protocol.TaskAction) (string, error) {
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return "unknown", err
	}
	if err := m.runCompose(ctx, ref, nil, "config", "--quiet"); err != nil {
		return "unchanged", ErrConfigInvalid
	}
	before, err := m.projectInstances(ctx, ref)
	if err != nil {
		return "unknown", ErrProjectUnavailable
	}
	var args []string
	var expected string
	switch action {
	case protocol.TaskComposeStart:
		args, expected = []string{"up", "-d"}, "running"
	case protocol.TaskComposeStop:
		if len(before) == 0 {
			return "stopped", nil
		}
		args, expected = []string{"stop"}, "stopped"
	case protocol.TaskComposeRestart:
		if len(before) == 0 {
			args = []string{"up", "-d"}
		} else {
			args = []string{"restart"}
		}
		expected = "running"
	case protocol.TaskComposeDeploy:
		return "", ErrConfigInvalid
	default:
		return "unchanged", ErrConfigInvalid
	}
	commandErr := m.runCompose(ctx, ref, nil, args...)
	if commandErr != nil {
		// A failed Compose command can leave some old services running while the
		// requested deployment never took effect. Never infer success from that
		// stale postcondition alone.
		return "unknown", ErrOperationUncertain
	}
	after, err := m.waitProjectState(ctx, ref, expected)
	if err != nil {
		return "unknown", ErrOperationUncertain
	}
	if action == protocol.TaskComposeDeploy && !allInstancesReplaced(before, after) {
		return "unknown", ErrOperationUncertain
	}
	if action == protocol.TaskComposeRestart && !allInstancesRestarted(before, after) {
		return "unknown", ErrOperationUncertain
	}
	return expected, nil
}

func (m *Manager) DeployProject(ctx context.Context, ref protocol.ComposeProjectRef, baseline ProjectConfigSnapshot) (string, error) {
	if err := m.requireLiveProject(ctx, ref); err != nil {
		return "unknown", err
	}
	before, err := m.projectInstances(ctx, ref)
	if err != nil {
		return "unknown", ErrProjectUnavailable
	}
	attempted, err := m.CaptureProjectConfig(ctx, ref)
	if err != nil || len(baseline.Files) != len(ref.ConfigFiles) {
		return "unknown", ErrConfigUnsafe
	}
	if err := m.preflightDeployment(ctx, ref); err != nil {
		code := "deployment_rejected"
		if errors.Is(err, ErrConfigInvalid) {
			code = "config_invalid"
		} else if errors.Is(err, ErrDeployPortConflict) {
			code = "port_conflict"
		} else if errors.Is(err, ErrDeployImageMissing) {
			code = "image_unavailable"
		} else if errors.Is(err, ErrDeployRejected) {
			code = "deployment_preflight_failed"
		}
		return m.recoverDeployment(ctx, ref, baseline, attempted, before, code, false, time.Time{})
	}
	started := time.Now()
	commandErr := m.runCompose(ctx, ref, nil, "up", "-d", "--force-recreate")
	var after []agentdocker.Container
	if commandErr == nil {
		after, err = m.waitProjectState(ctx, ref, "running")
	}
	if commandErr == nil && err == nil && len(before) > 0 && !allInstancesReplaced(before, after) {
		err = ErrOperationUncertain
	}
	if commandErr == nil && err == nil {
		current, captureErr := m.CaptureProjectConfig(ctx, ref)
		if captureErr != nil || !sameConfigSnapshot(current, attempted) {
			err = ErrConfigChanged
		}
	}
	if commandErr != nil || err != nil {
		code := "deployment_failed"
		if errors.Is(err, ErrConfigChanged) {
			code = "config_changed_during_deploy"
		}
		return m.recoverDeployment(ctx, ref, baseline, attempted, before, code, true, started)
	}
	return "running", nil
}

func (m *Manager) recoverDeployment(ctx context.Context, ref protocol.ComposeProjectRef, baseline, attempted ProjectConfigSnapshot, before []agentdocker.Container, failureCode string, mutationAttempted bool, started time.Time) (string, error) {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), composeOperationLimit)
	defer cancel()
	ctx = recoveryCtx
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "project_identity_conflict", unknown: true}
	}
	if err := m.restoreProjectConfig(ctx, ref, baseline, attempted); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "config_restore_failed", unknown: true}
	}
	if err := m.runCompose(ctx, ref, nil, "config", "--quiet"); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "saved_config_invalid", unknown: true}
	}
	if len(before) == 0 {
		if mutationAttempted {
			if err := m.removeCreatedProjectInstances(ctx, ref, before, started); err != nil {
				return "unknown", &deploymentResultError{state: "recovery_required", code: "unexpected_project_containers", unknown: true}
			}
		} else {
			remaining, err := m.projectInstances(ctx, ref)
			if err != nil || len(remaining) != 0 {
				return "unknown", &deploymentResultError{state: "recovery_required", code: "unexpected_project_containers", unknown: true}
			}
		}
		return "restored_offline", &deploymentResultError{state: "restored_offline", code: failureCode}
	}
	if !mutationAttempted {
		current, err := m.projectInstances(ctx, ref)
		if err != nil || !sameProjectContainerIDs(before, current) || !recoveredProjectMatches(before, current) {
			return "unknown", &deploymentResultError{state: "recovery_required", code: "service_verification_failed", unknown: true}
		}
		return "recovered", &deploymentResultError{state: "recovered", code: failureCode}
	}
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "project_identity_conflict", unknown: true}
	}
	// A partial `up` can leave task-created containers occupying ports needed by
	// the baseline. Remove only instances whose exact Compose ref, service slot,
	// and creation time prove they belong to this deployment before recreating
	// the baseline services.
	if err := m.removeTaskCreatedProjectInstances(ctx, ref, before, started); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "unexpected_project_containers", unknown: true}
	}
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "project_identity_conflict", unknown: true}
	}
	args := []string{"up", "-d", "--force-recreate"}
	for service, count := range serviceInstanceCounts(before) {
		args = append(args, "--scale", service+"="+strconv.Itoa(count))
	}
	if err := m.runCompose(ctx, ref, nil, args...); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "service_recovery_failed", unknown: true}
	}
	if err := m.restoreContainerStates(ctx, ref, before); err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "service_state_recovery_failed", unknown: true}
	}
	recovered, err := m.waitRecoveredProject(ctx, ref, before)
	if err != nil {
		return "unknown", &deploymentResultError{state: "recovery_required", code: "service_verification_failed", unknown: true}
	}
	if allInstancesReplaced(before, recovered) {
		return "recreated", &deploymentResultError{state: "recreated", code: failureCode}
	}
	return "recovered", &deploymentResultError{state: "recovered", code: failureCode}
}

func (m *Manager) removeCreatedProjectInstances(ctx context.Context, ref protocol.ComposeProjectRef, before []agentdocker.Container, started time.Time) error {
	if started.IsZero() || m.assertProjectNameOwned(ctx, ref) != nil {
		return ErrOperationUncertain
	}
	oldIDs := make(map[string]struct{}, len(before))
	for _, container := range before {
		oldIDs[container.ID] = struct{}{}
	}
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	created := make([]agentdocker.Container, 0)
	for _, id := range ids {
		container, inspectErr := m.engine.Inspect(ctx, id)
		if inspectErr != nil {
			return ErrProjectUnavailable
		}
		if container.Compose == nil || container.Compose.Project != ref.Name {
			continue
		}
		identity, identityErr := reference(container.Compose)
		if identityErr != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
		if _, existed := oldIDs[container.ID]; existed {
			continue
		}
		if container.CreatedAt == nil || container.CreatedAt.Before(started.Add(-5*time.Second)) {
			return ErrOperationUncertain
		}
		created = append(created, container)
	}
	for _, candidate := range created {
		current, inspectErr := m.engine.Inspect(ctx, candidate.ID)
		if inspectErr != nil || current.ID != candidate.ID || current.Compose == nil || current.Compose.Project != ref.Name ||
			current.CreatedAt == nil || current.CreatedAt.Before(started.Add(-5*time.Second)) {
			return ErrOperationUncertain
		}
		identity, identityErr := reference(current.Compose)
		if identityErr != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
		if err := m.runDocker(ctx, "container", "rm", "--force", "--", current.ID); err != nil {
			return err
		}
	}
	remaining, err := m.projectInstances(ctx, ref)
	if err != nil || len(remaining) != 0 {
		return ErrOperationUncertain
	}
	return nil
}

func (m *Manager) removeTaskCreatedProjectInstances(ctx context.Context, ref protocol.ComposeProjectRef, before []agentdocker.Container, started time.Time) error {
	if started.IsZero() || m.assertProjectNameOwned(ctx, ref) != nil {
		return ErrOperationUncertain
	}
	baselineIDs := make(map[string]string, len(before))
	baselineSlots := make(map[string]string, len(before))
	for _, container := range before {
		if container.ID == "" || container.Compose == nil || container.Compose.Service == "" || container.Compose.ContainerNumber == "" {
			return ErrProjectConflict
		}
		identity, identityErr := reference(container.Compose)
		if identityErr != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		if _, duplicate := baselineSlots[key]; duplicate {
			return ErrProjectConflict
		}
		if _, duplicate := baselineIDs[container.ID]; duplicate {
			return ErrProjectConflict
		}
		baselineIDs[container.ID] = key
		baselineSlots[key] = container.ID
	}
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	candidates := make([]agentdocker.Container, 0)
	for _, id := range ids {
		container, inspectErr := m.engine.Inspect(ctx, id)
		if inspectErr != nil {
			return ErrProjectUnavailable
		}
		if container.Compose == nil || container.Compose.Project != ref.Name {
			continue
		}
		identity, identityErr := reference(container.Compose)
		if identityErr != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
		if container.Compose.Service == "" || container.Compose.ContainerNumber == "" {
			return ErrProjectConflict
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		if baselineKey, baselineID := baselineIDs[container.ID]; baselineID {
			if baselineKey != key {
				return ErrProjectConflict
			}
			continue
		}
		if container.CreatedAt == nil || container.CreatedAt.Before(started.Add(-5*time.Second)) {
			return ErrOperationUncertain
		}
		candidates = append(candidates, container)
	}
	for _, candidate := range candidates {
		current, inspectErr := m.engine.Inspect(ctx, candidate.ID)
		if inspectErr != nil || current.ID != candidate.ID || current.Compose == nil || current.Compose.Project != ref.Name ||
			current.Compose.Service != candidate.Compose.Service || current.Compose.ContainerNumber != candidate.Compose.ContainerNumber ||
			current.CreatedAt == nil || current.CreatedAt.Before(started.Add(-5*time.Second)) ||
			candidate.CreatedAt == nil || !current.CreatedAt.Equal(*candidate.CreatedAt) {
			return ErrOperationUncertain
		}
		identity, identityErr := reference(current.Compose)
		if identityErr != nil || identity.Key != ref.Key {
			return ErrProjectConflict
		}
		if _, baselineID := baselineIDs[current.ID]; baselineID {
			return ErrProjectConflict
		}
		if err := m.runDocker(ctx, "container", "rm", "--force", "--", current.ID); err != nil {
			return err
		}
	}
	remaining, err := m.projectInstances(ctx, ref)
	if err != nil {
		return ErrProjectUnavailable
	}
	for _, container := range remaining {
		if _, isBaseline := baselineIDs[container.ID]; !isBaseline {
			return ErrOperationUncertain
		}
	}
	return nil
}

func serviceInstanceCounts(containers []agentdocker.Container) map[string]int {
	counts := make(map[string]int)
	for _, container := range containers {
		if container.Compose != nil && container.Compose.Service != "" {
			counts[container.Compose.Service]++
		}
	}
	return counts
}

func (m *Manager) restoreContainerStates(ctx context.Context, ref protocol.ComposeProjectRef, before []agentdocker.Container) error {
	current, err := m.projectInstances(ctx, ref)
	if err != nil {
		return ErrProjectUnavailable
	}
	byServiceNumber := make(map[string]agentdocker.Container, len(current))
	for _, container := range current {
		if container.Compose == nil {
			return ErrProjectConflict
		}
		byServiceNumber[container.Compose.Service+"\x00"+container.Compose.ContainerNumber] = container
	}
	for _, previous := range before {
		if previous.Compose == nil {
			return ErrProjectConflict
		}
		current, ok := byServiceNumber[previous.Compose.Service+"\x00"+previous.Compose.ContainerNumber]
		if !ok {
			return ErrOperationUncertain
		}
		if previous.Paused {
			if !current.Paused && m.runDocker(ctx, "pause", current.ID) != nil {
				return ErrOperationUncertain
			}
		} else if !previous.Running {
			if current.Running && m.runDocker(ctx, "stop", current.ID) != nil {
				return ErrOperationUncertain
			}
		}
	}
	return nil
}

type composeDeployConfig struct {
	Services map[string]struct {
		Image string          `json:"image"`
		Build json.RawMessage `json:"build"`
		Ports []struct {
			HostIP    string          `json:"host_ip"`
			Published json.RawMessage `json:"published"`
			Protocol  string          `json:"protocol"`
		} `json:"ports"`
	} `json:"services"`
}

type requestedHostPort struct {
	ip       string
	protocol string
	first    uint16
	last     uint16
}

func (m *Manager) preflightDeployment(ctx context.Context, ref protocol.ComposeProjectRef) error {
	output, err := m.runComposeOutput(ctx, ref, nil, "config", "--format", "json")
	if err != nil {
		return ErrConfigInvalid
	}
	var config composeDeployConfig
	if json.Unmarshal([]byte(output), &config) != nil || len(config.Services) == 0 {
		return ErrConfigInvalid
	}
	ports := make([]requestedHostPort, 0)
	for _, service := range config.Services {
		if service.Image != "" && len(service.Build) == 0 && string(service.Build) != "null" && !m.imageAvailable(ctx, service.Image) {
			return ErrDeployImageMissing
		}
		for _, port := range service.Ports {
			published, ok := configPublishedPort(port.Published)
			if !ok {
				continue
			}
			first, last, valid := parsePublishedRange(published)
			if !valid {
				return ErrConfigInvalid
			}
			protocolName := strings.ToLower(port.Protocol)
			if protocolName == "" {
				protocolName = "tcp"
			}
			ports = append(ports, requestedHostPort{ip: port.HostIP, protocol: protocolName, first: first, last: last})
		}
	}
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	for _, id := range ids {
		container, err := m.engine.Inspect(ctx, id)
		if err != nil {
			return ErrProjectUnavailable
		}
		if !container.Running || container.Paused || container.Restarting || container.Compose != nil && container.Compose.Project == ref.Name {
			continue
		}
		for _, desired := range ports {
			for _, port := range container.Ports {
				if strings.ToLower(port.Protocol) != desired.protocol {
					continue
				}
				for _, binding := range port.Published {
					actual, parseErr := strconv.ParseUint(binding.Port, 10, 16)
					if parseErr == nil && uint16(actual) >= desired.first && uint16(actual) <= desired.last && hostAddressesConflict(desired.ip, binding.IP) {
						return ErrDeployPortConflict
					}
				}
			}
		}
	}
	return nil
}

func (m *Manager) imageAvailable(ctx context.Context, image string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, composeValidationLimit)
	defer cancel()
	check := osexec.CommandContext(checkCtx, m.dockerPath, "image", "inspect", image)
	check.Env = os.Environ()
	check.Stdout, check.Stderr = io.Discard, io.Discard
	if check.Run() == nil {
		return true
	}
	pullCtx, pullCancel := context.WithTimeout(ctx, composeOperationLimit)
	defer pullCancel()
	pull := osexec.CommandContext(pullCtx, m.dockerPath, "image", "pull", image)
	pull.Env = os.Environ()
	pull.Stdout, pull.Stderr = io.Discard, io.Discard
	return pull.Run() == nil
}

func configPublishedPort(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var text string
	if raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return "", false
		}
	} else {
		text = string(raw)
	}
	return text, strings.TrimSpace(text) != ""
}

func parsePublishedRange(value string) (uint16, uint16, bool) {
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return 0, 0, false
	}
	first, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil || first == 0 {
		return 0, 0, false
	}
	last := first
	if len(parts) == 2 {
		last, err = strconv.ParseUint(parts[1], 10, 16)
		if err != nil || last < first {
			return 0, 0, false
		}
	}
	return uint16(first), uint16(last), true
}

func hostAddressesConflict(left, right string) bool {
	parse := func(value string) (netip.Addr, bool) {
		if value == "" {
			return netip.Addr{}, true
		}
		address, err := netip.ParseAddr(value)
		if err != nil {
			return netip.Addr{}, false
		}
		return address, true
	}
	a, okA := parse(left)
	b, okB := parse(right)
	if !okA || !okB {
		return false
	}
	if a.IsValid() && a.IsUnspecified() || b.IsValid() && b.IsUnspecified() || !a.IsValid() || !b.IsValid() {
		return true
	}
	return a.Unmap() == b.Unmap()
}

func (m *Manager) runDocker(ctx context.Context, args ...string) error {
	if m.dockerPath == "" {
		return ErrProjectUnavailable
	}
	commandCtx, cancel := context.WithTimeout(ctx, composeOperationLimit)
	defer cancel()
	command := osexec.CommandContext(commandCtx, m.dockerPath, args...)
	command.Env = os.Environ()
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return ErrOperationUncertain
	}
	return nil
}

func (m *Manager) waitRecoveredProject(ctx context.Context, ref protocol.ComposeProjectRef, before []agentdocker.Container) ([]agentdocker.Container, error) {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(composeStatePoll)
	defer ticker.Stop()
	for {
		current, err := m.projectInstances(ctx, ref)
		if err == nil && recoveredProjectMatches(before, current) && m.assertProjectNameOwned(ctx, ref) == nil {
			return current, nil
		}
		select {
		case <-ctx.Done():
			return nil, ErrOperationUncertain
		case <-deadline.C:
			return nil, ErrOperationUncertain
		case <-ticker.C:
		}
	}
}

func recoveredProjectMatches(before, current []agentdocker.Container) bool {
	if len(before) != len(current) || len(before) == 0 {
		return false
	}
	previous := make(map[string]agentdocker.Container, len(before))
	for _, container := range before {
		if container.Compose == nil {
			return false
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		if _, exists := previous[key]; exists {
			return false
		}
		previous[key] = container
	}
	seen := make(map[string]struct{}, len(current))
	for _, container := range current {
		if container.Compose == nil {
			return false
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		old, ok := previous[key]
		if !ok {
			return false
		}
		switch {
		case old.Paused:
			if !container.Running || !container.Paused || container.Restarting {
				return false
			}
		case old.Running:
			if !container.Running || container.Paused || container.Restarting {
				return false
			}
		default:
			if container.Running || container.Paused || container.Restarting {
				return false
			}
		}
		if old.HealthcheckConfigured && (!container.HealthcheckConfigured || container.Health != old.Health) {
			return false
		}
		if !sameMounts(old.Mounts, container.Mounts) || !samePortContract(old.Ports, container.Ports) {
			return false
		}
		seen[key] = struct{}{}
	}
	return len(seen) == len(previous)
}

func sameProjectContainerIDs(before, current []agentdocker.Container) bool {
	if len(before) == 0 || len(before) != len(current) {
		return false
	}
	previous := make(map[string]string, len(before))
	for _, container := range before {
		if container.Compose == nil || container.Compose.Service == "" || container.Compose.ContainerNumber == "" {
			return false
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		if _, duplicate := previous[key]; duplicate {
			return false
		}
		previous[key] = container.ID
	}
	seen := make(map[string]struct{}, len(current))
	for _, container := range current {
		if container.Compose == nil || container.Compose.Service == "" || container.Compose.ContainerNumber == "" {
			return false
		}
		key := container.Compose.Service + "\x00" + container.Compose.ContainerNumber
		if container.ID != previous[key] {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
	}
	return len(seen) == len(previous)
}

func sameMounts(left, right []agentdocker.Mount) bool {
	key := func(mount agentdocker.Mount) string {
		return strings.Join([]string{mount.Type, mount.Name, mount.Source, mount.Destination, mount.Mode, strconv.FormatBool(mount.ReadWrite)}, "\x00")
	}
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, mount := range left {
		counts[key(mount)]++
	}
	for _, mount := range right {
		counts[key(mount)]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func samePortContract(left, right []agentdocker.Port) bool {
	if len(left) != len(right) {
		return false
	}
	key := func(port agentdocker.Port) string {
		return strconv.Itoa(int(port.ContainerPort)) + "/" + strings.ToLower(port.Protocol)
	}
	other := make(map[string]agentdocker.Port, len(right))
	for _, port := range right {
		other[key(port)] = port
	}
	for _, expected := range left {
		actual, ok := other[key(expected)]
		if !ok || len(expected.Configured) != len(actual.Configured) || len(expected.Published) != len(actual.Published) {
			return false
		}
		for _, configured := range expected.Configured {
			found := false
			for _, candidate := range actual.Configured {
				found = found || configured == candidate
			}
			if !found {
				return false
			}
		}
		for _, published := range expected.Published {
			found := false
			for _, candidate := range actual.Published {
				found = found || published == candidate
			}
			if !found {
				return false
			}
		}
	}
	return true
}

func (m *Manager) requireLiveProject(ctx context.Context, ref protocol.ComposeProjectRef) error {
	if protocol.ValidateComposeProjectRef(ref) != nil {
		return ErrProjectUnavailable
	}
	root, err := openSafeWorkingRoot(ref.WorkingDirectory)
	if err != nil {
		return ErrConfigUnsafe
	}
	defer root.Close()
	for index := range ref.ConfigFiles {
		if _, err := m.safeConfigPath(ref, index); err != nil {
			return ErrConfigUnsafe
		}
	}
	// Core supplies only a node-scoped project ref retrieved from its Compose
	// registry. Docker labels are useful inventory, but disappear after an
	// external `docker compose down`; the canonical registered source remains
	// sufficient to validate and recreate that project.
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return err
	}
	return nil
}

func (m *Manager) safeConfigPath(ref protocol.ComposeProjectRef, index int) (string, error) {
	if protocol.ValidateComposeProjectRef(ref) != nil || index < 0 || index >= len(ref.ConfigFiles) {
		return "", ErrConfigUnsafe
	}
	root, err := filepath.EvalSymlinks(ref.WorkingDirectory)
	if err != nil || root != filepath.Clean(ref.WorkingDirectory) {
		return "", ErrConfigUnsafe
	}
	path := filepath.Clean(ref.ConfigFiles[index])
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrConfigUnsafe
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrConfigUnsafe
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", ErrConfigUnsafe
	}
	return path, nil
}

func (m *Manager) runCompose(ctx context.Context, ref protocol.ComposeProjectRef, overrides map[int]string, operation ...string) error {
	_, err := m.runComposeOutput(ctx, ref, overrides, operation...)
	return err
}

func (m *Manager) runComposeOutput(ctx context.Context, ref protocol.ComposeProjectRef, overrides map[int]string, operation ...string) (string, error) {
	if m.dockerPath == "" || protocol.ValidateComposeProjectRef(ref) != nil {
		return "", ErrProjectUnavailable
	}
	args := []string{"compose", "--project-name", ref.Name, "--project-directory", ref.WorkingDirectory}
	for index, config := range ref.ConfigFiles {
		if replacement, ok := overrides[index]; ok {
			config = replacement
		}
		args = append(args, "--file", config)
	}
	args = append(args, operation...)
	timeout := composeOperationLimit
	if len(operation) > 0 && operation[0] == "config" {
		timeout = composeValidationLimit
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := osexec.CommandContext(commandCtx, m.dockerPath, args...)
	command.Env = os.Environ()
	output := boundedOutput{limit: maxComposeCommandOutput}
	if len(operation) >= 2 && operation[0] == "config" && operation[1] == "--format" {
		output.limit = 4 << 20
	}
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return output.buffer.String(), errors.New("Docker Compose command failed")
	}
	return output.buffer.String(), nil
}

func (m *Manager) waitProjectState(ctx context.Context, ref protocol.ComposeProjectRef, expected string) ([]agentdocker.Container, error) {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(composeStatePoll)
	defer ticker.Stop()
	for {
		containers, err := m.projectInstances(ctx, ref)
		if err == nil && len(containers) > 0 {
			allRunning, allStopped := true, true
			for _, container := range containers {
				allRunning = allRunning && container.Running && !container.Paused && !container.Restarting
				allStopped = allStopped && !container.Running && !container.Paused && !container.Restarting
			}
			if expected == "stopped" && allStopped || expected == "running" && allRunning {
				return containers, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ErrOperationUncertain
		case <-deadline.C:
			return nil, ErrOperationUncertain
		case <-ticker.C:
		}
	}
}

func (m *Manager) waitProjectInstances(ctx context.Context, ref protocol.ComposeProjectRef) ([]agentdocker.Container, error) {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(composeStatePoll)
	defer ticker.Stop()
	for {
		containers, err := m.projectInstances(ctx, ref)
		if err == nil && len(containers) > 0 {
			return containers, nil
		}
		select {
		case <-ctx.Done():
			return nil, ErrOperationUncertain
		case <-deadline.C:
			return nil, ErrOperationUncertain
		case <-ticker.C:
		}
	}
}

func allInstancesReplaced(before, after []agentdocker.Container) bool {
	old := make(map[string]struct{}, len(before))
	for _, container := range before {
		old[container.ID] = struct{}{}
	}
	for _, container := range after {
		if _, exists := old[container.ID]; exists {
			return false
		}
	}
	return true
}

func allInstancesRestarted(before, after []agentdocker.Container) bool {
	afterByID := make(map[string]agentdocker.Container, len(after))
	for _, container := range after {
		afterByID[container.ID] = container
	}
	for _, old := range before {
		current, exists := afterByID[old.ID]
		if !exists || current.StartedAt == nil || old.StartedAt != nil && old.StartedAt.Equal(*current.StartedAt) {
			return false
		}
	}
	return len(before) > 0
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, ErrConfigUnsafe
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		return nil, ErrConfigUnsafe
	}
	return content, nil
}

func writeComposeCandidate(originalPath string, content []byte) (string, error) {
	info, err := os.Stat(originalPath)
	if err != nil || !info.Mode().IsRegular() {
		return "", ErrConfigUnsafe
	}
	temporary, err := os.CreateTemp(filepath.Dir(originalPath), ".nodedance-compose-*.tmp")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	cleanup := func(err error) (string, error) { _ = temporary.Close(); _ = os.Remove(name); return "", err }
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return cleanup(err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) {
			if err := temporary.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				return cleanup(err)
			}
		}
	}
	if _, err := temporary.Write(content); err != nil {
		return cleanup(err)
	}
	if err := temporary.Sync(); err != nil {
		return cleanup(err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(value) < remaining {
			_, _ = output.buffer.Write(value)
		} else {
			_, _ = output.buffer.Write(value[:remaining])
		}
	}
	return len(value), nil
}

func (m *Manager) projectInstances(ctx context.Context, ref protocol.ComposeProjectRef) ([]agentdocker.Container, error) {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	var containers []agentdocker.Container
	for _, id := range ids {
		container, err := m.engine.Inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if container.Compose == nil || container.Compose.Project != ref.Name {
			continue
		}
		current, err := reference(container.Compose)
		if err == nil && current.Key == ref.Key {
			containers = append(containers, container)
		}
	}
	return containers, nil
}

func (m *Manager) verifyConfigDigest(ref protocol.ComposeProjectRef, index int, digest string) error {
	path, err := m.safeConfigPath(ref, index)
	if err != nil {
		return err
	}
	content, err := readBoundedFile(path, protocol.MaxComposeFileBytes)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(content)
	if hex.EncodeToString(actual[:]) != digest {
		return fmt.Errorf("%w", ErrConfigChanged)
	}
	return nil
}

func (m *Manager) verifyCreatedProject(ctx context.Context, ref protocol.ComposeProjectRef, digest string) ([]agentdocker.Container, error) {
	if err := validateCreateProjectRef(ref); err != nil {
		return nil, err
	}
	if err := m.verifyConfigDigest(ref, 0, digest); err != nil {
		return nil, err
	}
	instances, err := m.projectInstances(ctx, ref)
	if err != nil || len(instances) == 0 {
		return nil, ErrProjectUnavailable
	}
	if err := m.assertProjectNameOwned(ctx, ref); err != nil {
		return nil, err
	}
	return instances, nil
}
