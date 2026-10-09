// Package composeedit implements the S11 source editor transaction used by
// the Agent. Source files and rollback backups stay on the managed node; only
// bounded, redacted preview data is returned to Core.
package composeedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentdocker "github.com/CST-Cat/NodeDance/internal/agent/docker"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"gopkg.in/yaml.v3"
)

var (
	ErrSourceConflict     = errors.New("Compose source changed since the editor loaded it")
	ErrInvalidSource      = errors.New("Compose source or structured port edit is invalid")
	ErrRollbackFailed     = errors.New("Compose deployment failed and rollback could not be verified")
	ErrResultUnknown      = errors.New("Compose operation result cannot yet be confirmed")
	ErrPortOccupied       = errors.New("Compose deployment could not bind the requested port")
	ErrComposeApplyFailed = errors.New("Docker Compose could not apply the affected services")
)

type Engine interface {
	ListAll(context.Context) ([]string, error)
	Inspect(context.Context, string) (agentdocker.Container, error)
}

type Runner interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}

// FileSnapshot includes enough metadata to replace a source atomically while
// retaining its original owner and mode.
type FileSnapshot struct {
	Content  []byte
	Mode     os.FileMode
	UID      int
	GID      int
	OwnerSet bool
}

type FileStore interface {
	Read(string) (FileSnapshot, error)
	WriteAtomic(string, FileSnapshot) error
	MkdirAll(string, os.FileMode) error
	Remove(string) error
	RemoveAll(string) error
	ReadDir(string) ([]os.DirEntry, error)
}

type OSFileStore struct{}

func (OSFileStore) Read(path string) (FileSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return FileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return FileSnapshot{}, ErrInvalidSource
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileSnapshot{}, err
	}
	out := FileSnapshot{Content: data, Mode: info.Mode().Perm()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		out.UID, out.GID = int(stat.Uid), int(stat.Gid)
		out.OwnerSet = true
	}
	return out, nil
}

func (OSFileStore) WriteAtomic(path string, snapshot FileSnapshot) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".nodedance-compose-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	defer os.Remove(tempPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(snapshot.Content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if snapshot.OwnerSet {
		info, err := os.Stat(tempPath)
		if err != nil {
			return err
		}
		current, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("filesystem does not expose file ownership")
		}
		if int(current.Uid) != snapshot.UID || int(current.Gid) != snapshot.GID {
			if err := os.Chown(tempPath, snapshot.UID, snapshot.GID); err != nil {
				return fmt.Errorf("preserve Compose source owner: %w", err)
			}
		}
	}
	if err := os.Chmod(tempPath, snapshot.Mode); err != nil {
		return fmt.Errorf("preserve Compose source mode: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (OSFileStore) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
func (OSFileStore) Remove(path string) error                     { return os.Remove(path) }
func (OSFileStore) RemoveAll(path string) error                  { return os.RemoveAll(path) }
func (OSFileStore) ReadDir(path string) ([]os.DirEntry, error)   { return os.ReadDir(path) }

type Options struct {
	DockerHost          string
	BackupDir           string
	OperationTimeout    time.Duration
	VerificationTimeout time.Duration
	Now                 func() time.Time
}

type Manager struct {
	engine Engine
	runner Runner
	files  FileStore
	opts   Options
}

func NewManager(engine Engine, runner Runner, files FileStore, options Options) (*Manager, error) {
	if engine == nil || runner == nil || files == nil {
		return nil, errors.New("Compose editor Engine, runner, and filesystem are required")
	}
	if options.DockerHost != "" && !strings.HasPrefix(options.DockerHost, "unix:///") {
		return nil, errors.New("Compose editor requires a local Docker Engine socket")
	}
	if options.BackupDir == "" || !filepath.IsAbs(options.BackupDir) {
		return nil, errors.New("Compose editor backup directory must be absolute")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = 10 * time.Minute
	}
	if options.OperationTimeout < time.Second || options.OperationTimeout > 30*time.Minute {
		return nil, errors.New("Compose editor operation timeout is outside supported bounds")
	}
	if options.VerificationTimeout == 0 {
		options.VerificationTimeout = 90 * time.Second
	}
	if options.VerificationTimeout < time.Second || options.VerificationTimeout > 5*time.Minute {
		return nil, errors.New("Compose editor verification timeout is outside supported bounds")
	}
	if err := files.MkdirAll(options.BackupDir, 0o700); err != nil {
		return nil, fmt.Errorf("create secured Compose backup directory: %w", err)
	}
	return &Manager{engine: engine, runner: runner, files: files, opts: options}, nil
}

func (m *Manager) Execute(ctx context.Context, request protocol.ComposeRequest) (protocol.ComposeResponse, error) {
	if err := protocol.ValidateComposeRequest(request); err != nil {
		return protocol.ComposeResponse{}, err
	}
	if request.Action == protocol.ComposeEditStatus {
		return m.status(ctx, request)
	}
	response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "failed"}
	var result protocol.ComposeEditorResult
	var err error
	switch request.Action {
	case protocol.ComposeEditRead:
		result, err = m.readSources(request)
	case protocol.ComposeEditPreview:
		result, _, err = m.preview(ctx, request)
	case protocol.ComposeEditApply:
		result, err = m.apply(ctx, request)
	default:
		return protocol.ComposeResponse{}, ErrInvalidSource
	}
	response.Editor = &result
	if err != nil {
		return response, err
	}
	response.Status, response.Verified = "succeeded", true
	return response, nil
}

func (m *Manager) readSources(request protocol.ComposeRequest) (protocol.ComposeEditorResult, error) {
	paths := composeContextPaths(request)
	result := protocol.ComposeEditorResult{Files: make([]protocol.ComposeSourceFile, 0, len(paths))}
	total := 0
	for _, path := range paths {
		snapshot, err := m.readSafe(path)
		if err != nil {
			return result, ErrInvalidSource
		}
		total += len(snapshot.Content)
		if total > protocol.MaxComposeEditBytes {
			return result, ErrInvalidSource
		}
		result.Files = append(result.Files, protocol.ComposeSourceFile{Path: path, Content: string(snapshot.Content), Version: sourceVersion(snapshot.Content)})
	}
	return result, nil
}

func (m *Manager) preview(ctx context.Context, request protocol.ComposeRequest) (protocol.ComposeEditorResult, map[string]FileSnapshot, error) {
	original, versions, err := m.currentSources(request)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, err
	}
	for path, expected := range request.Editor.ExpectedVersions {
		if versions[path] != expected {
			return protocol.ComposeEditorResult{}, nil, ErrSourceConflict
		}
	}
	proposed := make(map[string]FileSnapshot, len(original))
	for path, snapshot := range original {
		proposed[path] = snapshot
	}
	for _, file := range request.Editor.Files {
		current := proposed[file.Path]
		current.Content = []byte(file.Content)
		proposed[file.Path] = current
	}
	for _, edit := range request.Editor.PortEdits {
		snapshot, ok := proposed[edit.File]
		if !ok {
			return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
		}
		changed, err := applyPortEdit(snapshot.Content, edit)
		if err != nil {
			return protocol.ComposeEditorResult{}, nil, err
		}
		snapshot.Content = changed
		proposed[edit.File] = snapshot
	}
	proposedBytes := 0
	for _, snapshot := range proposed {
		proposedBytes += len(snapshot.Content)
	}
	if proposedBytes > protocol.MaxComposeEditBytes {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	oldResolved, err := m.resolve(ctx, request, nil)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	newResolved, err := m.resolve(ctx, request, proposed)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	servicesBefore, err := serviceModels(oldResolved)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	servicesAfter, err := serviceModels(newResolved)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	affected := changedServices(servicesBefore, servicesAfter)
	current, err := m.observeProject(ctx, request.Project)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrComposeEngine
	}
	if hasRemovedRunningService(affected, servicesAfter, current) {
		return protocol.ComposeEditorResult{}, nil, ErrRemoveServiceUnsupported
	}
	result := protocol.ComposeEditorResult{AffectedServices: affected, DataBackup: false}
	for _, path := range composeContextPaths(request) {
		before, after := original[path], proposed[path]
		if !bytes.Equal(before.Content, after.Content) {
			result.Diff += fileDiff(path, before.Content, after.Content)
			result.Files = append(result.Files, protocol.ComposeSourceFile{Path: path, Content: string(after.Content), Version: sourceVersion(after.Content)})
		}
	}
	redacted, err := redactResolvedConfig(newResolved)
	if err != nil {
		return protocol.ComposeEditorResult{}, nil, ErrInvalidSource
	}
	result.ResolvedConfig = redacted
	result.Impact = []string{"仅重建配置发生变化的服务；其他服务保持原实例。", "应用数据卷不在配置回滚范围内，需使用应用自身备份。"}
	if len(affected) == 0 {
		result.Impact = append(result.Impact, "解析后的服务模型没有变化；只保存源文件，不重建容器。")
	}
	return result, proposed, nil
}

func (m *Manager) apply(ctx context.Context, request protocol.ComposeRequest) (protocol.ComposeEditorResult, error) {
	operationCtx, cancel := context.WithTimeout(ctx, m.opts.OperationTimeout)
	defer cancel()
	ctx = operationCtx
	preview, proposed, err := m.preview(ctx, request)
	if err != nil {
		return protocol.ComposeEditorResult{}, err
	}
	operationID := request.OperationID
	txnDir := filepath.Join(m.opts.BackupDir, operationID)
	if err := m.files.MkdirAll(txnDir, 0o700); err != nil {
		return protocol.ComposeEditorResult{}, ErrSourceWrite
	}
	state, oldResolved, err := m.prepareTransaction(ctx, request, proposed, preview, txnDir)
	if err != nil {
		_ = m.files.RemoveAll(txnDir)
		return protocol.ComposeEditorResult{}, err
	}
	if err := m.writeJournal(txnDir, state); err != nil {
		return protocol.ComposeEditorResult{}, ErrSourceWrite
	}
	if len(preview.AffectedServices) > 0 {
		if err := m.pinOldImages(ctx, request, &state, preview.AffectedServices, txnDir); err != nil {
			return protocol.ComposeEditorResult{}, err
		}
	}
	state.Phase = "applying"
	if err := m.writeJournal(txnDir, state); err != nil {
		return protocol.ComposeEditorResult{}, ErrSourceWrite
	}
	for _, path := range composeContextPaths(request) {
		current, readErr := m.readSafe(path)
		if readErr != nil || sourceVersion(current.Content) != state.OriginalVersions[path] {
			state.Phase = "conflict"
			if err := m.writeJournal(txnDir, state); err != nil {
				return protocol.ComposeEditorResult{}, ErrSourceWrite
			}
			return preview, ErrSourceConflict
		}
	}
	for _, path := range state.ChangedFiles {
		if err := m.files.WriteAtomic(path, proposed[path]); err != nil {
			return m.failAndRollback(request, state, txnDir, oldResolved, preview, ErrSourceWrite)
		}
	}
	for _, path := range composeContextPaths(request) {
		current, readErr := m.readSafe(path)
		wanted := state.OriginalVersions[path]
		if contains(state.ChangedFiles, path) {
			wanted = state.NewVersions[path]
		}
		if readErr != nil || sourceVersion(current.Content) != wanted {
			return m.failAndRollback(request, state, txnDir, oldResolved, preview, ErrSourceConflict)
		}
	}
	if len(preview.AffectedServices) > 0 {
		if err := m.composeUp(ctx, request, txnDir, preview.AffectedServices, false); err != nil {
			return m.failAndRollback(request, state, txnDir, oldResolved, preview, err)
		}
	}
	newResolved, resolveErr := m.resolve(ctx, request, nil)
	if resolveErr != nil {
		return m.failAndRollback(request, state, txnDir, oldResolved, preview, ErrInvalidSource)
	}
	if len(preview.AffectedServices) > 0 {
		if err := m.verifyLive(ctx, request, preview.AffectedServices, newResolved); err != nil {
			return m.failAndRollback(request, state, txnDir, oldResolved, preview, err)
		}
	}
	state.Phase = "succeeded"
	if err := m.writeJournal(txnDir, state); err != nil {
		return preview, ErrResultUnknown
	}
	preview.DataBackup = false
	preview.Impact = append(preview.Impact, "Compose 文件及镜像回滚点已保留；本操作没有备份应用数据卷。")
	return preview, nil
}

var (
	ErrComposeEngine            = errors.New("Docker Engine inventory is unavailable")
	ErrRemoveServiceUnsupported = errors.New("removing a live Compose service is not part of the port-change transaction")
	ErrSourceWrite              = errors.New("Compose source transaction could not be committed")
	ErrHealthFailed             = errors.New("updated Compose service did not become healthy")
)

type transactionState struct {
	OperationID       string                     `json:"operationId"`
	Phase             string                     `json:"phase"`
	Project           protocol.ComposeProjectRef `json:"project"`
	EnvFiles          []string                   `json:"envFiles,omitempty"`
	Profiles          []string                   `json:"profiles,omitempty"`
	ChangedFiles      []string                   `json:"changedFiles"`
	BackupFiles       map[string]string          `json:"backupFiles"`
	Modes             map[string]uint32          `json:"modes"`
	UIDs              map[string]int             `json:"uids"`
	GIDs              map[string]int             `json:"gids"`
	PinnedImages      map[string]string          `json:"pinnedImages"`
	OriginalVersions  map[string]string          `json:"originalVersions"`
	NewVersions       map[string]string          `json:"newVersions"`
	Services          []string                   `json:"services"`
	OldResolved       string                     `json:"oldResolved"`
	ErrorCode         string                     `json:"errorCode,omitempty"`
	RollbackConfirmed bool                       `json:"rollbackConfirmed,omitempty"`
}

func (m *Manager) prepareTransaction(ctx context.Context, request protocol.ComposeRequest, proposed map[string]FileSnapshot, preview protocol.ComposeEditorResult, txnDir string) (transactionState, string, error) {
	oldResolvedBytes, err := m.resolve(ctx, request, nil)
	if err != nil {
		return transactionState{}, "", ErrInvalidSource
	}
	state := transactionState{OperationID: request.OperationID, Phase: "prepared", Project: request.Project, EnvFiles: append([]string(nil), request.EnvFiles...), Profiles: append([]string(nil), request.Profiles...),
		BackupFiles: map[string]string{}, Modes: map[string]uint32{}, UIDs: map[string]int{}, GIDs: map[string]int{}, PinnedImages: map[string]string{}, OriginalVersions: map[string]string{}, NewVersions: map[string]string{}, Services: append([]string(nil), preview.AffectedServices...), OldResolved: string(oldResolvedBytes)}
	for _, path := range composeContextPaths(request) {
		old, err := m.readSafe(path)
		if err != nil {
			return transactionState{}, "", ErrInvalidSource
		}
		if sourceVersion(old.Content) != request.Editor.ExpectedVersions[path] {
			return transactionState{}, "", ErrSourceConflict
		}
		name := fmt.Sprintf("source-%02d.backup", len(state.BackupFiles))
		backupPath := filepath.Join(txnDir, name)
		if err := m.files.WriteAtomic(backupPath, FileSnapshot{Content: old.Content, Mode: 0o600}); err != nil {
			return transactionState{}, "", ErrSourceWrite
		}
		state.BackupFiles[path] = name
		state.Modes[path], state.UIDs[path], state.GIDs[path] = uint32(old.Mode.Perm()), old.UID, old.GID
		state.OriginalVersions[path], state.NewVersions[path] = sourceVersion(old.Content), sourceVersion(proposed[path].Content)
		if !bytes.Equal(old.Content, proposed[path].Content) {
			state.ChangedFiles = append(state.ChangedFiles, path)
		}
	}
	if err := m.files.WriteAtomic(filepath.Join(txnDir, "resolved.old.json"), FileSnapshot{Content: oldResolvedBytes, Mode: 0o600}); err != nil {
		return transactionState{}, "", ErrSourceWrite
	}
	return state, string(oldResolvedBytes), nil
}

func (m *Manager) pinOldImages(ctx context.Context, request protocol.ComposeRequest, state *transactionState, services []string, txnDir string) error {
	containers, err := m.observeProject(ctx, request.Project)
	if err != nil {
		return ErrComposeEngine
	}
	for _, service := range services {
		imageID := ""
		for _, item := range containers {
			if item.Compose != nil && item.Compose.Service == service && item.Running {
				if item.ImageID == "" || (imageID != "" && imageID != item.ImageID) {
					return ErrInvalidSource
				}
				imageID = item.ImageID
			}
		}
		if imageID == "" {
			continue
		}
		alias := "nodedance-rollback:" + shortOperation(state.OperationID) + "-" + safeService(service)
		if _, err := m.run(ctx, request.Project.WorkingDirectory, []string{"image", "tag", imageID, alias}); err != nil {
			return ErrInvalidSource
		}
		state.PinnedImages[service] = alias
	}
	if len(state.PinnedImages) == 0 {
		return nil
	}
	data := rollbackOverride(state.PinnedImages)
	if err := m.files.WriteAtomic(filepath.Join(txnDir, "rollback.override.yaml"), FileSnapshot{Content: data, Mode: 0o600}); err != nil {
		return ErrSourceWrite
	}
	return nil
}

func (m *Manager) failAndRollback(request protocol.ComposeRequest, state transactionState, txnDir, oldResolved string, preview protocol.ComposeEditorResult, cause error) (protocol.ComposeEditorResult, error) {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.restoreFiles(state, txnDir); err != nil {
		return preview, ErrRollbackFailed
	}
	if len(state.Services) != 0 {
		if err := m.composeUp(rollbackCtx, request, txnDir, state.Services, true); err != nil {
			return preview, ErrRollbackFailed
		}
		if err := m.verifyLive(rollbackCtx, request, state.Services, []byte(oldResolved)); err != nil {
			return preview, ErrRollbackFailed
		}
	}
	state.Phase = "rolled_back"
	state.ErrorCode = editorErrorCode(cause)
	state.RollbackConfirmed = true
	_ = m.writeJournal(txnDir, state)
	preview.RollbackConfirmed = true
	preview.Impact = append(preview.Impact, "部署失败；Compose 源文件和受影响服务已恢复。应用数据卷不属于配置回滚。")
	if errors.Is(cause, ErrPortOccupied) {
		return preview, ErrPortOccupied
	}
	return preview, cause
}

func (m *Manager) restoreFiles(state transactionState, txnDir string) error {
	paths := make([]string, 0, len(state.BackupFiles))
	for path := range state.BackupFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		name := state.BackupFiles[path]
		current, currentErr := m.readSafe(path)
		if currentErr != nil {
			return currentErr
		}
		currentVersion := sourceVersion(current.Content)
		if currentVersion == state.OriginalVersions[path] {
			continue
		}
		if currentVersion != state.NewVersions[path] {
			return ErrSourceConflict
		}
		backup, err := m.readSafe(filepath.Join(txnDir, name))
		if err != nil {
			return err
		}
		if err := m.files.WriteAtomic(path, FileSnapshot{Content: backup.Content, Mode: os.FileMode(state.Modes[path]), UID: state.UIDs[path], GID: state.GIDs[path], OwnerSet: true}); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) composeUp(ctx context.Context, request protocol.ComposeRequest, txnDir string, services []string, rollback bool) error {
	args := prefix(request)
	if rollback {
		if len(mustReadJournal(m, txnDir).PinnedImages) > 0 {
			args = append(args, "-f", filepath.Join(txnDir, "rollback.override.yaml"))
		}
	}
	args = append(args, "up", "--detach", "--no-deps")
	if rollback {
		// A pinned image ID is the rollback source of truth. Never rebuild from
		// the current Compose build context while restoring that image.
		args = append(args, "--no-build")
	} else {
		args = append(args, "--build")
	}
	for _, service := range services {
		args = append(args, service)
	}
	output, err := m.run(ctx, request.Project.WorkingDirectory, args)
	if err == nil {
		return nil
	}
	message := strings.ToLower(string(output) + " " + err.Error())
	if strings.Contains(message, "port is already allocated") || strings.Contains(message, "address already in use") || strings.Contains(message, "bind: address in use") {
		return ErrPortOccupied
	}
	return ErrComposeApplyFailed
}

// mustReadJournal is used only while rolling back after the original journal
// was durably written. Failure is conservative: no override is applied.
func mustReadJournal(m *Manager, directory string) transactionState {
	snapshot, err := m.readSafe(filepath.Join(directory, "transaction.json"))
	if err != nil {
		return transactionState{}
	}
	var state transactionState
	_ = json.Unmarshal(snapshot.Content, &state)
	return state
}

func (m *Manager) verifyLive(ctx context.Context, request protocol.ComposeRequest, services []string, resolved []byte) error {
	model, err := serviceModels(resolved)
	if err != nil {
		return ErrHealthFailed
	}
	wanted := make(map[string]bool, len(services))
	for _, service := range services {
		wanted[service] = true
	}
	deadline := time.NewTimer(m.opts.VerificationTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	lastPortFailure := false
	for {
		containers, observeErr := m.observeProject(ctx, request.Project)
		if observeErr == nil {
			counts := map[string]int{}
			healthy := true
			portsOkay := true
			for _, item := range containers {
				if item.Compose == nil || !wanted[item.Compose.Service] {
					continue
				}
				counts[item.Compose.Service]++
				if !item.Running || item.Paused || item.Restarting || (item.HealthcheckConfigured && item.Health != agentdocker.HealthHealthy) {
					healthy = false
				}
				if !portsMatch(item, model[item.Compose.Service]) {
					portsOkay = false
				}
			}
			allPresent := true
			for service := range wanted {
				if counts[service] == 0 {
					allPresent = false
				}
			}
			if healthy && portsOkay && allPresent {
				return nil
			}
			lastPortFailure = !portsOkay
		}
		select {
		case <-ctx.Done():
			return ErrHealthFailed
		case <-deadline.C:
			if lastPortFailure {
				return ErrPortOccupied
			}
			return ErrHealthFailed
		case <-ticker.C:
		}
	}
}

func (m *Manager) Recover(ctx context.Context) error {
	entries, err := m.files.ReadDir(m.opts.BackupDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validOperationDir(entry.Name()) {
			continue
		}
		directory := filepath.Join(m.opts.BackupDir, entry.Name())
		journal, err := m.readSafe(filepath.Join(directory, "transaction.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var state transactionState
		if err := json.Unmarshal(journal.Content, &state); err != nil {
			return errors.New("invalid Compose editor recovery journal")
		}
		if state.Phase != "applying" {
			continue
		}
		if err := m.restoreFiles(state, directory); err != nil {
			return ErrRollbackFailed
		}
		rollbackRequest := protocol.ComposeRequest{OperationID: state.OperationID, Action: protocol.ComposeUp, Project: state.Project, EnvFiles: state.EnvFiles, Profiles: state.Profiles}
		if len(state.Services) > 0 {
			if err := m.composeUp(ctx, rollbackRequest, directory, state.Services, true); err != nil {
				return ErrRollbackFailed
			}
			if err := m.verifyLive(ctx, rollbackRequest, state.Services, []byte(state.OldResolved)); err != nil {
				return ErrRollbackFailed
			}
		}
		state.Phase = "recovered"
		state.ErrorCode = "recovered_after_restart"
		state.RollbackConfirmed = true
		if err := m.writeJournal(directory, state); err != nil {
			return err
		}
	}
	return nil
}

// status checks an Agent-local transaction journal and verifies its terminal
// postcondition against both source versions and Docker Engine state. It never
// replays a Compose command.
func (m *Manager) status(ctx context.Context, request protocol.ComposeRequest) (protocol.ComposeResponse, error) {
	response := protocol.ComposeResponse{OperationID: request.OperationID, Status: "unknown", ErrorCode: "result_pending", Editor: &protocol.ComposeEditorResult{DataBackup: false}}
	target := request.Editor.TargetOperationID
	directory := filepath.Join(m.opts.BackupDir, target)
	snapshot, err := m.readSafe(filepath.Join(directory, "transaction.json"))
	if errors.Is(err, os.ErrNotExist) {
		response.Status, response.ErrorCode = "failed", "not_executed"
		return response, nil
	}
	if err != nil {
		return response, nil
	}
	var state transactionState
	if err := json.Unmarshal(snapshot.Content, &state); err != nil || state.OperationID != target || state.Project.Key != request.Project.Key {
		return response, nil
	}
	response.Editor.AffectedServices = append([]string(nil), state.Services...)
	response.Editor.RollbackConfirmed = state.RollbackConfirmed
	switch state.Phase {
	case "succeeded":
		if !m.verifyTransactionState(ctx, state, false) {
			return response, nil
		}
		response.Status, response.ErrorCode, response.Verified = "succeeded", "", true
		response.Editor.Impact = []string{"Agent journal and current Engine state confirm the applied Compose configuration."}
	case "rolled_back", "recovered":
		if !m.verifyTransactionState(ctx, state, true) {
			return response, nil
		}
		response.Status = "failed"
		response.ErrorCode = state.ErrorCode
		if response.ErrorCode == "" {
			response.ErrorCode = "recovered_after_restart"
		}
		response.Editor.RollbackConfirmed = true
		response.Editor.Impact = []string{"Agent journal and current Engine state confirm the original Compose configuration was restored."}
	case "conflict":
		response.Status, response.ErrorCode = "failed", "source_conflict"
	case "prepared":
		response.Status, response.ErrorCode = "failed", "not_executed"
	case "applying":
		// An in-process operation may still be active after a Core reconnect. The
		// bridge serializes this read behind the same project lock; if it reaches
		// here, the outcome remains explicitly unresolved.
	default:
		response.Status, response.ErrorCode = "failed", "invalid_recovery_state"
	}
	return response, nil
}

func (m *Manager) verifyTransactionState(ctx context.Context, state transactionState, original bool) bool {
	versions := state.NewVersions
	resolved := []byte(nil)
	if original {
		versions = state.OriginalVersions
		resolved = []byte(state.OldResolved)
	}
	for path, expected := range versions {
		current, err := m.readSafe(path)
		if err != nil || sourceVersion(current.Content) != expected {
			return false
		}
	}
	if len(state.Services) == 0 {
		return true
	}
	request := protocol.ComposeRequest{OperationID: state.OperationID, Action: protocol.ComposeUp, Project: state.Project, EnvFiles: state.EnvFiles, Profiles: state.Profiles}
	if !original {
		var err error
		resolved, err = m.resolve(ctx, request, nil)
		if err != nil {
			return false
		}
	}
	verifyCtx, cancel := context.WithTimeout(ctx, m.opts.VerificationTimeout)
	defer cancel()
	return m.verifyLive(verifyCtx, request, state.Services, resolved) == nil
}

func editorErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrSourceConflict):
		return "source_conflict"
	case errors.Is(err, ErrPortOccupied):
		return "port_occupied"
	case errors.Is(err, ErrHealthFailed):
		return "health_failed"
	case errors.Is(err, ErrComposeApplyFailed):
		return "deployment_failed"
	case errors.Is(err, ErrSourceWrite):
		return "source_write_failed"
	case errors.Is(err, ErrInvalidSource):
		return "invalid_compose_source"
	default:
		return "deployment_failed"
	}
}

func (m *Manager) writeJournal(directory string, state transactionState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return m.files.WriteAtomic(filepath.Join(directory, "transaction.json"), FileSnapshot{Content: data, Mode: 0o600})
}

func (m *Manager) currentSources(request protocol.ComposeRequest) (map[string]FileSnapshot, map[string]string, error) {
	paths := composeContextPaths(request)
	files := make(map[string]FileSnapshot, len(paths))
	versions := make(map[string]string, len(paths))
	total := 0
	for _, path := range paths {
		snapshot, err := m.readSafe(path)
		if err != nil {
			return nil, nil, ErrInvalidSource
		}
		total += len(snapshot.Content)
		if total > protocol.MaxComposeEditBytes {
			return nil, nil, ErrInvalidSource
		}
		files[path], versions[path] = snapshot, sourceVersion(snapshot.Content)
	}
	return files, versions, nil
}

func composeContextPaths(request protocol.ComposeRequest) []string {
	paths := append([]string(nil), request.Project.ConfigFiles...)
	paths = append(paths, request.EnvFiles...)
	return paths
}

func (m *Manager) readSafe(path string) (FileSnapshot, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return FileSnapshot{}, ErrInvalidSource
	}
	return m.files.Read(path)
}

func sourceVersion(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func prefix(request protocol.ComposeRequest) []string {
	args := []string{"compose", "--project-name", request.Project.Name, "--project-directory", request.Project.WorkingDirectory}
	for _, path := range request.Project.ConfigFiles {
		args = append(args, "-f", path)
	}
	for _, path := range request.EnvFiles {
		args = append(args, "--env-file", path)
	}
	for _, profile := range request.Profiles {
		args = append(args, "--profile", profile)
	}
	return args
}

func (m *Manager) resolve(ctx context.Context, request protocol.ComposeRequest, proposed map[string]FileSnapshot) ([]byte, error) {
	if proposed == nil {
		if _, err := m.run(ctx, request.Project.WorkingDirectory, append(prefix(request), "config", "--quiet")); err != nil {
			return nil, err
		}
		return m.run(ctx, request.Project.WorkingDirectory, append(prefix(request), "config", "--format", "json"))
	}
	// Temporary files remain beside their originals so Compose resolves nested
	// includes and extends relative to the same directory. The explicit project
	// directory remains unchanged, preserving relative volume and build paths.
	args := []string{"compose", "--project-name", request.Project.Name, "--project-directory", request.Project.WorkingDirectory}
	created := make([]string, 0, len(request.Project.ConfigFiles))
	defer func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}()
	for _, path := range request.Project.ConfigFiles {
		snapshot, ok := proposed[path]
		if !ok || len(snapshot.Content) == 0 {
			return nil, ErrInvalidSource
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".nodedance-preview-*.yaml")
		if err != nil {
			return nil, err
		}
		temporary := file.Name()
		created = append(created, temporary)
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return nil, err
		}
		if _, err := file.Write(snapshot.Content); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		args = append(args, "-f", temporary)
	}
	for _, path := range request.EnvFiles {
		inputPath := path
		if snapshot, ok := proposed[path]; ok {
			file, err := os.CreateTemp(filepath.Dir(path), ".nodedance-preview-env-*.env")
			if err != nil {
				return nil, err
			}
			inputPath = file.Name()
			created = append(created, inputPath)
			if err := file.Chmod(0o600); err != nil {
				_ = file.Close()
				return nil, err
			}
			if _, err := file.Write(snapshot.Content); err != nil {
				_ = file.Close()
				return nil, err
			}
			if err := file.Sync(); err != nil {
				_ = file.Close()
				return nil, err
			}
			if err := file.Close(); err != nil {
				return nil, err
			}
		} else {
			return nil, ErrInvalidSource
		}
		args = append(args, "--env-file", inputPath)
	}
	for _, profile := range request.Profiles {
		args = append(args, "--profile", profile)
	}
	if _, err := m.run(ctx, request.Project.WorkingDirectory, append(append([]string(nil), args...), "config", "--quiet")); err != nil {
		return nil, err
	}
	return m.run(ctx, request.Project.WorkingDirectory, append(args, "config", "--format", "json"))
}

func restoreSnapshotMap(files FileStore, snapshots map[string]FileSnapshot) error {
	var first error
	for path, snapshot := range snapshots {
		if err := files.WriteAtomic(path, snapshot); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (m *Manager) run(ctx context.Context, directory string, args []string) ([]byte, error) {
	return m.runner.Run(ctx, directory, args, m.opts.DockerHost)
}

func (m *Manager) observeProject(ctx context.Context, ref protocol.ComposeProjectRef) ([]agentdocker.Container, error) {
	ids, err := m.engine.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]agentdocker.Container, 0)
	for _, id := range ids {
		item, err := m.engine.Inspect(ctx, id)
		if err != nil {
			return nil, err
		}
		if item.Compose != nil && item.Compose.Project == ref.Name && filepath.Clean(item.Compose.WorkingDir) == filepath.Clean(ref.WorkingDirectory) {
			result = append(result, item)
		}
	}
	return result, nil
}

func serviceModels(raw []byte) (map[string]json.RawMessage, error) {
	var model struct {
		Services map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(raw, &model); err != nil || len(model.Services) == 0 {
		return nil, ErrInvalidSource
	}
	return model.Services, nil
}

func changedServices(before, after map[string]json.RawMessage) []string {
	all := map[string]struct{}{}
	for name := range before {
		all[name] = struct{}{}
	}
	for name := range after {
		all[name] = struct{}{}
	}
	result := make([]string, 0)
	for name := range all {
		if !bytes.Equal(bytes.TrimSpace(before[name]), bytes.TrimSpace(after[name])) {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func hasRemovedRunningService(affected []string, after map[string]json.RawMessage, containers []agentdocker.Container) bool {
	for _, item := range containers {
		if item.Running && item.Compose != nil && contains(affected, item.Compose.Service) {
			if _, exists := after[item.Compose.Service]; !exists {
				return true
			}
		}
	}
	return false
}

func redactResolvedConfig(raw []byte) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	redact(value)
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(encoded) > protocol.MaxComposeEditBytes {
		return "", ErrInvalidSource
	}
	return string(encoded), nil
}

func redact(value any) {
	switch item := value.(type) {
	case map[string]any:
		for key, nested := range item {
			if strings.EqualFold(key, "environment") {
				redactEnvironment(nested)
				continue
			}
			if sensitiveConfigKey(key) {
				item[key] = "[REDACTED]"
				continue
			}
			redact(nested)
		}
	case []any:
		for index, nested := range item {
			if pair, ok := nested.(string); ok {
				if redacted, changed := redactAssignment(pair); changed {
					item[index] = redacted
					continue
				}
			}
			redact(nested)
		}
	}
}

var (
	composeAcronymBoundary = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
	composeCamelBoundary   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	composeNameSeparator   = regexp.MustCompile(`[^A-Za-z0-9]+`)
)

func sensitiveConfigKey(name string) bool {
	lowerName := strings.ToLower(name)
	for _, marker := range []string{"password", "passwd", "passphrase", "token", "secret", "credential", "authorization", "bearer", "session"} {
		if strings.Contains(lowerName, marker) {
			return true
		}
	}
	if strings.HasSuffix(lowerName, "key") && lowerName != "monkey" {
		return true
	}
	expanded := composeAcronymBoundary.ReplaceAllString(name, "$1 $2")
	expanded = composeCamelBoundary.ReplaceAllString(expanded, "$1 $2")
	for _, part := range composeNameSeparator.Split(expanded, -1) {
		switch strings.ToLower(part) {
		case "password", "passwd", "passphrase", "token", "secret", "key", "keys", "credential", "credentials", "auth", "authorization", "bearer", "session":
			return true
		}
	}
	return false
}

func redactEnvironment(value any) {
	switch env := value.(type) {
	case map[string]any:
		for name := range env {
			env[name] = "[REDACTED]"
		}
	case []any:
		for index, entry := range env {
			if pair, ok := entry.(string); ok {
				if at := strings.IndexByte(pair, '='); at >= 0 {
					env[index] = pair[:at+1] + "[REDACTED]"
				}
			}
		}
	}
}

func redactAssignment(value string) (string, bool) {
	for _, separator := range []byte{'=', ':'} {
		if at := strings.IndexByte(value, separator); at > 0 && sensitiveConfigKey(strings.TrimLeft(strings.TrimSpace(value[:at]), "-")) {
			return value[:at+1] + "[REDACTED]", true
		}
	}
	return value, false
}

func fileDiff(path string, old, next []byte) string {
	oldLines, newLines := strings.Split(strings.TrimSuffix(string(old), "\n"), "\n"), strings.Split(strings.TrimSuffix(string(next), "\n"), "\n")
	var output strings.Builder
	fmt.Fprintf(&output, "--- %s (current)\n+++ %s (proposed)\n", path, path)
	for index := 0; index < max(len(oldLines), len(newLines)); index++ {
		if index < len(oldLines) && index < len(newLines) && oldLines[index] == newLines[index] {
			continue
		}
		if index < len(oldLines) {
			output.WriteString("-" + oldLines[index] + "\n")
		}
		if index < len(newLines) {
			output.WriteString("+" + newLines[index] + "\n")
		}
	}
	return output.String()
}

func portsMatch(container agentdocker.Container, model json.RawMessage) bool {
	var service struct {
		Ports []struct {
			Target    uint16 `json:"target"`
			Published string `json:"published"`
			Protocol  string `json:"protocol"`
			HostIP    string `json:"host_ip"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(model, &service); err != nil {
		return false
	}
	for _, desired := range service.Ports {
		published, err := strconv.Atoi(desired.Published)
		if desired.Published == "" || err != nil || published == 0 {
			continue
		}
		found := false
		for _, observed := range container.Ports {
			if observed.ContainerPort != desired.Target || !strings.EqualFold(observed.Protocol, desired.Protocol) {
				continue
			}
			for _, binding := range observed.Published {
				actualPort, parseErr := strconv.Atoi(binding.Port)
				if parseErr == nil && actualPort == published && normalizeIP(binding.IP) == normalizeIP(desired.HostIP) {
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func normalizeIP(value string) string {
	if value == "" {
		return "0.0.0.0"
	}
	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	return value
}

func applyPortEdit(source []byte, edit protocol.ComposePortEdit) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil || len(document.Content) == 0 {
		return nil, ErrInvalidSource
	}
	root := mappingValue(document.Content[0], "services")
	service := mappingValue(root, edit.Service)
	ports := mappingValue(service, "ports")
	if ports == nil || ports.Kind != yaml.SequenceNode {
		return nil, ErrInvalidSource
	}
	matched := 0
	for _, entry := range ports.Content {
		current, err := parsePortNode(entry)
		if err != nil {
			continue
		}
		if current.Target != edit.Target || current.Protocol != edit.Protocol || current.HostIP != edit.OldHostIP || current.Published != edit.OldPublished {
			continue
		}
		matched++
		if entry.Kind == yaml.ScalarNode {
			entry.Value = formatShortPort(edit.NewHostIP, edit.NewPublished, edit.Target, edit.Protocol)
		} else if entry.Kind == yaml.MappingNode {
			setMapping(entry, "published", strconv.Itoa(int(edit.NewPublished)))
			if edit.NewHostIP != "" {
				setMapping(entry, "host_ip", edit.NewHostIP)
			} else {
				removeMapping(entry, "host_ip")
			}
		} else {
			return nil, ErrInvalidSource
		}
	}
	if matched != 1 {
		return nil, ErrInvalidSource
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, ErrInvalidSource
	}
	_ = encoder.Close()
	return output.Bytes(), nil
}

type normalizedPort struct {
	Target    uint16
	Published uint16
	HostIP    string
	Protocol  string
}

func parsePortNode(node *yaml.Node) (normalizedPort, error) {
	if node.Kind == yaml.ScalarNode {
		return parseShortPort(node.Value)
	}
	if node.Kind != yaml.MappingNode {
		return normalizedPort{}, ErrInvalidSource
	}
	get := func(key string) string {
		value := mappingValue(node, key)
		if value == nil {
			return ""
		}
		return value.Value
	}
	target, err := strconv.Atoi(get("target"))
	if err != nil || target < 1 || target > 65535 {
		return normalizedPort{}, ErrInvalidSource
	}
	published := 0
	if get("published") != "" {
		published, err = strconv.Atoi(get("published"))
		if err != nil || published < 0 || published > 65535 {
			return normalizedPort{}, ErrInvalidSource
		}
	}
	protocolName := get("protocol")
	if protocolName == "" {
		protocolName = "tcp"
	}
	return normalizedPort{Target: uint16(target), Published: uint16(published), HostIP: get("host_ip"), Protocol: protocolName}, nil
}

func parseShortPort(raw string) (normalizedPort, error) {
	protocolName := "tcp"
	if slash := strings.LastIndexByte(raw, '/'); slash >= 0 {
		protocolName, raw = raw[slash+1:], raw[:slash]
	}
	if protocolName != "tcp" && protocolName != "udp" {
		return normalizedPort{}, ErrInvalidSource
	}
	var hostIP string
	parts := []string{}
	if strings.HasPrefix(raw, "[") {
		closing := strings.IndexByte(raw, ']')
		if closing < 0 || closing+1 >= len(raw) || raw[closing+1] != ':' {
			return normalizedPort{}, ErrInvalidSource
		}
		hostIP = raw[1:closing]
		parts = strings.Split(raw[closing+2:], ":")
	} else {
		parts = strings.Split(raw, ":")
	}
	if len(parts) == 1 {
		return normalizedPort{}, ErrInvalidSource
	}
	if len(parts) == 3 && hostIP == "" {
		hostIP = parts[0]
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return normalizedPort{}, ErrInvalidSource
	}
	published, err1 := strconv.Atoi(parts[0])
	target, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || published < 0 || published > 65535 || target < 1 || target > 65535 {
		return normalizedPort{}, ErrInvalidSource
	}
	return normalizedPort{Target: uint16(target), Published: uint16(published), HostIP: hostIP, Protocol: protocolName}, nil
}

func formatShortPort(hostIP string, published, target uint16, protocolName string) string {
	address := hostIP
	if strings.Contains(hostIP, ":") {
		address = "[" + hostIP + "]"
	}
	if address != "" {
		return fmt.Sprintf("%s:%d:%d/%s", address, published, target, protocolName)
	}
	return fmt.Sprintf("%d:%d/%s", published, target, protocolName)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}

func setMapping(node *yaml.Node, key, value string) {
	if existing := mappingValue(node, key); existing != nil {
		existing.Value = value
		existing.Tag = "!!int"
		return
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valueNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: value}
	node.Content = append(node.Content, keyNode, valueNode)
}

func removeMapping(node *yaml.Node, key string) {
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			node.Content = append(node.Content[:index], node.Content[index+2:]...)
			return
		}
	}
}

func rollbackOverride(images map[string]string) []byte {
	var output strings.Builder
	output.WriteString("services:\n")
	services := make([]string, 0, len(images))
	for service := range images {
		services = append(services, service)
	}
	sort.Strings(services)
	for _, service := range services {
		fmt.Fprintf(&output, "  %s:\n    image: %s\n", yamlScalar(service), yamlScalar(images[service]))
	}
	return []byte(output.String())
}

func yamlScalar(value string) string {
	encoded, _ := yaml.Marshal(value)
	return strings.TrimSpace(string(encoded))
}
func sourceMap(input *protocol.ComposeEditorInput) map[string]string {
	out := map[string]string{}
	for _, file := range input.Files {
		out[file.Path] = file.Content
	}
	return out
}
func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
func shortOperation(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
func safeService(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}
func validOperationDir(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_.:", r)) {
			return false
		}
	}
	return true
}
