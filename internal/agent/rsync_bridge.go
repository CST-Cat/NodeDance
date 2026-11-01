package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/transfer/rsyncbundle"
)

type agentRsyncSession struct {
	taskID         string
	requestID      string
	targetVirtual  string
	stagingVirtual string
	configDir      string
	cmd            *exec.Cmd
	done           chan struct{}
	leaseTimer     *time.Timer
	port           int
}

func (b *agentFileBridge) rsyncBinaryPath() (string, error) {
	b.rsyncOnce.Do(func() {
		if b.stateDir == "" {
			b.rsyncErr = errors.New("Agent state directory is unavailable")
			return
		}
		b.rsyncBin, b.rsyncErr = rsyncbundle.Install(b.stateDir)
	})
	return b.rsyncBin, b.rsyncErr
}

func rsyncSessionName(taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	return "rsync-" + hex.EncodeToString(sum[:12])
}

func (b *agentFileBridge) prepareRsyncStaging(ctx context.Context, request protocol.FileRequest) (string, string, error) {
	if b.tasks == nil {
		return "", "", errors.New("durable file task runner is unavailable")
	}
	uploadSpec, err := b.tasks.UploadSpec(ctx, request.TaskID)
	if err != nil {
		return "", "", err
	}
	if previous := b.stopRsyncSession(request.TaskID); previous != nil {
		b.cleanupRsyncSession(previous)
	}
	stagingVirtual, err := b.service.PrepareRsyncStaging(uploadSpec.Path, request.TaskID)
	if err != nil {
		return "", "", err
	}
	return uploadSpec.Path, stagingVirtual, nil
}

func (b *agentFileBridge) registerRsyncStaging(request protocol.FileRequest, id, targetPath, stagingVirtual string) *agentRsyncSession {
	done := make(chan struct{})
	close(done)
	session := &agentRsyncSession{taskID: request.TaskID, requestID: id, targetVirtual: targetPath,
		stagingVirtual: stagingVirtual, done: done}
	b.rsyncMu.Lock()
	b.rsyncSessions[request.TaskID] = session
	b.rsyncMu.Unlock()
	return session
}

func (b *agentFileBridge) rsyncReceive(ctx context.Context, id string, request protocol.FileRequest) error {
	spec := request.Rsync
	targetPath, stagingVirtual, err := b.prepareRsyncStaging(ctx, request)
	if err != nil {
		return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, err)
	}
	uploadSpec := protocol.FileTaskSpec{Path: targetPath}
	fail := func(err error) error {
		_ = b.service.RemoveRsyncStaging(uploadSpec.Path, stagingVirtual)
		return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, err)
	}
	binary, err := b.rsyncBinaryPath()
	if err != nil {
		return fail(err)
	}
	port := spec.BindPort
	if port == 0 {
		listener, listenErr := net.Listen("tcp", ":0")
		if listenErr != nil {
			return fail(listenErr)
		}
		port = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
	}
	stagingReal, err := b.service.RealPath(stagingVirtual)
	if err != nil {
		return fail(err)
	}
	configDir := filepath.Join(b.stateDir, rsyncSessionName(request.TaskID))
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fail(err)
	}
	allowed := spec.AllowedHost
	bind := ""
	if spec.LocalOnly {
		allowed = "127.0.0.1"
		bind = "127.0.0.1"
	}
	secrets := filepath.Join(configDir, "secrets")
	if err := os.WriteFile(secrets, []byte(spec.User+":"+spec.Password+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(configDir)
		return fail(err)
	}
	config := strings.Join([]string{
		"pid file = " + filepath.Join(configDir, "rsyncd.pid"),
		"lock file = " + filepath.Join(configDir, "rsyncd.lock"),
		"use chroot = no",
		"uid = " + strconv.Itoa(os.Getuid()),
		"gid = " + strconv.Itoa(os.Getgid()),
		"max connections = 1",
		"timeout = " + strconv.Itoa(spec.LeaseSeconds),
		"[xfer]",
		"path = " + stagingReal,
		"read only = no",
		"auth users = " + spec.User,
		"secrets file = " + secrets,
		"hosts allow = " + allowed,
		"hosts deny = *",
		"",
	}, "\n")
	configPath := filepath.Join(configDir, "rsyncd.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		_ = os.RemoveAll(configDir)
		return fail(err)
	}
	args := []string{"--daemon", "--no-detach", "--config", configPath, "--port", strconv.Itoa(port)}
	if bind != "" {
		args = append(args, "--address", bind)
	}
	logFile, err := os.Create(filepath.Join(configDir, "rsyncd.log"))
	if err != nil {
		_ = os.RemoveAll(configDir)
		return fail(err)
	}
	command := exec.Command(binary, args...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		_ = os.RemoveAll(configDir)
		return fail(err)
	}
	session := &agentRsyncSession{taskID: request.TaskID, requestID: id, targetVirtual: uploadSpec.Path,
		stagingVirtual: stagingVirtual, configDir: configDir, cmd: command, done: make(chan struct{}), port: port}
	go func() {
		_ = command.Wait()
		_ = logFile.Close()
		close(session.done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			break
		}
		select {
		case <-session.done:
			_ = os.RemoveAll(configDir)
			return fail(errors.New("rsync daemon exited before accepting connections"))
		default:
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			<-session.done
			_ = os.RemoveAll(configDir)
			return fail(errors.New("rsync daemon did not start listening"))
		}
		time.Sleep(50 * time.Millisecond)
	}
	session.leaseTimer = time.AfterFunc(time.Duration(spec.LeaseSeconds)*time.Second, func() {
		b.stopRsyncSession(request.TaskID)
		b.cleanupRsyncSession(session)
	})
	b.rsyncMu.Lock()
	b.rsyncSessions[request.TaskID] = session
	b.rsyncMu.Unlock()
	return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation, Port: port}, nil)
}

func (b *agentFileBridge) stopRsyncSession(taskID string) *agentRsyncSession {
	b.rsyncMu.Lock()
	session := b.rsyncSessions[taskID]
	delete(b.rsyncSessions, taskID)
	b.rsyncMu.Unlock()
	if session == nil {
		return nil
	}
	if session.leaseTimer != nil {
		session.leaseTimer.Stop()
	}
	if session.cmd != nil && session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}
	<-session.done
	return session
}

func (b *agentFileBridge) cleanupRsyncSession(session *agentRsyncSession) {
	if session == nil {
		return
	}
	_ = os.RemoveAll(session.configDir)
	_ = b.service.RemoveRsyncStaging(session.targetVirtual, session.stagingVirtual)
}

func (b *agentFileBridge) rsyncFinish(ctx context.Context, id string, request protocol.FileRequest) {
	session := b.stopRsyncSession(request.TaskID)
	if session == nil {
		b.sendFailure(ctx, id, request.Operation, "unavailable")
		return
	}
	defer b.cleanupRsyncSession(session)
	stagingReal, err := b.service.RealPath(session.stagingVirtual)
	if err != nil {
		b.sendFailure(ctx, id, request.Operation, fileErrorCode(err))
		return
	}
	staged := filepath.Join(stagingReal, path.Base(session.targetVirtual))
	info, err := os.Stat(staged)
	if err != nil || !info.Mode().IsRegular() {
		b.sendFailure(ctx, id, request.Operation, "transfer_failed")
		return
	}
	if err := b.tasks.FeedUploadFromFile(ctx, request.TaskID, staged); err != nil {
		b.sendFailure(ctx, id, request.Operation, fileErrorCode(err))
		return
	}
	if err := b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, nil); err != nil {
		b.cancel(id)
	}
}

func (b *agentFileBridge) rsyncAbort(ctx context.Context, id string, request protocol.FileRequest) error {
	if session := b.stopRsyncSession(request.TaskID); session != nil {
		b.cleanupRsyncSession(session)
	}
	return b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, nil)
}

func (b *agentFileBridge) rsyncAbortByRequest(id string) {
	b.rsyncMu.Lock()
	var taskID string
	for _, session := range b.rsyncSessions {
		if session.requestID == id {
			taskID = session.taskID
		}
	}
	b.rsyncMu.Unlock()
	if taskID != "" {
		if session := b.stopRsyncSession(taskID); session != nil {
			b.cleanupRsyncSession(session)
		}
	}
}

func (b *agentFileBridge) rsyncFetch(ctx context.Context, id string, request protocol.FileRequest) {
	spec := request.Rsync
	respondErr := func(err error) {
		if !b.isCanceled(id) {
			b.sendFailure(context.Background(), id, request.Operation, fileErrorCode(err))
		}
	}
	targetPath, stagingVirtual, err := b.prepareRsyncStaging(ctx, request)
	if err != nil {
		respondErr(err)
		return
	}
	b.registerRsyncStaging(request, id, targetPath, stagingVirtual)
	stagingReal, err := b.service.RealPath(stagingVirtual)
	if err != nil {
		respondErr(err)
		return
	}
	host := spec.Host
	if host == "" {
		host = b.coreHost
	}
	if host == "" {
		respondErr(errors.New("rsync source host is unknown"))
		return
	}
	binary, err := b.rsyncBinaryPath()
	if err != nil {
		respondErr(err)
		return
	}
	source := "rsync://" + spec.User + "@" + net.JoinHostPort(host, strconv.Itoa(spec.Port)) + "/xfer/" + spec.RemoteName
	destination := filepath.Join(stagingReal, path.Base(targetPath))
	command := exec.CommandContext(ctx, binary,
		"--timeout="+strconv.Itoa(spec.TimeoutSeconds), "--contimeout=30",
		"-t", "--ignore-times", "--no-perms", "--no-owner", "--no-group", "--", source, destination)
	command.Env = append(os.Environ(), "RSYNC_PASSWORD="+spec.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		respondErr(fmt.Errorf("rsync fetch failed: %w: %s", err, strings.TrimSpace(string(output))))
		return
	}
	if err := b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, nil); err != nil {
		b.cancel(id)
	}
}

func (b *agentFileBridge) rsyncSend(ctx context.Context, id string, request protocol.FileRequest) {
	spec := request.Rsync
	respondErr := func(err error) {
		if !b.isCanceled(id) {
			b.sendFailure(context.Background(), id, request.Operation, fileErrorCode(err))
		}
	}
	entry, err := b.service.Stat(request.Path)
	if err != nil {
		respondErr(err)
		return
	}
	if entry.Kind != "file" {
		respondErr(errors.New("rsync source is not a regular file"))
		return
	}
	source, err := b.service.RealPath(request.Path)
	if err != nil {
		respondErr(err)
		return
	}
	host := spec.Host
	if host == "" {
		host = b.coreHost
	}
	if host == "" {
		respondErr(errors.New("rsync destination host is unknown"))
		return
	}
	binary, err := b.rsyncBinaryPath()
	if err != nil {
		respondErr(err)
		return
	}
	target := "rsync://" + spec.User + "@" + net.JoinHostPort(host, strconv.Itoa(spec.Port)) + "/xfer/" + spec.RemoteName
	command := exec.CommandContext(ctx, binary,
		"--timeout="+strconv.Itoa(spec.TimeoutSeconds), "--contimeout=30",
		"-t", "--ignore-times", "--no-perms", "--no-owner", "--no-group", "--", source, target)
	command.Env = append(os.Environ(), "RSYNC_PASSWORD="+spec.Password)
	output, err := command.CombinedOutput()
	if err != nil {
		respondErr(fmt.Errorf("rsync send failed: %w: %s", err, strings.TrimSpace(string(output))))
		return
	}
	if err := b.respond(ctx, id, protocol.FileResponse{Operation: request.Operation}, nil); err != nil {
		b.cancel(id)
	}
}
