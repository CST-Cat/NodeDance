package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CST-Cat/NodeDance/internal/core/agents"
)

func TestEnrollmentMethodSelectsCommandBeforeIssuingToken(t *testing.T) {
	s, err := New("test", Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BootstrapAdministrator(context.Background(), "test-admin", "test-password-strong"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{agents.DeploymentNative, agents.DeploymentDocker, "", "unsupported"} {
		raw, _ := json.Marshal(agents.EnrollmentRequest{DisplayName: "node", CoreURL: "https://core.example", DeploymentMethod: method})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/enrollments", strings.NewReader(string(raw)))
		res := httptest.NewRecorder()
		s.adminCreateAgentEnrollment(res, req, nil)
		if method == "unsupported" {
			if res.Code != http.StatusBadRequest {
				t.Fatalf("unsupported method status=%d", res.Code)
			}
			continue
		}
		if res.Code != http.StatusCreated {
			t.Fatalf("method %q status=%d", method, res.Code)
		}
		var body struct {
			DeploymentMethod string `json:"deploymentMethod"`
			InstallCommand   string `json:"installCommand"`
			NativeCommand    string `json:"nativeInstallCommand"`
			DockerCommand    string `json:"dockerInstallCommand"`
			Token            string `json:"token"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		expected := method
		if expected == "" {
			expected = agents.DeploymentNative
		}
		selected := body.NativeCommand
		if expected == agents.DeploymentDocker {
			selected = body.DockerCommand
		}
		if body.DeploymentMethod != expected || body.InstallCommand != selected || body.Token == "" || body.NativeCommand == "" || body.DockerCommand == "" {
			t.Fatalf("enrollment contract mismatch for %q", method)
		}
		if strings.Contains(body.InstallCommand, body.Token) {
			t.Fatal("enrollment token leaked into command")
		}
	}
}
