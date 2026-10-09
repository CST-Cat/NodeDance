// Package backup creates and restores private, integrity-checked Core backups.
package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlitedriver "modernc.org/sqlite"
)

const (
	archiveFormat  = "nodedance-core-backup"
	archiveVersion = 1
	manifestName   = "nodedance-backup.json"
	databaseName   = "nodedance.sqlite"
	alertKeyName   = "alert-channel-encryption.key"
	csrfKeyName    = "csrf-signing.key"
	setupName      = "setup-credential.txt"
	tailscaleName  = "tailscale-deployments.json"
	maxManifest    = 1 << 20
	maxFileSize    = 1 << 40
)

var requiredFiles = []string{databaseName, alertKeyName, csrfKeyName}
var optionalFiles = []string{setupName, tailscaleName}

type manifest struct {
	Format  string         `json:"format"`
	Version int            `json:"version"`
	Files   []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type sourceFile struct {
	archivePath string
	localPath   string
}

// Create writes a fresh mode-0600 backup file. SQLite's online backup API
// includes committed WAL content in a consistent database snapshot.
func Create(ctx context.Context, dataDir, outputPath string) (retErr error) {
	root, err := secureDataDirectory(dataDir)
	if err != nil {
		return err
	}
	output, err := filepath.Abs(outputPath)
	if err != nil || strings.TrimSpace(outputPath) == "" {
		return errors.New("backup output path is invalid")
	}
	outputParent := filepath.Dir(output)
	realOutputParent, err := filepath.EvalSymlinks(outputParent)
	if err != nil {
		return errors.New("backup output parent directory is unavailable")
	}
	outputParent = realOutputParent
	output = filepath.Join(outputParent, filepath.Base(output))
	if pathWithin(root, output) {
		return errors.New("backup output must be outside the Core data directory")
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect backup output: %w", err)
	}
	parentInfo, err := os.Stat(outputParent)
	if err != nil || !parentInfo.IsDir() {
		return errors.New("backup output parent directory is unavailable")
	}

	snapshotDir, err := os.MkdirTemp("", "nodedance-backup-snapshot-")
	if err != nil {
		return fmt.Errorf("create private SQLite snapshot workspace: %w", err)
	}
	defer os.RemoveAll(snapshotDir)
	if err := os.Chmod(snapshotDir, 0o700); err != nil {
		return fmt.Errorf("secure SQLite snapshot workspace: %w", err)
	}
	snapshotPath := filepath.Join(snapshotDir, databaseName)
	if err := createSQLiteSnapshot(ctx, filepath.Join(root, databaseName), snapshotPath); err != nil {
		return fmt.Errorf("create consistent SQLite snapshot: %w", err)
	}
	if err := os.Chmod(snapshotPath, 0o600); err != nil {
		return fmt.Errorf("secure SQLite snapshot: %w", err)
	}
	if err := checkSQLiteIntegrity(ctx, snapshotPath); err != nil {
		return fmt.Errorf("verify SQLite snapshot: %w", err)
	}

	files := []sourceFile{{archivePath: databaseName, localPath: snapshotPath}}
	for _, name := range []string{alertKeyName, csrfKeyName} {
		if err := validatePrivateFile(filepath.Join(root, name), 32); err != nil {
			return fmt.Errorf("Core backup is missing a valid %s: %w", name, err)
		}
		files = append(files, sourceFile{archivePath: name, localPath: filepath.Join(root, name)})
	}
	for _, name := range optionalFiles {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return fmt.Errorf("inspect optional Core state file %s: %w", name, err)
		}
		if err := validatePrivateFile(path, -1); err != nil {
			return fmt.Errorf("validate optional Core state file %s: %w", name, err)
		}
		files = append(files, sourceFile{archivePath: name, localPath: path})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].archivePath < files[j].archivePath })

	archiveManifest := manifest{Format: archiveFormat, Version: archiveVersion, Files: make([]manifestFile, 0, len(files))}
	for _, file := range files {
		entry, err := describeFile(file)
		if err != nil {
			return err
		}
		archiveManifest.Files = append(archiveManifest.Files, entry)
	}
	manifestBytes, err := json.Marshal(archiveManifest)
	if err != nil {
		return fmt.Errorf("encode backup manifest: %w", err)
	}

	return writeArchive(output, outputParent, manifestBytes, files, archiveManifest.Files)
}

// Restore validates the whole archive into a private sibling directory before
// atomically installing it at dataDir. Existing nonempty targets are refused.
func Restore(ctx context.Context, inputPath, dataDir string) (retErr error) {
	if strings.TrimSpace(inputPath) == "" || strings.TrimSpace(dataDir) == "" {
		return errors.New("backup input and restore data directory are required")
	}
	input, err := filepath.Abs(inputPath)
	if err != nil {
		return errors.New("backup input path is invalid")
	}
	inputInfo, err := os.Lstat(input)
	if err != nil || !inputInfo.Mode().IsRegular() || inputInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("backup input must be a regular file")
	}
	destination, err := filepath.Abs(dataDir)
	if err != nil || filepath.Clean(destination) == filepath.Clean(filepath.VolumeName(destination)+string(filepath.Separator)) {
		return errors.New("restore data directory is invalid")
	}
	initialTarget, targetExists, err := inspectRestoreTarget(destination)
	if err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create restore parent directory: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".nodedance-restore-*")
	if err != nil {
		return fmt.Errorf("create private restore workspace: %w", err)
	}
	stageIdentity, err := os.Lstat(stage)
	if err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("inspect private restore workspace: %w", err)
	}
	installed := false
	defer func() {
		if !installed {
			current, err := os.Lstat(stage)
			if err == nil && os.SameFile(stageIdentity, current) {
				_ = os.RemoveAll(stage)
			}
		}
	}()
	if err := os.Chmod(stage, 0o700); err != nil {
		return fmt.Errorf("secure restore workspace: %w", err)
	}

	archive, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open backup input: %w", err)
	}
	if err := extractAndVerify(ctx, archive, stage); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close backup input: %w", err)
	}
	for _, name := range []string{alertKeyName, csrfKeyName} {
		if err := validatePrivateFile(filepath.Join(stage, name), 32); err != nil {
			return fmt.Errorf("restored Core backup has an invalid %s: %w", name, err)
		}
	}
	if err := checkSQLiteIntegrity(ctx, filepath.Join(stage, databaseName)); err != nil {
		return fmt.Errorf("restored SQLite database failed integrity check: %w", err)
	}

	if err := installRestoreStage(stage, destination, initialTarget, targetExists); err != nil {
		return err
	}
	installed = true
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync restored Core parent directory: %w", err)
	}
	return nil
}

func secureDataDirectory(dataDir string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("Core data directory is required")
	}
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return "", errors.New("Core data directory path is invalid")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Core data directory must be an existing real directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Core data directory permissions must be private")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("Core data directory path cannot be resolved")
	}
	return realRoot, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func validatePrivateFile(path string, exactSize int64) error {
	f, err := openPrivateFile(path, exactSize)
	if err != nil {
		return err
	}
	return f.Close()
}

func openPrivateFile(path string, exactSize int64) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("file must be a private regular file")
	}
	if exactSize >= 0 && info.Size() != exactSize {
		return nil, errors.New("file has an invalid size")
	}
	if info.Size() > maxFileSizeFor(filepath.Base(path)) {
		return nil, errors.New("file exceeds the Core backup size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("file changed while being inspected")
	}
	if exactSize >= 0 && opened.Size() != exactSize {
		_ = f.Close()
		return nil, errors.New("file has an invalid size")
	}
	return f, nil
}

func createSQLiteSnapshot(ctx context.Context, sourcePath, snapshotPath string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Core database must be an existing regular file")
	}
	sourceURL := sqliteURI(sourcePath, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(5000)"}})
	db, err := sql.Open("sqlite", sourceURL)
	if err != nil {
		return fmt.Errorf("open Core database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect Core database: %w", err)
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlitedriver.Backup, error)
		})
		if !ok {
			return errors.New("SQLite driver does not provide online backup support")
		}
		backup, err := backuper.NewBackup(sqliteURI(snapshotPath, nil))
		if err != nil {
			return fmt.Errorf("initialize online SQLite backup: %w", err)
		}
		for more := true; more; {
			more, err = backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return fmt.Errorf("copy online SQLite snapshot: %w", err)
			}
		}
		if err := backup.Finish(); err != nil {
			return fmt.Errorf("finish online SQLite snapshot: %w", err)
		}
		return nil
	})
}

func sqliteURI(path string, query url.Values) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	if query != nil {
		uri.RawQuery = query.Encode()
	}
	return uri.String()
}

func checkSQLiteIntegrity(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("SQLite database file is missing or unsafe")
	}
	db, err := sql.Open("sqlite", sqliteURI(path, url.Values{"mode": {"ro"}}))
	if err != nil {
		return fmt.Errorf("open SQLite database for integrity check: %w", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("run SQLite integrity_check: %w", err)
	}
	if result != "ok" {
		return errors.New("SQLite integrity_check did not return ok")
	}
	return nil
}

func describeFile(file sourceFile) (manifestFile, error) {
	if !validArchivePath(file.archivePath) || !allowedPath(file.archivePath) {
		return manifestFile{}, errors.New("Core backup contains an unsupported path")
	}
	f, err := openPrivateFile(file.localPath, -1)
	if err != nil {
		return manifestFile{}, fmt.Errorf("validate Core backup file %s: %w", file.archivePath, err)
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return manifestFile{}, fmt.Errorf("hash Core backup file %s: %w", file.archivePath, err)
	}
	if size > maxFileSizeFor(file.archivePath) {
		return manifestFile{}, errors.New("Core backup file exceeds the size limit")
	}
	return manifestFile{Path: file.archivePath, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func writeArchive(output, outputParent string, manifestBytes []byte, files []sourceFile, entries []manifestFile) error {
	temp, err := os.CreateTemp(outputParent, ".nodedance-backup-*")
	if err != nil {
		return fmt.Errorf("create temporary backup file: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure temporary backup file: %w", err)
	}
	writer := tar.NewWriter(temp)
	if err := writer.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR}); err != nil {
		_ = writer.Close()
		_ = temp.Close()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	if _, err := writer.Write(manifestBytes); err != nil {
		_ = writer.Close()
		_ = temp.Close()
		return fmt.Errorf("write backup manifest: %w", err)
	}
	for index, file := range files {
		entry := entries[index]
		if err := writer.WriteHeader(&tar.Header{Name: entry.Path, Mode: 0o600, Size: entry.Size, Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR}); err != nil {
			_ = writer.Close()
			_ = temp.Close()
			return fmt.Errorf("write backup entry %s: %w", entry.Path, err)
		}
		source, err := openPrivateFile(file.localPath, entry.Size)
		if err != nil {
			_ = writer.Close()
			_ = temp.Close()
			return fmt.Errorf("open backup entry %s: %w", entry.Path, err)
		}
		h := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(writer, h), source, entry.Size)
		stat, statErr := source.Stat()
		closeErr := source.Close()
		if copyErr != nil || statErr != nil || closeErr != nil || written != entry.Size || stat.Size() != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			_ = writer.Close()
			_ = temp.Close()
			return fmt.Errorf("backup entry %s changed while the archive was being written", entry.Path)
		}
	}
	if err := writer.Close(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("finish backup archive: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync backup archive: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close backup archive: %w", err)
	}
	if err := os.Link(tempPath, output); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errors.New("backup output already exists")
		}
		return fmt.Errorf("install backup without overwriting: %w", err)
	}
	if err := syncDirectory(outputParent); err != nil {
		return fmt.Errorf("sync backup output directory: %w", err)
	}
	return nil
}

func extractAndVerify(ctx context.Context, input io.Reader, stage string) error {
	reader := tar.NewReader(input)
	header, err := reader.Next()
	if err != nil || header.Name != manifestName || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maxManifest {
		return errors.New("backup archive manifest is missing or invalid")
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(reader, maxManifest+1))
	if err != nil || int64(len(manifestBytes)) != header.Size {
		return errors.New("backup archive manifest is truncated")
	}
	var archiveManifest manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&archiveManifest); err != nil {
		return errors.New("backup archive manifest is invalid")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("backup archive manifest has trailing data")
	}
	if archiveManifest.Format != archiveFormat || archiveManifest.Version != archiveVersion || len(archiveManifest.Files) < len(requiredFiles) || len(archiveManifest.Files) > len(requiredFiles)+len(optionalFiles) {
		return errors.New("backup archive format or version is unsupported")
	}
	entries := make(map[string]manifestFile, len(archiveManifest.Files))
	for _, entry := range archiveManifest.Files {
		if !validArchivePath(entry.Path) || !allowedPath(entry.Path) || entry.Size < 0 || entry.Size > maxFileSizeFor(entry.Path) || len(entry.SHA256) != sha256.Size*2 {
			return errors.New("backup archive contains an unsafe or invalid path")
		}
		if _, exists := entries[entry.Path]; exists {
			return errors.New("backup archive contains duplicate paths")
		}
		decoded, err := hex.DecodeString(entry.SHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(entry.SHA256) != entry.SHA256 {
			return errors.New("backup archive contains an invalid file digest")
		}
		entries[entry.Path] = entry
	}
	for _, name := range requiredFiles {
		if _, ok := entries[name]; !ok {
			return errors.New("backup archive is missing required Core data")
		}
	}

	seen := make(map[string]struct{}, len(entries))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("backup archive is truncated or malformed")
		}
		if header.Typeflag != tar.TypeReg || !validArchivePath(header.Name) || header.Name == manifestName {
			return errors.New("backup archive contains an unsafe entry")
		}
		entry, ok := entries[header.Name]
		if !ok || header.Size != entry.Size {
			return errors.New("backup archive entry does not match its manifest")
		}
		if _, exists := seen[entry.Path]; exists {
			return errors.New("backup archive contains duplicate entries")
		}
		seen[entry.Path] = struct{}{}
		destination := filepath.Join(stage, filepath.FromSlash(entry.Path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create restored Core directory: %w", err)
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("create restored Core file %s: %w", entry.Path, err)
		}
		if err := file.Chmod(0o600); err != nil {
			_ = file.Close()
			return fmt.Errorf("secure restored Core file %s: %w", entry.Path, err)
		}
		h := sha256.New()
		written, copyErr := io.CopyN(io.MultiWriter(file, h), reader, entry.Size)
		if copyErr == nil && written != entry.Size {
			copyErr = io.ErrUnexpectedEOF
		}
		if copyErr == nil && hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			copyErr = errors.New("checksum mismatch")
		}
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("verify restored Core file %s: %w", entry.Path, errors.Join(copyErr, closeErr))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if len(seen) != len(entries) {
		return errors.New("backup archive is missing one or more declared files")
	}
	return nil
}

func validArchivePath(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(name))) == name
}

func allowedPath(name string) bool {
	if name == databaseName || name == alertKeyName || name == csrfKeyName {
		return true
	}
	return name == setupName || name == tailscaleName
}

func maxFileSizeFor(name string) int64 {
	switch name {
	case setupName:
		return 1024
	case tailscaleName:
		return 16 << 20
	default:
		return maxFileSize
	}
}

func inspectRestoreTarget(destination string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect restore target: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, true, errors.New("restore target must be a real directory or not exist")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return nil, true, fmt.Errorf("read restore target: %w", err)
	}
	if len(entries) != 0 {
		return nil, true, errors.New("restore target directory must be empty")
	}
	return info, true, nil
}

func verifyTargetUnchanged(destination string, initial os.FileInfo, initiallyExists bool) error {
	current, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		if initiallyExists {
			return errors.New("restore target changed while restore was being validated")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck restore target: %w", err)
	}
	if !initiallyExists || initial == nil || !os.SameFile(initial, current) || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore target changed while restore was being validated")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		return errors.New("restore target is no longer empty")
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
