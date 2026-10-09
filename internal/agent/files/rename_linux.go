package files

import (
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// renameNoReplace uses directory handles rooted by os.Root and RENAME_NOREPLACE
// so a destination created during the rename cannot be silently overwritten.
func renameNoReplace(root *os.Root, oldName, newName string) error {
	oldDir, oldBase := path.Split(oldName)
	newDir, newBase := path.Split(newName)
	if oldDir == "" {
		oldDir = "."
	}
	if newDir == "" {
		newDir = "."
	}
	source, err := root.Open(oldDir)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := root.Open(newDir)
	if err != nil {
		return err
	}
	defer destination.Close()
	return unix.Renameat2(int(source.Fd()), oldBase, int(destination.Fd()), newBase, unix.RENAME_NOREPLACE)
}
