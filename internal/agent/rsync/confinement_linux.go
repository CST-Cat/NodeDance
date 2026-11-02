package rsync

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"unsafe"

	"golang.org/x/sys/unix"
)

func landlockABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return int(version), nil
}

func confinementAvailable() error {
	version, err := landlockABI()
	if err != nil || version < 3 {
		return errors.New("rsync requires Landlock with truncate protection")
	}
	return nil
}

func addLandlockPath(ruleset, fd int, access uint64) error {
	var attr [12]byte
	*(*uint64)(unsafe.Pointer(&attr[0])) = access
	*(*int32)(unsafe.Pointer(&attr[8])) = int32(fd)
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr[0])), 0, 0, 0)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return errno
	}
	return nil
}

func runConfined(args []string) error {
	if len(args) < 2 || args[1] != "--server" && args[1] != "--version" {
		return errors.New("invalid rsync process invocation")
	}
	size, err := strconv.ParseUint(args[0], 10, 64)
	if err != nil {
		return err
	}
	version, err := landlockABI()
	if err != nil || version < 3 {
		return errors.New("rsync confinement is unavailable")
	}
	runtime.LockOSThread()
	directory := os.NewFile(3, "rsync-stage")
	payload := os.NewFile(4, "rsync-payload")
	binary := os.NewFile(5, "rsync-binary")
	for index, file := range []*os.File{directory, payload, binary} {
		info, err := file.Stat()
		if err != nil || index == 0 && (!info.IsDir() || info.Mode().Perm() != 0700) || index > 0 && !info.Mode().IsRegular() {
			return errors.New("invalid pinned rsync process file")
		}
	}
	attr := struct{ FS, Net uint64 }{FS: 0x7fff}
	attrSize := uintptr(8)
	if version >= 4 {
		attr.Net = unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP
		attrSize = 16
	}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), attrSize, 0)
	if errno != 0 {
		return errno
	}
	defer unix.Close(int(ruleset))
	if err := addLandlockPath(int(ruleset), int(directory.Fd()), unix.LANDLOCK_ACCESS_FS_READ_DIR); err != nil {
		return err
	}
	if err := addLandlockPath(int(ruleset), int(payload.Fd()), unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_WRITE_FILE|unix.LANDLOCK_ACCESS_FS_TRUNCATE); err != nil {
		return err
	}
	if err := addLandlockPath(int(ruleset), int(binary.Fd()), unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_EXECUTE); err != nil {
		return err
	}
	if err := unix.Fchdir(int(directory.Fd())); err != nil {
		return err
	}
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &unix.Rlimit{Cur: size, Max: size}); err != nil {
		return err
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return errno
	}
	argv := append([]string{"rsync"}, args[1:]...)
	return unix.Exec("/proc/self/fd/5", argv, []string{"PATH=", "LC_ALL=C"})
}
