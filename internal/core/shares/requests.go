package shares

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type RequestInput struct {
	RequestID  string `json:"requestId"`
	Name       string `json:"name"`
	Notes      string `json:"notes"`
	ExpiryDays int    `json:"expiryDays"`
	MaxFiles   int    `json:"maxFiles"`
	MaxBytes   int64  `json:"maxBytes"`
	Revision   int    `json:"revision,omitempty"`
}
type Request struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Notes         string  `json:"notes"`
	ExpiresAt     int64   `json:"expiresAt"`
	MaxFiles      int     `json:"maxFiles"`
	MaxBytes      int64   `json:"maxBytes"`
	Entries       []Entry `json:"entries"`
	Revision      int     `json:"revision"`
	Revoked       bool    `json:"revoked"`
	UploadedFiles int     `json:"uploadedFiles"`
	Owner         int64   `json:"-"`
}

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var v Request
	var entries string
	e := row.Scan(&v.ID, &v.Owner, &v.Name, &v.Notes, &v.ExpiresAt, &v.MaxFiles, &v.MaxBytes, &entries, &v.Revision, &v.Revoked, &v.UploadedFiles)
	if e != nil {
		return v, e
	}
	e = json.Unmarshal([]byte(entries), &v.Entries)
	return v, e
}

const requestColumns = `id,owner_id,name,notes,expires_at,max_files,max_bytes,entries_json,revision,revoked,json_array_length(entries_json)`

func (s Store) GetRequest(ctx context.Context, id string) (Request, error) {
	return scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestColumns+` FROM file_share_requests WHERE id=?`, id))
}
func (s Store) ListRequests(ctx context.Context, owner int64) ([]Request, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT `+strings.Replace(requestColumns, "entries_json", "'[]'", 1)+` FROM file_share_requests WHERE owner_id=? ORDER BY revoked,rowid DESC LIMIT 1000`, owner)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		v, e := scanRequest(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s Store) SaveRequest(ctx context.Context, owner int64, id string, in RequestInput) (Request, error) {
	if !ValidID(in.RequestID) || in.Name == "" || len(in.Name) > 255 || len(in.Notes) > 4000 || in.ExpiryDays < 0 || in.ExpiryDays > 3650 || in.MaxFiles < 0 || in.MaxFiles > 1000 || in.MaxBytes < 0 {
		return Request{}, errors.New("invalid file request")
	}
	expires := int64(0)
	if in.ExpiryDays > 0 {
		expires = s.Now().Add(time.Duration(in.ExpiryDays) * 24 * time.Hour).Unix()
	}
	if id == "" {
		var old string
		e := s.DB.QueryRowContext(ctx, `SELECT id FROM file_share_requests WHERE owner_id=? AND request_id=?`, owner, in.RequestID).Scan(&old)
		if e == nil {
			return s.GetRequest(ctx, old)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return Request{}, e
		}
		var count int
		if e = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_share_requests WHERE revoked=0`).Scan(&count); e != nil {
			return Request{}, e
		}
		if count >= 1000 {
			return Request{}, errors.New("file request limit reached")
		}
		id = Token()
		_, e = s.DB.ExecContext(ctx, `INSERT INTO file_share_requests(id,owner_id,request_id,name,notes,expires_at,max_files,max_bytes,entries_json,revision,revoked) VALUES(?,?,?,?,?,?,?,?, '[]',1,0)`, id, owner, in.RequestID, in.Name, in.Notes, expires, in.MaxFiles, in.MaxBytes)
		if e != nil {
			return Request{}, e
		}
	} else {
		r, e := s.DB.ExecContext(ctx, `UPDATE file_share_requests SET name=?,notes=?,expires_at=?,max_files=?,max_bytes=?,revision=revision+1 WHERE id=? AND owner_id=? AND revision=? AND revoked=0`, in.Name, in.Notes, expires, in.MaxFiles, in.MaxBytes, id, owner, in.Revision)
		if e != nil {
			return Request{}, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return Request{}, ErrConflict
		}
	}
	return s.GetRequest(ctx, id)
}
func (s Store) RequestAvailable(ctx context.Context, v Request) bool {
	if v.Revoked || v.ExpiresAt > 0 && v.ExpiresAt <= s.Now().Unix() {
		return false
	}
	var enabled bool
	e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND enabled=1 AND role='administrator')`, v.Owner).Scan(&enabled)
	return e == nil && enabled
}
func (s Store) RevokeRequest(ctx context.Context, owner int64, id string, revision int) error {
	r, e := s.DB.ExecContext(ctx, `UPDATE file_share_requests SET revoked=1,entries_json='[]',revision=revision+1 WHERE id=? AND owner_id=? AND revision=?`, id, owner, revision)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s Store) AppendRequest(ctx context.Context, v Request, entry Entry) (Request, error) {
	if !s.RequestAvailable(ctx, v) || len(v.Entries) >= MaxEntries || v.MaxFiles > 0 && len(v.Entries) >= v.MaxFiles {
		return Request{}, ErrUnavailable
	}
	var total int64
	for _, e := range v.Entries {
		total += e.Size
		if e.Path == entry.Path {
			return Request{}, errors.New("filename already exists")
		}
	}
	if v.MaxBytes > 0 && total+entry.Size > v.MaxBytes {
		return Request{}, errors.New("file request size exceeded")
	}
	v.Entries = append(v.Entries, entry)
	raw, _ := json.Marshal(v.Entries)
	r, e := s.DB.ExecContext(ctx, `UPDATE file_share_requests SET entries_json=?,revision=revision+1 WHERE id=? AND revision=? AND revoked=0`, string(raw), v.ID, v.Revision)
	if e != nil {
		return Request{}, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return Request{}, ErrConflict
	}
	return s.GetRequest(ctx, v.ID)
}
