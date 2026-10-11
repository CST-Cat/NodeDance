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
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"github.com/coder/websocket"
)

type connectionFlags struct {
	server, origin, stateDir, caFile *string
	dev                              *bool
	timeout                          *time.Duration
}

func addConnectionFlags(f *flag.FlagSet, lookup LookupEnv) connectionFlags {
	configHome, _ := os.UserConfigDir()
	stderr := f.Output()
	return connectionFlags{
		server:   f.String("server", firstNonEmpty(lookupValue(lookup, "NODEDANCE_SERVER"), "https://127.0.0.1:8180"), i18n.TextFor(stderr, "Core HTTPS origin")),
		origin:   f.String("origin", lookupValue(lookup, "NODEDANCE_ORIGIN"), i18n.TextFor(stderr, "public Core origin when connecting through another address")),
		stateDir: f.String("state-dir", firstNonEmpty(lookupValue(lookup, "NODEDANCE_CLIENT_STATE_DIR"), filepath.Join(configHome, "nodedance-client")), i18n.TextFor(stderr, "private CLI session directory (0700)")),
		caFile:   f.String("ca-file", "", i18n.TextFor(stderr, "private CA PEM file")), dev: f.Bool("dev", false, i18n.TextFor(stderr, "allow HTTP only to literal loopback")), timeout: f.Duration("timeout", 60*time.Second, i18n.TextFor(stderr, "request/stream timeout; 0 disables deadline")),
	}
}
func (f connectionFlags) client() (*control.Client, error) {
	return control.NewClient(control.ClientOptions{Server: *f.server, Origin: *f.origin, StateDir: *f.stateDir, CAFile: *f.caFile, Development: *f.dev})
}
func localOperations() []control.Operation {
	result := []control.Operation{}
	for _, item := range []struct{ name, desc string }{
		{"core.serve", "Run Core; arguments are the same as nodedance serve"}, {"core.backup", "Back up Core to a new private archive"}, {"core.restore", "Restore Core to a new/empty directory"}, {"core.version", "Show Core version"},
		{"agent.enroll", "Enroll root Agent; --token-stdin; stdin is never placed in process arguments"}, {"agent.recover", "Recover root Agent enrollment"}, {"agent.run", "Run root Agent"}, {"agent.install-systemd", "Install root Agent systemd service"}, {"agent.uninstall-systemd", "Remove managed Agent unit; preserve credentials"}, {"agent.validate-systemd-state", "Validate protected Agent credentials"}, {"agent.prepare-systemd-state", "Prepare root-owned Agent state"}, {"agent.validate-file-root", "Validate Agent file root"}, {"agent.version", "Show Agent version"},
	} {
		result = append(result, control.Operation{Name: item.name, Description: item.desc, Method: "LOCAL", Transport: "local", Parameters: []string{}, Query: []string{}, BodySchema: map[string]any{"type": "array", "items": map[string]string{"type": "string"}}})
	}
	return result
}
func allOperations() []control.Operation { return append(control.Operations(), localOperations()...) }
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
func readValue(value string, stdin io.Reader) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	if value == "-" {
		return io.ReadAll(io.LimitReader(stdin, 4*1024*1024+1))
	}
	if strings.HasPrefix(value, "@") {
		return os.ReadFile(strings.TrimPrefix(value, "@"))
	}
	return []byte(value), nil
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
			if strings.HasPrefix(name, "agent.") {
				return runAgentCommand(ctx, append([]string{strings.TrimPrefix(name, "agent.")}, args...), stdin, stdout, stderr)
			}
			return RunWithInput(ctx, append([]string{strings.TrimPrefix(name, "core.")}, args...), lookup, stdin, stdout, stderr, version)
		}
	}
	o, err := control.Find(name)
	if err != nil {
		return err
	}
	f := flag.NewFlagSet("nodedance "+name, flag.ContinueOnError)
	f.SetOutput(stderr)
	connection := addConnectionFlags(f, lookup)
	params := f.String("params", "", i18n.TextFor(stderr, "path parameters as JSON, @file or -"))
	query := f.String("query", "", i18n.TextFor(stderr, "query parameters as JSON, @file or -"))
	body := f.String("body", "", i18n.TextFor(stderr, "JSON body, @private-file or - for stdin"))
	key := f.String("idempotency-key", "", i18n.TextFor(stderr, "stable key for task submission and retries"))
	input := f.String("input", "", i18n.TextFor(stderr, "upload file or terminal JSONL input (- for stdin)"))
	output := f.String("output", "", i18n.TextFor(stderr, "new download file (required for binary downloads)"))
	fileVersion := f.String("file-version", "", i18n.TextFor(stderr, "expected version for upload"))
	if err = f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", f.Args())
	}
	stdinUses := 0
	for _, v := range []string{*params, *query, *body, *input} {
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
	b, err := readValue(*body, stdin)
	if err != nil {
		return err
	}
	if len(b) > 4*1024*1024 {
		return errors.New("JSON body exceeds 4 MiB")
	}
	in := control.Input{Params: p, Query: q, Body: b, IdempotencyKey: *key, FileVersion: *fileVersion}
	if *input != "" && o.Transport != "upload" && o.Transport != "multipart" && name != "terminal.connect" && name != "terminal.open" {
		return errors.New("--input is supported only for uploads and terminal.connect")
	}
	if *output != "" && o.Transport != "download" {
		return errors.New("--output is supported only for downloads")
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
	var upload io.Reader
	if *input == "-" {
		upload = stdin
	} else if *input != "" {
		file, err := os.Open(*input)
		if err != nil {
			return err
		}
		defer file.Close()
		upload = file
	}
	if o.Transport == "websocket" || o.Transport == "terminal" {
		return streamJSONL(requestCtx, client, o, in, upload, stdout)
	}
	var download *os.File
	if o.Transport == "download" {
		if *output == "" {
			return errors.New("download requires --output")
		}
		download, err = os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer download.Close()
	}
	var writer io.Writer
	if download != nil {
		writer = download
	}
	result := client.Execute(requestCtx, o, in, upload, writer)
	if download != nil {
		if result.Error == "" {
			if err = download.Sync(); err != nil {
				result.Error = err.Error()
			}
			if err = download.Close(); err != nil {
				result.Error = err.Error()
			}
		}
		if result.Error != "" {
			os.Remove(*output)
		} else {
			result.File = *output
		}
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		return err
	}
	return result.Err()
}
func streamJSONL(ctx context.Context, client *control.Client, o control.Operation, in control.Input, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var conn *websocket.Conn
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
		if streamID != "" || o.Name == "terminal.connect" {
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
