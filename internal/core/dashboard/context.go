package dashboard

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxMemoBytes = 8192

var ErrMemoConflict = errors.New("memo changed; reload before saving")
var ErrInvalidMemo = errors.New("invalid memo")

type Memo struct {
	Content   string    `json:"content"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type MemoUpdate struct {
	Content  string `json:"content" request:"required"`
	Revision int64  `json:"revision" request:"required"`
}
type CoreInfo struct {
	Hostname        string    `json:"hostname"`
	Platform        string    `json:"platform"`
	PlatformVersion string    `json:"platformVersion"`
	KernelVersion   string    `json:"kernelVersion"`
	Architecture    string    `json:"architecture"`
	Version         string    `json:"version"`
	BootTime        uint64    `json:"bootTime"`
	Uptime          uint64    `json:"uptime"`
	Environment     string    `json:"environment"`
	Addresses       []string  `json:"addresses"`
	SampledAt       time.Time `json:"sampledAt"`
}

func (r Repository) Memo(ctx context.Context) (Memo, error) {
	var m Memo
	var at int64
	err := r.DB.QueryRowContext(ctx, `SELECT content,revision,updated_at FROM dashboard_memo WHERE id=1`).Scan(&m.Content, &m.Revision, &at)
	m.UpdatedAt = time.Unix(at, 0).UTC()
	return m, err
}
func (r Repository) SaveMemo(ctx context.Context, input MemoUpdate) (Memo, error) {
	if input.Revision < 1 || len(input.Content) > MaxMemoBytes || !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, 0) {
		return Memo{}, ErrInvalidMemo
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	at := now.Unix()
	result, err := r.DB.ExecContext(ctx, `UPDATE dashboard_memo SET content=?,revision=revision+1,updated_at=? WHERE id=1 AND revision=?`, input.Content, at, input.Revision)
	if err != nil {
		return Memo{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Memo{}, err
	}
	if n != 1 {
		return Memo{}, ErrMemoConflict
	}
	return Memo{Content: input.Content, Revision: input.Revision + 1, UpdatedAt: time.Unix(at, 0).UTC()}, nil
}
