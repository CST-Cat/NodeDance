package server

import (
	"github.com/CST-Cat/NodeDance/internal/core/dashboard"
	"github.com/CST-Cat/NodeDance/internal/core/geolocation"
	"net/http"
	"slices"
	"strings"
)

func (s *Server) handleLocationAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/api/v1/locations" && !strings.HasPrefix(r.URL.Path, "/api/v1/locations/") {
		return false
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	if r.URL.Path == "/api/v1/locations" {
		if len(r.URL.Query().Get("q")) > 128 || !slices.Contains(dashboard.CountryCodes(), r.URL.Query().Get("countryCode")) {
			http.Error(w, "invalid city search", http.StatusBadRequest)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"cities": geolocation.SearchCities(r.URL.Query().Get("q"), r.URL.Query().Get("countryCode"))})
		return true
	}
	city, ok := geolocation.FindCity(strings.TrimPrefix(r.URL.Path, "/api/v1/locations/"))
	if !ok {
		http.NotFound(w, r)
		return true
	}
	writeJSON(w, http.StatusOK, city)
	return true
}
