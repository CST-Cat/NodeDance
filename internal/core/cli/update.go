package cli

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
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/control"
	"github.com/CST-Cat/NodeDance/internal/update"
)

// This command is presentation over the connected Core operation contract.
func runUpdate(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nodedance update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connection := addConnectionFlags(flags, lookup)
	checkOnly := flags.Bool("check", false, "check the newest stable component release without updating")
	yes := flags.Bool("yes", false, "skip rollback confirmation")
	component := flags.String("component", "core", "core or agent")
	node := flags.String("node-id", "", "Agent node ID")
	version := flags.String("version", "", "published component tag; default is newest stable")
	rollback := flags.String("rollback", "", "successful update job ID to roll back")
	status := flags.Bool("status", false, "show durable update history or --job-id progress")
	jobID := flags.String("job-id", "", "update job ID for --status")
	noWait := flags.Bool("no-wait", false, "return after the update is accepted")
	key := flags.String("idempotency-key", "", "reuse after interrupted submission")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected update arguments")
	}
	client, err := connection.client()
	if err != nil {
		return err
	}
	defer client.Close()
	request := update.Request{Component: *component, NodeID: *node, Version: *version, Confirm: true}
	requestContext := func() (context.Context, context.CancelFunc) {
		if *connection.timeout == 0 {
			return context.WithCancel(ctx)
		}
		return context.WithTimeout(ctx, *connection.timeout)
	}
	catalogCtx, cancel := requestContext()
	defer cancel()
	catalog, err := client.Catalog(catalogCtx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	invoke := func(action string, r update.Request, key string) (control.Result, error) {
		operation, err := control.FindIn(catalog, "updates."+action)
		if err != nil {
			return control.Result{}, err
		}
		input := control.Input{IdempotencyKey: key}
		if operation.Method == "GET" {
			input.Query = map[string]string{"component": r.Component}
			if r.NodeID != "" {
				input.Query["nodeId"] = r.NodeID
			}
			if r.Version != "" {
				input.Query["version"] = r.Version
			}
			if r.JobID != "" {
				input.Query["jobId"] = r.JobID
			}
		} else {
			input.Body, _ = json.Marshal(r)
		}
		requestCtx, cancel := requestContext()
		defer cancel()
		result := client.Execute(requestCtx, operation, input, nil, nil)
		return result, result.Err()
	}
	if *status {
		request.JobID = *jobID
		result, err := invoke("status", request, "")
		if err != nil {
			return err
		}
		return encoder.Encode(json.RawMessage(result.Data))
	}
	if *rollback == "" {
		result, err := invoke("check", request, "")
		if err != nil {
			return err
		}
		if err = encoder.Encode(json.RawMessage(result.Data)); err != nil {
			return err
		}
		if *checkOnly {
			return nil
		}
		var check update.Check
		if err = json.Unmarshal(result.Data, &check); err != nil {
			return err
		}
		if !check.Configured || !check.Compatible {
			return errors.New("update preflight did not pass: " + check.Reason)
		}
		if !check.Available {
			return nil
		}
		request.Version = check.Latest
	} else {
		request.JobID = *rollback
	}
	if *rollback != "" && !*yes {
		fmt.Fprint(stderr, "Roll back to the previous version and restore its data? [y/N] ")
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil || !strings.EqualFold(strings.TrimSpace(line), "y") {
			return errors.New("rollback cancelled")
		}
	}
	if *key == "" {
		var raw [16]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return err
		}
		*key = hex.EncodeToString(raw[:])
	}
	fmt.Fprintln(stderr, "Idempotency-Key:", *key)
	action := "apply"
	if *rollback != "" {
		action = "rollback"
	}
	result, err := invoke(action, request, *key)
	if err != nil {
		return err
	}
	var job update.Job
	if err = json.Unmarshal(result.Data, &job); err != nil {
		return err
	}
	if err = encoder.Encode(job); err != nil {
		return err
	}
	if *noWait {
		return nil
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 20*time.Minute)
	defer waitCancel()
	request.Version = ""
	request.JobID = job.ID
	last := job.Stage
	for {
		select {
		case <-waitCtx.Done():
			return errors.New("update wait expired; use nodedance update --status --job-id " + job.ID)
		case <-time.After(2 * time.Second):
		}
		result, err = invoke("status", request, "")
		if err != nil {
			if result.Status >= 400 && result.Status < 500 {
				return err
			}
			continue
		}
		if err = json.Unmarshal(result.Data, &job); err != nil {
			return err
		}
		if job.Stage != last {
			fmt.Fprintln(stderr, "Update:", job.Stage)
			last = job.Stage
		}
		if job.Finished != nil {
			if err = encoder.Encode(job); err != nil {
				return err
			}
			if job.Status != "succeeded" {
				return errors.New("update " + job.Status + ": " + job.Error)
			}
			return nil
		}
	}
}
