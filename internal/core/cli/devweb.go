package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/CST-Cat/NodeDance/internal/i18n"
)

// DevWeb constants fix the shared development preview instance: it always
// serves the newest Web build on port 8181 with its own data directory,
// separate from the production Core on 8180.
const (
	devWebListen  = "0.0.0.0:8181"
	devWebDataDir = "/var/lib/nodedance-devweb"
	devWebOrigin  = "https://devweb.2023989.xyz"
	devWebExe     = "/usr/local/bin/nodedance-devweb"
)

func runDevWeb(ctx context.Context, args []string, lookup LookupEnv, stdin io.Reader, stdout, stderr io.Writer, version string) error {
	flags := flag.NewFlagSet("nodedance devweb", flag.ContinueOnError)
	flags.SetOutput(stderr)
	skipBuild := flags.Bool("no-build", false, i18n.TextFor(stderr, "start the preview without rebuilding Web assets and binary"))
	reset := flags.Bool("reset", false, i18n.TextFor(stderr, "clear the preview data directory before starting"))
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	repo, err := repositoryRoot()
	if err != nil {
		return err
	}
	if !*skipBuild {
		if err := buildDevWeb(repo, stdout, stderr); err != nil {
			return err
		}
	} else if _, err := os.Stat(devWebExe); err != nil {
		return fmt.Errorf("no previous devweb build at %s; run without --no-build", devWebExe)
	}
	if *reset {
		if err := removeDevWebData(); err != nil {
			return err
		}
	}
	if err := prepareDevWebData(); err != nil {
		return err
	}
	serve := exec.CommandContext(ctx, devWebExe, "serve",
		"--listen", devWebListen,
		"--data-dir", devWebDataDir,
		"--public-origin", devWebOrigin,
	)
	serve.Env = append(os.Environ(), "NODEDANCE_LISTEN=", "NODEDANCE_DATA_DIR=")
	serve.Stdin = stdin
	serve.Stdout = stdout
	serve.Stderr = stderr
	if err := serve.Start(); err != nil {
		return fmt.Errorf("start devweb Core: %w", err)
	}
	fmt.Fprintf(stdout, i18n.TextFor(stdout, "Development Web preview: https://devweb.2023989.xyz (local %s, data %s)\n"), devWebListen, devWebDataDir)
	serveCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	wait := make(chan error, 1)
	go func() { wait <- serve.Wait() }()
	select {
	case <-serveCtx.Done():
		if serve.Process != nil {
			_ = serve.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			_ = serve.Process.Kill()
		}
		return nil
	case err := <-wait:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("devweb Core exited: %w", err)
	}
}

func prepareDevWebData() error {
	if err := os.MkdirAll(devWebDataDir, 0700); err == nil {
		return nil
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("prepare %s requires root: %w", devWebDataDir, err)
	}
	command := exec.Command(sudo, "mkdir", "-p", devWebDataDir)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("prepare %s: %w", devWebDataDir, err)
	}
	return exec.Command(sudo, "chown", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), devWebDataDir).Run()
}

func removeDevWebData() error {
	if err := os.RemoveAll(devWebDataDir); err == nil {
		return nil
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return fmt.Errorf("clear %s requires root: %w", devWebDataDir, err)
	}
	if err := exec.Command(sudo, "rm", "-rf", devWebDataDir).Run(); err != nil {
		return fmt.Errorf("clear %s: %w", devWebDataDir, err)
	}
	return nil
}

func repositoryRoot() (string, error) {
	if _, err := os.Stat("web/package.json"); err == nil {
		if abs, err := filepath.Abs("."); err == nil {
			if _, err := os.Stat(filepath.Join(abs, "go.mod")); err == nil {
				return abs, nil
			}
		}
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", errors.New("run from the NodeDance repository; devweb rebuilds web/ and repackages the binary")
	}
	return strings.TrimSpace(string(out)), nil
}

func buildDevWeb(repo string, stdout, stderr io.Writer) error {
	pnpm, err := exec.LookPath("pnpm")
	if err != nil {
		return errors.New("pnpm is required to build the Web assets")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return errors.New("go is required to repackage the preview binary")
	}
	run := func(name string, arguments ...string) error {
		command := exec.Command(name, arguments...)
		command.Dir = repo
		command.Stdout = stdout
		command.Stderr = stderr
		command.Env = append(os.Environ(), "NODEDANCE_LISTEN=", "NODEDANCE_DATA_DIR=")
		fmt.Fprintf(stdout, i18n.TextFor(stdout, "devweb: %s %s\n"), name, strings.Join(arguments, " "))
		return command.Run()
	}
	if err := run(pnpm, "--dir", "web", "install", "--frozen-lockfile"); err != nil {
		return fmt.Errorf("install web dependencies: %w", err)
	}
	if err := run(pnpm, "--dir", "web", "run", "build"); err != nil {
		return fmt.Errorf("build web assets: %w", err)
	}
	if err := run(goTool, "build", "-trimpath", "-o", filepath.Join(repo, ".build", "nodedance-devweb"), "./cmd/nodedance"); err != nil {
		return fmt.Errorf("build devweb binary: %w", err)
	}
	if err := installDevWebBinary(filepath.Join(repo, ".build", "nodedance-devweb")); err != nil {
		return err
	}
	return nil
}

func installDevWebBinary(built string) error {
	if _, err := os.Stat(devWebExe); err == nil {
		if same, err := sameFile(built, devWebExe); err == nil && same {
			return nil
		}
	}
	staged := filepath.Join(os.TempDir(), "nodedance-devweb.new")
	if err := copyFile(built, staged, 0755); err != nil {
		return fmt.Errorf("stage %s: %w", staged, err)
	}
	if err := os.Rename(staged, devWebExe); err != nil {
		if !os.IsPermission(err) {
			return fmt.Errorf("install %s: %w", devWebExe, err)
		}
		sudo, err := exec.LookPath("sudo")
		if err != nil {
			return fmt.Errorf("install %s requires root: %w", devWebExe, err)
		}
		command := exec.Command(sudo, "mv", staged, devWebExe)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("install %s with sudo: %w", devWebExe, err)
		}
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	first, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	second, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(first, second), nil
}

func copyFile(source, target string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, data, mode)
}
