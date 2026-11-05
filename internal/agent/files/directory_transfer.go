package files

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
	"golang.org/x/sys/unix"
)

func (e *TaskExecutor) OpenTransfer(ctx context.Context, virtual string, directory bool) (_ *os.File, entry protocol.FileEntry, failure error) {
	if !directory {
		return e.service.OpenDownload(virtual)
	}
	entry, failure = e.service.Stat(virtual)
	if failure != nil {
		return nil, entry, failure
	}
	if entry.Kind != "directory" || virtual == "/" || e.service.containsProtected(virtual) {
		return nil, entry, ErrInvalidPath
	}
	var random [24]byte
	if _, failure = rand.Read(random[:]); failure != nil {
		return nil, entry, failure
	}
	name := ".directory-transfer-" + hex.EncodeToString(random[:])
	file, err := e.stateOpen(name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0600)
	if err != nil {
		return nil, entry, err
	}
	if err = unix.Unlinkat(int(e.stateRoot.Fd()), name, 0); err != nil {
		file.Close()
		return nil, entry, err
	}
	defer func() {
		if failure != nil {
			file.Close()
		}
	}()
	digest := sha256.New()
	out := &boundedFileWriter{ctx: ctx, out: io.MultiWriter(file, digest), remaining: e.service.limit}
	compressed := gzip.NewWriter(out)
	archive := tar.NewWriter(compressed)
	add := func(info os.FileInfo, relative string) (io.Writer, error) {
		if len(relative) > 4096 {
			return nil, ErrLimitExceeded
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil, err
		}
		header.Name, header.Uid, header.Gid, header.Uname, header.Gname = relative, 0, 0, "", ""
		header.ModTime, header.AccessTime, header.ChangeTime = time.Time{}, time.Time{}, time.Time{}
		header.Mode = int64(info.Mode().Perm())
		if err = archive.WriteHeader(header); err != nil {
			return nil, err
		}
		return archive, nil
	}
	count := 0
	var total int64
	err = e.service.archiveTree(ctx, add, virtual, path.Base(virtual), 0, &count, &total)
	err = errors.Join(err, archive.Close(), compressed.Close())
	if err != nil {
		return nil, entry, err
	}
	entry.Size, err = file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, entry, err
	}
	entry.Version = hex.EncodeToString(digest.Sum(nil))
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, entry, err
	}
	return file, entry, nil
}

func directoryTransferStage(target, taskID string) string {
	return path.Join(path.Dir(target), "."+path.Base(target)+".nodedance-directory-"+taskID)
}

func (u *Upload) commitDirectory(ctx context.Context, taskID string) error {
	if u == nil || u.closed || u.file == nil {
		return errors.New("upload is not active")
	}
	defer u.Abort()
	if u.initialInfo != nil || u.expectedVersion != "" {
		return ErrExists
	}
	if u.written != u.expectedSize {
		return io.ErrUnexpectedEOF
	}
	if hex.EncodeToString(u.hash.Sum(nil)) != u.expectedDigest {
		return ErrTransferDigest
	}
	if err := u.file.Sync(); err != nil {
		return err
	}
	stage := directoryTransferStage(u.targetVirtual, taskID)
	return u.service.extractArchiveRoot(ctx, u.file, u.expectedSize, stage, path.Dir(u.targetVirtual), path.Base(u.targetVirtual))
}

func (e *TaskExecutor) verifyDirectoryTransfer(ctx context.Context, spec protocol.FileTaskSpec) (bool, error) {
	file, entry, err := e.OpenTransfer(ctx, spec.Path, true)
	if err != nil {
		return false, err
	}
	defer file.Close()
	return entry.Size == spec.Size && entry.Version == spec.SHA256, nil
}
