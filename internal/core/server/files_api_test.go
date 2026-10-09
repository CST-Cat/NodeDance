package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFileRoutesRequireAdministratorSession(t *testing.T) {
	s := newTestServer(t)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/nodes/01234567-89ab-4cde-8fab-0123456789ab/files?path=/"},
		{http.MethodGet, "/api/v1/nodes/01234567-89ab-4cde-8fab-0123456789ab/files/text?path=/secret"},
		{http.MethodPost, "/api/v1/nodes/01234567-89ab-4cde-8fab-0123456789ab/files/directories"},
		{http.MethodGet, "/api/v1/nodes/01234567-89ab-4cde-8fab-0123456789ab/files/download?path=/secret"},
	} {
		request := httptest.NewRequest(route.method, route.path, nil)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status=%d body=%s, want 401", route.method, route.path, response.Code, response.Body.String())
		}
		if response.Code == http.StatusOK || response.Code == http.StatusPartialContent {
			t.Errorf("unauthenticated file endpoint returned data for %s", route.path)
		}
	}
}
