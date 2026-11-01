package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/CST-Cat/NodeDance/internal/agent/transferbuffer"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/transfer/rsyncbundle"
)

var rsyncStreamFlags = []string{"-t", "--ignore-times", "--no-perms", "--no-owner", "--no-group", "--timeout=120"}

type pendingStreamData struct {
	data  []byte
	final bool
}

type rsyncArgvOffer struct {
	args []string
	conn net.Conn
}

type rsyncStreamSession struct {
	id          string
	key         string
	opID        string
	cancel      context.CancelFunc
	inbound     chan []byte
	inboundOnce sync.Once
	nextInSeq   uint64
	finReceived bool
	registering bool

	pipe           *io.PipeWriter
	stagingVirtual string
	stagedReal     string
	startChan      chan []string

	argvChan chan rsyncArgvOffer
	goChan   chan struct{}
	goOnce   sync.Once
	opSeq    uint64
	conn     net.Conn
}

func (s *rsyncStreamSession) closeInbound() {
	s.inboundOnce.Do(func() { close(s.inbound) })
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

func (b *agentFileBridge) rsyncServerTemplate() ([]string, error) {
	b.rsyncTemplateOnce.Do(func() {
		binary, err := b.rsyncBinaryPath()
		if err != nil {
			b.rsyncTemplateErr = err
			return
		}
		executable, err := os.Executable()
		if err != nil {
			b.rsyncTemplateErr = err
			return
		}
		dir, err := os.MkdirTemp("", "nd-rsync-probe-")
		if err != nil {
			b.rsyncTemplateErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		source := filepath.Join(dir, "probe")
		if err := os.WriteFile(source, nil, 0o600); err != nil {
			b.rsyncTemplateErr = err
			return
		}
		outFile := filepath.Join(dir, "argv.json")
		wrapperPath := filepath.Join(dir, "rsh")
		script := "#!/bin/sh\nexec " + shellQuote(executable) + " rsync-argv \"$@\"\n"
		if err := os.WriteFile(wrapperPath, []byte(script), 0o700); err != nil {
			b.rsyncTemplateErr = err
			return
		}
		args := append(append([]string{}, rsyncStreamFlags...), "-e", wrapperPath, "--", source, "nd:probe")
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "ND_RSYNC_ARGV_OUT="+outFile)
		_ = cmd.Run()
		data, err := os.ReadFile(outFile)
		if err != nil {
			b.rsyncTemplateErr = errors.New("rsync server argument probe failed")
			return
		}
		var argv []string
		if err := json.Unmarshal(data, &argv); err != nil || len(argv) < 6 || argv[1] != "rsync" || argv[len(argv)-1] != "probe" {
			b.rsyncTemplateErr = errors.New("rsync server argument probe failed")
			return
		}
		hasServer := false
		for _, arg := range argv[1 : len(argv)-1] {
			if arg == "--server" {
				hasServer = true
			}
		}
		if !hasServer {
			b.rsyncTemplateErr = errors.New("rsync server argument probe failed")
			return
		}
		b.rsyncTemplate = argv[1:]
	})
	return b.rsyncTemplate, b.rsyncTemplateErr
}

func buildRsyncServerArgs(proposed, template []string, stagedReal string) ([]string, error) {
	if len(proposed) != len(template) {
		return nil, errors.New("rsync server arguments are invalid")
	}
	for index := 0; index < len(template)-1; index++ {
		if proposed[index] != template[index] {
			return nil, errors.New("rsync server arguments do not match the expected transfer profile")
		}
	}
	argv := append([]string(nil), proposed[:len(proposed)-1]...)
	return append(argv, stagedReal), nil
}

func (b *agentFileBridge) startRsyncServer(request protocol.FileRequest) error {
	b.streamMu.Lock()
	session := b.streamSessions[request.TaskID+"/t"]
	b.streamMu.Unlock()
	if session == nil || session.startChan == nil {
		return errors.New("rsync receive session is unavailable")
	}
	template, err := b.rsyncServerTemplate()
	if err != nil {
		return err
	}
	argv, err := buildRsyncServerArgs(request.Args, template, session.stagedReal)
	if err != nil {
		return err
	}
	select {
	case session.startChan <- argv:
		return nil
	default:
		return errors.New("rsync server was already started")
	}
}

func (b *agentFileBridge) signalRsyncGo(taskID string) error {
	b.streamMu.Lock()
	session := b.streamSessions[taskID+"/s"]
	b.streamMu.Unlock()
	if session == nil || session.goChan == nil {
		return errors.New("rsync send session is unavailable")
	}
	session.goOnce.Do(func() { close(session.goChan) })
	return nil
}

func (b *agentFileBridge) deliverStreamFrame(ctx context.Context, frame protocol.StreamFrame) error {
	data, err := protocol.DecodeStreamData(frame)
	if err != nil {
		return err
	}
	b.streamMu.Lock()
	if _, dead := b.streamDead[frame.StreamID]; dead {
		b.streamMu.Unlock()
		return nil
	}
	session := b.streamSessions[frame.StreamID]
	if session == nil || session.registering {
		if frame.Sequence != b.streamPendingN[frame.StreamID]+1 || len(b.streamPending[frame.StreamID]) >= 256 {
			b.streamMu.Unlock()
			return errors.New("stream frame arrived out of order for an unknown stream")
		}
		b.streamPendingN[frame.StreamID] = frame.Sequence
		b.streamPending[frame.StreamID] = append(b.streamPending[frame.StreamID], pendingStreamData{data: data, final: frame.Final})
		b.streamMu.Unlock()
		return nil
	}
	if session.finReceived || frame.Sequence != session.nextInSeq {
		b.streamMu.Unlock()
		return errors.New("stream frame sequence is invalid")
	}
	session.nextInSeq++
	if frame.Final {
		session.finReceived = true
	}
	b.streamMu.Unlock()
	if frame.Final {
		session.closeInbound()
		return nil
	}
	select {
	case session.inbound <- data:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *agentFileBridge) registerStreamSession(ctx context.Context, session *rsyncStreamSession) error {
	b.streamMu.Lock()
	if existing := b.streamSessions[session.key]; existing != nil && existing != session && existing.cancel != nil {
		existing.cancel()
	}
	session.registering = true
	b.streamSessions[session.key] = session
	b.streamOps[session.opID] = session
	b.streamMu.Unlock()
	for {
		b.streamMu.Lock()
		pending := b.streamPending[session.key]
		b.streamPending[session.key] = nil
		lastSeq := b.streamPendingN[session.key]
		if len(pending) == 0 {
			session.nextInSeq = lastSeq + 1
			session.registering = false
			delete(b.streamPending, session.key)
			delete(b.streamPendingN, session.key)
			b.streamMu.Unlock()
			return nil
		}
		b.streamMu.Unlock()
		fin := false
		for _, entry := range pending {
			if entry.final {
				fin = true
				break
			}
			select {
			case session.inbound <- entry.data:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if fin {
			b.streamMu.Lock()
			session.finReceived = true
			session.nextInSeq = lastSeq + 1
			session.registering = false
			delete(b.streamPending, session.key)
			delete(b.streamPendingN, session.key)
			b.streamMu.Unlock()
			session.closeInbound()
			return nil
		}
	}
}

func (b *agentFileBridge) unregisterStreamSession(session *rsyncStreamSession) {
	b.streamMu.Lock()
	if b.streamSessions[session.key] == session {
		delete(b.streamSessions, session.key)
	}
	if b.streamOps[session.opID] == session {
		delete(b.streamOps, session.opID)
	}
	delete(b.streamPending, session.key)
	delete(b.streamPendingN, session.key)
	if _, dead := b.streamDead[session.key]; !dead {
		b.streamDead[session.key] = struct{}{}
		b.streamDeadOrder = append(b.streamDeadOrder, session.key)
		if len(b.streamDeadOrder) > 64 {
			oldest := b.streamDeadOrder[0]
			b.streamDeadOrder = b.streamDeadOrder[1:]
			delete(b.streamDead, oldest)
		}
	}
	b.streamMu.Unlock()
}

func (b *agentFileBridge) abortRsyncSession(taskID string) {
	b.streamMu.Lock()
	var targets []*rsyncStreamSession
	for _, session := range b.streamSessions {
		if session.id == taskID {
			targets = append(targets, session)
		}
	}
	b.streamMu.Unlock()
	for _, session := range targets {
		if session.cancel != nil {
			session.cancel()
		}
	}
}

func (b *agentFileBridge) offerStreamFrame(ctx context.Context, session *rsyncStreamSession, sequence uint64, data []byte, final bool) error {
	frame := protocol.StreamFrame{StreamID: session.key, Sequence: sequence, Final: final}
	if len(data) > 0 {
		frame.Data = base64.StdEncoding.EncodeToString(data)
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeStreamData, Generation: b.generation,
		RequestID: session.key, Sequence: sequence, Payload: payload}
	if err := protocol.ValidateStreamFrame(envelope, b.generation, frame); err != nil {
		return err
	}
	for {
		err := b.writer.offerFile(ctx, envelope)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errFileWriterQueueFull) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *agentFileBridge) pumpStreamOut(ctx context.Context, session *rsyncStreamSession, reader io.Reader) {
	lease := transferbuffer.Acquire()
	defer transferbuffer.Release(lease)
	buffer := lease[:]
	if len(buffer) > protocol.MaxFileChunkBytes {
		buffer = buffer[:protocol.MaxFileChunkBytes]
	}
	var sequence uint64
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			sequence++
			if err := b.offerStreamFrame(ctx, session, sequence, buffer[:n], false); err != nil {
				return
			}
		}
		if readErr != nil {
			sequence++
			finCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_ = b.offerStreamFrame(finCtx, session, sequence, nil, true)
			cancel()
			return
		}
	}
}

func writeStreamInbound(session *rsyncStreamSession, writer io.Writer, closeWrite func()) {
	for data := range session.inbound {
		if _, err := writer.Write(data); err != nil {
			if session.cancel != nil {
				session.cancel()
			}
			return
		}
	}
	if closeWrite != nil {
		closeWrite()
	}
}

func (b *agentFileBridge) respondStreamOp(ctx context.Context, session *rsyncStreamSession, response protocol.FileResponse) error {
	session.opSeq++
	payload, err := json.Marshal(response)
	if err != nil || len(payload) > protocol.FileResponseLimit(response.Operation) {
		return errors.New("Agent file response exceeds its bound")
	}
	envelope := protocol.Envelope{Version: protocol.CurrentVersion, Type: protocol.TypeFileResponse, Generation: b.generation,
		RequestID: session.opID, Sequence: session.opSeq, Payload: payload}
	if protocol.ValidateFileResponse(envelope, b.generation, response) != nil {
		return errors.New("Agent file response is invalid")
	}
	return b.writer.send(ctx, envelope)
}

type cappedTailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (c *cappedTailBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = append(c.data, p...)
	if len(c.data) > c.limit {
		c.data = append([]byte(nil), c.data[len(c.data)-c.limit:]...)
	}
	return len(p), nil
}

func (c *cappedTailBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.data)
}

func rsyncErrorText(stderr string) string {
	text := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, stderr)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[len(text)-200:]
	}
	if text == "" {
		return "rsync transfer failed"
	}
	return text
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

type streamContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *streamContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (b *agentFileBridge) beginRsyncReceive(ctx context.Context, id string, request protocol.FileRequest) {
	fail := func(err error) {
		if b.isCanceled(id) {
			return
		}
		_ = b.respond(context.Background(), id, protocol.FileResponse{Operation: request.Operation,
			Code: fileErrorCode(err), Message: rsyncErrorText(err.Error())}, nil)
	}
	if _, err := b.rsyncBinaryPath(); err != nil {
		fail(err)
		return
	}
	pipe, err := b.tasks.AttachUpload(ctx, request.TaskID, protocol.FileRequest{Operation: protocol.FileUploadBegin,
		TaskID: request.TaskID, Path: request.Path, ExpectedVersion: request.ExpectedVersion, Size: request.Size, SHA256: request.SHA256})
	if err != nil {
		fail(err)
		return
	}
	stagingVirtual, err := b.service.PrepareRsyncStaging(request.Path, request.TaskID)
	if err != nil {
		_ = pipe.CloseWithError(err)
		fail(err)
		return
	}
	stagedReal, err := b.service.RealPath(path.Join(stagingVirtual, path.Base(request.Path)))
	if err != nil {
		_ = pipe.CloseWithError(err)
		_ = b.service.RemoveRsyncStaging(request.Path, stagingVirtual)
		fail(err)
		return
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &rsyncStreamSession{id: request.TaskID, key: request.TaskID + "/t", opID: id, cancel: cancel,
		inbound: make(chan []byte, 32), nextInSeq: 1,
		pipe: pipe, stagingVirtual: stagingVirtual, stagedReal: stagedReal, startChan: make(chan []string, 1)}
	if err := b.registerStreamSession(sessionCtx, session); err != nil {
		cancel()
		_ = pipe.CloseWithError(err)
		_ = b.service.RemoveRsyncStaging(request.Path, stagingVirtual)
		fail(err)
		return
	}
	defer b.unregisterStreamSession(session)
	defer func() { _ = b.service.RemoveRsyncStaging(request.Path, stagingVirtual) }()
	if err := b.respond(sessionCtx, id, protocol.FileResponse{Operation: protocol.FileRsyncReceive, Size: request.Size}, nil); err != nil {
		cancel()
		_ = pipe.CloseWithError(err)
		return
	}
	select {
	case argv := <-session.startChan:
		b.runRsyncServer(sessionCtx, session, argv)
	case <-sessionCtx.Done():
		_ = pipe.CloseWithError(errors.New("rsync receive was canceled"))
	}
}

func (b *agentFileBridge) runRsyncServer(ctx context.Context, session *rsyncStreamSession, argv []string) {
	binary, err := b.rsyncBinaryPath()
	if err != nil {
		_ = session.pipe.CloseWithError(err)
		return
	}
	cmd := exec.CommandContext(ctx, binary, argv[1:]...)
	stderr := &cappedTailBuffer{limit: 4096}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = session.pipe.CloseWithError(err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = session.pipe.CloseWithError(err)
		return
	}
	if err := cmd.Start(); err != nil {
		_ = session.pipe.CloseWithError(err)
		return
	}
	go writeStreamInbound(session, stdin, func() { _ = stdin.Close() })
	go b.pumpStreamOut(ctx, session, stdout)
	if waitErr := cmd.Wait(); waitErr != nil {
		_ = session.pipe.CloseWithError(errors.New("rsync receive failed: " + rsyncErrorText(stderr.String())))
		return
	}
	staged, err := os.Open(session.stagedReal)
	if err != nil {
		_ = session.pipe.CloseWithError(err)
		return
	}
	lease := transferbuffer.Acquire()
	_, copyErr := io.CopyBuffer(session.pipe, &streamContextReader{ctx: ctx, reader: staged}, lease[:])
	transferbuffer.Release(lease)
	_ = staged.Close()
	if copyErr != nil {
		_ = session.pipe.CloseWithError(copyErr)
		return
	}
	_ = session.pipe.Close()
}

func (b *agentFileBridge) beginRsyncSend(ctx context.Context, id string, request protocol.FileRequest) {
	fail := func(err error) {
		if b.isCanceled(id) {
			return
		}
		_ = b.respond(context.Background(), id, protocol.FileResponse{Operation: request.Operation,
			Code: fileErrorCode(err), Message: rsyncErrorText(err.Error())}, nil)
	}
	entry, err := b.service.Stat(request.Path)
	if err != nil {
		fail(err)
		return
	}
	if entry.Kind != "file" {
		fail(errors.New("rsync source is not a file"))
		return
	}
	binary, err := b.rsyncBinaryPath()
	if err != nil {
		fail(err)
		return
	}
	realSource, err := b.service.RealPath(request.Path)
	if err != nil {
		fail(err)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		fail(err)
		return
	}
	sessionDir, err := os.MkdirTemp("", "nd-rsync-")
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = os.RemoveAll(sessionDir) }()
	socketPath := filepath.Join(sessionDir, "s")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fail(err)
		return
	}
	defer listener.Close()
	wrapperPath := filepath.Join(sessionDir, "rsh")
	script := "#!/bin/sh\nexec " + shellQuote(executable) + " rsync-rsh " + shellQuote(socketPath) + " \"$@\"\n"
	if err := os.WriteFile(wrapperPath, []byte(script), 0o700); err != nil {
		fail(err)
		return
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &rsyncStreamSession{id: request.TaskID, key: request.TaskID + "/s", opID: id, cancel: cancel,
		inbound: make(chan []byte, 32), nextInSeq: 1,
		argvChan: make(chan rsyncArgvOffer, 1), goChan: make(chan struct{})}
	if err := b.registerStreamSession(sessionCtx, session); err != nil {
		cancel()
		fail(err)
		return
	}
	defer func() {
		cancel()
		if session.conn != nil {
			_ = session.conn.Close()
		}
		b.unregisterStreamSession(session)
	}()
	args := append(append([]string{}, rsyncStreamFlags...), "-e", wrapperPath, "--", realSource, "nd:"+path.Base(request.Path))
	cmd := exec.CommandContext(sessionCtx, binary, args...)
	stderr := &cappedTailBuffer{limit: 4096}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		fail(err)
		return
	}
	cmdDone := make(chan error, 1)
	go func() { cmdDone <- cmd.Wait() }()
	go b.acceptRsyncStream(sessionCtx, session, listener)
	finish := func(waitErr error) {
		if b.isCanceled(id) {
			return
		}
		if waitErr != nil {
			_ = b.respondStreamOp(context.Background(), session, protocol.FileResponse{Operation: request.Operation,
				Code: "transfer_failed", Message: rsyncErrorText(stderr.String())})
			return
		}
		_ = b.respondStreamOp(context.Background(), session, protocol.FileResponse{Operation: request.Operation, Size: entry.Size})
	}
	select {
	case offer := <-session.argvChan:
		session.conn = offer.conn
		prelim := protocol.FileResponse{Operation: request.Operation, Args: offer.args}
		if err := b.respondStreamOp(sessionCtx, session, prelim); err != nil {
			return
		}
		select {
		case waitErr := <-cmdDone:
			finish(waitErr)
		case <-sessionCtx.Done():
		}
	case waitErr := <-cmdDone:
		finish(waitErr)
	case <-sessionCtx.Done():
	}
}

func (b *agentFileBridge) acceptRsyncStream(ctx context.Context, session *rsyncStreamSession, listener net.Listener) {
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	remote, err := readRsyncArgvHeader(conn)
	if err != nil || len(remote) < 2 {
		_ = conn.Close()
		return
	}
	select {
	case session.argvChan <- rsyncArgvOffer{args: remote[1:], conn: conn}:
	case <-ctx.Done():
		_ = conn.Close()
		return
	}
	select {
	case <-session.goChan:
	case <-ctx.Done():
		_ = conn.Close()
		return
	}
	if _, err := conn.Write([]byte{'G'}); err != nil {
		if session.cancel != nil {
			session.cancel()
		}
		return
	}
	go writeStreamInbound(session, conn, func() {
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
	})
	b.pumpStreamOut(ctx, session, conn)
}

func readRsyncArgvHeader(conn net.Conn) ([]string, error) {
	buffer := make([]byte, 0, 256)
	single := make([]byte, 1)
	for {
		n, err := conn.Read(single)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if single[0] == '\n' {
			break
		}
		buffer = append(buffer, single[0])
		if len(buffer) > 64<<10 {
			return nil, errors.New("rsync argv header is too large")
		}
	}
	var argv []string
	if err := json.Unmarshal(buffer, &argv); err != nil {
		return nil, err
	}
	return argv, nil
}

func RunRsyncRSH(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 3 {
		return errors.New("rsync-rsh requires the bridge socket path and the remote command")
	}
	conn, err := net.Dial("unix", args[0])
	if err != nil {
		return err
	}
	defer conn.Close()
	header, err := json.Marshal(args[1:])
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(header, '\n')); err != nil {
		return err
	}
	signal := make([]byte, 1)
	if _, err := io.ReadFull(conn, signal); err != nil || signal[0] != 'G' {
		return errors.New("rsync stream was not authorized")
	}
	go func() {
		_, _ = io.Copy(conn, stdin)
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
	}()
	_, err = io.Copy(stdout, conn)
	return err
}

func RunRsyncArgv(args []string) error {
	out := os.Getenv("ND_RSYNC_ARGV_OUT")
	if out == "" {
		return errors.New("rsync argv output is not configured")
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}
