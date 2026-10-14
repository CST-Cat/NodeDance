package server

import (
	"context"
	"database/sql"
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComposeDeleteConfirmationIsBoundToRouteProject(t *testing.T) {
	for _, body := range []string{`{"action":"delete"}`, `{"action":"delete","deleteConfirmationKey":"other"}`, `{"action":"start","deleteConfirmationKey":"project"}`} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("Idempotency-Key", "test")
		response := httptest.NewRecorder()
		(&Server{}).handleCreateComposeTask(response, request, nil, "node", "project")
		if response.Code != http.StatusBadRequest {
			t.Fatal(body, response.Code)
		}
	}
}
func TestComposeDisplayNameUsesSharedNodeScopedPreference(t *testing.T) {
	s := featureCore(t)
	ctx := context.Background()
	enrollment, err := s.agents.CreateEnrollment(ctx, "project alias", "test", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	preference := dashboard.Preference{NodeID: enrollment.NodeID, TargetKind: "compose_project", Identity: "compose_project:" + strings.Repeat("a", 64), Alias: "My app", Visible: true}
	if err := s.dashboardPreferences.Put(ctx, preference); err != nil {
		t.Fatal(err)
	}
	saved, err := s.dashboardPreferences.List(ctx, enrollment.NodeID)
	if err != nil || len(saved) != 1 || saved[0].Alias != "My app" {
		t.Fatal(saved, err)
	}
	preference.Identity = "compose_project:../other"
	if s.dashboardPreferences.Put(ctx, preference) == nil {
		t.Fatal("unsafe project identity accepted")
	}
}
