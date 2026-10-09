package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	corebackup "github.com/CST-Cat/NodeDance/internal/core/backup"
)

func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nodedance backup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dataDir := flags.String("data-dir", "", "path to Core data directory")
	output := flags.String("output", "", "new backup file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*dataDir) == "" || strings.TrimSpace(*output) == "" {
		return errors.New("backup requires --data-dir DIR and --output FILE")
	}
	if err := corebackup.Create(ctx, *dataDir, *output); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Core backup created.")
	return nil
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("nodedance restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "backup file path")
	dataDir := flags.String("data-dir", "", "new or empty Core data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*input) == "" || strings.TrimSpace(*dataDir) == "" {
		return errors.New("restore requires --input FILE and --data-dir NEW_DIR")
	}
	if err := corebackup.Restore(ctx, *input, *dataDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Core data restored.")
	return nil
}
