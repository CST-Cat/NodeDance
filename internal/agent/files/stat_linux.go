//go:build linux

package files

import (
	"os"
	"syscall"
)

type sysStat = syscall.Stat_t

func ownerID(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(stat.Uid)<<32 | int64(stat.Gid)
	}
	return -1
}

func ownerIDs(info os.FileInfo) (int, int) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid)
	}
	return -1, -1
}
