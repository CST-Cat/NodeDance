package update

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// InstallLayout keeps the supervisor executable outside the atomically
// switched Agent symlink. The state directory is already private (0700).
func InstallLayout(source, stateDir, version string) (helper, current string, err error) {
	version, err = cleanVersion(version)
	if err != nil {
		return "", "", err
	}
	versionDir := filepath.Join(stateDir, "bin", "versions", version)
	if err := os.MkdirAll(versionDir, 0700); err != nil {
		return "", "", err
	}
	if err := copyExecutable(source, filepath.Join(versionDir, "nodedance-agent")); err != nil {
		return "", "", err
	}
	helper = filepath.Join(stateDir, "bin", "nodedance-agent-helper")
	if err := copyExecutable(source, helper); err != nil {
		return "", "", err
	}
	current = filepath.Join(stateDir, "bin", "current")
	tmp := current + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("versions", version, "nodedance-agent"), tmp); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, current); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := syncDir(filepath.Dir(current)); err != nil {
		return "", "", err
	}
	return helper, current, nil
}

func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".nodedance-agent-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0700); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
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
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}
	return syncDir(filepath.Dir(destination))
}

func versionDirectoryName(version string) string {
	version = strings.TrimPrefix(version, "v")
	for _, c := range version {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '+' || c == '-') {
			return ""
		}
	}
	if version == "" {
		return ""
	}
	return version
}

func cleanVersion(version string) (string, error) {
	value := versionDirectoryName(version)
	if value == "" {
		return "", os.ErrInvalid
	}
	return value, nil
}
