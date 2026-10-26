package server

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/CST-Cat/NodeDance/internal/core/shares"
)

func (s *Server) handleShareRequestsAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	prefix := "/api/v1/share-requests"
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		return false
	}
	tail := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	id := parts[0]
	store := s.shareStore()
	if tail == "" && r.Method == "GET" {
		v, e := store.ListRequests(r.Context(), current.UserID)
		if e != nil {
			shareError(w, e)
		} else {
			writeJSON(w, 200, map[string]any{"requests": v})
		}
		return true
	}
	if tail != "" && !shares.ValidID(id) {
		http.NotFound(w, r)
		return true
	}
	if len(parts) == 2 && parts[1] == "download" && r.Method == "GET" {
		v, e := store.GetRequest(r.Context(), id)
		if e != nil || v.Owner != current.UserID || v.Revoked {
			http.NotFound(w, r)
			return true
		}
		s.serveShareEntry(w, r, shares.Share{Entries: v.Entries}, false)
		return true
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return true
	}
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	switch {
	case tail == "" && r.Method == "POST" || tail != "" && r.Method == "PUT":
		var in shares.RequestInput
		if !decodeJSON(w, r, &in) {
			return true
		}
		v, e := store.SaveRequest(r.Context(), current.UserID, id, in)
		if e != nil {
			shareError(w, e)
		} else {
			writeJSON(w, 200, v)
		}
	case tail != "" && r.Method == "DELETE":
		revision, e := strconv.Atoi(r.URL.Query().Get("revision"))
		if e != nil || revision < 1 {
			http.Error(w, "revision is required", 400)
			return true
		}
		if e = store.RevokeRequest(r.Context(), current.UserID, id, revision); e != nil {
			shareError(w, e)
		} else {
			w.WriteHeader(204)
		}
	case tail != "" && r.Method == "GET":
		v, e := store.GetRequest(r.Context(), id)
		if e != nil || v.Owner != current.UserID {
			http.NotFound(w, r)
		} else {
			writeJSON(w, 200, v)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}
func (s *Server) handlePublicShareRequests(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/public/share-requests/"), "/")
	id := parts[0]
	if !shares.ValidID(id) || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	store := s.shareStore()
	v, e := store.GetRequest(r.Context(), id)
	if e != nil || !store.RequestAvailable(r.Context(), v) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == "GET" && r.URL.RawQuery == "" {
		writeJSON(w, 200, map[string]any{"id": v.ID, "name": v.Name, "notes": v.Notes, "expiresAt": v.ExpiresAt, "maxFiles": v.MaxFiles, "maxBytes": v.MaxBytes, "uploadedFiles": len(v.Entries)})
		return
	}
	if len(parts) == 2 && parts[1] == "upload" && r.Method == "PUT" {
		if !s.validOrigin(r) {
			http.Error(w, "request rejected", 403)
			return
		}
		if len(r.URL.Query()) != 1 || len(r.URL.Query()["name"]) != 1 {
			http.Error(w, "filename is required", 400)
			return
		}
		name := r.URL.Query().Get("name")
		if !shares.SafePath(name) || path.Base(name) != name || len(name) > 255 {
			http.Error(w, "invalid filename", 400)
			return
		}
		limit := s.fileTransferLimit
		var total int64
		for _, entry := range v.Entries {
			total += entry.Size
		}
		if v.MaxBytes > 0 && v.MaxBytes-total < limit {
			limit = v.MaxBytes - total
		}
		if limit <= 0 || v.MaxFiles > 0 && len(v.Entries) >= v.MaxFiles {
			http.Error(w, "file request limit reached", 409)
			return
		}
		select {
		case s.shareUploadSlots <- struct{}{}:
			defer func() { <-s.shareUploadSlots }()
		default:
			http.Error(w, "upload capacity reached", 429)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit+1)
		entry, e := store.PutBlob(r.Context(), r.Body, limit)
		if e != nil {
			http.Error(w, "file upload failed or exceeds limit", 400)
			return
		}
		entry.Path = name
		s.shareMu.Lock()
		defer s.shareMu.Unlock()
		v, e = store.GetRequest(r.Context(), id)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		_, e = store.AppendRequest(r.Context(), v, entry)
		if e != nil {
			shareError(w, e)
			return
		}
		writeJSON(w, 201, map[string]bool{"uploaded": true})
		return
	}
	http.Error(w, "method not allowed", 405)
}
