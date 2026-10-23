package server

import (
	"errors"
	corehistory "github.com/CST-Cat/NodeDance/internal/core/history"
	"net/http"
)

func (s *Server) handleHistorySettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.history.Settings(r.Context())
		if err != nil {
			http.Error(w, "monitoring settings unavailable", 500)
			return
		}
		writeJSON(w, 200, settings)
	case http.MethodPut:
		var settings corehistory.Settings
		if !decodeJSON(w, r, &settings) {
			return
		}
		err := s.history.SaveSettings(r.Context(), settings, s.now())
		if errors.Is(err, corehistory.ErrInvalidSettings) {
			http.Error(w, err.Error(), 400)
			return
		}
		if err != nil {
			http.Error(w, "monitoring settings could not be saved", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", 405)
	}
}
