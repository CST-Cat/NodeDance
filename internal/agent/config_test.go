package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDirectoryMustBePrivateAndNotSymlinked(t *testing.T) {
	base := t.TempDir()
	config := Config{Schema: ConfigSchema, Server: "https://127.0.0.1:8180", Credential: strings.Repeat("a", 64)}

	unsafeDirectory := filepath.Join(base, "unsafe")
	if err := os.Mkdir(unsafeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(unsafeDirectory, "agent.json")
	if err := SaveConfig(unsafePath, config, true); err == nil {
		t.Fatal("SaveConfig accepted a group-readable credential directory")
	}

	privateDirectory := filepath.Join(base, "private")
	if err := os.Mkdir(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	privatePath := filepath.Join(privateDirectory, "agent.json")
	if err := SaveConfig(privatePath, config, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privateDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(privatePath); err == nil {
		t.Fatal("LoadConfig accepted a credential in a group-accessible directory")
	}
	if err := os.Chmod(privateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(base, "alias")
	if err := os.Symlink(privateDirectory, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filepath.Join(alias, "agent.json")); err == nil {
		t.Fatal("LoadConfig accepted a symlinked parent path")
	}
	if err := SaveConfig(filepath.Join(alias, "new.json"), config, true); err == nil {
		t.Fatal("SaveConfig accepted a symlinked parent path")
	}

	fileAlias := filepath.Join(privateDirectory, "agent-link.json")
	if err := os.Symlink(privatePath, fileAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(fileAlias); err == nil {
		t.Fatal("LoadConfig accepted a symlinked credential file")
	}
}

func TestConfigUpdatesCannotReplaceIdentityAndAllowOnlyPendingPromotion(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "agent.json")
	oldCredential := strings.Repeat("1", 64)
	config := Config{Schema: ConfigSchema, Server: "https://127.0.0.1:8180", Credential: oldCredential,
		EnrollmentToken: strings.Repeat("t", 40), RequestID: "01234567-89ab-4cde-8fab-0123456789ab"}
	if err := SaveConfig(path, config, true); err != nil {
		t.Fatal(err)
	}
	config.AgentID = "11234567-89ab-4cde-8fab-0123456789ab"
	config.NodeID = "21234567-89ab-4cde-8fab-0123456789ab"
	config.EnrollmentToken = ""
	if err := SaveConfig(path, config, false); err != nil {
		t.Fatalf("assign Core identity: %v", err)
	}

	otherIdentity := config
	otherIdentity.AgentID = "31234567-89ab-4cde-8fab-0123456789ab"
	if err := SaveConfig(path, otherIdentity, false); err == nil {
		t.Fatal("SaveConfig replaced an existing device identity")
	}
	otherServer := config
	otherServer.Server = "https://other.example"
	if err := SaveConfig(path, otherServer, false); err == nil {
		t.Fatal("SaveConfig changed the bound Core server")
	}
	otherCredential := config
	otherCredential.Credential = strings.Repeat("2", 64)
	if err := SaveConfig(path, otherCredential, false); err == nil {
		t.Fatal("SaveConfig replaced the active credential without a committed rotation")
	}

	config.PendingCredential = strings.Repeat("3", 64)
	config.PendingRotationID = "41234567-89ab-4cde-8fab-0123456789ab"
	if err := SaveConfig(path, config, false); err != nil {
		t.Fatalf("persist pending rotation: %v", err)
	}
	config.Credential = config.PendingCredential
	config.PendingCredential = ""
	config.PendingRotationID = ""
	if err := SaveConfig(path, config, false); err != nil {
		t.Fatalf("promote Core-committed rotation: %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Credential != strings.Repeat("3", 64) || loaded.PendingCredential != "" {
		t.Fatalf("promoted config=%+v err=%v", loaded, err)
	}
}

func TestHTTPDevelopmentRequiresLiteralLoopbackIP(t *testing.T) {
	for _, address := range []string{"http://127.0.0.1:8180", "http://[::1]:8180"} {
		if _, err := ParseServerURL(address, true); err != nil {
			t.Errorf("literal loopback %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"http://localhost:8180", "http://core.local:8180", "http://192.0.2.1:8180"} {
		if _, err := ParseServerURL(address, true); err == nil {
			t.Errorf("non-literal or non-loopback HTTP address %q accepted", address)
		}
	}
}
