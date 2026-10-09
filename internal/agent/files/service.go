// Package files provides the Agent-side, root-confined filesystem operations
// used by the S10 remote file manager. Transfer methods write to sibling
// temporary files and never buffer a full upload in memory.
package files

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var (
	ErrConflict              = errors.New("file version conflict")
	ErrExists                = errors.New("destination already exists")
	ErrNotText               = errors.New("file is not a supported UTF-8 text file")
	ErrTextTooLarge          = errors.New("text file exceeds the edit limit")
	ErrLimitExceeded         = errors.New("file transfer exceeds the configured limit")
	ErrInvalidPath           = errors.New("file path is invalid or outside the configured root")
	ErrConfirmation          = errors.New("exact target confirmation is required")
	ErrTransferDigest        = errors.New("file transfer digest does not match")
	ErrDirectoryLarge        = errors.New("directory listing exceeds the protocol response limit")
	ErrMutationResultUnknown = errors.New("file mutation committed but its result could not be verified")
)

const defaultTransferLimit int64 = protocol.DefaultFileLimit

type Service struct {
	root  *os.Root
	name  string
	limit int64
	// afterAtomicReplace is a package-test fault-injection hook for the
	// otherwise hard-to-reproduce post-commit verification failure path.
	afterAtomicReplace func()
}

// MutationBaseline stores opaque metadata fingerprints only. It lets recovery
// distinguish a requested postcondition from the pre-existing state without
// persisting file contents.
type MutationBaseline struct {
	TargetExists       bool
	TargetFingerprint  string
	NewPathExists      bool
	NewPathFingerprint string
}

func New(rootPath string, limit int64) (*Service, error) {
	if limit == 0 {
		limit = defaultTransferLimit
	}
	if limit < 1 || limit > protocol.MaxFileSize {
		return nil, errors.New("file transfer limit is outside the protocol hard limit")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open Agent file root: %w", err)
	}
	return &Service{root: root, name: rootPath, limit: limit}, nil
}

func (s *Service) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

func (s *Service) Limit() int64 { return s.limit }

func (s *Service) normalize(virtual string) (string, string, error) {
	if s == nil || s.root == nil || virtual == "" || !strings.HasPrefix(virtual, "/") || strings.ContainsRune(virtual, '\x00') || len(virtual) > 4096 {
		return "", "", ErrInvalidPath
	}
	for _, part := range strings.Split(virtual, "/") {
		if part == ".." {
			return "", "", ErrInvalidPath
		}
	}
	clean := path.Clean(virtual)
	if clean == "/" {
		return ".", "/", nil
	}
	if clean != virtual && strings.HasSuffix(virtual, "/..") {
		return "", "", ErrInvalidPath
	}
	return strings.TrimPrefix(clean, "/"), clean, nil
}

func (s *Service) List(virtual string) ([]protocol.FileEntry, error) {
	name, canonical, err := s.normalize(virtual)
	if err != nil {
		return nil, err
	}
	directory, err := s.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(protocol.MaxFileEntries + 1)
	if err != nil {
		return nil, err
	}
	if len(entries) > protocol.MaxFileEntries {
		return nil, errors.New("directory contains too many entries; use a narrower path")
	}
	result := make([]protocol.FileEntry, 0, len(entries))
	for _, item := range entries {
		child := path.Join(canonical, item.Name())
		childName, _, pathErr := s.normalize(child)
		if pathErr != nil {
			return nil, ErrInvalidPath
		}
		info, statErr := s.root.Lstat(childName)
		if statErr != nil {
			return nil, statErr
		}
		entry := makeEntry(item.Name(), child, info)
		if info.Mode().IsRegular() {
			entry.Version, err = s.version(childName, info, false)
			if err != nil {
				return nil, err
			}
		}
		result, err = appendBoundedListingEntry(result, entry)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func appendBoundedListingEntry(entries []protocol.FileEntry, entry protocol.FileEntry) ([]protocol.FileEntry, error) {
	candidate := append(entries, entry)
	payload, err := json.Marshal(protocol.FileResponse{Operation: protocol.FileList, Entries: candidate})
	if err != nil || len(payload) > protocol.MaxFileControlBytes-1024 {
		return nil, ErrDirectoryLarge
	}
	return candidate, nil
}

func (s *Service) Stat(virtual string) (protocol.FileEntry, error) {
	name, canonical, err := s.normalize(virtual)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return protocol.FileEntry{}, err
	}
	entry := makeEntry(path.Base(canonical), canonical, info)
	if info.Mode().IsRegular() {
		entry.Version, err = s.version(name, info, info.Size() <= protocol.MaxTextFileBytes)
	}
	return entry, err
}

func makeEntry(name, virtual string, info os.FileInfo) protocol.FileEntry {
	kind := "file"
	switch {
	case info.IsDir():
		kind = "directory"
	case info.Mode()&os.ModeSymlink != 0:
		kind = "symlink"
	case !info.Mode().IsRegular():
		kind = "other"
	}
	entry := protocol.FileEntry{Name: name, Path: virtual, Kind: kind, Size: info.Size(), Mode: uint32(info.Mode().Perm()), ModifiedAt: info.ModTime().UnixNano()}
	entry.OwnerUID, entry.OwnerGID = ownerIDs(info)
	return entry
}

func (s *Service) version(name string, info os.FileInfo, includeContent bool) (string, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%d:%d:%d:", info.Size(), info.Mode().Perm(), info.ModTime().UnixNano(), ownerID(info))
	if includeContent && info.Mode().IsRegular() {
		file, err := s.root.Open(name)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, io.LimitReader(file, protocol.MaxTextFileBytes+1))
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Service) ReadText(virtual string) (string, string, error) {
	name, _, err := s.normalize(virtual)
	if err != nil {
		return "", "", err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() {
		return "", "", ErrNotText
	}
	if info.Size() > protocol.MaxTextFileBytes {
		return "", "", ErrTextTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxTextFileBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(data) > protocol.MaxTextFileBytes {
		return "", "", ErrTextTooLarge
	}
	if !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
		return "", "", ErrNotText
	}
	version, err := s.version(name, info, true)
	return string(data), version, err
}

func (s *Service) Mkdir(virtual string) error {
	name, _, err := s.normalize(virtual)
	if err != nil {
		return err
	}
	return s.root.Mkdir(name, 0o755)
}

func (s *Service) Rename(oldVirtual, newVirtual string) error {
	oldName, _, err := s.normalize(oldVirtual)
	if err != nil {
		return err
	}
	newName, _, err := s.normalize(newVirtual)
	if err != nil {
		return err
	}
	if _, err := s.root.Lstat(newName); err == nil {
		return ErrExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.root.Rename(oldName, newName)
}

func (s *Service) Delete(virtual string, confirmed bool) error {
	if !confirmed {
		return ErrConfirmation
	}
	name, canonical, err := s.normalize(virtual)
	if err != nil || canonical == "/" {
		return ErrInvalidPath
	}
	return s.root.RemoveAll(name)
}

func (s *Service) CaptureMutationBaseline(operation, virtual, newVirtual string) (MutationBaseline, error) {
	var baseline MutationBaseline
	name, _, err := s.normalize(virtual)
	if err != nil {
		return baseline, err
	}
	baseline.TargetExists, baseline.TargetFingerprint, err = s.pathFingerprint(name)
	if err != nil {
		return baseline, err
	}
	if operation == protocol.FileRename {
		newName, _, err := s.normalize(newVirtual)
		if err != nil {
			return baseline, err
		}
		baseline.NewPathExists, baseline.NewPathFingerprint, err = s.pathFingerprint(newName)
		if err != nil {
			return baseline, err
		}
	}
	return baseline, nil
}

func (s *Service) pathFingerprint(name string) (bool, string, error) {
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, "", errors.New("filesystem identity is unavailable")
	}
	uid, gid := ownerIDs(info)
	value := fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d", stat.Dev, stat.Ino, info.Mode(), info.Size(), info.ModTime().UnixNano(), uid, gid, info.Mode().Type())
	digest := sha256.Sum256([]byte(value))
	return true, hex.EncodeToString(digest[:]), nil
}

// VerifyMutation checks the requested postcondition without retaining or
// returning file bytes. It is used after a reconnect when the Agent journal
// proves that a write was started but the response was not persisted.
func (s *Service) VerifyMutation(operation, virtual, newVirtual string, expectedSize int64, expectedSHA256 string, baseline MutationBaseline) (bool, error) {
	name, canonical, err := s.normalize(virtual)
	if err != nil {
		return false, err
	}
	switch operation {
	case protocol.FileMkdir:
		if !baseline.BaselineAllowsCreatedTarget() {
			return false, nil
		}
		info, err := s.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return info.IsDir(), nil
	case protocol.FileDelete:
		if canonical == "/" || !baseline.TargetExists {
			return false, ErrInvalidPath
		}
		_, err := s.root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	case protocol.FileRename:
		if !baseline.TargetExists || baseline.NewPathExists || baseline.TargetFingerprint == "" {
			return false, nil
		}
		newName, _, err := s.normalize(newVirtual)
		if err != nil {
			return false, err
		}
		_, sourceErr := s.root.Lstat(name)
		_, destinationErr := s.root.Lstat(newName)
		if !errors.Is(sourceErr, os.ErrNotExist) || destinationErr != nil {
			if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
				return false, sourceErr
			}
			if destinationErr != nil && !errors.Is(destinationErr, os.ErrNotExist) {
				return false, destinationErr
			}
			return false, nil
		}
		_, destinationFingerprint, err := s.pathFingerprint(newName)
		if err != nil {
			return false, err
		}
		return destinationFingerprint == baseline.TargetFingerprint, nil
	case protocol.FileSaveText, protocol.FileUploadBegin, protocol.FileUploadCommit, "upload":
		if baseline.TargetExists {
			_, fingerprint, fingerprintErr := s.pathFingerprint(name)
			if fingerprintErr != nil {
				return false, fingerprintErr
			}
			if fingerprint == baseline.TargetFingerprint {
				return false, nil
			}
		}
		return s.verifyFileDigest(name, expectedSize, expectedSHA256)
	default:
		return false, errors.New("unsupported file mutation postcondition")
	}
}

func (b MutationBaseline) BaselineAllowsCreatedTarget() bool { return !b.TargetExists }

func (s *Service) verifyFileDigest(name string, expectedSize int64, expectedSHA256 string) (bool, error) {
	if expectedSize < 0 || expectedSize > s.limit || len(expectedSHA256) != 64 {
		return false, ErrLimitExceeded
	}
	info, err := s.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() != expectedSize {
		return false, nil
	}
	file, err := s.root.Open(name)
	if err != nil {
		return false, err
	}
	hash := sha256.New()
	read, copyErr := io.Copy(hash, io.LimitReader(file, s.limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return false, copyErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if read != expectedSize {
		return false, nil
	}
	return hex.EncodeToString(hash.Sum(nil)) == expectedSHA256, nil
}

type Upload struct {
	service         *Service
	target          string
	targetVirtual   string
	temporary       string
	file            *os.File
	expectedSize    int64
	expectedDigest  string
	expectedVersion string
	initialVersion  string
	initialInfo     os.FileInfo
	written         int64
	hash            hash.Hash
	closed          bool
}

func (u *Upload) Written() int64 {
	if u == nil {
		return 0
	}
	return u.written
}

func (u *Upload) TemporaryPath() string {
	if u == nil || u.temporary == "" {
		return ""
	}
	return "/" + u.temporary
}

func (s *Service) BeginUpload(virtual, expectedVersion string, size int64, digest string) (*Upload, error) {
	temporary, err := s.ReserveUploadTemporaryPath(virtual)
	if err != nil {
		return nil, err
	}
	return s.BeginUploadAtTemporary(virtual, expectedVersion, size, digest, temporary)
}

// ReserveUploadTemporaryPath chooses an unpredictable sibling name without
// creating it. The Agent journals this exact name before BeginUploadAtTemporary
// can create a file, so a crash immediately after creation can be cleaned up.
func (s *Service) ReserveUploadTemporaryPath(virtual string) (string, error) {
	target, _, err := s.normalize(virtual)
	if err != nil || target == "." {
		return "", ErrInvalidPath
	}
	name, err := s.newSiblingTemporaryName(target, ".nodedance-upload-")
	if err != nil {
		return "", err
	}
	return "/" + name, nil
}

func (s *Service) BeginUploadAtTemporary(virtual, expectedVersion string, size int64, digest, temporaryVirtual string) (*Upload, error) {
	if size < 0 || size > s.limit {
		return nil, ErrLimitExceeded
	}
	if digest != "" {
		if len(digest) != 64 || strings.ToLower(digest) != digest {
			return nil, errors.New("SHA-256 digest is invalid")
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return nil, errors.New("SHA-256 digest is invalid")
		}
	}
	target, canonical, err := s.normalize(virtual)
	if err != nil || target == "." {
		return nil, ErrInvalidPath
	}
	info, statErr := s.root.Lstat(target)
	var initialVersion string
	if statErr == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("upload target must be a regular file")
		}
		initialVersion, err = s.version(target, info, info.Size() <= protocol.MaxTextFileBytes)
		if err != nil {
			return nil, err
		}
		if expectedVersion == "" || expectedVersion != initialVersion {
			return nil, ErrExists
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	} else if expectedVersion != "" {
		return nil, ErrConflict
	}
	temporary, _, err := s.normalize(temporaryVirtual)
	if err != nil || path.Dir(temporary) != path.Dir(target) || !validUploadTempName(path.Base(target), path.Base(temporary)) {
		return nil, ErrInvalidPath
	}
	file, err := s.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	return &Upload{service: s, target: target, targetVirtual: canonical, temporary: temporary, file: file, hash: sha256.New(),
		expectedSize: size, expectedDigest: digest, expectedVersion: expectedVersion,
		initialVersion: initialVersion, initialInfo: info}, nil
}

func validUploadTempName(targetBase, temporaryBase string) bool {
	prefix := "." + targetBase + ".nodedance-upload-"
	if !strings.HasPrefix(temporaryBase, prefix) || len(temporaryBase) != len(prefix)+24 {
		return false
	}
	for _, char := range strings.TrimPrefix(temporaryBase, prefix) {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

// RemoveUploadTemporary removes only a temporary upload path that was returned
// by this service and durably recorded by the Agent journal. It never scans a
// user directory or removes files based on a broad prefix.
func (s *Service) RemoveUploadTemporary(targetVirtual, temporaryVirtual string) error {
	target, _, err := s.normalize(targetVirtual)
	if err != nil || target == "." {
		return ErrInvalidPath
	}
	temporary, _, err := s.normalize(temporaryVirtual)
	if err != nil || temporary == "." || path.Dir(temporary) != path.Dir(target) {
		return ErrInvalidPath
	}
	if !validUploadTempName(path.Base(target), path.Base(temporary)) {
		return ErrInvalidPath
	}
	if err := s.root.Remove(temporary); errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
}

func (u *Upload) WriteChunk(data []byte) error {
	if u == nil || u.closed || u.file == nil || len(data) > protocol.MaxFileChunkBytes {
		return errors.New("upload is not active or chunk is invalid")
	}
	if int64(len(data)) > u.expectedSize-u.written || u.written+int64(len(data)) > u.service.limit {
		return ErrLimitExceeded
	}
	n, err := u.file.Write(data)
	u.written += int64(n)
	if n > 0 {
		_, _ = u.hash.Write(data[:n])
	}
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (u *Upload) Commit() (protocol.FileEntry, error) {
	return u.CommitWithDigest(u.expectedDigest)
}

func (u *Upload) CommitWithDigest(expectedDigest string) (protocol.FileEntry, error) {
	if u == nil || u.closed || u.file == nil {
		return protocol.FileEntry{}, errors.New("upload is not active")
	}
	defer u.Abort()
	if u.written != u.expectedSize {
		return protocol.FileEntry{}, io.ErrUnexpectedEOF
	}
	actualDigest := hex.EncodeToString(u.hash.Sum(nil))
	if expectedDigest == "" || u.expectedDigest != "" && actualDigest != u.expectedDigest || actualDigest != expectedDigest {
		return protocol.FileEntry{}, ErrTransferDigest
	}
	if err := u.file.Sync(); err != nil {
		return protocol.FileEntry{}, err
	}
	if u.initialInfo != nil {
		uid, gid := ownerIDs(u.initialInfo)
		if err := u.file.Chown(uid, gid); err != nil {
			return protocol.FileEntry{}, fmt.Errorf("preserve target owner: %w", err)
		}
		if err := u.file.Chmod(u.initialInfo.Mode().Perm()); err != nil {
			return protocol.FileEntry{}, fmt.Errorf("preserve target mode: %w", err)
		}
	} else if err := u.file.Chmod(0o644); err != nil {
		return protocol.FileEntry{}, err
	}
	if err := u.file.Close(); err != nil {
		u.file = nil
		return protocol.FileEntry{}, err
	}
	u.file = nil
	currentInfo, err := u.service.root.Lstat(u.target)
	if u.initialInfo == nil {
		if err == nil {
			return protocol.FileEntry{}, ErrConflict
		}
		if !errors.Is(err, os.ErrNotExist) {
			return protocol.FileEntry{}, err
		}
	} else {
		if err != nil {
			return protocol.FileEntry{}, ErrConflict
		}
		current, versionErr := u.service.version(u.target, currentInfo, currentInfo.Size() <= protocol.MaxTextFileBytes)
		if versionErr != nil {
			return protocol.FileEntry{}, versionErr
		}
		if current != u.initialVersion || u.expectedVersion != u.initialVersion {
			return protocol.FileEntry{}, ErrConflict
		}
	}
	if u.initialInfo == nil {
		// A prior Lstat is not enough: another process could create the target
		// before the rename and Root.Rename would replace it. Creating a hard
		// link is atomic and fails with EEXIST instead of overwriting. The temp
		// file is a sibling, so it is on the same filesystem as the target.
		if err := linkUploadWithoutReplace(u.service.root, u.temporary, u.target); err != nil {
			return protocol.FileEntry{}, err
		}
		if err := u.service.root.Remove(u.temporary); err == nil {
			u.closed = true
		}
	} else {
		if err := u.service.root.Rename(u.temporary, u.target); err != nil {
			return protocol.FileEntry{}, err
		}
		u.closed = true
	}
	if u.service.afterAtomicReplace != nil {
		u.service.afterAtomicReplace()
	}
	info, err := u.service.root.Lstat(u.target)
	if err != nil {
		return protocol.FileEntry{}, fmt.Errorf("%w: %v", ErrMutationResultUnknown, err)
	}
	entry := makeEntry(filepath.Base(u.targetVirtual), u.targetVirtual, info)
	entry.Version, err = u.service.version(u.target, info, info.Size() <= protocol.MaxTextFileBytes)
	if err != nil {
		return protocol.FileEntry{}, fmt.Errorf("%w: %v", ErrMutationResultUnknown, err)
	}
	return entry, nil
}

func linkUploadWithoutReplace(root *os.Root, temporary, target string) error {
	if err := root.Link(temporary, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (u *Upload) Abort() {
	if u == nil || u.closed {
		return
	}
	u.closed = true
	if u.file != nil {
		_ = u.file.Close()
		u.file = nil
	}
	_ = u.service.root.Remove(u.temporary)
}

func (s *Service) OpenDownload(virtual string) (*os.File, protocol.FileEntry, error) {
	name, canonical, err := s.normalize(virtual)
	if err != nil {
		return nil, protocol.FileEntry{}, err
	}
	file, err := s.root.Open(name)
	if err != nil {
		return nil, protocol.FileEntry{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, protocol.FileEntry{}, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, protocol.FileEntry{}, errors.New("download target must be a regular file")
	}
	if info.Size() > s.limit {
		_ = file.Close()
		return nil, protocol.FileEntry{}, ErrLimitExceeded
	}
	entry := makeEntry(path.Base(canonical), canonical, info)
	entry.Version, err = s.version(name, info, info.Size() <= protocol.MaxTextFileBytes)
	if err != nil {
		_ = file.Close()
		return nil, protocol.FileEntry{}, err
	}
	return file, entry, nil
}

func (s *Service) SaveText(virtual, expectedVersion, text string) (protocol.FileEntry, string, error) {
	if len(text) > protocol.MaxTextFileBytes || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return protocol.FileEntry{}, "", ErrNotText
	}
	name, canonical, err := s.normalize(virtual)
	if err != nil || name == "." {
		return protocol.FileEntry{}, "", ErrInvalidPath
	}
	old, err := s.root.Open(name)
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	defer old.Close()
	info, err := old.Stat()
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	if !info.Mode().IsRegular() {
		return protocol.FileEntry{}, "", ErrNotText
	}
	if info.Size() > protocol.MaxTextFileBytes {
		return protocol.FileEntry{}, "", ErrTextTooLarge
	}
	currentVersion, err := s.version(name, info, true)
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	if expectedVersion == "" || currentVersion != expectedVersion {
		return protocol.FileEntry{}, "", ErrConflict
	}
	temporary, replacement, err := s.createSiblingTemporary(name, ".nodedance-edit-")
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	cleanupReplacement := true
	defer func() {
		if cleanupReplacement {
			_ = replacement.Close()
			_ = s.root.Remove(temporary)
		}
	}()
	if _, err := io.WriteString(replacement, text); err != nil {
		return protocol.FileEntry{}, "", err
	}
	if err := replacement.Sync(); err != nil {
		return protocol.FileEntry{}, "", err
	}
	uid, gid := ownerIDs(info)
	if err := replacement.Chown(uid, gid); err != nil {
		return protocol.FileEntry{}, "", fmt.Errorf("preserve target owner: %w", err)
	}
	if err := replacement.Chmod(info.Mode().Perm()); err != nil {
		return protocol.FileEntry{}, "", fmt.Errorf("preserve target mode: %w", err)
	}
	backup, err := s.backup(name, canonical, info)
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	latestInfo, err := s.root.Stat(name)
	if err != nil {
		return protocol.FileEntry{}, "", ErrConflict
	}
	latestVersion, err := s.version(name, latestInfo, true)
	if err != nil {
		return protocol.FileEntry{}, "", err
	}
	if latestVersion != expectedVersion {
		return protocol.FileEntry{}, "", ErrConflict
	}
	if err := replacement.Close(); err != nil {
		return protocol.FileEntry{}, "", err
	}
	if err := s.root.Rename(temporary, name); err != nil {
		return protocol.FileEntry{}, "", err
	}
	cleanupReplacement = false
	if s.afterAtomicReplace != nil {
		s.afterAtomicReplace()
	}
	newInfo, err := s.root.Lstat(name)
	if err != nil {
		return protocol.FileEntry{}, "", fmt.Errorf("%w: %v", ErrMutationResultUnknown, err)
	}
	entry := makeEntry(path.Base(canonical), canonical, newInfo)
	entry.Version, err = s.version(name, newInfo, true)
	if err != nil {
		return protocol.FileEntry{}, "", fmt.Errorf("%w: %v", ErrMutationResultUnknown, err)
	}
	return entry, backup, nil
}

func (s *Service) backup(name, canonical string, info os.FileInfo) (string, error) {
	temporary, output, err := s.createSiblingTemporary(name, ".nodedance-backup-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = output.Close()
		if !ok {
			_ = s.root.Remove(temporary)
		}
	}()
	input, err := s.root.Open(name)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := input.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	uid, gid := ownerIDs(info)
	if err := output.Chown(uid, gid); err != nil {
		return "", fmt.Errorf("preserve backup owner: %w", err)
	}
	if err := output.Chmod(info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("preserve backup mode: %w", err)
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	backupName := temporary + ".saved"
	if err := s.root.Rename(temporary, backupName); err != nil {
		return "", err
	}
	ok = true
	return "/" + backupName, nil
}

func (s *Service) createSiblingTemporary(target, prefix string) (string, *os.File, error) {
	base := path.Base(target)
	if target == "." || base == "/" {
		return "", nil, ErrInvalidPath
	}
	for attempt := 0; attempt < 10; attempt++ {
		name, err := s.newSiblingTemporaryName(target, prefix)
		if err != nil {
			return "", nil, err
		}
		file, err := s.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("could not allocate a unique temporary file")
}

func (s *Service) newSiblingTemporaryName(target, prefix string) (string, error) {
	directory, base := path.Dir(target), path.Base(target)
	if target == "." || base == "/" {
		return "", ErrInvalidPath
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return path.Join(directory, "."+base+prefix+hex.EncodeToString(random[:])), nil
}

// ownerIDs uses the platform's stat owner when available. The helper is kept
// in a separate file on non-Unix ports; NodeDance Agents are deployed to Linux.
