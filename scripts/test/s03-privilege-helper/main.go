// Command s03-privilege-helper prepares one owned test config and launches the
// real Agent as an unprivileged UID inside the isolated Linux guest.
package main

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: s03-privilege-helper <chown|assert-owner|identity|exec> ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "chown":
		uid, gid := parseIDs(5)
		for _, path := range os.Args[4:] {
			if err := os.Chown(path, uid, gid); err != nil {
				fatal(fmt.Sprintf("chown %s: %v", path, err))
			}
		}
	case "assert-owner":
		if len(os.Args) < 6 {
			fatal("assert-owner requires uid, gid, mode, and at least one path")
		}
		uid, gid := parseIDs(6)
		mode, err := strconv.ParseUint(os.Args[4], 8, 32)
		if err != nil || mode > 0o7777 {
			fatal("invalid mode")
		}
		for _, path := range os.Args[5:] {
			info, err := os.Lstat(path)
			if err != nil {
				fatal(fmt.Sprintf("inspect %s: %v", path, err))
			}
			if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != os.FileMode(mode) {
				fatal(fmt.Sprintf("%s has unexpected type or mode %#o", path, info.Mode()))
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
				fatal(fmt.Sprintf("%s is not owned by uid=%d gid=%d", path, uid, gid))
			}
			fmt.Printf("S03:AGENT_CREDENTIAL_OWNER path=%q uid=%d gid=%d mode=%04o\n",
				path, stat.Uid, stat.Gid, info.Mode().Perm())
		}
	case "identity":
		if len(os.Args) != 2 {
			fatal("identity accepts no additional arguments")
		}
		fmt.Printf("uid=%d gid=%d\n", os.Geteuid(), os.Getegid())
	case "exec":
		uid, gid := parseIDs(5)
		if len(os.Args) < 5 {
			fatal("missing executable")
		}
		if err := syscall.Setgroups([]int{}); err != nil {
			fatal(fmt.Sprintf("clear supplementary groups: %v", err))
		}
		if err := syscall.Setgid(gid); err != nil {
			fatal(fmt.Sprintf("setgid: %v", err))
		}
		if err := syscall.Setuid(uid); err != nil {
			fatal(fmt.Sprintf("setuid: %v", err))
		}
		command := os.Args[4:]
		if err := syscall.Exec(command[0], command, os.Environ()); err != nil {
			fatal(fmt.Sprintf("exec %s: %v", command[0], err))
		}
	default:
		fatal("unknown operation")
	}
}

func parseIDs(minimumArgs int) (int, int) {
	if len(os.Args) < minimumArgs {
		fatal("missing uid or gid")
	}
	uid, err := strconv.Atoi(os.Args[2])
	if err != nil || uid < 0 {
		fatal("invalid uid")
	}
	gid, err := strconv.Atoi(os.Args[3])
	if err != nil || gid < 0 {
		fatal("invalid gid")
	}
	return uid, gid
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
