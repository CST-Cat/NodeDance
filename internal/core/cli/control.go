package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/i18n"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/CST-Cat/NodeDance/internal/update"
	"github.com/coder/websocket"
	"golang.org/x/term"
)

type connectionFlags struct {
	flags                            *flag.FlagSet
	server, origin, stateDir, caFile *string
	dev                              *bool
	timeout                          *time.Duration
}

func addConnectionFlags(f *flag.FlagSet, lookup LookupEnv) connectionFlags {
	configHome, _ := os.UserConfigDir()
	stderr := f.Output()
	return connectionFlags{
		flags:    f,
		server:   f.String("server", lookupValue(lookup, "NODEDANCE_SERVER"), i18n.TextFor(stderr, "Core HTTPS origin")),
		origin:   f.String("origin", lookupValue(lookup, "NODEDANCE_ORIGIN"), i18n.TextFor(stderr, "public Core origin when connecting through another address")),
		stateDir: f.String("state-dir", firstNonEmpty(lookupValue(lookup, "NODEDANCE_CLIENT_STATE_DIR"), filepath.Join(configHome, "nodedance-client")), i18n.TextFor(stderr, "private CLI session directory (0700)")),
		caFile:   f.String("ca-file", "", i18n.TextFor(stderr, "private CA PEM file")), dev: f.Bool("dev", false, i18n.TextFor(stderr, "allow HTTP only to literal loopback")), timeout: f.Duration("timeout", 60*time.Second, i18n.TextFor(stderr, "request/stream timeout; 0 disables deadline")),
	}
}
func (f connectionFlags) client() (*control.Client, error) {
	explicit := map[string]bool{}
	f.flags.Visit(func(flag *flag.Flag) { explicit[flag.Name] = true })
	client, err := control.NewClient(control.ClientOptions{Server: *f.server, Origin: *f.origin, StateDir: *f.stateDir, CAFile: *f.caFile, Development: *f.dev, UseSavedConnection: true, Explicit: explicit})
	if err == nil {
		resolved := client.Connection()
		*f.server = resolved.Server
		*f.origin = resolved.Origin
		*f.caFile = resolved.CAFile
		*f.dev = resolved.Development
	}
	return client, err
}

func localOperations() []control.Operation {
	result := []control.Operation{}
	for _, item := range []struct{ name, desc string }{
		{"client.update", "Update workflow: preflight, submit and observe using the connected Core; confirm rollback"}, {"core.serve", "Run Core; arguments are the same as nodedance serve"}, {"core.backup", "Back up Core to a new private archive"}, {"core.restore", "Restore Core to a new/empty directory"}, {"core.version", "Show Core version"},
		{"agent.enroll", "Enroll root Agent; --token-stdin; stdin is never placed in process arguments"}, {"agent.recover", "Recover root Agent enrollment"}, {"agent.update", "Update the local root Agent to the latest stable release"}, {"agent.run", "Run root Agent"}, {"agent.install-systemd", "Install root Agent systemd service"}, {"agent.uninstall-systemd", "Remove managed Agent unit; preserve credentials"}, {"agent.validate-systemd-state", "Validate protected Agent credentials"}, {"agent.prepare-systemd-state", "Prepare root-owned Agent state"}, {"agent.validate-file-root", "Validate Agent file root"}, {"agent.version", "Show Agent version"}, {"agent.tmux-version", "Show bundled Agent tmux version"},
	} {
		result = append(result, control.Operation{Name: item.name, Description: item.desc, Method: "LOCAL", Transport: "local", Parameters: []string{}, Query: []string{}, BodySchema: map[string]any{"type": "array", "items": map[string]string{"type": "string"}}})
	}
	return result
}
func allOperations() []control.Operation { return terminalOperations(control.Describe()) }
func localizedOperations(w io.Writer) []control.Operation {
	list := allOperations()
	for i := range list {
		list[i].Description = i18n.TextFor(w, list[i].Description)
	}
	return list
}
func runOperations(args []string, stdout io.Writer) error {
	if len(args) > 1 {
		return errors.New("usage: nodedance operations [NAME]")
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if len(args) == 1 {
		for _, o := range localizedOperations(stdout) {
			if o.Name == args[0] {
				return encoder.Encode(o)
			}
		}
		return fmt.Errorf("unknown operation %q", args[0])
	}
	return encoder.Encode(map[string]any{"version": 1, "operations": localizedOperations(stdout)})
}
func runOperationCatalog(ctx context.Context, args []string, lookup LookupEnv, stdout, stderr io.Writer) error {
	remote := lookupValue(lookup, "NODEDANCE_SERVER") != ""
	for _, arg := range args {
		if arg == "--server" || strings.HasPrefix(arg, "--server=") {
			remote = true
		}
	}
	if !remote {
		return runOperations(args, stdout)
	}
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet("operations", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connection := addConnectionFlags(flags, lookup)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 || (flags.NArg() > 0 && name != "") {
		return errors.New("usage: nodedance operations [NAME] --server URL")
	}
	if flags.NArg() == 1 {
		name = flags.Arg(0)
	}
	client, err := connection.client()
	if err != nil {
		return err
	}
	defer client.Close()
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	catalog, err := client.Catalog(requestCtx)
	if err != nil {
		return err
	}
	for i := range catalog.Operations {
		catalog.Operations[i].Description = i18n.TextFor(stdout, catalog.Operations[i].Description)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if name != "" {
		operation, err := control.FindIn(catalog, name)
		if err != nil {
			return err
		}
		return encoder.Encode(operation)
	}
	return encoder.Encode(catalog)
}

func readValue(value string, stdin io.Reader) ([]byte, error) {
	return readOperationValue(value, stdin, 4<<20)
}
func readOperationValue(value string, stdin io.Reader, limit int64) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 4 << 20
	}
	var input io.Reader
	if value == "-" {
		input = stdin
	} else if strings.HasPrefix(value, "@") {
		file, err := os.Open(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		input = file
	} else {
		input = strings.NewReader(value)
	}
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("input exceeds operation limit")
	}
	return data, nil
}

func decodeMap(value string, stdin io.Reader) (map[string]string, error) {
	raw, err := readValue(value, stdin)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

func runControl(ctx context.Context, name string, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer, version string) error {
	for _, o := range localOperations() {
		if o.Name == name {
			if name == "client.update" {
				return runUpdate(ctx, args, lookup, stdin, stdout, stderr)
			}
			if strings.HasPrefix(name, "agent.") {
				if name == "agent.tmux-version" {
					return runAgentCommand(ctx, append([]string{"--tmux-version"}, args...), stdin, stdout, stderr)
				}
				return runAgentCommand(ctx, append([]string{strings.TrimPrefix(name, "agent.")}, args...), stdin, stdout, stderr)
			}
			return RunWithInput(ctx, append([]string{strings.TrimPrefix(name, "core.")}, args...), lookup, stdin, stdout, stderr, version)
		}
	}
	f := flag.NewFlagSet("nodedance "+name, flag.ContinueOnError)
	f.SetOutput(stderr)
	connection := addConnectionFlags(f, lookup)
	params := f.String("params", "", i18n.TextFor(stderr, "path parameters as JSON, @file or -"))
	query := f.String("query", "", i18n.TextFor(stderr, "query parameters as JSON, @file or -"))
	body := f.String("body", "", i18n.TextFor(stderr, "JSON body, @private-file or - for stdin"))
	var deploymentMethod *string
	if name == "agents.enrollment" {
		deploymentMethod = f.String("deployment-method", "", i18n.TextFor(stderr, "Agent deployment method: native or docker (overrides JSON body)"))
	}
	wait := f.Bool("wait", false, "observe accepted work until terminal status without resubmitting")
	interactive := f.Bool("interactive", false, "use the same interactive terminal as TUI (terminal transport only)")
	headers := f.String("headers", "", "operation headers as JSON, @file or -")
	key := f.String("idempotency-key", "", i18n.TextFor(stderr, "stable key for task submission and retries"))
	input := f.String("input", "", i18n.TextFor(stderr, "upload file or terminal JSONL input (- for stdin)"))
	output := f.String("output", "", i18n.TextFor(stderr, "new download file (required for binary downloads)"))
	fileVersion := f.String("file-version", "", i18n.TextFor(stderr, "expected version for upload"))
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	stdinUses := 0
	for _, v := range []string{*params, *query, *body, *input, *headers} {
		if v == "-" {
			stdinUses++
		}
	}
	if stdinUses > 1 {
		return errors.New("only one input may consume stdin")
	}
	p, err := decodeMap(*params, stdin)
	if err != nil {
		return fmt.Errorf("params: %w", err)
	}
	q, err := decodeMap(*query, stdin)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	if deploymentMethod != nil && *deploymentMethod != "" && !agents.ValidDeploymentMethod(*deploymentMethod) {
		return errors.New("deployment method must be native or docker")
	}
	client, err := connection.client()
	if err != nil {
		return err
	}
	defer client.Close()
	requestCtx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	if *connection.timeout > 0 {
		var stop context.CancelFunc
		requestCtx, stop = context.WithTimeout(requestCtx, *connection.timeout)
		defer stop()
	}
	catalog, err := client.Catalog(requestCtx)
	if err != nil {
		return err
	}
	o, err := control.FindIn(catalog, name)
	if err != nil {
		return err
	}
	b, err := readOperationValue(*body, stdin, o.MaxBodyBytes)
	if err != nil {
		return err
	}
	if deploymentMethod != nil && *deploymentMethod != "" {
		var request map[string]json.RawMessage
		if err := json.Unmarshal(b, &request); err != nil || request == nil {
			return errors.New("Agent enrollment requires a JSON object body")
		}
		request["deploymentMethod"], _ = json.Marshal(*deploymentMethod)
		b, _ = json.Marshal(request)
	}
	h, err := decodeMap(*headers, stdin)
	if err != nil {
		return fmt.Errorf("headers: %w", err)
	}
	in := control.Input{Params: p, Query: q, Body: b, IdempotencyKey: *key, FileVersion: *fileVersion, Headers: h}

	if *input != "" && o.Transport != "upload" && o.Transport != "multipart" && !o.Duplex {
		return errors.New("--input is supported only for uploads and terminal.connect")
	}
	if *output != "" && o.Transport != "download" {
		return errors.New("--output is supported only for downloads")
	}
	var upload io.Reader
	if *input != "" {
		var closeInput func()
		upload, closeInput, err = uploadInput(*input, stdin)
		if err != nil {
			return err
		}
		defer closeInput()
	}
	if *interactive {
		if o.Transport != "terminal" || *input != "" {
			return errors.New("interactive requires terminal.open and no input file")
		}
		tty, ok := stdin.(*os.File)
		if !ok || !term.IsTerminal(int(tty.Fd())) {
			return errors.New("interactive terminal requires terminal stdin")
		}
		state, err := term.MakeRaw(int(tty.Fd()))
		if err != nil {
			return err
		}
		defer term.Restore(int(tty.Fd()), state)
		return interactiveTerminal(requestCtx, client, in, tty, stdout)
	}
	if *wait && o.Completion == nil {
		return errors.New("Core does not expose completion observation for this operation")
	}
	if o.Transport == "websocket" || o.Transport == "terminal" {
		return streamJSONL(requestCtx, client, o, in, upload, stdout)
	}
	var observer func(control.Result)
	if *wait {
		first := true
		observer = func(result control.Result) {
			if first {
				json.NewEncoder(stdout).Encode(result)
				first = false
			}
		}
	}
	result := executeFinite(requestCtx, client, catalog, o, in, upload, *output, *wait, observer)
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		return err
	}
	return result.Err()
}

func streamJSONL(ctx context.Context, client *control.Client, o control.Operation, in control.Input, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var conn control.StreamConnection
	var streamID string
	var err error
	if o.Transport == "terminal" {
		conn, streamID, err = client.OpenTerminal(ctx, in, 24, 80)
	} else {
		conn, err = client.Connect(ctx, o, in)
	}
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	inputErr := make(chan error, 1)
	if input != nil {
		go func() {
			scanner := bufio.NewScanner(input)
			scanner.Buffer(make([]byte, 4096), 40*1024)
			for scanner.Scan() {
				line := append([]byte{}, scanner.Bytes()...)
				if !json.Valid(line) {
					inputErr <- errors.New("terminal input must be JSONL")
					cancel()
					return
				}
				if streamID != "" {
					var frame map[string]any
					if json.Unmarshal(line, &frame) != nil || frame == nil {
						inputErr <- errors.New("terminal input must be a JSON object")
						cancel()
						return
					}
					if _, ok := frame["streamId"]; !ok {
						frame["streamId"] = streamID
					}
					line, _ = json.Marshal(frame)
				}
				writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				err := conn.Write(writeCtx, websocket.MessageText, line)
				stop()
				if err != nil {
					inputErr <- errors.New("terminal write failed")
					cancel()
					return
				}
			}
			if err := scanner.Err(); err != nil {
				inputErr <- err
				cancel()
			}
		}()
	}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			select {
			case e := <-inputErr:
				return e
			default:
			}
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Core stream closed unexpectedly")
		}
		if kind != websocket.MessageText || !json.Valid(data) {
			return errors.New("Core returned invalid stream JSON")
		}
		var compact strings.Builder
		var value json.RawMessage = data
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		compact.Write(encoded)
		compact.WriteByte('\n')
		if _, err = io.WriteString(output, compact.String()); err != nil {
			return err
		}
		if o.Duplex {
			var message struct {
				Frame protocol.TerminalFrame `json:"frame"`
			}
			if json.Unmarshal(data, &message) == nil {
				switch message.Frame.Action {
				case protocol.TerminalActionExit, protocol.TerminalActionClosed:
					return nil
				case protocol.TerminalActionError:
					return errors.New(message.Frame.Message)
				}
			}
		}
	}
}
func runAgentCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "update" {
		return update.Local(ctx, "agent", "/var/lib/nodedance-agent", args[1:], stdin, stdout, stderr)
	}
	// Prefer a colocated Agent so a local build or extracted distribution is
	// not shadowed by an unrelated executable on PATH. Agent stays separate.
	path := ""
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "nodedance-agent")
		if info, err := os.Stat(candidate); err == nil {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return errors.New("colocated nodedance-agent must be an executable regular file")
			}
			path = candidate
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if path == "" {
		var err error
		path, err = exec.LookPath("nodedance-agent")
		if err != nil {
			return errors.New("install nodedance-agent beside nodedance or on PATH")
		}
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Env = append(os.Environ(), "NODEDANCE_LANG="+string(i18n.From(ctx)))
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}
