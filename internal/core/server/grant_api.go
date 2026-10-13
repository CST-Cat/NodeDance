package server

import (
	"database/sql"
	"github.com/CST-Cat/NodeDance/internal/core/agents"
	"github.com/CST-Cat/NodeDance/internal/installer"
	"net/http"
	"strings"
)

func (s *Server) handleGrantAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	const path = "/api/v1/agents/enrollment-grants"
	if r.URL.Path != path && !strings.HasPrefix(r.URL.Path, path+"/") {
		return false
	}
	if r.URL.Path != path {
		id := strings.TrimPrefix(r.URL.Path, path+"/")
		if !validUUID(id) {
			http.NotFound(w, r)
			return true
		}
		if r.Method != "DELETE" {
			http.Error(w, "method not allowed", 405)
			return true
		}
		if err := s.agents.RevokeGrant(r.Context(), id, current.RemoteAddr, sql.NullInt64{Int64: 1, Valid: true}); err != nil {
			http.Error(w, "cannot revoke grant", 404)
			return true
		}
		w.WriteHeader(204)
		return true
	}
	switch r.Method {
	case "GET":
		grants, err := s.agents.ListGrants(r.Context())
		if err != nil {
			http.Error(w, "cannot list grants", 500)
			return true
		}
		writeJSON(w, 200, map[string]any{"grants": grants})
	case "POST":
		var v agents.GrantRequest
		if !decodeJSON(w, r, &v) {
			return true
		}
		if err := agents.ValidateGrant(v); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		if v.CoreURL == "" {
			v.CoreURL = r.Header.Get("Origin")
		}
		if err := installer.ValidateServer(v.CoreURL, s.development); err != nil {
			http.Error(w, err.Error(), 400)
			return true
		}
		g, err := s.agents.CreateGrant(r.Context(), v, current.RemoteAddr, sql.NullInt64{Int64: 1, Valid: true})
		if err != nil {
			http.Error(w, "cannot create grant", 500)
			return true
		}
		command, err := installer.Command(v.CoreURL, g.Token, s.development)
		if err != nil {
			http.Error(w, "cannot render installation command", 500)
			return true
		}
		docker, err := installer.DockerCommand(v.CoreURL, g.Token, s.development)
		if err != nil {
			http.Error(w, "cannot render Docker installation command", 500)
			return true
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 201, map[string]any{"grant": g, "installCommand": command, "dockerInstallCommand": docker})
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}
