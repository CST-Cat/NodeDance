//go:build !linux

package files

import "os"

type sysStat = struct{}

func ownerID(os.FileInfo) int64       { return -1 }
func ownerIDs(os.FileInfo) (int, int) { return -1, -1 }
