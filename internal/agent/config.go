package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const ConfigSchema = 1

type Config struct {
	Schema            int       `json:"schema"`
	Server            string    `json:"server"`
	Shell             string    `json:"shell,omitempty"`
	CAFile            string    `json:"caFile,omitempty"`
	Development       bool      `json:"development,omitempty"`
	Credential        string    `json:"credential"`
	EnrollmentToken   string    `json:"enrollmentToken,omitempty"`
	RequestID         string    `json:"requestId,omitempty"`
	NodeID            string    `json:"nodeId,omitempty"`
	AgentID           string    `json:"agentId,omitempty"`
	PendingCredential string    `json:"pendingCredential,omitempty"`
	PendingRotationID string    `json:"pendingRotationId,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}

func ParseServerURL(value string, development bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("server URL must contain only scheme and authority")
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !development || !isLiteralLoopback(parsed.Hostname()) {
			return "", errors.New("plain HTTP is allowed only with --dev and a literal loopback address")
		}
	default:
		return "", errors.New("server URL must use https; use --dev only for an HTTP loopback Core")
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "", errors.New("server URL has an invalid port")
		}
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func isLiteralLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func NewCredential() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func NewRequestID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = buffer[6]&0x0f | 0x40
	buffer[8] = buffer[8]&0x3f | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func ReadEnrollmentToken(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil {
		return "", fmt.Errorf("read enrollment token from stdin: %w", err)
	}
	if len(data) > 4096 {
		return "", errors.New("enrollment token input is too large")
	}
	value := strings.TrimSpace(string(data))
	if len(value) < 32 || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("enrollment token input is empty or invalid")
	}
	return value, nil
}

func LoadConfig(path string) (Config, error) {
	return loadConfigForUID(path, os.Geteuid())
}

func loadConfigForUID(path string, ownerUID int) (Config, error) {
	path, err := canonicalCredentialPath(path)
	if err != nil {
		return Config{}, err
	}
	if err := checkPrivateDirectoryForUID(filepath.Dir(path), ownerUID, false); err != nil {
		return Config{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("Agent credential path must be a regular non-symlink file")
	}
	if info.Mode().Perm() != 0o600 {
		return Config{}, fmt.Errorf("Agent credential file mode is %#o, want 0600", info.Mode().Perm())
	}
	if err := checkOwnerUID(info, ownerUID, "Agent credential file"); err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var config Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode Agent config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("Agent config has trailing data")
	}
	if config.Schema != ConfigSchema || config.Server == "" || !isCredential(config.Credential) || !validConfiguredShell(config.Shell) {
		return Config{}, errors.New("Agent config is incomplete or unsupported")
	}
	if config.PendingCredential != "" && (!isCredential(config.PendingCredential) || !isUUID(config.PendingRotationID)) {
		return Config{}, errors.New("Agent pending credential state is invalid")
	}
	if config.PendingCredential == "" && config.PendingRotationID != "" {
		return Config{}, errors.New("Agent rotation ID has no pending credential")
	}
	if config.NodeID != "" && !isUUID(config.NodeID) || config.AgentID != "" && !isUUID(config.AgentID) {
		return Config{}, errors.New("Agent identity in config is invalid")
	}
	if (config.NodeID == "") != (config.AgentID == "") {
		return Config{}, errors.New("Agent config has only half of its assigned identity")
	}
	if config.Development {
		if _, err := ParseServerURL(config.Server, true); err != nil {
			return Config{}, err
		}
	} else if _, err := ParseServerURL(config.Server, false); err != nil {
		return Config{}, err
	}
	if config.EnrollmentToken != "" && (config.NodeID != "" || len(config.EnrollmentToken) < 32 || len(config.EnrollmentToken) > 256) {
		return Config{}, errors.New("Agent enrollment state is invalid")
	}
	if config.EnrollmentToken != "" && !isUUID(config.RequestID) {
		return Config{}, errors.New("Agent enrollment request ID is invalid")
	}
	return config, nil
}

func SaveConfig(path string, config Config, exclusive bool) error {
	if config.Schema == 0 {
		config.Schema = ConfigSchema
	}
	if config.Schema != ConfigSchema || config.Server == "" || !isCredential(config.Credential) || !validConfiguredShell(config.Shell) {
		return errors.New("refusing to save invalid Agent configuration")
	}
	if config.PendingCredential != "" && (!isCredential(config.PendingCredential) || !isUUID(config.PendingRotationID)) {
		return errors.New("refusing to save invalid pending credential state")
	}
	if config.PendingCredential == "" && config.PendingRotationID != "" {
		return errors.New("refusing to save rotation ID without a pending credential")
	}
	if config.NodeID != "" && !isUUID(config.NodeID) || config.AgentID != "" && !isUUID(config.AgentID) || (config.NodeID == "") != (config.AgentID == "") {
		return errors.New("refusing to save invalid Agent identity")
	}
	if _, err := ParseServerURL(config.Server, config.Development); err != nil {
		return fmt.Errorf("refusing to save invalid Agent server URL: %w", err)
	}
	if config.EnrollmentToken != "" && (config.NodeID != "" || len(config.EnrollmentToken) < 32 || len(config.EnrollmentToken) > 256 || !isUUID(config.RequestID)) {
		return errors.New("refusing to save invalid Agent enrollment state")
	}
	path, err := canonicalCredentialPath(path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := checkPrivateDirectory(directory, true); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Agent credential path must be a regular non-symlink file")
		}
		if err := checkOwner(info, "Agent credential file"); err != nil {
			return err
		}
		if exclusive {
			return errors.New("Agent credential file already exists; refusing to replace device identity")
		}
		previous, err := LoadConfig(path)
		if err != nil {
			return fmt.Errorf("refusing to update unreadable Agent credentials: %w", err)
		}
		if err := validateConfigTransition(previous, config); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Agent credential file: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".nodedance-agent-credential-*")
	if err != nil {
		return fmt.Errorf("create temporary credential file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write Agent credential file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync Agent credential file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Agent credential file: %w", err)
	}
	if exclusive {
		if err := os.Link(tempName, path); err != nil {
			return fmt.Errorf("install Agent credential file: %w", err)
		}
		if err := os.Remove(tempName); err != nil {
			return fmt.Errorf("remove temporary Agent credential file: %w", err)
		}
	} else if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace Agent credential file: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync Agent credential directory: %w", err)
	}
	return nil
}

func validConfiguredShell(shell string) bool {
	if shell == "" {
		return true
	}
	return filepath.IsAbs(shell) && len(shell) <= 4096 && !strings.ContainsAny(shell, "\x00\r\n")
}

func validateConfigTransition(previous, next Config) error {
	if previous.Server != next.Server || previous.CAFile != next.CAFile || previous.Development != next.Development || previous.RequestID != next.RequestID {
		return errors.New("refusing to change an existing Agent server or enrollment identity")
	}
	if previous.AgentID != "" && (previous.AgentID != next.AgentID || previous.NodeID != next.NodeID) {
		return errors.New("refusing to replace an existing Agent device identity")
	}
	if previous.EnrollmentToken != "" && next.EnrollmentToken != "" && previous.EnrollmentToken != next.EnrollmentToken {
		return errors.New("refusing to replace a pending enrollment credential")
	}
	if previous.Credential != next.Credential {
		if previous.PendingCredential == "" || previous.PendingCredential != next.Credential || next.PendingCredential != "" || previous.PendingRotationID == "" {
			return errors.New("refusing to replace an Agent credential without a committed rotation")
		}
	}
	if previous.PendingCredential != "" && next.PendingCredential != previous.PendingCredential {
		promoted := next.PendingCredential == "" && next.Credential == previous.PendingCredential
		if !promoted {
			return errors.New("refusing to replace a pending Agent credential")
		}
	}
	if previous.PendingRotationID != "" && next.PendingRotationID != previous.PendingRotationID && !(next.PendingRotationID == "" && next.Credential == previous.PendingCredential) {
		return errors.New("refusing to replace a pending Agent rotation")
	}
	if previous.EnrollmentToken == "" && next.EnrollmentToken != "" {
		return errors.New("refusing to restore a consumed enrollment credential")
	}
	return nil
}

func checkPrivateDirectory(directory string, create bool) error {
	return checkPrivateDirectoryForUID(directory, os.Geteuid(), create)
}

func checkPrivateDirectoryForUID(directory string, ownerUID int, create bool) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve private Agent credential directory: %w", err)
	}
	directory = filepath.Clean(absolute)
	if create {
		if err := rejectSymlinkPath(directory); err != nil {
			return err
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create private Agent credential directory: %w", err)
		}
	}
	if err := rejectSymlinkPath(directory); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect private Agent credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Agent credential directory must be a real directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("Agent credential directory mode is %#o; use a dedicated directory with mode 0700", info.Mode().Perm())
	}
	return checkOwnerUID(info, ownerUID, "Agent credential directory")
}

func canonicalCredentialPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("Agent credential path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Agent credential path: %w", err)
	}
	clean := filepath.Clean(abs)
	if err := rejectSymlinkPath(clean); err != nil {
		return "", err
	}
	return clean, nil
}

func rejectSymlinkPath(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	current := volume + string(os.PathSeparator)
	rest := strings.TrimPrefix(clean, current)
	for _, part := range strings.Split(rest, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("inspect Agent credential path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Agent credential path may not contain a symlink")
		}
	}
	return nil
}

func checkOwner(info os.FileInfo, label string) error {
	return checkOwnerUID(info, os.Geteuid(), label)
}

func checkOwnerUID(info os.FileInfo, ownerUID int, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != ownerUID {
		return fmt.Errorf("%s must be owned by the selected service user", label)
	}
	return nil
}

func isCredential(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func isUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16 && decoded[6]>>4 == 4 && decoded[8]>>6 == 2
}
