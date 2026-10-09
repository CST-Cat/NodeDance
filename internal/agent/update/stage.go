package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

const diskReserveBytes = 16 << 20

var ErrPrepared = errors.New("Core-confirmed Agent update is ready for supervisor switch")

type Journal struct {
	State           string    `json:"state"` // staged, prepared (Core acknowledged), awaiting_confirmation, confirmed
	TaskID          string    `json:"taskId"`
	OldTarget       string    `json:"oldTarget"`
	CandidateTarget string    `json:"candidateTarget"`
	Version         string    `json:"version"`
	StartedAt       time.Time `json:"startedAt"`
}

type Request struct {
	TaskID              string   `json:"taskId"`
	Manifest            Manifest `json:"manifest"`
	ArtifactURL         string   `json:"artifactUrl"`
	CoreVersion         string   `json:"coreVersion"`
	CurrentAgentVersion string   `json:"currentAgentVersion"`
	Protocol            int      `json:"protocol"`
}

// Stage downloads to a private temporary file, verifies the signed metadata
// and exact bytes, then durably records a candidate without switching it.
func Stage(ctx context.Context, client *http.Client, credential string, publicKey ed25519.PublicKey, request Request, stateDir string) error {
	if client == nil || credential == "" {
		return errors.New("Agent update transport is not configured")
	}
	if _, err := uuid.Parse(request.TaskID); err != nil {
		return errors.New("Agent update task ID is invalid")
	}
	if err := request.Manifest.Verify(publicKey); err != nil {
		return err
	}
	if err := RuntimeCompatibility(request.Manifest, request.CurrentAgentVersion, request.CoreVersion, request.Protocol); err != nil {
		return err
	}
	if request.Manifest.OS != runtime.GOOS || request.Manifest.Architecture != runtime.GOARCH {
		return errors.New("release architecture does not match this Agent")
	}
	artifactURL, err := url.Parse(request.ArtifactURL)
	if err != nil || artifactURL.User != nil || artifactURL.Host == "" || (artifactURL.Scheme != "https" && !(artifactURL.Scheme == "http" && isLiteralLoopback(artifactURL.Hostname()))) {
		return errors.New("Agent update artifact requires HTTPS (HTTP is allowed only for literal loopback development Core)")
	}
	absStateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	stateDir = absStateDir
	versions := filepath.Join(stateDir, "bin", "versions")
	if err := os.MkdirAll(versions, 0700); err != nil {
		return err
	}
	free, err := availableBytes(versions)
	if err != nil {
		return fmt.Errorf("check update storage capacity: %w", err)
	}
	if free < request.Manifest.Size+diskReserveBytes {
		return errors.New("insufficient disk space for Agent update staging")
	}
	version, err := cleanVersion(request.Manifest.Version)
	if err != nil {
		return err
	}
	destination := filepath.Join(versions, version)
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("Agent release version is already staged")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpDir, err := os.MkdirTemp(versions, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	tmp, err := os.OpenFile(filepath.Join(tmpDir, "nodedance-agent"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	requestHTTP, err := http.NewRequestWithContext(ctx, http.MethodGet, request.ArtifactURL, nil)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	requestHTTP.Header.Set("Authorization", "Bearer "+credential)
	stageClient := *client
	stageClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := stageClient.Do(requestHTTP)
	if err != nil {
		_ = tmp.Close()
		return errors.New("download Agent update artifact failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = tmp.Close()
		return fmt.Errorf("download Agent update artifact returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != request.Manifest.Size {
		_ = tmp.Close()
		return errors.New("Agent update artifact size does not match manifest")
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(response.Body, request.Manifest.Size+1))
	if copyErr != nil {
		_ = tmp.Close()
		return errors.New("download Agent update artifact failed")
	}
	if written != request.Manifest.Size {
		_ = tmp.Close()
		return errors.New("Agent update artifact size does not match manifest")
	}
	if hex.EncodeToString(hasher.Sum(nil)) != request.Manifest.SHA256 {
		_ = tmp.Close()
		return errors.New("Agent update artifact digest verification failed")
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := syncDir(tmpDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, destination); err != nil {
		return err
	}
	if err := syncDir(versions); err != nil {
		return err
	}
	current := filepath.Join(stateDir, "bin", "current")
	oldTarget, err := os.Readlink(current)
	if err != nil {
		return fmt.Errorf("read active Agent target: %w", err)
	}
	candidate := filepath.Join("versions", version, "nodedance-agent")
	journal := Journal{State: "staged", TaskID: request.TaskID, OldTarget: oldTarget, CandidateTarget: candidate, Version: version, StartedAt: time.Now().UTC()}
	if err := WriteJournal(stateDir, journal); err != nil {
		_ = os.RemoveAll(destination)
		return err
	}
	return nil
}

func availableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

func isLiteralLoopback(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func JournalPath(stateDir string) string { return filepath.Join(stateDir, "update-journal.json") }
func ReadJournal(stateDir string) (Journal, error) {
	var value Journal
	data, err := os.ReadFile(JournalPath(stateDir))
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, err
	}
	if value.State != "staged" && value.State != "prepared" && value.State != "awaiting_confirmation" && value.State != "confirmed" {
		return Journal{}, errors.New("Agent update journal state is invalid")
	}
	if _, err := uuid.Parse(value.TaskID); err != nil {
		return Journal{}, errors.New("Agent update journal task ID is invalid")
	}
	return value, nil
}
func WriteJournal(stateDir string, value Journal) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".update-journal-*")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, JournalPath(stateDir)); err != nil {
		return err
	}
	return syncDir(stateDir)
}

func swapCurrent(stateDir, target string) error {
	if !filepath.IsLocal(target) || strings.Contains(target, "..") {
		return errors.New("Agent update target is invalid")
	}
	info, err := os.Stat(filepath.Join(stateDir, "bin", target))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("Agent update target is unavailable")
	}
	current := filepath.Join(stateDir, "bin", "current")
	tmp := current + ".next"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, current); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(current))
}

func ApplyPrepared(stateDir string) error {
	j, err := ReadJournal(stateDir)
	if err != nil {
		return err
	}
	if j.State != "prepared" {
		return errors.New("no prepared Agent update exists")
	}
	j.State = "awaiting_confirmation"
	j.StartedAt = time.Now().UTC()
	if err := WriteJournal(stateDir, j); err != nil {
		return err
	}
	return swapCurrent(stateDir, j.CandidateTarget)
}

// AcknowledgePrepared durably records Core's receipt of the Agent's prepared
// report. The supervisor switches versions only after this transition.
func AcknowledgePrepared(stateDir, taskID string) error {
	if _, err := uuid.Parse(taskID); err != nil {
		return errors.New("Agent update acknowledgement task ID is invalid")
	}
	j, err := ReadJournal(stateDir)
	if err != nil {
		return err
	}
	if j.TaskID != taskID {
		return errors.New("Agent update acknowledgement does not match staged task")
	}
	if j.State == "prepared" {
		return nil
	}
	if j.State != "staged" {
		return errors.New("Agent update is not waiting for Core acknowledgement")
	}
	j.State = "prepared"
	return WriteJournal(stateDir, j)
}

func ConfirmStartup(stateDir, version string) error {
	j, err := ReadJournal(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.State != "awaiting_confirmation" || j.Version != version {
		return nil
	}
	j.State = "confirmed"
	return WriteJournal(stateDir, j)
}

func Rollback(stateDir string) error {
	j, err := ReadJournal(stateDir)
	if err != nil {
		return err
	}
	if j.State == "confirmed" {
		return os.Remove(JournalPath(stateDir))
	}
	if err := swapCurrent(stateDir, j.OldTarget); err != nil {
		return err
	}
	if err := os.Remove(JournalPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(stateDir)
}
