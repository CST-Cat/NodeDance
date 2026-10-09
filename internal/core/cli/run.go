package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/config"
	"github.com/CST-Cat/NodeDance/internal/core/server"
)

type LookupEnv func(string) (string, bool)

func Run(ctx context.Context, args []string, lookup LookupEnv, stdout, stderr io.Writer, version string) error {
	if len(args) == 0 {
		return runServe(ctx, nil, lookup, stdout, stderr, version)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "NodeDance %s\n", version)
		return nil
	case "--help", "-h", "help":
		printUsage(stdout)
		return nil
	case "serve":
		return runServe(ctx, args[1:], lookup, stdout, stderr, version)
	default:
		return fmt.Errorf("unknown command %q (try 'nodedance --help')", args[0])
	}
}

func runServe(ctx context.Context, args []string, lookup LookupEnv, stdout, stderr io.Writer, version string) error {
	flags := flag.NewFlagSet("nodedance serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenFlag := flags.String("listen", "", "IP:port to listen on")
	configFlag := flags.String("config", "", "path to config JSON file")
	dataDirFlag := flags.String("data-dir", "", "path to Core data directory")
	maxFileBytesFlag := flags.String("max-file-bytes", "", "maximum single file transfer size in bytes (default 1073741824)")
	historyRetentionDaysFlag := flags.String("history-retention-days", "", "non-metric task, event, and audit history retention in days (default 90)")
	publicOriginFlag := flags.String("public-origin", "", "public HTTPS origin used for browser Origin validation")
	trustedProxiesFlag := flags.String("trusted-proxies", "", "comma-separated trusted proxy IPs or CIDRs")
	dev := flags.Bool("dev", false, "development mode; requires a loopback address")
	updatePublicKey := flags.String("agent-update-public-key", "", "trusted Ed25519 Agent release public key (base64)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	configPath := *configFlag
	if configPath == "" {
		if value, ok := lookup("NODEDANCE_CONFIG"); ok && value != "" {
			configPath = value
		} else if configHome, err := os.UserConfigDir(); err == nil {
			configPath = config.DefaultConfigPath(configHome)
		}
	}
	fileConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}
	envListen, _ := lookup("NODEDANCE_LISTEN")
	envDataDir, _ := lookup("NODEDANCE_DATA_DIR")
	dataDir := config.ResolveDataDir(config.Sources{
		CLIDataDir:  *dataDirFlag,
		EnvDataDir:  envDataDir,
		Config:      fileConfig,
		XDGDataHome: lookupValue(lookup, "XDG_DATA_HOME"),
		UserHome:    userHome(),
	})
	listen, err := config.ResolveListen(config.Sources{
		CLIListen: *listenFlag,
		EnvListen: envListen,
		Config:    fileConfig,
	})
	if err != nil {
		return err
	}
	if *dev {
		if err := config.ValidateDevListen(listen); err != nil {
			return err
		}
	} else if err := config.ValidateListen(listen); err != nil {
		return err
	}
	publicOrigin := firstNonEmpty(*publicOriginFlag, lookupValue(lookup, "NODEDANCE_PUBLIC_ORIGIN"), fileConfig.PublicOrigin)
	if err := config.ValidatePublicOrigin(publicOrigin, *dev); err != nil {
		return err
	}
	trustedProxyValues := fileConfig.TrustedProxies
	if value := firstNonEmpty(*trustedProxiesFlag, lookupValue(lookup, "NODEDANCE_TRUSTED_PROXIES")); value != "" {
		trustedProxyValues = splitCSV(value)
	}
	if _, err := config.ParseTrustedProxies(trustedProxyValues); err != nil {
		return err
	}
	idleTimeout, loginMaxAttempts, loginLockout, err := config.RuntimeValues(fileConfig, *dev)
	if err != nil {
		return err
	}
	websocketCheckInterval, err := config.RuntimeWebSocketCheckInterval(fileConfig, *dev)
	if err != nil {
		return err
	}
	fileTransferLimit, err := config.RuntimeFileTransferLimit(fileConfig, lookupValue(lookup, "NODEDANCE_MAX_FILE_BYTES"), *maxFileBytesFlag)
	if err != nil {
		return err
	}
	historyRetentionDays, err := config.RuntimeNonMetricHistoryRetentionDays(fileConfig, lookupValue(lookup, "NODEDANCE_HISTORY_RETENTION_DAYS"), *historyRetentionDaysFlag)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	defer listener.Close()
	serverInstance, err := server.New(version, server.Options{
		DataDir:                    dataDir,
		Development:                *dev,
		PublicOrigin:               publicOrigin,
		TrustedProxies:             trustedProxyValues,
		SessionIdleTimeout:         idleTimeout,
		LoginMaxAttempts:           loginMaxAttempts,
		LoginLockoutDuration:       loginLockout,
		WebSocketCheckInterval:     websocketCheckInterval,
		FileTransferLimit:          fileTransferLimit,
		NonMetricHistoryRetention:  time.Duration(historyRetentionDays) * 24 * time.Hour,
		AgentUpdatePublicKeyBase64: firstNonEmpty(*updatePublicKey, lookupValue(lookup, "NODEDANCE_AGENT_UPDATE_PUBLIC_KEY")),
	})
	if err != nil {
		return err
	}
	defer serverInstance.Close()
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           serverInstance,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	fmt.Fprintf(stdout, "NodeDance listening on http://%s\n", listen)
	if credentialPath, ok := serverInstance.SetupCredentialPath(); ok {
		fmt.Fprintf(stdout, "First-run setup credential file: %s\n", credentialPath)
	}
	go func() { serveErr <- httpServer.Serve(listener) }()
	select {
	case <-serveCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down server: %w", err)
		}
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP on %s: %w", listen, err)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "NodeDance - unified Linux server and container management")
	fmt.Fprintln(w, "Usage: nodedance serve [--listen IP:port] [--config path] [--data-dir path] [--max-file-bytes bytes] [--history-retention-days days] [--public-origin https://host] [--trusted-proxies IP/CIDR,...] [--dev]")
	fmt.Fprintln(w, "       nodedance version")
	fmt.Fprintln(w, "Default listen address: 127.0.0.1:8180")
}

func lookupValue(lookup LookupEnv, name string) string {
	value, _ := lookup(name)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitCSV(value string) []string {
	items := strings.Split(value, ",")
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func userHome() string {
	home, _ := os.UserHomeDir()
	return home
}
