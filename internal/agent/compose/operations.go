package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
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
	ErrOperationUncertain = errors.New("Compose operation result could not be verified")
)

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
	if err != nil || len(before) == 0 {
		return "unknown", ErrProjectUnavailable
	}
	var args []string
	var expected string
	switch action {
	case protocol.TaskComposeStart:
		args, expected = []string{"up", "-d"}, "running"
	case protocol.TaskComposeStop:
		args, expected = []string{"stop"}, "stopped"
	case protocol.TaskComposeRestart:
		args, expected = []string{"restart"}, "running"
	case protocol.TaskComposeDeploy:
		args, expected = []string{"up", "-d", "--force-recreate"}, "running"
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

func (m *Manager) requireLiveProject(ctx context.Context, ref protocol.ComposeProjectRef) error {
	if protocol.ValidateComposeProjectRef(ref) != nil {
		return ErrProjectUnavailable
	}
	projects, err := m.List(ctx)
	if err != nil {
		return ErrProjectUnavailable
	}
	for _, project := range projects {
		if project.Ref.Key == ref.Key && project.Ref.Name == ref.Name && project.Ref.WorkingDirectory == ref.WorkingDirectory &&
			strings.Join(project.Ref.ConfigFiles, "\x00") == strings.Join(ref.ConfigFiles, "\x00") && project.ConfigAvailable {
			return nil
		}
	}
	return ErrProjectUnavailable
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
	var output boundedOutput
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

type boundedOutput struct{ buffer bytes.Buffer }

func (output *boundedOutput) Write(value []byte) (int, error) {
	remaining := maxComposeCommandOutput - output.buffer.Len()
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
