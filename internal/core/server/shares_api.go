package server

import (
	"context"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/CST-Cat/NodeDance/internal/core/shares"
	"github.com/CST-Cat/NodeDance/internal/protocol"
)

func (s *Server) shareStore() shares.Store {
	return shares.Store{DB: s.store.DB, Dir: s.dataDir, Now: s.now, BlobMu: &s.shareBlobMu}
}
func shareError(w http.ResponseWriter, err error) {
	if errors.Is(err, shares.ErrConflict) {
		http.Error(w, err.Error(), 409)
	} else {
		http.Error(w, "share is unavailable or request is invalid", 400)
	}
}
func (s *Server) handleSharesAPI(w http.ResponseWriter, r *http.Request, current *session) bool {
	if r.URL.Path != "/api/v1/shares" && !strings.HasPrefix(r.URL.Path, "/api/v1/shares/") {
		return false
	}
	store := s.shareStore()
	if r.URL.Path == "/api/v1/shares/upload" {
		s.handleShareUpload(w, r, current)
		return true
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/v1/shares")
	id := strings.TrimPrefix(tail, "/")
	if tail == "" && r.Method == "GET" {
		v, e := store.List(r.Context(), current.UserID)
		if e != nil {
			shareError(w, e)
		} else {
			writeJSON(w, 200, map[string]any{"shares": v})
		}
		return true
	}
	if tail == "" && r.Method == "POST" {
		var in shares.Input
		if !decodeJSON(w, r, &in) {
			return true
		}
		if e := shares.Validate(in); e != nil {
			shareError(w, e)
			return true
		}
		if !validUUID(in.NodeID) || len(in.Paths) == 0 {
			http.Error(w, "nodeId and paths are required", 400)
			return true
		}
		if v, e := store.Retry(r.Context(), current.UserID, in.RequestID); e == nil {
			writeJSON(w, 200, v)
			return true
		}
		select {
		case s.shareUploadSlots <- struct{}{}:
			defer func() { <-s.shareUploadSlots }()
		default:
			http.Error(w, "share creation capacity reached", 429)
			return true
		}
		ctx, cancel := context.WithTimeout(r.Context(), min(s.transferReadTimeout, 30*time.Minute))
		defer cancel()
		r = r.Clone(ctx)
		entries, e := s.snapshotShare(ctx, r, current, in)
		if e != nil {
			shareError(w, e)
			return true
		}
		if !s.dashboardSessionStillValid(r.Context(), current.ID) {
			http.Error(w, "authentication required", 401)
			return true
		}
		s.shareMu.Lock()
		defer s.shareMu.Unlock()
		v, e := store.Create(r.Context(), current.UserID, in, entries)
		if e != nil {
			shareError(w, e)
		} else {
			writeJSON(w, 201, v)
		}
		return true
	}
	parts := strings.Split(id, "/")
	id = parts[0]
	if !shares.ValidID(id) {
		http.NotFound(w, r)
		return true
	}
	if len(parts) == 2 && parts[1] == "download" && r.Method == "GET" {
		v, e := store.Get(r.Context(), id)
		if e != nil || v.Owner != current.UserID || v.Revoked {
			http.NotFound(w, r)
			return true
		}
		s.serveShareEntry(w, r, v, false)
		return true
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return true
	}
	if r.Method == "GET" {
		v, e := store.Get(r.Context(), id)
		if e != nil || v.Owner != current.UserID {
			http.NotFound(w, r)
		} else {
			writeJSON(w, 200, v)
		}
		return true
	}
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	switch r.Method {
	case "PUT":
		var in shares.Input
		if !decodeJSON(w, r, &in) {
			return true
		}
		v, e := store.Update(r.Context(), current.UserID, id, in)
		if e != nil {
			shareError(w, e)
		} else {
			writeJSON(w, 200, v)
		}
	case "DELETE":
		revision, e := strconv.Atoi(r.URL.Query().Get("revision"))
		if e != nil || revision < 1 {
			http.Error(w, "revision is required", 400)
			return true
		}
		if e = store.Revoke(r.Context(), current.UserID, id, revision); e != nil {
			shareError(w, e)
		} else {
			w.WriteHeader(204)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
	return true
}

type shareSink struct {
	header http.Header
	file   *os.File
	status int
	size   int64
	limit  int64
}

func (c *shareSink) Header() http.Header { return c.header }
func (c *shareSink) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *shareSink) Write(p []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	if c.status != 200 {
		return len(p), nil
	}
	if c.size+int64(len(p)) > c.limit {
		return 0, errors.New("share size exceeded")
	}
	n, e := c.file.Write(p)
	c.size += int64(n)
	return n, e
}
func (s *Server) snapshotShare(ctx context.Context, r *http.Request, current *session, in shares.Input) ([]shares.Entry, error) {
	store := s.shareStore()
	out := []shares.Entry{}
	seen := map[string]bool{}
	var total int64
	var visit func(string, string, int) error
	visit = func(source, relative string, depth int) error {
		if depth > 64 || len(out) >= shares.MaxEntries {
			return errors.New("share has too many entries")
		}
		if !shares.SafePath(relative) || seen[relative] {
			return errors.New("share contains duplicate or unsafe paths")
		}
		seen[relative] = true
		v, e := s.runFileOperation(ctx, current, in.NodeID, protocol.FileRequest{Operation: protocol.FileStat, Path: source})
		if e != nil {
			return e
		}
		if v.Entry == nil {
			return shares.ErrUnavailable
		}
		if v.Entry.Kind == "directory" {
			out = append(out, shares.Entry{Path: relative, Directory: true})
			after := ""
			for {
				page, e := s.runFileOperation(ctx, current, in.NodeID, protocol.FileRequest{Operation: protocol.FileList, Path: source, After: after})
				if e != nil {
					return e
				}
				for _, entry := range page.Entries {
					if path.Dir(entry.Path) != source {
						return errors.New("invalid directory entry")
					}
					if e = visit(entry.Path, path.Join(relative, entry.Name), depth+1); e != nil {
						return e
					}
				}
				if page.NextAfter == "" {
					break
				}
				if page.NextAfter <= after {
					return errors.New("invalid directory pagination")
				}
				after = page.NextAfter
			}
			return nil
		}
		if v.Entry.Kind != "file" {
			return errors.New("only regular files and directories can be shared")
		}
		f, e := os.CreateTemp(s.dataDir, ".share-source-")
		if e != nil {
			return e
		}
		defer f.Close()
		defer os.Remove(f.Name())
		copyRequest := r.Clone(ctx)
		copyRequest.Method = "GET"
		u := *r.URL
		u.Path = "/api/v1/nodes/" + in.NodeID + "/files/download"
		u.RawQuery = url.Values{"path": {source}}.Encode()
		copyRequest.URL = &u
		sink := &shareSink{header: http.Header{}, file: f, limit: s.fileTransferLimit - total}
		s.handleFileDownload(sink, copyRequest, current, in.NodeID)
		if sink.status != 200 || sink.size != v.Entry.Size || v.Entry.Version != "" && sink.Header().Get("X-File-Version") != v.Entry.Version {
			return errors.New("source download failed or changed")
		}
		if _, e = f.Seek(0, 0); e != nil {
			return e
		}
		entry, e := store.PutBlob(ctx, f, s.fileTransferLimit-total)
		if e != nil {
			return e
		}
		entry.Path = relative
		total += entry.Size
		out = append(out, entry)
		return nil
	}
	for _, source := range in.Paths {
		if source == "/" || !strings.HasPrefix(source, "/") || path.Clean(source) != source {
			return nil, errors.New("select a file or directory")
		}
		if e := visit(source, path.Base(source), 0); e != nil {
			return nil, e
		}
	}
	return out, nil
}

func (s *Server) handlePublicShares(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	tail := strings.TrimPrefix(r.URL.Path, "/api/v1/public/shares/")
	parts := strings.Split(tail, "/")
	id := parts[0]
	if !shares.ValidID(id) || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" && !(len(parts) == 2 && (parts[1] == "download" || parts[1] == "hotlink") && len(r.URL.Query()) == 1 && len(r.URL.Query()["path"]) == 1) {
		http.Error(w, "invalid share query", 400)
		return
	}
	store := s.shareStore()
	v, e := store.Get(r.Context(), id)
	if e != nil || !store.Available(r.Context(), v) {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		v = store.Public(v)
		if v.PasswordProtected {
			v.Entries = []shares.Entry{}
			v.Name = ""
		}
		writeJSON(w, 200, v)
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	if (action == "unlock" || action == "download") && r.Method == "POST" {
		if !s.validOrigin(r) {
			http.Error(w, "request rejected", 403)
			return
		}
		if !s.shareLimiter.reserve(s.effectiveRemoteAddr(r) + "|" + id) {
			http.Error(w, "too many password attempts", 429)
			return
		}
		select {
		case s.passwordSlots <- struct{}{}:
			defer func() { <-s.passwordSlots }()
		default:
			http.Error(w, "share is busy", 429)
			return
		}
		var in struct {
			Password string `json:"password"`
		}
		if !decodeJSON(w, r, &in) {
			return
		}
		if len(in.Password) > 72 || !store.CheckPassword(v, in.Password) {
			http.Error(w, "incorrect password", 403)
			return
		}
		s.shareLimiter.succeeded(s.effectiveRemoteAddr(r) + "|" + id)
		if action == "unlock" {
			writeJSON(w, 200, store.Public(v))
			return
		}
		s.serveShareEntry(w, r, v, true)
		return
	}
	if action == "hotlink" && r.Method == "GET" && !v.PasswordProtected {
		s.serveShareEntry(w, r, v, true)
		return
	}
	http.Error(w, "method not allowed", 405)
}
func (s *Server) serveShareEntry(w http.ResponseWriter, r *http.Request, v shares.Share, consume bool) {
	var entry *shares.Entry
	requested := r.URL.Query().Get("path")
	for i := range v.Entries {
		if !v.Entries[i].Directory && (v.Entries[i].Path == requested || requested == "" && len(v.Entries) == 1) {
			entry = &v.Entries[i]
			break
		}
	}
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	store := s.shareStore()
	f, e := store.Blob(*entry)
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if consume && shareDownloadStartsAtZero(r) {
		if e = store.Consume(r.Context(), v); e != nil {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	disposition := "attachment"
	if strings.HasSuffix(r.URL.Path, "/hotlink") {
		kind := mime.TypeByExtension(path.Ext(entry.Path))
		if strings.HasPrefix(kind, "image/") && safeFilePreviewType(kind) {
			w.Header().Set("Content-Type", kind)
			disposition = "inline"
		}
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(entry.Path)}))
	var modTime time.Time
	if info, statErr := f.Stat(); statErr == nil {
		modTime = info.ModTime()
	}
	http.ServeContent(w, r, path.Base(entry.Path), modTime, f)
}

func shareDownloadStartsAtZero(r *http.Request) bool {
	header := r.Header.Get("Range")
	if header == "" {
		return true
	}
	unit, ranges, found := strings.Cut(header, "=")
	if !found || unit != "bytes" {
		return true
	}
	first, _, _ := strings.Cut(ranges, ",")
	start, _, found := strings.Cut(strings.TrimSpace(first), "-")
	if !found {
		return true
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return err != nil || offset == 0
}
func (s *Server) startShareCleanup() {
	s.agentWait.Add(1)
	go func() {
		defer s.agentWait.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-s.agentContext.Done():
				return
			case <-ticker.C:
				s.shareMu.Lock()
				if err := s.shareStore().Cleanup(s.agentContext); err != nil && s.agentContext.Err() == nil {
					log.Printf("share cleanup: %v", err)
				}
				s.shareMu.Unlock()
			}
		}
	}()
}

func (s *Server) handleShareUpload(w http.ResponseWriter, r *http.Request, current *session) {
	if r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := r.URL.Query()
	days, e := strconv.Atoi(q.Get("expiryDays"))
	if e != nil {
		http.Error(w, "invalid expiryDays", 400)
		return
	}
	downloads, e := strconv.Atoi(q.Get("maxDownloads"))
	if e != nil {
		http.Error(w, "invalid maxDownloads", 400)
		return
	}
	name := q.Get("name")
	password := r.Header.Get("X-Share-Password")
	in := shares.Input{RequestID: q.Get("requestId"), Name: name, Paths: []string{}, ExpiryDays: days, MaxDownloads: downloads, Password: &password}
	if e = shares.Validate(in); e != nil || !shares.SafePath(name) || path.Base(name) != name {
		http.Error(w, "invalid share upload", 400)
		return
	}
	store := s.shareStore()
	if old, e := store.Retry(r.Context(), current.UserID, in.RequestID); e == nil {
		writeJSON(w, 200, old)
		return
	}
	select {
	case s.shareUploadSlots <- struct{}{}:
		defer func() { <-s.shareUploadSlots }()
	default:
		http.Error(w, "upload capacity reached", 429)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.fileTransferLimit+1)
	entry, e := store.PutBlob(r.Context(), r.Body, s.fileTransferLimit)
	if e != nil {
		http.Error(w, "upload failed or exceeds limit", 400)
		return
	}
	entry.Path = name
	if !s.dashboardSessionStillValid(r.Context(), current.ID) {
		http.Error(w, "authentication required", 401)
		return
	}
	s.shareMu.Lock()
	defer s.shareMu.Unlock()
	v, e := store.Create(r.Context(), current.UserID, in, []shares.Entry{entry})
	if e != nil {
		shareError(w, e)
		return
	}
	writeJSON(w, 201, v)
}
