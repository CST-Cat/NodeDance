package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const MaxEntries = 1000

var ErrUnavailable = errors.New("share is unavailable")
var ErrConflict = errors.New("share changed; refresh and retry")

type Input struct {
	RequestID    string   `json:"requestId"`
	NodeID       string   `json:"nodeId"`
	Paths        []string `json:"paths"`
	Name         string   `json:"name"`
	ExpiryDays   int      `json:"expiryDays"`
	MaxDownloads int      `json:"maxDownloads"`
	Password     *string  `json:"password,omitempty"`
	Revision     int      `json:"revision,omitempty"`
	ReplaceID    string   `json:"replaceId,omitempty"`
}
type Entry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Blob      string `json:"blob,omitempty"`
	Directory bool   `json:"directory,omitempty"`
}
type Share struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	NodeID            string   `json:"nodeId,omitempty"`
	Paths             []string `json:"paths,omitempty"`
	Entries           []Entry  `json:"entries"`
	Size              int64    `json:"size"`
	CreatedAt         int64    `json:"createdAt"`
	ExpiresAt         int64    `json:"expiresAt"`
	MaxDownloads      int      `json:"maxDownloads"`
	Downloads         int      `json:"downloads"`
	PasswordProtected bool     `json:"passwordProtected"`
	Revision          int      `json:"revision"`
	Revoked           bool     `json:"revoked"`
	PasswordHash      []byte   `json:"-"`
	Owner             int64    `json:"-"`
	RequestID         string   `json:"-"`
}
type Store struct {
	BlobMu *sync.Mutex
	DB     *sql.DB
	Dir    string
	Now    func() time.Time
}

func (s Store) Init(ctx context.Context) error {
	if err := os.MkdirAll(s.blobDir(), 0700); err != nil {
		return err
	}
	return os.Chmod(s.blobDir(), 0700)
}
func (s Store) blobDir() string { return filepath.Join(s.Dir, "shares") }
func Token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func ValidID(v string) bool {
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == 32 && v == strings.ToLower(v)
}
func Validate(in Input) error {
	if !ValidID(in.RequestID) || in.ExpiryDays < 0 || in.ExpiryDays > 3650 || in.MaxDownloads < 0 || in.MaxDownloads > 1000000 || len(in.Name) > 255 || strings.ContainsAny(in.Name, "\x00\r\n") || len(in.Paths) > 100 {
		return errors.New("invalid share settings")
	}
	if in.Password != nil && len(*in.Password) > 72 {
		return errors.New("password must be at most 72 bytes")
	}
	return nil
}
func Password(value *string) ([]byte, error) {
	if value == nil || *value == "" {
		return nil, nil
	}
	return bcrypt.GenerateFromPassword([]byte(*value), bcrypt.DefaultCost)
}
func (s Store) CheckPassword(v Share, p string) bool {
	return len(v.PasswordHash) == 0 || bcrypt.CompareHashAndPassword(v.PasswordHash, []byte(p)) == nil
}
func (s Store) PutBlob(ctx context.Context, reader io.Reader, limit int64) (Entry, error) {
	f, err := os.CreateTemp(s.blobDir(), ".pending-")
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	defer os.Remove(f.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(reader, limit+1))
	if err != nil {
		return Entry{}, err
	}
	if ctx.Err() != nil {
		return Entry{}, ctx.Err()
	}
	if n > limit {
		return Entry{}, errors.New("share exceeds transfer limit")
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if err = f.Sync(); err != nil {
		return Entry{}, err
	}
	if s.BlobMu != nil {
		s.BlobMu.Lock()
		defer s.BlobMu.Unlock()
	}
	if err = os.Link(f.Name(), filepath.Join(s.blobDir(), digest)); err != nil && !errors.Is(err, os.ErrExist) {
		return Entry{}, err
	}
	if err = os.Chtimes(filepath.Join(s.blobDir(), digest), s.Now(), s.Now()); err != nil {
		return Entry{}, err
	}
	return Entry{Size: n, Blob: digest}, nil
}
func (s Store) Blob(v Entry) (*os.File, error) {
	if !ValidID(v.Blob) {
		return nil, ErrUnavailable
	}
	return os.Open(filepath.Join(s.blobDir(), v.Blob))
}

const columns = `id,owner_id,request_id,node_id,paths_json,name,entries_json,size,created_at,expires_at,max_downloads,downloads,password_hash,revision,revoked`

func scan(row interface{ Scan(...any) error }) (Share, error) {
	var v Share
	var entries, paths string
	err := row.Scan(&v.ID, &v.Owner, &v.RequestID, &v.NodeID, &paths, &v.Name, &entries, &v.Size, &v.CreatedAt, &v.ExpiresAt, &v.MaxDownloads, &v.Downloads, &v.PasswordHash, &v.Revision, &v.Revoked)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(entries), &v.Entries); err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(paths), &v.Paths)
	v.PasswordProtected = len(v.PasswordHash) > 0
	return v, err
}
func (s Store) Get(ctx context.Context, id string) (Share, error) {
	return scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM file_shares WHERE id=?`, id))
}
func (s Store) Retry(ctx context.Context, owner int64, request string) (Share, error) {
	return scan(s.DB.QueryRowContext(ctx, `SELECT `+columns+` FROM file_shares WHERE owner_id=? AND request_id=?`, owner, request))
}
func (s Store) List(ctx context.Context, owner int64) ([]Share, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+strings.ReplaceAll(strings.ReplaceAll(columns, "entries_json", "'[]'"), "paths_json", "'[]'")+` FROM file_shares WHERE owner_id=? ORDER BY revoked,created_at DESC,id LIMIT 1000`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s Store) Create(ctx context.Context, owner int64, in Input, entries []Entry) (Share, error) {
	if err := Validate(in); err != nil {
		return Share{}, err
	}
	if len(entries) == 0 || len(entries) > MaxEntries {
		return Share{}, errors.New("share must contain between 1 and 1000 entries")
	}
	if old, err := s.Retry(ctx, owner, in.RequestID); err == nil {
		return old, nil
	}
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_shares WHERE revoked=0`).Scan(&count); err != nil {
		return Share{}, err
	}
	if count >= 1000 {
		return Share{}, errors.New("share limit reached")
	}
	hash, err := Password(in.Password)
	if err != nil {
		return Share{}, err
	}
	v := Share{ID: Token(), Owner: owner, Name: in.Name, NodeID: in.NodeID, Paths: in.Paths, Entries: entries, CreatedAt: s.Now().Unix(), MaxDownloads: in.MaxDownloads, PasswordHash: hash, Revision: 1}
	if v.Name == "" {
		v.Name = path.Base(entries[0].Path)
	}
	if in.ExpiryDays > 0 {
		v.ExpiresAt = s.Now().Add(time.Duration(in.ExpiryDays) * 24 * time.Hour).Unix()
	}
	for _, e := range entries {
		if !SafePath(e.Path) {
			return Share{}, errors.New("invalid share entry")
		}
		v.Size += e.Size
	}
	raw, _ := json.Marshal(entries)
	paths, _ := json.Marshal(in.Paths)
	if paths == nil {
		paths = []byte("[]")
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO file_shares (`+columns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, owner, in.RequestID, v.NodeID, string(paths), v.Name, string(raw), v.Size, v.CreatedAt, v.ExpiresAt, v.MaxDownloads, 0, hash, 1, false)
	if err != nil {
		return Share{}, err
	}
	return s.Get(ctx, v.ID)
}
func SafePath(p string) bool {
	return p != "" && p != "." && !strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.ContainsAny(p, "\\\x00") && p != ".." && !strings.HasPrefix(p, "../")
}
func (s Store) Update(ctx context.Context, owner int64, id string, in Input) (Share, error) {
	if err := Validate(in); err != nil {
		return Share{}, err
	}
	v, err := s.Get(ctx, id)
	if err != nil || v.Owner != owner {
		return Share{}, ErrUnavailable
	}
	if v.Revoked {
		return Share{}, ErrUnavailable
	}
	hash := v.PasswordHash
	if in.Password != nil {
		hash, err = Password(in.Password)
		if err != nil {
			return Share{}, err
		}
	}
	entries, size := v.Entries, v.Size
	nodeID, sourcePaths := v.NodeID, v.Paths
	if in.ReplaceID != "" {
		other, e := s.Get(ctx, in.ReplaceID)
		if e != nil || other.Owner != owner || other.Revoked {
			return Share{}, ErrUnavailable
		}
		entries, size = other.Entries, other.Size
		nodeID, sourcePaths = other.NodeID, other.Paths
	}
	for _, entry := range entries {
		if entry.Directory {
			continue
		}
		f, e := s.Blob(entry)
		if e != nil {
			return Share{}, ErrUnavailable
		}
		f.Close()
	}
	expires := int64(0)
	if in.ExpiryDays > 0 {
		expires = s.Now().Add(time.Duration(in.ExpiryDays) * 24 * time.Hour).Unix()
	}
	raw, _ := json.Marshal(entries)
	paths, _ := json.Marshal(sourcePaths)
	result, err := s.DB.ExecContext(ctx, `UPDATE file_shares SET name=?,expires_at=?,max_downloads=?,password_hash=?,entries_json=?,size=?,node_id=?,paths_json=?,revision=revision+1 WHERE id=? AND owner_id=? AND revision=? AND revoked=0`, in.Name, expires, in.MaxDownloads, hash, string(raw), size, nodeID, string(paths), id, owner, in.Revision)
	if err != nil {
		return Share{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return Share{}, ErrConflict
	}
	return s.Get(ctx, id)
}
func (s Store) Revoke(ctx context.Context, owner int64, id string, revision int) error {
	r, e := s.DB.ExecContext(ctx, `UPDATE file_shares SET revoked=1,revision=revision+1 WHERE id=? AND owner_id=? AND revision=?`, id, owner, revision)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s Store) Available(ctx context.Context, v Share) bool {
	if v.Revoked || v.ExpiresAt > 0 && v.ExpiresAt <= s.Now().Unix() || v.MaxDownloads > 0 && v.Downloads >= v.MaxDownloads {
		return false
	}
	var active bool
	if err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND enabled=1 AND role='administrator') AND (?='' OR EXISTS(SELECT 1 FROM nodes WHERE id=? AND status!='revoked'))`, v.Owner, v.NodeID, v.NodeID).Scan(&active); err != nil {
		return false
	}
	return active
}
func (s Store) Consume(ctx context.Context, v Share) error {
	r, e := s.DB.ExecContext(ctx, `UPDATE file_shares SET downloads=downloads+1 WHERE id=? AND revision=? AND revoked=0 AND (expires_at=0 OR expires_at>?) AND (max_downloads=0 OR downloads<max_downloads) AND EXISTS(SELECT 1 FROM users WHERE id=owner_id AND enabled=1 AND role='administrator') AND (node_id='' OR EXISTS(SELECT 1 FROM nodes WHERE id=node_id AND status!='revoked'))`, v.ID, v.Revision, s.Now().Unix())
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrUnavailable
	}
	return nil
}
func (s Store) Public(v Share) Share {
	v.NodeID = ""
	v.Paths = nil
	v.Owner = 0
	v.PasswordHash = nil
	v.RequestID = ""
	for i := range v.Entries {
		v.Entries[i].Blob = ""
	}
	return v
}
func (s Store) Cleanup(ctx context.Context) error {
	if s.BlobMu != nil {
		s.BlobMu.Lock()
		defer s.BlobMu.Unlock()
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT entries_json FROM file_shares WHERE revoked=0 AND (expires_at=0 OR expires_at>?) AND (max_downloads=0 OR downloads<max_downloads) UNION ALL SELECT entries_json FROM file_share_requests WHERE revoked=0`, s.Now().Unix())
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var entries []Entry
		if err = json.Unmarshal([]byte(raw), &entries); err != nil {
			rows.Close()
			return err
		}
		for _, e := range entries {
			live[e.Blob] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	files, err := os.ReadDir(s.blobDir())
	if err != nil {
		return err
	}
	for _, f := range files {
		if live[f.Name()] {
			continue
		}
		info, e := f.Info()
		if e != nil {
			return e
		}
		if s.Now().Sub(info.ModTime()) < time.Hour {
			continue
		}
		if e = os.Remove(filepath.Join(s.blobDir(), f.Name())); e != nil {
			return fmt.Errorf("clean share storage: %w", e)
		}
	}
	return nil
}
