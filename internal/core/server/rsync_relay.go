package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CST-Cat/NodeDance/internal/transfer/rsyncbundle"
)

const rsyncRelayPortSpan = 32

type rsyncRelaySession struct {
	port       int
	dir        string
	stagingDir string
	cmd        *exec.Cmd
	done       chan struct{}
	leaseTimer *time.Timer
}

type rsyncRelayManager struct {
	server     *Server
	mu         sync.Mutex
	sessions   map[int]*rsyncRelaySession
	binaryOnce sync.Once
	binary     string
	binaryErr  error
}

func newRsyncRelayManager(server *Server) *rsyncRelayManager {
	return &rsyncRelayManager{server: server, sessions: make(map[int]*rsyncRelaySession)}
}

func (m *rsyncRelayManager) binaryPath() (string, error) {
	m.binaryOnce.Do(func() {
		m.binary, m.binaryErr = rsyncbundle.Install(m.server.dataDir)
	})
	return m.binary, m.binaryErr
}

func (m *rsyncRelayManager) openReceive(taskKey, allowedHost, user, password string, leaseSeconds int) (*rsyncRelaySession, error) {
	sum := sha256.Sum256([]byte(taskKey))
	dir := filepath.Join(m.server.dataDir, "transfers", hex.EncodeToString(sum[:12]))
	staging := filepath.Join(dir, "files")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return nil, err
	}
	return m.open(dir, staging, false, allowedHost, user, password, leaseSeconds)
}

func (m *rsyncRelayManager) openServe(taskKey, stagingDir, allowedHost, user, password string, leaseSeconds int) (*rsyncRelaySession, error) {
	sum := sha256.Sum256([]byte(taskKey + "-serve"))
	dir := filepath.Join(m.server.dataDir, "transfers", hex.EncodeToString(sum[:12])+"-serve")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return m.open(dir, stagingDir, true, allowedHost, user, password, leaseSeconds)
}

func (m *rsyncRelayManager) open(dir, staging string, readOnly bool, allowedHost, user, password string, leaseSeconds int) (*rsyncRelaySession, error) {
	binary, err := m.binaryPath()
	if err != nil {
		return nil, err
	}
	secrets := filepath.Join(dir, "secrets")
	if err := os.WriteFile(secrets, []byte(user+":"+password+"\n"), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	readOnlyValue := "no"
	if readOnly {
		readOnlyValue = "yes"
	}
	config := strings.Join([]string{
		"pid file = " + filepath.Join(dir, "rsyncd.pid"),
		"lock file = " + filepath.Join(dir, "rsyncd.lock"),
		"use chroot = no",
		"uid = " + strconv.Itoa(os.Getuid()),
		"gid = " + strconv.Itoa(os.Getgid()),
		"max connections = 1",
		"timeout = " + strconv.Itoa(leaseSeconds),
		"[xfer]",
		"path = " + staging,
		"read only = " + readOnlyValue,
		"auth users = " + user,
		"secrets file = " + secrets,
		"hosts allow = " + allowedHost,
		"hosts deny = *",
		"",
	}, "\n")
	configPath := filepath.Join(dir, "rsyncd.conf")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	base := m.server.fileTransferPort
	for offset := 0; offset < rsyncRelayPortSpan; offset++ {
		port := base + offset
		m.mu.Lock()
		_, taken := m.sessions[port]
		m.mu.Unlock()
		if taken {
			continue
		}
		logFile, err := os.Create(filepath.Join(dir, "rsyncd.log"))
		if err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
		command := exec.Command(binary, "--daemon", "--no-detach", "--config", configPath, "--port", strconv.Itoa(port))
		command.Stdout = logFile
		command.Stderr = logFile
		if err := command.Start(); err != nil {
			_ = logFile.Close()
			continue
		}
		session := &rsyncRelaySession{port: port, dir: dir, stagingDir: staging, cmd: command, done: make(chan struct{})}
		go func() {
			_ = command.Wait()
			_ = logFile.Close()
			close(session.done)
		}()
		if !waitRsyncListener(session, port) {
			continue
		}
		session.leaseTimer = time.AfterFunc(time.Duration(leaseSeconds)*time.Second, func() {
			m.discard(port)
		})
		m.mu.Lock()
		m.sessions[port] = session
		m.mu.Unlock()
		return session, nil
	}
	_ = os.RemoveAll(dir)
	return nil, errors.New("no relay port is available")
}

func waitRsyncListener(session *rsyncRelaySession, port int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return true
		}
		select {
		case <-session.done:
			return false
		default:
		}
		if time.Now().After(deadline) {
			_ = session.cmd.Process.Kill()
			<-session.done
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *rsyncRelayManager) stop(port int) *rsyncRelaySession {
	m.mu.Lock()
	session := m.sessions[port]
	delete(m.sessions, port)
	m.mu.Unlock()
	if session == nil {
		return nil
	}
	if session.leaseTimer != nil {
		session.leaseTimer.Stop()
	}
	if session.cmd.Process != nil {
		_ = session.cmd.Process.Kill()
	}
	<-session.done
	return session
}

func (m *rsyncRelayManager) discard(port int) {
	if session := m.stop(port); session != nil {
		_ = os.RemoveAll(session.dir)
	}
}

func (m *rsyncRelayManager) closeAll() {
	m.mu.Lock()
	ports := make([]int, 0, len(m.sessions))
	for port := range m.sessions {
		ports = append(ports, port)
	}
	m.mu.Unlock()
	for _, port := range ports {
		m.discard(port)
	}
}

func (m *rsyncRelayManager) send(ctx context.Context, stagedFile, host string, port int, user, password, remoteName string, timeoutSeconds int) error {
	binary, err := m.binaryPath()
	if err != nil {
		return err
	}
	target := "rsync://" + user + "@" + net.JoinHostPort(host, strconv.Itoa(port)) + "/xfer/" + remoteName
	command := exec.CommandContext(ctx, binary,
		"--timeout="+strconv.Itoa(timeoutSeconds), "--contimeout=30",
		"-t", "--no-perms", "--no-owner", "--no-group", "--", stagedFile, target)
	command.Env = append(os.Environ(), "RSYNC_PASSWORD="+password)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("rsync relay send failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
