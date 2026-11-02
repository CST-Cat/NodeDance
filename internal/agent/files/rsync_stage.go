package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"reflect"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/agent/taskjournal"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/taskstate"
	"golang.org/x/sys/unix"
)

type RsyncStage struct {
	Directory *os.File
	Payload   *os.File
}

type transferContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r transferContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func (stage *RsyncStage) Close() {
	_ = stage.Payload.Close()
	_ = stage.Directory.Close()
}

func (e *TaskExecutor) ConfigureTransfers(stateDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("file transfers require the root Agent")
	}
	if err := localstate.Directory(stateDir); err != nil {
		return err
	}
	fd, err := unix.Open(stateDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), stateDir)
	info, err := root.Stat()
	if err != nil || !privateStageInfo(info, true) {
		_ = root.Close()
		return errors.New("file transfer state must be owner-protected")
	}
	e.stateRoot, e.stateDir = root, stateDir
	return nil
}

func (e *TaskExecutor) Close() error {
	if e == nil || e.stateRoot == nil {
		return nil
	}
	return e.stateRoot.Close()
}

func privateStageInfo(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	uid, _ := ownerIDs(info)
	if uid != 0 {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && info.Mode().IsRegular() && info.Mode().Perm() == 0600
}

func (e *TaskExecutor) stateOpen(name string, flags int, mode uint32) (*os.File, error) {
	if e.stateRoot == nil {
		return nil, errors.New("private file transfer state is unavailable")
	}
	fd, err := openAt2(int(e.stateRoot.Fd()), name, flags, mode)
	if err != nil {
		return nil, safePathError(err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (e *TaskExecutor) stageDirectory(taskID string, create bool) (*os.File, error) {
	if e.stateRoot == nil {
		return nil, errors.New("private file transfer state is unavailable")
	}
	if create {
		if err := unix.Mkdirat(int(e.stateRoot.Fd()), "file-transfers", 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	parent, err := e.stateOpen("file-transfers", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	info, _ := parent.Stat()
	if !privateStageInfo(info, true) {
		return nil, ErrInvalidPath
	}
	if create {
		if err := unix.Mkdirat(int(parent.Fd()), taskID, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
	}
	directory, err := e.stateOpen(path.Join("file-transfers", taskID), unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	info, _ = directory.Stat()
	if !privateStageInfo(info, true) {
		_ = directory.Close()
		return nil, ErrInvalidPath
	}
	return directory, nil
}

func stageRecord(directory *os.File) (protocol.FileTransferDescriptor, error) {
	fd, err := openAt2(int(directory.Fd()), "descriptor.json", unix.O_RDONLY, 0)
	if err != nil {
		return protocol.FileTransferDescriptor{}, err
	}
	file := os.NewFile(uintptr(fd), "descriptor.json")
	defer file.Close()
	info, _ := file.Stat()
	if !privateStageInfo(info, false) {
		return protocol.FileTransferDescriptor{}, ErrInvalidPath
	}
	var descriptor protocol.FileTransferDescriptor
	data, err := io.ReadAll(io.LimitReader(file, 32<<10))
	if err != nil || json.Unmarshal(data, &descriptor) != nil || !protocol.ValidFileTransferDescriptor(descriptor) {
		return protocol.FileTransferDescriptor{}, ErrInvalidPath
	}
	return descriptor, nil
}

func sameTransferContent(left, right protocol.FileTransferDescriptor) bool {
	if left.File.Transfer == nil || right.File.Transfer == nil {
		return false
	}
	l, r := left.File, right.File
	lt, rt := *l.Transfer, *r.Transfer
	lt.RetryOfTaskID, rt.RetryOfTaskID = "", ""
	l.Transfer, r.Transfer = &lt, &rt
	return left.TargetNodeID == right.TargetNodeID && reflect.DeepEqual(l, r)
}

func (e *TaskExecutor) openRsyncStage(ctx context.Context, descriptor protocol.FileTransferDescriptor) (*RsyncStage, error) {
	directory, err := e.stageDirectory(descriptor.TaskID, true)
	if err != nil {
		return nil, err
	}
	var payload *os.File
	ok := false
	defer func() {
		if !ok {
			_ = directory.Close()
			if payload != nil {
				_ = payload.Close()
			}
		}
	}()
	record, recordErr := stageRecord(directory)
	if errors.Is(recordErr, os.ErrNotExist) {
		fd, err := openAt2(int(directory.Fd()), "descriptor.json", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), "descriptor.json")
		data, _ := json.Marshal(descriptor)
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		_ = file.Close()
		if err != nil {
			return nil, err
		}
	} else if recordErr != nil || !reflect.DeepEqual(record, descriptor) {
		return nil, ErrInvalidPath
	}
	fd, err := openAt2(int(directory.Fd()), "payload", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = openAt2(int(directory.Fd()), "payload", unix.O_RDWR, 0)
	}
	if err != nil {
		return nil, err
	}
	payload = os.NewFile(uintptr(fd), "payload")
	info, _ := payload.Stat()
	if !privateStageInfo(info, false) || info.Size() > descriptor.File.Size {
		return nil, ErrInvalidPath
	}
	if descriptor.File.Transfer.RetryOfTaskID != "" && info.Size() == 0 {
		if err := e.adoptPartial(ctx, descriptor, payload); err != nil {
			return nil, err
		}
	}
	if err := directory.Sync(); err != nil {
		return nil, err
	}
	ok = true
	return &RsyncStage{Directory: directory, Payload: payload}, nil
}

func (e *TaskExecutor) adoptPartial(ctx context.Context, descriptor protocol.FileTransferDescriptor, payload *os.File) error {
	oldID := descriptor.File.Transfer.RetryOfTaskID
	oldTask, err := e.journal.Get(context.Background(), oldID)
	if err != nil || oldTask.NodeID != descriptor.TargetNodeID || oldTask.Status != taskstate.Failed && oldTask.Status != taskstate.Canceled && oldTask.Status != taskstate.TimedOut {
		return errors.New("partial retry requires a confirmed failed or canceled task")
	}
	directory, err := e.stageDirectory(oldID, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	old, err := stageRecord(directory)
	if err != nil || old.TaskID != oldID || !sameTransferContent(old, descriptor) {
		return ErrInvalidPath
	}
	identity, err := protocol.TaskIdentity(oldID, oldTask.NodeID, oldTask.IdempotencyKey, protocol.TaskIntent{Action: protocol.TaskFileUpload, ContainerID: protocol.FileTargetKey(old.File.Path), File: &old.File})
	if err != nil {
		return err
	}
	digest, err := taskstate.RequestDigest(identity)
	if err != nil || digest != oldTask.RequestDigest {
		return ErrInvalidPath
	}
	fd, err := openAt2(int(directory.Fd()), "payload", unix.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	previous := os.NewFile(uintptr(fd), "partial")
	defer previous.Close()
	info, _ := previous.Stat()
	if !privateStageInfo(info, false) || info.Size() > descriptor.File.Size {
		return ErrInvalidPath
	}
	if _, err := io.Copy(payload, transferContextReader{ctx: ctx, reader: io.LimitReader(previous, descriptor.File.Size+1)}); err != nil {
		return err
	}
	return payload.Sync()
}

func (e *TaskExecutor) AcquireRsync(ctx context.Context, descriptor protocol.FileTransferDescriptor) (context.Context, *RsyncStage, func(), error) {
	if !protocol.ValidFileTransferDescriptor(descriptor) {
		return nil, nil, nil, ErrInvalidPath
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		e.mu.Lock()
		pending := e.uploads[descriptor.TaskID]
		if pending != nil {
			if pending.used || pending.committing || pending.ctx.Err() != nil || pending.nodeID != descriptor.TargetNodeID || !reflect.DeepEqual(pending.spec, descriptor.File) || pending.attemptDone != nil {
				e.mu.Unlock()
				return nil, nil, nil, errors.New("rsync descriptor does not match a ready upload task")
			}
			attemptCtx, cancel := context.WithCancel(pending.ctx)
			stop := context.AfterFunc(ctx, cancel)
			done := make(chan struct{})
			pending.attemptDone = done
			release := func() {
				stop()
				cancel()
				e.mu.Lock()
				pending.attemptDone = nil
				close(done)
				e.mu.Unlock()
			}
			e.mu.Unlock()
			if pending.stage == nil {
				stage, err := e.openRsyncStage(attemptCtx, descriptor)
				if err != nil {
					release()
					return nil, nil, nil, err
				}
				e.mu.Lock()
				pending.stage = stage
				e.mu.Unlock()
			}
			return attemptCtx, pending.stage, release, nil
		}
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, nil, ctx.Err()
		case <-timer.C:
			return nil, nil, nil, errors.New("rsync upload task is not ready")
		case <-ticker.C:
		}
	}
}

func (e *TaskExecutor) CommitRsync(ctx context.Context, descriptor protocol.FileTransferDescriptor, transport string) error {
	if transport != "direct" && transport != "relay" {
		return errors.New("invalid rsync commit transport")
	}
	e.mu.Lock()
	pending := e.uploads[descriptor.TaskID]
	if pending == nil || pending.stage == nil || pending.attemptDone != nil || pending.used || pending.committing || pending.nodeID != descriptor.TargetNodeID || !reflect.DeepEqual(pending.spec, descriptor.File) || pending.ctx.Err() != nil {
		e.mu.Unlock()
		return errors.New("rsync upload is not ready for commit")
	}
	stage := pending.stage
	pending.transport = "rsync_" + transport
	pending.committing = true
	e.mu.Unlock()
	commitParent := ctx
	ctx, cancel := context.WithCancel(pending.ctx)
	defer cancel()
	stop := context.AfterFunc(commitParent, cancel)
	defer stop()
	fail := func(err error) error {
		request := protocol.FileRequest{Operation: protocol.FileUploadBegin, Path: descriptor.File.Path, ExpectedVersion: descriptor.File.ExpectedVersion, Size: descriptor.File.Size, SHA256: descriptor.File.SHA256}
		if writer, attachErr := e.attachUpload(ctx, descriptor.TaskID, request, true); attachErr == nil {
			_ = writer.CloseWithError(err)
		}
		return err
	}
	if err := stage.Payload.Sync(); err != nil {
		return fail(err)
	}
	if _, err := stage.Payload.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, transferContextReader{ctx: ctx, reader: io.LimitReader(stage.Payload, descriptor.File.Size+1)})
	if err != nil {
		return fail(err)
	}
	if n != descriptor.File.Size || hex.EncodeToString(hash.Sum(nil)) != descriptor.File.SHA256 {
		return fail(ErrTransferDigest)
	}
	if _, err := stage.Payload.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	request := protocol.FileRequest{Operation: protocol.FileUploadBegin, Path: descriptor.File.Path, ExpectedVersion: descriptor.File.ExpectedVersion, Size: descriptor.File.Size, SHA256: descriptor.File.SHA256}
	writer, err := e.attachUpload(ctx, descriptor.TaskID, request, true)
	if err != nil {
		return err
	}
	stopWriter := context.AfterFunc(ctx, func() { _ = writer.CloseWithError(ctx.Err()) })
	defer stopWriter()
	_, err = io.Copy(writer, transferContextReader{ctx: ctx, reader: io.LimitReader(stage.Payload, descriptor.File.Size+1)})
	if err != nil {
		_ = writer.CloseWithError(err)
		return err
	}
	return writer.Close()
}

func (e *TaskExecutor) removeRsyncStage(taskID string) {
	directory, err := e.stageDirectory(taskID, false)
	if err != nil {
		return
	}
	_ = unix.Unlinkat(int(directory.Fd()), "payload", 0)
	_ = unix.Unlinkat(int(directory.Fd()), "descriptor.json", 0)
	_ = directory.Close()
	parent, err := e.stateOpen("file-transfers", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err == nil {
		_ = unix.Unlinkat(int(parent.Fd()), taskID, unix.AT_REMOVEDIR)
		_ = parent.Close()
	}
}

func (e *TaskExecutor) reconcileTransfer(ctx context.Context, task taskjournal.Snapshot, spec protocol.FileTaskSpec) (taskjournal.Snapshot, error) {
	e.mu.Lock()
	active := e.uploads[task.TaskID] != nil
	e.mu.Unlock()
	if active {
		return task, errors.New("file transfer is still active")
	}
	entry, err := e.service.Stat(spec.Path)
	if errors.Is(err, os.ErrNotExist) && spec.ExpectedVersion == "" || err == nil && entry.Kind == "file" && entry.Version == spec.ExpectedVersion {
		return e.finish(ctx, task.TaskID, taskstate.Failed, "unchanged", "interrupted")
	}
	if err != nil {
		return task, err
	}
	if entry.Kind != "file" || entry.Size != spec.Size {
		return task, nil
	}
	name, _, err := e.service.normalize(spec.Path)
	if err != nil {
		return task, err
	}
	verified, err := e.service.verifyFileDigest(name, spec.Size, spec.SHA256)
	if err != nil || !verified {
		return task, err
	}
	final, err := e.finish(ctx, task.TaskID, taskstate.Succeeded, "transfer_verified", spec.SHA256)
	if err == nil {
		e.removeRsyncStage(task.TaskID)
	}
	return final, err
}
