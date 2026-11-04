//go:build !linux

package tcppunch

import (
	"errors"
	"syscall"
)

func reuseSocket(_, _ string, _ syscall.RawConn) error {
	return errors.New("TCP simultaneous open requires Linux")
}
