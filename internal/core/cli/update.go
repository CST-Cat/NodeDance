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

func updateUsesRemote(args []string, lookup LookupEnv) bool {
	remote := lookupValue(lookup, "NODEDANCE_SERVER") != ""
	for i, arg := range args {
		arg = strings.TrimLeft(arg, "-")
		if arg == "server" || strings.HasPrefix(arg, "server=") || arg == "node" || strings.HasPrefix(arg, "node=") || arg == "node-id" || strings.HasPrefix(arg, "node-id=") || arg == "component=agent" || arg == "component" && i+1 < len(args) && args[i+1] == "agent" {
			remote = true
		}
	}
	return remote
}

// Local updates use the built-in maintenance executor; explicit remote targets
// use the connected Core operation contract.
func runUpdate(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer) error {
	if !updateUsesRemote(args, lookup) {
		return update.Local(ctx, "core", "/var/lib/nodedance", args, stdin, stdout, stderr)
	}
	flags := flag.NewFlagSet("nodedance update", flag.ContinueOnError)
	flags.SetOutput(stderr)
	connection := addConnectionFlags(flags, lookup)
	checkOnly := flags.Bool("check", false, "check the newest stable component release without updating")
	yes := flags.Bool("yes", false, "skip rollback confirmation")
	component := flags.String("component", "core", "core or agent")
	node := flags.String("node-id", "", "Agent node ID")
	nodeName := flags.String("node", "", "Agent node name or full ID; selects Agent updates through the saved Core connection")
	version := flags.String("version", "", "published component tag; default is newest stable")
	channel := flags.String("channel", "stable", "stable or dev")
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
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	if explicit["node"] && explicit["node-id"] {
		return errors.New("use either --node or --node-id")
	}
	if explicit["node"] && strings.TrimSpace(*nodeName) == "" || explicit["node-id"] && strings.TrimSpace(*node) == "" {
		return errors.New("node target must not be empty")
	}
	if explicit["node"] || explicit["node-id"] {
		if explicit["component"] && *component != "agent" {
			return errors.New("a node target requires component agent")
		}
		*component = "agent"
	}
	selectedVersion, err := update.ChannelVersion(*channel, *version)
	if err != nil {
		return err
	}
	client, err := connection.client()
	if err != nil {
		return err
	}
	defer client.Close()
	request := update.Request{Component: *component, NodeID: *node, Version: selectedVersion, Confirm: true}
	requestContext := func(parent context.Context) (context.Context, context.CancelFunc) {
		if *connection.timeout == 0 {
			return context.WithCancel(parent)
		}
		return context.WithTimeout(parent, *connection.timeout)
	}
	catalogCtx, cancel := requestContext(ctx)
	defer cancel()
	catalog, err := client.Catalog(catalogCtx)
	if err != nil {
		return err
	}
	if *nodeName != "" {
		request.NodeID, err = resolveUpdateNode(catalogCtx, client, catalog, *nodeName)
		if err != nil {
			return err
		}
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	invoke := func(parent context.Context, action string, r update.Request, key string) (control.Result, error) {
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
			if r.Version != "" && action != "status" {
				input.Query["version"] = r.Version
			}
			if r.JobID != "" {
				input.Query["jobId"] = r.JobID
			}
		} else {
			input.Body, _ = json.Marshal(r)
		}
		requestCtx, cancel := requestContext(parent)
		defer cancel()
		result := client.Execute(requestCtx, operation, input, nil, nil)
		return result, result.Err()
	}
	if *status {
		request.JobID = *jobID
		result, err := invoke(ctx, "status", request, "")
		if err != nil {
			return err
		}
		return encoder.Encode(json.RawMessage(result.Data))
	}
	if *checkOnly {
		result, err := invoke(ctx, "check", request, "")
		if err != nil {
			return err
		}
		if err = encoder.Encode(json.RawMessage(result.Data)); err != nil {
			return err
		}
		return nil
	}
	request.JobID = *rollback
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
	result, err := invoke(ctx, action, request, *key)
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
		result, err = invoke(waitCtx, "status", request, "")
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

func resolveUpdateNode(ctx context.Context, client *control.Client, catalog control.Catalog, name string) (string, error) {
	operation, err := control.FindIn(catalog, "nodes.list")
	if err != nil {
		return "", err
	}
	result := client.Execute(ctx, operation, control.Input{}, nil, nil)
	if err := result.Err(); err != nil {
		return "", err
	}
	var response struct {
		Nodes []struct {
			ID   string `json:"nodeId"`
			Name string `json:"displayName"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(result.Data, &response); err != nil {
		return "", err
	}
	for _, node := range response.Nodes {
		if node.ID == name {
			return node.ID, nil
		}
	}
	matched := ""
	for _, node := range response.Nodes {
		if node.Name == name && node.ID != "" {
			if matched != "" {
				return "", fmt.Errorf("node name %q is ambiguous; use the full node ID", name)
			}
			matched = node.ID
		}
	}
	if matched == "" {
		return "", fmt.Errorf("node %q was not found in the connected Core", name)
	}
	return matched, nil
}
