package update

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Local is the product's local maintenance presentation over the same requests and
// transaction executor used by Core's discovered updates operations.
func Local(ctx context.Context, component, dataDir string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s local update requires root", component)
	}
	return local(ctx, component, dataDir, args, stdin, stdout, stderr, Invoke)
}

func local(ctx context.Context, component, dataDir string, args []string, stdin io.Reader, stdout, stderr io.Writer, invoke func(context.Context, string, string, RPC) (Response, error)) error {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	selectedComponent := flags.String("component", component, "local component")
	check := flags.Bool("check", false, "check without updating")
	status := flags.Bool("status", false, "show update history or job progress")
	version := flags.String("version", "", "pin a published version")
	job := flags.String("job-id", "", "job to observe")
	rollback := flags.String("rollback", "", "roll back a successful update")
	yes := flags.Bool("yes", false, "confirm rollback")
	noWait := flags.Bool("no-wait", false, "return after durable acceptance")
	config := flags.String("config", "", "Agent configuration path; defaults to the installed state")
	key := flags.String("idempotency-key", "", "reuse an interrupted submission identity")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *selectedComponent != component {
		return errors.New("local update component does not match the running product")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected update arguments")
	}
	if *config != "" {
		if component != "agent" {
			return errors.New("config is an Agent maintenance option")
		}
		path, err := filepath.Abs(*config)
		if err != nil {
			return err
		}
		dataDir = filepath.Dir(path)
	}
	request := Request{Component: component, Version: *version, Confirm: true}
	action := "check"
	if *status {
		action = "status"
		request.JobID = *job
	}
	call := func(action string, key string) (Response, error) {
		return invoke(ctx, component, dataDir, RPC{Action: action, Request: request, IdempotencyKey: key})
	}
	output := json.NewEncoder(stdout)
	output.SetIndent("", "  ")
	var response Response
	var err error
	if *status || *check || *rollback == "" {
		response, err = call(action, "")
		if err != nil {
			return err
		}
		if response.Error != "" {
			return errors.New(response.Error)
		}
		if *check || *status {
			return output.Encode(json.RawMessage(response.Data))
		}
	}
	if *rollback != "" {
		if !*yes {
			fmt.Fprint(stderr, "Roll back to the previous version and restore its data? [y/N] ")
			line, err := bufio.NewReader(stdin).ReadString('\n')
			if err != nil || !strings.EqualFold(strings.TrimSpace(line), "y") {
				return errors.New("rollback cancelled")
			}
		}
		request.JobID = *rollback
		action = "rollback"
	} else {
		var result Check
		if err = json.Unmarshal(response.Data, &result); err != nil {
			return err
		}
		if !result.Configured {
			return errors.New("update preflight did not pass: " + result.Reason)
		}
		if !result.Available {
			return output.Encode(json.RawMessage(response.Data))
		}
		request.Version = result.Latest
		action = "apply"
	}
	if *key == "" {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		*key = hex.EncodeToString(nonce[:])
	}
	// Emit the retry identity before submission, exactly as the terminal Core workflow does.
	if _, err = io.WriteString(stderr, "Idempotency-Key: "+*key+"\n"); err != nil {
		return err
	}
	response, err = call(action, *key)
	if err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if err = output.Encode(json.RawMessage(response.Data)); err != nil || *noWait {
		return err
	}
	var accepted Job
	if err = json.Unmarshal(response.Data, &accepted); err != nil {
		return err
	}
	request.JobID = accepted.ID
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("update wait expired; observe with --status --job-id " + accepted.ID)
		case <-ticker.C:
		}
		response, err = call("status", "")
		if err != nil {
			continue
		}
		if response.Error != "" {
			return errors.New(response.Error)
		}
		var current Job
		if err = json.Unmarshal(response.Data, &current); err != nil {
			return err
		}
		if current.Finished != nil {
			if err = output.Encode(current); err != nil {
				return err
			}
			if current.Status != "succeeded" {
				return errors.New("update finished with " + current.Status + ": " + current.Error)
			}
			return nil
		}
	}
}
