//go:build !linux

package tcppunch

import (
	"errors"
	"syscall"
)

func ReuseSocketControl(_, _ string, _ syscall.RawConn) error {
	return errors.New("TCP simultaneous open requires Linux")
}
