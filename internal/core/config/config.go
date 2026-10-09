package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

const DefaultListen = "127.0.0.1:8180"
const DefaultSessionIdleTimeout = 12 * time.Hour
const DefaultLoginMaxAttempts = 5
const DefaultLoginLockoutDuration = 15 * time.Minute

type File struct {
	Listen                 string   `json:"listen"`
	DataDir                string   `json:"data_dir"`
	PublicOrigin           string   `json:"public_origin"`
	TrustedProxies         []string `json:"trusted_proxies"`
	SessionIdleTimeout     string   `json:"session_idle_timeout"`
	LoginMaxAttempts       int      `json:"login_max_attempts"`
	LoginLockoutDuration   string   `json:"login_lockout_duration"`
	WebSocketCheckInterval string   `json:"websocket_check_interval"`
	MaxFileTransferBytes   int64    `json:"max_file_transfer_bytes"`
}

type Sources struct {
	CLIListen   string
	EnvListen   string
	CLIDataDir  string
	EnvDataDir  string
	Config      File
	XDGDataHome string
	UserHome    string
}

// ResolveListen applies the documented CLI > environment > config > default
// precedence. Each non-empty value is validated before it is returned.
func ResolveListen(s Sources) (string, error) {
	for _, candidate := range []string{s.CLIListen, s.EnvListen, s.Config.Listen, DefaultListen} {
		if candidate == "" {
			continue
		}
		if err := ValidateListen(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", errors.New("no listen address configured")
}

func ValidateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if host == "" {
		return fmt.Errorf("listen address %q must name an IP address", address)
	}
	if strings.Contains(host, "%") {
		// IPv6 zone identifiers are allowed by net.ListenConfig, but they are
		// link-specific and should never be added without explicit support.
		return fmt.Errorf("listen address %q with an IPv6 zone is not supported", address)
	}
	if ip := net.ParseIP(host); ip == nil {
		return fmt.Errorf("listen host %q must be an IP address (not a hostname)", host)
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return fmt.Errorf("listen port %q must contain decimal digits only", port)
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("listen port %q must be between 1 and 65535", port)
	}
	return nil
}

func ValidateDevListen(address string) error {
	if err := ValidateListen(address); err != nil {
		return err
	}
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--dev requires a loopback listen address; got %q", address)
	}
	return nil
}

func DefaultDataDir(xdgDataHome, userHome string) string {
	if xdgDataHome != "" {
		return filepath.Join(xdgDataHome, "nodedance")
	}
	if userHome != "" {
		return filepath.Join(userHome, ".local", "share", "nodedance")
	}
	return filepath.Join(os.TempDir(), "nodedance")
}

func ResolveDataDir(s Sources) string {
	for _, candidate := range []string{s.CLIDataDir, s.EnvDataDir, s.Config.DataDir} {
		if strings.TrimSpace(candidate) != "" {
			return filepath.Clean(candidate)
		}
	}
	return DefaultDataDir(s.XDGDataHome, s.UserHome)
}

func ValidatePublicOrigin(origin string, development bool) error {
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("public origin %q must contain only scheme and authority", origin)
	}
	if parsed.Scheme != "https" && !(development && parsed.Scheme == "http") {
		return fmt.Errorf("public origin must use https outside --dev mode")
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("public origin %q has no host", origin)
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("public origin %q has an invalid port", origin)
		}
	}
	return nil
}

func ParseTrustedProxies(values []string) ([]*net.IPNet, error) {
	result := make([]*net.IPNet, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if ip := net.ParseIP(value); ip != nil {
			maskSize := 128
			if ip.To4() != nil {
				maskSize = 32
			}
			result = append(result, &net.IPNet{IP: ip, Mask: net.CIDRMask(maskSize, maskSize)})
			continue
		}
		_, network, err := net.ParseCIDR(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: expected IP or CIDR", value)
		}
		result = append(result, network)
	}
	return result, nil
}

func RuntimeValues(file File, development bool) (time.Duration, int, time.Duration, error) {
	idle := DefaultSessionIdleTimeout
	if file.SessionIdleTimeout != "" {
		parsed, err := time.ParseDuration(file.SessionIdleTimeout)
		if err != nil || parsed <= 0 || parsed > 24*time.Hour {
			return 0, 0, 0, fmt.Errorf("session_idle_timeout must be a duration greater than zero and no more than 24h")
		}
		idle = parsed
	}
	attempts := DefaultLoginMaxAttempts
	if file.LoginMaxAttempts != 0 {
		if file.LoginMaxAttempts < 1 || file.LoginMaxAttempts > 100 {
			return 0, 0, 0, fmt.Errorf("login_max_attempts must be between 1 and 100")
		}
		attempts = file.LoginMaxAttempts
	}
	lockout := DefaultLoginLockoutDuration
	if file.LoginLockoutDuration != "" {
		parsed, err := time.ParseDuration(file.LoginLockoutDuration)
		if err != nil || parsed <= 0 || parsed > 24*time.Hour {
			return 0, 0, 0, fmt.Errorf("login_lockout_duration must be a duration greater than zero and no more than 24h")
		}
		lockout = parsed
	}
	if !development && (idle != DefaultSessionIdleTimeout || attempts != DefaultLoginMaxAttempts || lockout != DefaultLoginLockoutDuration) {
		return 0, 0, 0, fmt.Errorf("test timing overrides are accepted only in --dev mode")
	}
	return idle, attempts, lockout, nil
}

func RuntimeWebSocketCheckInterval(file File, development bool) (time.Duration, error) {
	const defaultInterval = 10 * time.Second
	if file.WebSocketCheckInterval == "" {
		return defaultInterval, nil
	}
	parsed, err := time.ParseDuration(file.WebSocketCheckInterval)
	if err != nil || parsed <= 0 || parsed > 30*time.Second {
		return 0, errors.New("websocket_check_interval must be greater than zero and no more than 30s")
	}
	if !development && parsed != defaultInterval {
		return 0, errors.New("websocket_check_interval override is accepted only in --dev mode")
	}
	return parsed, nil
}

func RuntimeFileTransferLimit(file File, envValue, cliValue string) (int64, error) {
	for _, candidate := range []string{cliValue, envValue} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(candidate), 10, 64)
		if err != nil || value < 1 || value > protocol.MaxFileSize {
			return 0, errors.New("max file transfer bytes must be between 1 and the protocol hard limit")
		}
		return value, nil
	}
	if file.MaxFileTransferBytes != 0 {
		if file.MaxFileTransferBytes < 1 || file.MaxFileTransferBytes > protocol.MaxFileSize {
			return 0, errors.New("max_file_transfer_bytes is outside the protocol hard limit")
		}
		return file.MaxFileTransferBytes, nil
	}
	return protocol.DefaultFileLimit, nil
}

func DefaultConfigPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, "nodedance", "config.json")
}

func Load(path string) (File, error) {
	if path == "" {
		return File{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{}, nil
	}
	if err != nil {
		return File{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var value File
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return File{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if value.Listen != "" {
		if err := ValidateListen(value.Listen); err != nil {
			return File{}, fmt.Errorf("config %q: %w", path, err)
		}
	}
	if value.PublicOrigin != "" {
		if err := ValidatePublicOrigin(value.PublicOrigin, false); err != nil {
			// An HTTP origin is permitted only when runServe has accepted --dev.
			parsed, parseErr := url.Parse(value.PublicOrigin)
			if parseErr != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
				return File{}, fmt.Errorf("config %q: %w", path, err)
			}
		}
	}
	if _, err := ParseTrustedProxies(value.TrustedProxies); err != nil {
		return File{}, fmt.Errorf("config %q: %w", path, err)
	}
	if _, _, _, err := RuntimeValues(value, true); err != nil {
		return File{}, fmt.Errorf("config %q: %w", path, err)
	}
	return value, nil
}
