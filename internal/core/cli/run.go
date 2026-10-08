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
	"path/filepath"
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
	dev := flags.Bool("dev", false, "development mode; requires a loopback address")
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
		} else if home, err := os.UserConfigDir(); err == nil {
			configPath = filepath.Join(home, "nodedance", "config.json")
		}
	}
	fileConfig, err := config.Load(configPath)
	if err != nil {
		return err
	}
	envListen, _ := lookup("NODEDANCE_LISTEN")
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

	serverInstance, err := server.New(version)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              listen,
		Handler:           serverInstance,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listen, err)
	}
	fmt.Fprintf(stdout, "NodeDance listening on http://%s\n", listen)
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
	fmt.Fprintln(w, "Usage: nodedance serve [--listen IP:port] [--config path] [--dev]")
	fmt.Fprintln(w, "       nodedance version")
	fmt.Fprintln(w, "Default listen address: 127.0.0.1:8180")
}
