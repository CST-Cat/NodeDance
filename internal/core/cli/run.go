package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"github.com/CST-Cat/NodeDance/internal/i18n"
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
	return RunWithInput(ctx, args, lookup, os.Stdin, stdout, stderr, version)
}

func RunWithInput(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer, version string) (resultErr error) {
	var err error
	ctx, args, err = i18n.Options(ctx, args, lookup)
	if err != nil {
		return err
	}
	defer func() { resultErr = i18n.From(ctx).Error(resultErr) }()
	stdout = i18n.Writer(stdout, i18n.From(ctx))
	stderr = i18n.Writer(stderr, i18n.From(ctx))
	if len(args) == 0 {
		return runServe(ctx, nil, lookup, stdin, stdout, stderr, version)
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, i18n.TextFor(stdout, "NodeDance %s\n"), version)
		return nil
	case "--help", "-h", "help":
		printUsage(stdout)
		return nil
	case "operations", "describe":
		return runOperationCatalog(ctx, args[1:], lookup, stdout, stderr)
	case "tui":
		return runTUI(ctx, args[1:], lookup, stdin, stdout, stderr, version)
	case "agent":
		return runAgentCommand(ctx, args[1:], stdin, stdout, stderr)
	case "call":
		if len(args) < 2 {
			return errors.New("usage: nodedance call OPERATION [flags]")
		}
		return runControl(ctx, args[1], args[2:], lookup, stdin, stdout, stderr, version)
	case "serve":
		return runServe(ctx, args[1:], lookup, stdin, stdout, stderr, version)
	case "backup":
		return runBackup(ctx, args[1:], stdout, stderr)
	case "restore":
		return runRestore(ctx, args[1:], stdout, stderr)
	default:
		name, rest := args[0], args[1:]
		if !strings.Contains(name, ".") && len(rest) > 0 {
			name += "." + rest[0]
			rest = rest[1:]
		}
		return runControl(ctx, name, rest, lookup, stdin, stdout, stderr, version)
	}
}

func runServe(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer, version string) error {
	flags := flag.NewFlagSet("nodedance serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenFlag := flags.String("listen", "", i18n.TextFor(stderr, "IP:port to listen on"))
	configFlag := flags.String("config", "", i18n.TextFor(stderr, "path to config JSON file"))
	dataDirFlag := flags.String("data-dir", "", i18n.TextFor(stderr, "path to Core data directory"))
	maxFileBytesFlag := flags.String("max-file-bytes", "", i18n.TextFor(stderr, "maximum single file transfer size in bytes (default 1073741824)"))
	historyRetentionDaysFlag := flags.String("history-retention-days", "", i18n.TextFor(stderr, "non-metric task, event, and audit history retention in days (default 90)"))
	publicOriginFlag := flags.String("public-origin", "", i18n.TextFor(stderr, "public HTTPS origin used for browser Origin validation"))
	trustedProxiesFlag := flags.String("trusted-proxies", "", i18n.TextFor(stderr, "comma-separated trusted proxy IPs or CIDRs"))
	tlsCertFlag := flags.String("tls-cert", "", i18n.TextFor(stderr, "TLS certificate PEM file; must be used with --tls-key"))
	tlsKeyFlag := flags.String("tls-key", "", i18n.TextFor(stderr, "TLS private key PEM file; must be used with --tls-cert"))
	dev := flags.Bool("dev", false, i18n.TextFor(stderr, "development mode; requires a loopback address"))
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
	tlsCertificate, err := loadTransportCertificate(*tlsCertFlag, *tlsKeyFlag)
	if err != nil {
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
		DataDir:                   dataDir,
		Development:               *dev,
		PublicOrigin:              publicOrigin,
		TrustedProxies:            trustedProxyValues,
		SessionIdleTimeout:        idleTimeout,
		LoginMaxAttempts:          loginMaxAttempts,
		LoginLockoutDuration:      loginLockout,
		WebSocketCheckInterval:    websocketCheckInterval,
		FileTransferLimit:         fileTransferLimit,
		NonMetricHistoryRetention: time.Duration(historyRetentionDays) * 24 * time.Hour,
	})
	if err != nil {
		return err
	}
	defer serverInstance.Close()
	if _, pending := serverInstance.SetupCredentialPath(); pending {
		username, password, err := promptInitialAdministrator(stdin, stdout)
		if err != nil {
			return err
		}
		if err := serverInstance.BootstrapAdministrator(ctx, username, password); err != nil {
			return fmt.Errorf("initialize administrator: %w", err)
		}
	}
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           serverInstance,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if tlsCertificate != nil {
		httpServer.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*tlsCertificate}}
	}
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	scheme := "http"
	if tlsCertificate != nil {
		scheme = "https"
	}
	fmt.Fprintf(stdout, i18n.TextFor(stdout, "NodeDance listening on %s://%s\n"), scheme, listen)
	if info, err := os.Lstat(serverInstance.InitialAdminPath()); err == nil && info.Mode().IsRegular() {
		fmt.Fprintf(stdout, i18n.TextFor(stdout, "Initial administrator credentials (0600; removed after first login): %s\n"), serverInstance.InitialAdminPath())
	}
	go func() {
		if tlsCertificate != nil {
			serveErr <- httpServer.ServeTLS(listener, "", "")
			return
		}
		serveErr <- httpServer.Serve(listener)
	}()
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
		return fmt.Errorf("serve %s on %s: %w", scheme, listen, err)
	}
}

func loadTransportCertificate(certPath, keyPath string) (*tls.Certificate, error) {
	if (certPath == "") != (keyPath == "") {
		return nil, errors.New("--tls-cert and --tls-key must be provided together")
	}
	if certPath == "" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load Core TLS certificate and key: %w", err)
	}
	return &certificate, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, i18n.TextFor(w, "NodeDance - unified Linux server and container management"))
	fmt.Fprintln(w, i18n.TextFor(w, "Usage: nodedance serve [--listen IP:port] [--config path] [--data-dir path] [--max-file-bytes bytes] [--history-retention-days days] [--public-origin https://host] [--trusted-proxies IP/CIDR,...] [--tls-cert PEM --tls-key PEM] [--dev]"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance backup --data-dir DIR --output FILE"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance restore --input FILE --data-dir NEW_DIR"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance version"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance operations [NAME]  # JSON operation catalog / schemas"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance call NAME --server https://core --params JSON --query JSON --body @FILE"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance nodes list [connection flags]  # resource/action aliases"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance tui [--server https://core] [connection flags]"))
	fmt.Fprintln(w, i18n.TextFor(w, "       nodedance agent COMMAND [flags]  # root Agent lifecycle"))
	fmt.Fprintln(w, i18n.TextFor(w, "Global language: --lang en|zh-CN (or NODEDANCE_LANG)"))
	fmt.Fprintln(w, i18n.TextFor(w, "Default listen address: 127.0.0.1:8180"))
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
