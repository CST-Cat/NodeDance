package tcppunch

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func ReuseSocketControl(_, _ string, raw syscall.RawConn) error {
	var optionErr error
	err := raw.Control(func(fd uintptr) {
		if optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); optionErr == nil {
			optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		}
	})
	if err != nil {
		return err
	}
	return optionErr
}
