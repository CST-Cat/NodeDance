package hostcontrol

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/CST-Cat/NodeDance/internal/agent/localstate"
	"github.com/CST-Cat/NodeDance/internal/protocol"
	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

const maxCommandHistoryFiles = 10000

// Compacted outcomes retain the original request hash and ID. Dropping output
// must never turn an uncertain retry into a second script execution.
func (m *Manager) openArchive() error {
	path := filepath.Join(m.dir, "command-history.sqlite")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	err = unix.Fstat(fd, &st)
	unix.Close(fd)
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0777 != 0600 {
		return errors.New("command archive must be a root-owned 0600 regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000", `CREATE TABLE IF NOT EXISTS command_archive(id TEXT PRIMARY KEY,job_json TEXT NOT NULL,started_at INTEGER NOT NULL,expires_at INTEGER NOT NULL)`, `CREATE INDEX IF NOT EXISTS command_archive_expiry ON command_archive(expires_at)`, `CREATE INDEX IF NOT EXISTS command_archive_started ON command_archive(started_at DESC)`} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return err
		}
	}
	m.archive = db
	return nil
}

func (m *Manager) readJob(id string, j *protocol.CommandJob) error {
	if cached, ok := m.history[id]; ok && cached.Status == "unknown" {
		*j = cached
		return nil
	}
	err := localstate.Read(m.jobPath(id), j)
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var raw string
	err = m.archive.QueryRow("SELECT job_json FROM command_archive WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return os.ErrNotExist
	}
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(raw), j)
}

func (m *Manager) cleanupLocked(now time.Time) (protocol.CommandCleanup, error) {
	result := protocol.CommandCleanup{}
	// Interrupted outcomes have an unknown external effect and no expiry.
	removed, err := m.archive.Exec("DELETE FROM command_archive WHERE expires_at>0 AND expires_at<?", now.UnixNano())
	if err != nil {
		return result, err
	}
	n, err := removed.RowsAffected()
	if err != nil {
		return result, err
	}
	result.Expired = int(n)
	jobs := make([]protocol.CommandJob, 0, len(m.history))
	for _, j := range m.history {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.Before(jobs[j].StartedAt) })
	retention := time.Duration(m.settings.CommandHistoryRetentionDays) * 24 * time.Hour
	pressure := len(jobs) >= maxCommandHistoryFiles
	target := maxCommandHistoryFiles - 128
	for _, j := range jobs {
		if j.FinishedAt == nil || j.Status == "running" || j.Status == "unknown" {
			continue
		}
		expires := j.FinishedAt.Add(retention)
		expired := j.Status != "interrupted" && now.After(expires)
		if !expired && (!pressure || len(m.history) <= target) {
			continue
		}
		if !expired {
			j.Output = ""
			j.Archived = true
			expiry := expires.UnixNano()
			if j.Status == "interrupted" {
				expiry = 0
			}
			raw, err := json.Marshal(j)
			if err != nil {
				return result, err
			}
			if _, err = m.archive.Exec(`INSERT INTO command_archive(id,job_json,started_at,expires_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET job_json=excluded.job_json,expires_at=excluded.expires_at`, j.ID, string(raw), j.StartedAt.UnixNano(), expiry); err != nil {
				return result, err
			}
		}
		if err := os.Remove(m.jobPath(j.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		delete(m.history, j.ID)
		if expired {
			result.Expired++
		} else {
			result.Archived++
		}
	}
	result.Retained = len(m.history)
	return result, nil
}

func (m *Manager) historyLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.mu.Lock()
			_, err := m.cleanupLocked(now)
			m.mu.Unlock()
			if err != nil {
				fmt.Fprintln(os.Stderr, "command history cleanup failed:", err)
			}
		}
	}
}
