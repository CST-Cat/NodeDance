//go:build linux

package backup

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func installRestoreStage(stage, destination string, initial os.FileInfo, initiallyExists bool) error {
	if err := verifyTargetUnchanged(destination, initial, initiallyExists); err != nil {
		return err
	}
	if !initiallyExists {
		if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return errors.New("restore target appeared while restore was being installed")
			}
			return fmt.Errorf("atomically install restored Core data without replacing a target: %w", err)
		}
		return nil
	}

	stageInfo, err := os.Lstat(stage)
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore workspace changed before installation")
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("atomically exchange restored data with the empty target: %w", err)
	}

	oldTarget, oldTargetErr := os.Lstat(stage)
	installedStage, installedStageErr := os.Lstat(destination)
	if oldTargetErr != nil || installedStageErr != nil {
		return errors.New("restore target changed during atomic installation; exchanged paths could not be inspected safely")
	}
	if initial == nil || !os.SameFile(initial, oldTarget) || !oldTarget.IsDir() || oldTarget.Mode()&os.ModeSymlink != 0 || !os.SameFile(stageInfo, installedStage) {
		return rollbackRestoreExchange(stage, destination, oldTarget, stageInfo,
			errors.New("restore target changed during atomic installation"))
	}
	entries, err := os.ReadDir(stage)
	if err != nil || len(entries) != 0 {
		return rollbackRestoreExchange(stage, destination, oldTarget, stageInfo,
			errors.New("restore target was no longer empty during atomic installation"))
	}
	if err := os.Remove(stage); err != nil {
		return rollbackRestoreExchange(stage, destination, oldTarget, stageInfo,
			fmt.Errorf("remove the verified empty restore target: %w", err))
	}
	return nil
}

func rollbackRestoreExchange(stage, destination string, exchangedTarget, installedStage os.FileInfo, cause error) error {
	oldTarget, oldTargetErr := os.Lstat(stage)
	currentDestination, destinationErr := os.Lstat(destination)
	if oldTargetErr != nil || destinationErr != nil || exchangedTarget == nil || installedStage == nil || !os.SameFile(exchangedTarget, oldTarget) || !os.SameFile(installedStage, currentDestination) {
		return fmt.Errorf("%w; restore exchange could not be reversed safely", cause)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_EXCHANGE); err != nil {
		return errors.Join(cause, fmt.Errorf("restore original target after failed install exchange: %w", err))
	}
	return cause
}
