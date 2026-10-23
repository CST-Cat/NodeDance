package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/control"
)

type batchEntry struct {
	Operation string `json:"operation"`
	control.Input
	BodyFile   string `json:"bodyFile,omitempty"`
	InputFile  string `json:"inputFile,omitempty"`
	OutputFile string `json:"outputFile,omitempty"`
	Wait       bool   `json:"wait,omitempty"`
}
type preparedBatchEntry struct {
	entry     batchEntry
	operation control.Operation
}

func runBatch(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nodedance batch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connection := addConnectionFlags(flags, lookup)
	source := flags.String("input", "-", "JSONL @file or - for stdin; entries use operation/params/query/body/bodyFile/headers/idempotencyKey/inputFile/outputFile/wait")
	dry := flags.Bool("dry-run", false, "validate all entries against the connected Core contract without executing")
	keepGoing := flags.Bool("continue-on-error", false, "run remaining entries after an operation fails; still exit nonzero")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("usage: nodedance batch --input @private.jsonl [--dry-run] [--continue-on-error]")
	}
	sourceValue := *source
	if sourceValue != "-" && !strings.HasPrefix(sourceValue, "@") {
		sourceValue = "@" + sourceValue
	}
	raw, e := readOperationValue(sourceValue, stdin, 64<<20)
	if e != nil {
		return e
	}
	if len(raw) == 0 {
		return errors.New("batch is empty")
	}
	client, e := connection.client()
	if e != nil {
		return e
	}
	defer client.Close()
	requestCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	if *connection.timeout > 0 {
		var cancel context.CancelFunc
		requestCtx, cancel = context.WithTimeout(requestCtx, *connection.timeout)
		defer cancel()
	}
	catalog, e := client.Catalog(requestCtx)
	if e != nil {
		return e
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 16<<20)
	prepared := []preparedBatchEntry{}
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		if len(prepared) >= 1000 {
			return errors.New("batch limit is 1000 entries")
		}
		var entry batchEntry
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&entry); e != nil {
			return fmt.Errorf("batch line %d: invalid entry JSON", line)
		}
		var extra any
		if !errors.Is(decoder.Decode(&extra), io.EOF) {
			return fmt.Errorf("batch line %d: provide exactly one object", line)
		}
		operation, e := control.FindIn(catalog, entry.Operation)
		if e != nil {
			return fmt.Errorf("batch line %d: %w", line, e)
		}
		if operation.Transport == "websocket" || operation.Transport == "terminal" || operation.Transport == "local" {
			return fmt.Errorf("batch line %d: streaming and local operations use their dedicated executor", line)
		}
		if entry.BodyFile != "" {
			if len(entry.Body) > 0 {
				return fmt.Errorf("batch line %d: body and bodyFile are mutually exclusive", line)
			}
			if entry.BodyFile == "-" {
				return errors.New("batch bodies cannot consume stdin; use bodyFile paths")
			}
			entry.Body, e = readOperationValue("@"+strings.TrimPrefix(entry.BodyFile, "@"), nil, operation.MaxBodyBytes)
			if e != nil {
				return fmt.Errorf("batch line %d bodyFile: %w", line, e)
			}
		}
		if entry.InputFile == "-" {
			return errors.New("batch uploads require file paths; stdin is the JSONL source")
		}
		if entry.Wait && operation.Completion == nil {
			return fmt.Errorf("batch line %d: Core exposes no completion contract", line)
		}
		if operation.Transport == "download" && entry.OutputFile == "" {
			return fmt.Errorf("batch line %d: download requires outputFile", line)
		}
		if operation.Transport != "download" && entry.OutputFile != "" {
			return fmt.Errorf("batch line %d: outputFile is only for downloads", line)
		}
		upload := operation.Transport == "upload" || operation.Transport == "multipart"
		if upload != (entry.InputFile != "") {
			return fmt.Errorf("batch line %d: upload transport and inputFile must match", line)
		}
		if e = client.ValidateInput(operation, entry.Input); e != nil {
			return fmt.Errorf("batch line %d: %w", line, e)
		}
		prepared = append(prepared, preparedBatchEntry{entry, operation})
	}
	if e = scanner.Err(); e != nil {
		return errors.New("batch line exceeds input limits")
	}
	if len(prepared) == 0 {
		return errors.New("batch is empty")
	}
	encoder := json.NewEncoder(stdout)
	failed := false
	for index, item := range prepared {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
		if *dry {
			if e = encoder.Encode(map[string]any{"index": index + 1, "operation": item.operation.Name, "valid": true, "dryRun": true}); e != nil {
				return e
			}
			continue
		}
		e = nil
		var upload io.Reader
		closeInput := func() {}
		if item.entry.InputFile != "" {
			upload, closeInput, e = uploadInput(item.entry.InputFile, nil)
		}
		result := control.Result{Operation: item.operation.Name}
		if e != nil {
			result.Error = e.Error()
		} else {
			result = executeFinite(requestCtx, client, catalog, item.operation, item.entry.Input, upload, item.entry.OutputFile, item.entry.Wait, nil)
		}
		closeInput()
		if e = encoder.Encode(struct {
			Index int `json:"index"`
			control.Result
		}{index + 1, result}); e != nil {
			return e
		}
		if result.Err() != nil {
			failed = true
			if !*keepGoing {
				break
			}
		}
	}
	if failed {
		return errors.New("one or more batch operations failed; inspect JSONL results before retrying")
	}
	return nil
}
