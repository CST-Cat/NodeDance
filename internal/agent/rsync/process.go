package rsync

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/filetransport"
	"github.com/CST-Cat/NodeDance/internal/transfer/rsyncbundle"
	"github.com/coder/websocket"
)

const shellCommand = "__nodedance-rsync-shell"
const serverCommand = "__nodedance-rsync-server"

var progressLine = regexp.MustCompile(`^\s*([0-9,]+)\s+([0-9]+)%\s`)

func Available(stateDirectory string) (string, func(), error) {
	if err := confinementAvailable(); err != nil {
		return "", nil, err
	}
	binary, cleanup, err := rsyncbundle.Install(stateDirectory)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0700 {
		cleanup()
		return "", nil, errors.New("bundled rsync executable is not protected")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || stat.Nlink != 1 {
		cleanup()
		return "", nil, errors.New("bundled rsync executable must be root-owned with one link")
	}
	if err := localstate.Directory(filepath.Dir(binary)); err != nil {
		cleanup()
		return "", nil, err
	}
	return binary, cleanup, nil
}

func PrintVersion(ctx context.Context, output io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("bundled rsync requires the root Agent")
	}
	state, err := os.MkdirTemp("/tmp", "nodedance-rsync-version-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	binary, cleanup, err := rsyncbundle.Install(state)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	cmd.Env = []string{"PATH=", "LC_ALL=C"}
	cmd.Stdout, cmd.Stderr = output, io.Discard
	configureProcess(cmd)
	return cmd.Run()
}

func Internal(args []string) (bool, error) {
	if len(args) == 0 || args[0] != shellCommand && args[0] != serverCommand {
		return false, nil
	}
	if os.Geteuid() != 0 {
		return true, errors.New("rsync task helpers require the root Agent")
	}
	if args[0] == shellCommand {
		return true, runShell(args[1:])
	}
	return true, runConfined(args[1:])
}

func runShell(args []string) error {
	if len(args) < 4 || args[1] != "nodedance" || args[2] != "rsync" || args[3] != "--server" {
		return errors.New("invalid rsync shell invocation")
	}
	connection, err := net.DialTimeout("unix", args[0], 5*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	data, err := json.Marshal(args[3:])
	if err != nil || len(data) > 32<<10 {
		return errors.New("invalid rsync server arguments")
	}
	if _, err := connection.Write(append(data, '\n')); err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(connection, os.Stdin)
		_ = connection.(*net.UnixConn).CloseWrite()
	}()
	_, err = io.Copy(os.Stdout, connection)
	return err
}

func clientCommand(ctx context.Context, binary, socket string, source *os.File, bandwidth int64, output io.Writer) (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	remoteShell := quote(executable) + " " + shellCommand + " " + quote(socket)
	args := []string{"--inplace", "--partial", "--no-whole-file", "--checksum", "--copy-links", "--no-perms", "--no-owner", "--no-group", "--info=progress2", "--outbuf=L"}
	if bandwidth != 0 {
		args = append(args, "--bwlimit="+strconv.FormatInt(bandwidth, 10)+"B")
	}
	args = append(args, "-e", remoteShell, "--", "/proc/self/fd/3", "nodedance:payload")
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.ExtraFiles = []*os.File{source}
	cmd.Env = []string{"PATH=", "LC_ALL=C"}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	configureProcess(cmd)
	return cmd, nil
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
}

func openShellSocket(stateDir string) (string, *net.UnixListener, func(), error) {
	directory, err := os.MkdirTemp(stateDir, "rsync-shell-")
	if err != nil {
		return "", nil, nil, err
	}
	root, err := os.Open(directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", nil, nil, err
	}
	name := "/proc/" + strconv.Itoa(os.Getpid()) + "/fd/" + strconv.FormatUint(uint64(root.Fd()), 10) + "/socket"
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		_ = root.Close()
		_ = os.RemoveAll(directory)
		return "", nil, nil, err
	}
	cleanup := func() { _ = listener.Close(); _ = root.Close(); _ = os.RemoveAll(directory) }
	return name, listener, cleanup, nil
}

func readServerArgs(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 32<<10 {
		return nil, errors.New("rsync server arguments exceed their bound")
	}
	var args []string
	if json.Unmarshal(line, &args) != nil || len(args) < 3 || len(args) > 32 || args[0] != "--server" {
		return nil, errors.New("invalid rsync server argument template")
	}
	return args, nil
}

func ProbeReceiver(ctx context.Context, binary, stateDir string, bandwidth int64) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	socket, listener, cleanup, err := openShellSocket(stateDir)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stopListener := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stopListener()
	source, err := os.CreateTemp(stateDir, "rsync-probe-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(source.Name())
	defer source.Close()
	cmd, err := clientCommand(ctx, binary, socket, source, bandwidth, io.Discard)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); _ = listener.Close() }()
	connection, err := listener.AcceptUnix()
	if err != nil {
		cancel()
		<-done
		return nil, err
	}
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	args, err := readServerArgs(bufio.NewReaderSize(connection, 32<<10))
	_ = connection.Close()
	cancel()
	<-done
	if err != nil {
		return nil, err
	}
	if err := ValidateServerArgs(args, args); err != nil {
		return nil, err
	}
	return args, nil
}

func ValidateServerArgs(args, template []string) error {
	if len(template) < 4 || template[0] != "--server" || template[len(template)-2] != "." || template[len(template)-1] != "payload" || !reflect.DeepEqual(args, template) {
		return errors.New("rsync server arguments differ from the target runtime probe")
	}
	for _, arg := range args {
		if strings.ContainsAny(arg, "\x00\r\n") || strings.HasPrefix(arg, "--delete") || strings.HasPrefix(arg, "--remove") || arg == "--sender" || strings.HasPrefix(arg, "--daemon") || strings.HasPrefix(arg, "--files-from") || strings.HasPrefix(arg, "--log-file") || strings.HasPrefix(arg, "--backup") {
			return errors.New("rsync server argument is forbidden")
		}
	}
	return nil
}

func confinedCommand(ctx context.Context, binary string, directory, payload *os.File, size int64, args []string) (*exec.Cmd, func(), error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	rs, err := os.Open(binary)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, executable, append([]string{serverCommand, strconv.FormatInt(size, 10)}, args...)...)
	cmd.ExtraFiles = []*os.File{directory, payload, rs}
	cmd.Env = []string{"PATH=", "LC_ALL=C"}
	cmd.Stderr = io.Discard
	configureProcess(cmd)
	return cmd, func() { _ = rs.Close() }, nil
}

func ProbeConfinement(ctx context.Context, binary string, directory, payload *os.File, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, cleanup, err := confinedCommand(ctx, binary, directory, payload, size, []string{"--version"})
	if err != nil {
		return err
	}
	defer cleanup()
	return cmd.Run()
}

func Receive(ctx context.Context, binary string, directory, payload *os.File, size int64, template []string, conn filetransport.Connection) error {
	handshake, cancelHandshake := context.WithTimeout(ctx, 10*time.Second)
	defer cancelHandshake()
	conn.SetReadLimit(32 << 10)
	kind, data, err := conn.Read(handshake)
	if err != nil {
		return err
	}
	var args []string
	if kind != websocket.MessageText || json.Unmarshal(data, &args) != nil || ValidateServerArgs(args, template) != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, "rsync server arguments rejected")
		return errors.New("rsync server arguments rejected")
	}
	cmd, cleanup, err := confinedCommand(ctx, binary, directory, payload, size, args)
	if err != nil {
		return err
	}
	defer cleanup()
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stream := filetransport.New(ctx, conn)
	cmd.Stdout = stream
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := conn.Write(handshake, websocket.MessageText, []byte("OK")); err != nil {
		_ = cmd.Cancel()
		_ = input.Close()
		_ = cmd.Wait()
		return err
	}
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(input, stream)
		_ = input.Close()
		copyDone <- err
	}()
	processErr := cmd.Wait()
	eofErr := stream.CloseWrite()
	_ = input.Close()
	if processErr != nil {
		return fmt.Errorf("rsync receiver failed: %w", processErr)
	}
	if eofErr != nil {
		return eofErr
	}
	select {
	case err := <-copyDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

type progressWriter struct {
	mu     sync.Mutex
	buffer []byte
	last   time.Time
	notify func(uint64)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buffer = append(w.buffer, data...)
	for {
		end := bytes.IndexAny(w.buffer, "\r\n")
		if end < 0 {
			break
		}
		line := w.buffer[:end]
		if matched := progressLine.FindSubmatch(line); matched != nil && time.Since(w.last) >= 250*time.Millisecond {
			completed, err := strconv.ParseUint(strings.ReplaceAll(string(matched[1]), ",", ""), 10, 64)
			if err == nil && w.notify != nil {
				w.last = time.Now()
				w.notify(completed)
			}
		}
		w.buffer = w.buffer[end+1:]
	}
	if len(w.buffer) > 4096 {
		w.buffer = nil
	}
	return len(data), nil
}

func Send(ctx context.Context, binary, stateDir string, source *os.File, bandwidth int64, conn filetransport.Connection, onProgress func(uint64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	socket, listener, cleanup, err := openShellSocket(stateDir)
	if err != nil {
		return err
	}
	defer cleanup()
	stop := context.AfterFunc(ctx, func() { _ = listener.Close(); _ = conn.CloseNow() })
	defer stop()
	cmd, err := clientCommand(ctx, binary, socket, source, bandwidth, &progressWriter{notify: onProgress})
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); _ = listener.Close() }()
	waitOnError := func(err error) error { cancel(); <-done; return err }
	local, err := listener.AcceptUnix()
	if err != nil {
		return waitOnError(err)
	}
	defer local.Close()
	stopLocal := context.AfterFunc(ctx, func() { _ = local.Close() })
	defer stopLocal()
	_ = local.SetReadDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReaderSize(local, 32<<10)
	args, err := readServerArgs(reader)
	_ = local.SetReadDeadline(time.Time{})
	if err != nil {
		return waitOnError(err)
	}
	handshake, cancelHandshake := context.WithTimeout(ctx, 10*time.Second)
	data, _ := json.Marshal(args)
	err = conn.Write(handshake, websocket.MessageText, data)
	if err == nil {
		var kind websocket.MessageType
		var reply []byte
		kind, reply, err = conn.Read(handshake)
		if err == nil && (kind != websocket.MessageText || string(reply) != "OK") {
			err = errors.New("target rejected rsync arguments")
		}
	}
	cancelHandshake()
	if err != nil {
		return waitOnError(err)
	}
	stream := filetransport.New(ctx, conn)
	copies := make(chan error, 2)
	go func() {
		_, err := io.Copy(stream, reader)
		if err == nil {
			err = stream.CloseWrite()
		}
		copies <- err
	}()
	go func() {
		_, err := io.Copy(local, stream)
		_ = local.CloseWrite()
		copies <- err
	}()
	for range 2 {
		if err := <-copies; err != nil {
			return waitOnError(err)
		}
	}
	if err := <-done; err != nil {
		return fmt.Errorf("rsync sender failed: %w", err)
	}
	return ctx.Err()
}
