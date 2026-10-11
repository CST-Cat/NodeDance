package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

func TestBootstrapCredentialsAndUsernameLogin(t *testing.T) {
	for _, input := range [][2]string{{"", ""}, {"custom-admin", "custom-strong-password"}, {"custom-admin", ""}, {"", "custom-strong-password"}} {
		t.Run(input[0]+input[1], func(t *testing.T) {
			dir := t.TempDir()
			core, err := New("test", Options{DataDir: dir, Development: true})
			if err != nil {
				t.Fatal(err)
			}
			if err = core.BootstrapAdministrator(context.Background(), input[0], input[1]); err != nil {
				t.Fatal(err)
			}
			path := core.InitialAdminPath()
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var credentials struct {
				Username string
				Password string
			}
			if err = json.Unmarshal(data, &credentials); err != nil {
				t.Fatal(err)
			}
			if !auth.ValidUsername(credentials.Username) || len(credentials.Password) < 12 {
				t.Fatal("invalid credentials")
			}
			if input[0] != "" && credentials.Username != input[0] || input[1] != "" && credentials.Password != input[1] {
				t.Fatal("custom credentials changed")
			}
			for name, mode := range map[string]os.FileMode{dir: 0700, path: 0600} {
				info, err := os.Lstat(name)
				if err != nil || info.Mode().Perm() != mode {
					t.Fatal("unsafe bootstrap permissions")
				}
			}
			if _, err := os.Stat(setupCredentialPath(dir)); !os.IsNotExist(err) {
				t.Fatal("setup credential retained")
			}
			if err = core.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			core, err = New("test", Options{DataDir: dir, Development: true})
			if err != nil {
				t.Fatal(err)
			}
			defer core.Close()
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatal("restart did not protect credentials")
			}
			if err = core.BootstrapAdministrator(context.Background(), "another-admin", "another-strong-password"); err == nil {
				t.Fatal("bootstrap replaced existing admin")
			}
			saved, err := os.ReadFile(path)
			if err != nil || string(saved) != string(data) {
				t.Fatal("restart changed credentials")
			}
			var hash, salt []byte
			if err = core.store.DB.QueryRow(`SELECT password_hash,password_salt FROM admin_user WHERE id=1`).Scan(&hash, &salt); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(hash), credentials.Password) || !auth.VerifyPassword(credentials.Password, hash, salt) {
				t.Fatal("password was not hashed correctly")
			}
			login := func(username, password string) int {
				body, _ := json.Marshal(map[string]string{"username": username, "password": password})
				request := httptest.NewRequest("POST", "http://localhost/api/v1/auth/login", strings.NewReader(string(body)))
				request.RemoteAddr = "127.0.0.1:1234"
				response := httptest.NewRecorder()
				core.handleLogin(response, request)
				return response.Code
			}
			if status := login("wrong-admin", credentials.Password); status != 401 {
				t.Fatalf("wrong username: %d", status)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatal("failed login deleted credentials")
			}
			if status := login("", credentials.Password); status != 401 {
				t.Fatalf("missing username: %d", status)
			}
			if status := login(credentials.Username, "wrong-long-password"); status != 401 {
				t.Fatalf("wrong password: %d", status)
			}
			if status := login(credentials.Username, credentials.Password); status != 200 {
				t.Fatalf("valid login: %d", status)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("plaintext bootstrap credentials retained after login")
			}
		})
	}
}

func TestSetupAPIGeneratesOmittedCredentials(t *testing.T) {
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	token, err := readSetupCredential(setupCredentialPath(core.dataDir))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"credential": token})
	request := httptest.NewRequest("POST", "http://localhost/api/v1/auth/setup", strings.NewReader(string(body)))
	request.Header.Set("Origin", "http://localhost")
	response := httptest.NewRecorder()
	core.handleSetup(response, request)
	if response.Code != 201 {
		t.Fatalf("setup: %d", response.Code)
	}
	var result struct {
		InitialCredentials struct {
			Username string
			Password string
		}
		User struct{ DisplayName string }
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !auth.ValidUsername(result.InitialCredentials.Username) || len(result.InitialCredentials.Password) != 43 {
		t.Fatal("missing generated credentials")
	}
	if result.User.DisplayName == result.InitialCredentials.Username {
		t.Fatal("login username exposed as public display name")
	}
	var username string
	if err = core.store.DB.QueryRow(`SELECT username FROM admin_user WHERE id=1`).Scan(&username); err != nil || username != result.InitialCredentials.Username {
		t.Fatal("generated username not persisted")
	}
	request = httptest.NewRequest("POST", "http://localhost/api/v1/auth/setup", strings.NewReader(string(body)))
	request.Header.Set("Origin", "http://localhost")
	response = httptest.NewRecorder()
	core.handleSetup(response, request)
	if response.Code != 409 {
		t.Fatal("setup could replace account")
	}
}

func TestInvalidCustomBootstrapLeavesSetupAvailable(t *testing.T) {
	core, err := New("test", Options{DataDir: t.TempDir(), Development: true})
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if err = core.BootstrapAdministrator(context.Background(), "bad username", "long-custom-password"); err == nil {
		t.Fatal("invalid username accepted")
	}
	if err = core.BootstrapAdministrator(context.Background(), "custom-admin", "short"); err == nil {
		t.Fatal("weak password accepted")
	}
	if _, pending := core.SetupCredentialPath(); !pending {
		t.Fatal("failed bootstrap consumed setup")
	}
	if _, err = os.Stat(core.InitialAdminPath()); !os.IsNotExist(err) {
		t.Fatal("failed bootstrap left credentials")
	}
}
