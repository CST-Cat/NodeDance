//go:build !linux

package backup

import (
	"errors"
	"os"
)

func installRestoreStage(stage, destination string, initial os.FileInfo, initiallyExists bool) error {
	_ = stage
	_ = destination
	_ = initial
	_ = initiallyExists
	return errors.New("atomic Core restore installation is supported on Linux only")
}
