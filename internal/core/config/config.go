package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const DefaultListen = "127.0.0.1:8180"

type File struct {
	Listen string `json:"listen"`
}

type Sources struct {
	CLIListen string
	EnvListen string
	Config    File
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

func DefaultConfigPath(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "nodedance", "config.json")
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
	return value, nil
}
