package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CST-Cat/NodeDance/internal/agent"
)

var version = "dev"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nodedance-agent:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "NodeDance Agent %s\n", version)
		return nil
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	case "enroll":
		return runEnroll(ctx, args[1:], stdin, stdout, stderr)
	case "recover":
		return runRecover(ctx, args[1:], stdout, stderr)
	case "run":
		return runAgent(ctx, args[1:], stderr)
	case "install-systemd":
		return runInstallSystemd(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q (try 'nodedance-agent --help')", args[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "NodeDance Agent - outbound-only Linux host management agent")
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  nodedance-agent enroll --server https://core.example --token-stdin [--ca-file path] [--config path]")
	fmt.Fprintln(output, "  nodedance-agent recover [--config path]")
	fmt.Fprintln(output, "  nodedance-agent run [--config path]")
	fmt.Fprintln(output, "  nodedance-agent install-systemd --user <service-user> [--config path] [--enable]")
	fmt.Fprintln(output, "The Agent never accepts inbound management connections. Enrollment tokens are read only from stdin.")
}

func runEnroll(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := newFlagSet("enroll", stderr)
	server := flags.String("server", "", "Core HTTPS URL")
	caFile := flags.String("ca-file", "", "additional trusted CA PEM file")
	configPath := flags.String("config", "", "private Agent config path")
	dev := flags.Bool("dev", false, "allow HTTP only for literal loopback development Core")
	tokenStdin := flags.Bool("token-stdin", false, "read one-time enrollment credential from stdin")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected enroll arguments: %v", flags.Args())
	}
	if strings.TrimSpace(*server) == "" {
		return fmt.Errorf("--server is required")
	}
	if !*tokenStdin {
		return fmt.Errorf("--token-stdin is required; enrollment credentials are never accepted as command-line arguments")
	}
	path, err := resolveConfigPath(*configPath)
	if err != nil {
		return err
	}
	if err := agent.Enroll(ctx, *server, *caFile, *dev, stdin, path); err != nil {
		return err
	}
	config, err := agent.LoadConfig(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Agent enrolled: node=%s agent=%s config=%s\n", config.NodeID, config.AgentID, path)
	return nil
}

func runRecover(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("recover", stderr)
	configPath := flags.String("config", "", "private Agent config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected recover arguments: %v", flags.Args())
	}
	path, err := resolveConfigPath(*configPath)
	if err != nil {
		return err
	}
	if err := agent.Recover(ctx, path); err != nil {
		return err
	}
	config, err := agent.LoadConfig(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Agent identity recovered: node=%s agent=%s\n", config.NodeID, config.AgentID)
	return nil
}

func runAgent(ctx context.Context, args []string, stderr io.Writer) error {
	flags := newFlagSet("run", stderr)
	configPath := flags.String("config", "", "private Agent config path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected run arguments: %v", flags.Args())
	}
	path, err := resolveConfigPath(*configPath)
	if err != nil {
		return err
	}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return agent.Run(runCtx, path, version, stderr)
}

func runInstallSystemd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("install-systemd", stderr)
	serviceUser := flags.String("user", "", "explicit Linux account that will run the Agent")
	configPath := flags.String("config", "/var/lib/nodedance-agent/agent.json", "existing private Agent config path")
	unitDir := flags.String("unit-dir", "/etc/systemd/system", "system unit directory")
	reload := flags.Bool("reload", true, "run systemctl daemon-reload for the system unit directory")
	enable := flags.Bool("enable", false, "enable and start the installed Agent unit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected install-systemd arguments: %v", flags.Args())
	}
	if strings.TrimSpace(*serviceUser) == "" {
		return fmt.Errorf("--user is required; installation never guesses or elevates the service identity")
	}
	path, err := agent.InstallSystemd(ctx, agent.SystemdInstallOptions{
		User: *serviceUser, ConfigPath: *configPath, UnitDir: *unitDir,
		Reload: *reload, EnableNow: *enable,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed %s\n", path)
	if !*enable {
		fmt.Fprintln(stdout, "Enable and start with: systemctl enable --now nodedance-agent.service")
	}
	return nil
}

func resolveConfigPath(value string) (string, error) {
	if value != "" {
		return value, nil
	}
	return agent.DefaultConfigPath()
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("nodedance-agent "+name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}
