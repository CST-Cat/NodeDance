package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const confirmationTimeout = 2 * time.Minute
const HelperEnvironment = "NODEDANCE_AGENT_HELPER"

// Supervisor is the stable updater helper. It owns the child process and the
// symlink transaction, so candidate failure can be recovered without Core.
func Supervisor(ctx context.Context, stateDir, configPath string) error {
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if err := recoverJournalAtBoot(stateDir); err != nil {
		return err
	}
	current := filepath.Join(stateDir, "bin", "current")
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		command := exec.Command(current, "run", "--config", configPath)
		command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
		command.Env = withHelperEnvironment(os.Environ())
		if err := command.Start(); err != nil {
			// After the candidate symlink is installed, an exec failure is just as
			// much a failed candidate as a process that starts and exits. The
			// helper must restore the previous version before trying again.
			j, journalErr := ReadJournal(stateDir)
			if journalErr == nil && j.State == "awaiting_confirmation" {
				if rollbackErr := Rollback(stateDir); rollbackErr != nil {
					return errors.Join(fmt.Errorf("start managed Agent candidate: %w", err), rollbackErr)
				}
				continue
			}
			if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
				return errors.Join(fmt.Errorf("start managed Agent: %w", err), journalErr)
			}
			return fmt.Errorf("start managed Agent: %w", err)
		}

		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		restart := false
		var updateStarted time.Time
		ticker := time.NewTicker(500 * time.Millisecond)
		for !restart {
			select {
			case <-ctx.Done():
				ticker.Stop()
				stopChild(command, done)
				return nil
			case waitErr := <-done:
				restart = true
				if err := finishAfterChildExit(stateDir); err != nil {
					ticker.Stop()
					return err
				}
				_ = waitErr // A child exit is handled by restarting the selected version.
			case <-ticker.C:
				j, err := ReadJournal(stateDir)
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					ticker.Stop()
					_ = command.Process.Kill()
					<-done
					return err
				}
				switch j.State {
				case "staged":
					// The artifact is durable, but Core has not confirmed that its
					// prepared report was persisted. Keep running the old version.
					continue
				case "prepared":
					if err := ApplyPrepared(stateDir); err != nil {
						ticker.Stop()
						_ = command.Process.Kill()
						<-done
						return err
					}
					_ = command.Process.Signal(os.Interrupt)
					waitChild(done, command)
					restart = true
				case "awaiting_confirmation":
					if updateStarted.IsZero() {
						updateStarted = j.StartedAt
						if updateStarted.IsZero() {
							updateStarted = time.Now()
						}
					}
					if time.Since(updateStarted) > confirmationTimeout {
						_ = command.Process.Kill()
						<-done
						if err := Rollback(stateDir); err != nil {
							ticker.Stop()
							return err
						}
						restart = true
					}
				case "confirmed":
					if err := clearConfirmedJournal(stateDir); err != nil {
						ticker.Stop()
						return err
					}
					updateStarted = time.Time{}
				}
			}
		}
		ticker.Stop()
		if err := ctx.Err(); err != nil {
			return nil
		}
		if err := waitContext(ctx, 3*time.Second); err != nil {
			return nil
		}
	}
}

func recoverJournalAtBoot(stateDir string) error {
	j, err := ReadJournal(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch j.State {
	case "staged":
		return nil
	case "prepared":
		return ApplyPrepared(stateDir)
	case "awaiting_confirmation":
		return Rollback(stateDir)
	case "confirmed":
		// Confirmation is durable. Keep the selected candidate and only remove
		// the recovery record left by a crash between confirmation and cleanup.
		return clearConfirmedJournal(stateDir)
	default:
		return errors.New("Agent update journal state is invalid")
	}
}

func finishAfterChildExit(stateDir string) error {
	j, err := ReadJournal(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	switch j.State {
	case "staged":
		return nil
	case "prepared":
		return ApplyPrepared(stateDir)
	case "awaiting_confirmation":
		return Rollback(stateDir)
	case "confirmed":
		return clearConfirmedJournal(stateDir)
	default:
		return errors.New("Agent update journal state is invalid")
	}
}

func clearConfirmedJournal(stateDir string) error {
	if err := os.Remove(JournalPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(stateDir)
}

func withHelperEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, value := range environment {
		if !strings.HasPrefix(value, HelperEnvironment+"=") {
			result = append(result, value)
		}
	}
	return append(result, HelperEnvironment+"=1")
}

func waitChild(done <-chan error, command *exec.Cmd) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func stopChild(command *exec.Cmd, done <-chan error) {
	_ = command.Process.Signal(os.Interrupt)
	waitChild(done, command)
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// ParseStateDir is used by the stable CLI helper and rejects non-private path
// surprises such as an empty path or a filesystem root.
func ParseStateDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("--state-dir is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if abs == string(filepath.Separator) {
		return "", errors.New("unsafe Agent state directory")
	}
	return filepath.Clean(abs), nil
}
