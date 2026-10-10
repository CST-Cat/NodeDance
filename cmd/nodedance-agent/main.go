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
	case "uninstall-systemd":
		return runUninstallSystemd(ctx, args[1:], stdout, stderr)
	case "validate-systemd-state":
		return runValidateSystemdState(args[1:], stdout, stderr)
	case "prepare-systemd-state":
		return runPrepareSystemdState(args[1:], stdout, stderr)
	case "validate-file-root":
		return runValidateFileRoot(args[1:], stdout, stderr)
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
	fmt.Fprintln(output, "  nodedance-agent validate-file-root --file-root <absolute-directory> [--state-dir <path>]")
	fmt.Fprintln(output, "  nodedance-agent validate-systemd-state [--config path]")
	fmt.Fprintln(output, "  nodedance-agent prepare-systemd-state [--user root] [--require-new]")
	fmt.Fprintln(output, "  nodedance-agent install-systemd [--user root] [--config path] [--file-root absolute-directory] [--no-file-root] [--supplementary-group <gid>] [--enable]")
	fmt.Fprintln(output, "  nodedance-agent uninstall-systemd [--unit-dir path]")
	fmt.Fprintln(output, "The Agent never accepts inbound management connections. Enrollment tokens are read only from stdin.")
}

func runValidateSystemdState(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("validate-systemd-state", stderr)
	configPath := flags.String("config", "/var/lib/nodedance-agent/agent.json", "Agent config path to validate without following symlinks")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected validate-systemd-state arguments: %v", flags.Args())
	}
	if err := agent.ValidateSystemdStatePath(*configPath); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Agent state path is safe")
	return nil
}

func runPrepareSystemdState(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("prepare-systemd-state", stderr)
	serviceUser := flags.String("user", "", "root-only Agent service account; omitted defaults to root")
	requireNew := flags.Bool("require-new", false, "create a fresh state directory and refuse to adopt existing state")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected prepare-systemd-state arguments: %v", flags.Args())
	}
	userExplicit := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "user" {
			userExplicit = true
		}
	})
	if userExplicit {
		if strings.TrimSpace(*serviceUser) != "root" {
			return fmt.Errorf("NodeDance Agent systemd service only supports --user root")
		}
	}
	if *requireNew {
		identity, err := agent.PrepareNewSystemdStateForUser(*serviceUser, "/var/lib/nodedance-agent/agent.json")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created:%s\n", identity)
		return nil
	}
	exists, err := agent.PrepareSystemdStateForUser(*serviceUser, "/var/lib/nodedance-agent/agent.json")
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintln(stdout, "existing")
	} else {
		fmt.Fprintln(stdout, "new")
	}
	return nil
}

// runValidateFileRoot is a read-only target-side preflight for SSH deployment.
// It deliberately calls the same validator used by foreground and systemd
// Agent startup without creating configuration or consuming enrollment.
func runValidateFileRoot(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("validate-file-root", stderr)
	fileRoot := flags.String("file-root", "", "absolute existing host directory exposed to the Agent file manager")
	stateDir := flags.String("state-dir", "/var/lib/nodedance-agent", "private Agent state directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected validate-file-root arguments: %v", flags.Args())
	}
	if _, err := agent.ValidateFileRoot(*fileRoot, *stateDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Agent file root is valid")
	return nil
}

func runEnroll(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := requireAgentCommandRoot("enrollment", os.Geteuid()); err != nil {
		return err
	}
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
	if isTerminalInput(stdin) {
		fmt.Fprintln(stderr, "Paste the one-time enrollment token, press Enter, then press Ctrl-D on a blank line (Linux/macOS) or Ctrl-Z followed by Enter (Windows) to finish stdin.")
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

func isTerminalInput(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runRecover(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := requireAgentCommandRoot("recovery", os.Geteuid()); err != nil {
		return err
	}
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
	if err := requireAgentCommandRoot("runtime", os.Geteuid()); err != nil {
		return err
	}
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

func requireAgentCommandRoot(operation string, uid int) error {
	if uid == 0 {
		return nil
	}
	return fmt.Errorf("NodeDance Agent %s requires root privileges for full host management", operation)
}

func runInstallSystemd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("install-systemd", stderr)
	serviceUser := flags.String("user", "", "root-only Agent service account; omitted defaults to root")
	configPath := flags.String("config", "/var/lib/nodedance-agent/agent.json", "existing private Agent config path")
	unitDir := flags.String("unit-dir", "/etc/systemd/system", "system unit directory")
	fileRoot := flags.String("file-root", "", "explicit absolute, existing host directory exposed to the Agent file manager")
	noFileRoot := flags.Bool("no-file-root", false, "disable host file access, even when replacing a previously configured unit")
	supplementaryGroup := flags.String("supplementary-group", "", "additional Linux group name or numeric GID (used for authorized Docker socket access)")
	reload := flags.Bool("reload", true, "run systemctl daemon-reload for the system unit directory")
	enable := flags.Bool("enable", false, "enable and start the installed Agent unit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected install-systemd arguments: %v", flags.Args())
	}
	fileRootSpecified := false
	flags.Visit(func(flag *flag.Flag) {
		if flag.Name == "file-root" {
			fileRootSpecified = true
		}
	})
	if *noFileRoot && fileRootSpecified {
		return fmt.Errorf("--file-root and --no-file-root cannot be used together")
	}
	groups := []string(nil)
	if *supplementaryGroup != "" {
		groups = []string{*supplementaryGroup}
	}
	path, err := agent.InstallSystemd(ctx, agent.SystemdInstallOptions{
		User: *serviceUser, ConfigPath: *configPath, UnitDir: *unitDir,
		ExpectedUnitState:  os.Getenv("NODEDANCE_INTERNAL_EXPECTED_SYSTEMD_UNIT_STATE"),
		ExpectedUnitSHA256: os.Getenv("NODEDANCE_INTERNAL_EXPECTED_SYSTEMD_UNIT_SHA256"),
		ResultSHA256Path:   os.Getenv("NODEDANCE_INTERNAL_SYSTEMD_RESULT_FILE"),
		FileRoot:           *fileRoot, FileRootSpecified: fileRootSpecified, DisableFileRoot: *noFileRoot,
		SupplementaryGroups: groups, Reload: *reload, EnableNow: *enable,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed %s\n", path)
	if !*enable {
		fmt.Fprintln(stdout, "Enable and start with: systemctl enable --now nodedance-agent.service")
	}
	installedUnit, readErr := os.ReadFile(path)
	unitText := string(installedUnit)
	switch {
	case readErr == nil && strings.Contains(unitText, "Environment=NODEDANCE_AGENT_FILE_ACCESS=disabled"):
		fmt.Fprintln(stdout, "Host file access is disabled by the systemd Agent unit.")
	case readErr == nil && strings.Contains(unitText, "NODEDANCE_AGENT_FILE_ROOT=\"/\""):
		fmt.Fprintln(stdout, "Host file access covers the host filesystem; kernel/runtime paths and Agent credentials are protected.")
	case readErr == nil && strings.Contains(unitText, "NODEDANCE_AGENT_FILE_ROOT="):
		fmt.Fprintln(stdout, "Host file access is confined to the configured absolute file root.")
	default:
		fmt.Fprintln(stdout, "Host file access is disabled; no absolute file root is configured.")
	}
	return nil
}

func runUninstallSystemd(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("uninstall-systemd", stderr)
	unitDir := flags.String("unit-dir", "/etc/systemd/system", "system unit directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected uninstall-systemd arguments: %v", flags.Args())
	}
	if err := agent.UninstallSystemd(ctx, *unitDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Uninstalled the NodeDance Agent systemd unit; Agent configuration and identity were preserved.")
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
