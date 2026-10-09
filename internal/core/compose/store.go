// Package compose stores the last known Compose project inventory. Operation
// state is managed only by Core Tasks when Compose writes are reintroduced.
package compose

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/CST-Cat/NodeDance/internal/protocol"
)

var nodeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Options struct{ Now func() time.Time }

type Store struct {
	db  *sql.DB
	now func() time.Time
}

type Project struct {
	NodeID       string
	Value        protocol.ComposeProject
	DiscoveredAt time.Time
	LastSeenAt   time.Time
}

func NewStore(db *sql.DB, options Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("Compose database is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{db: db, now: options.Now}, nil
}

func (s *Store) RegisterProjects(ctx context.Context, nodeID string, projects []protocol.ComposeProject) error {
	if !nodeIDPattern.MatchString(nodeID) {
		return errors.New("invalid Compose node ID")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UnixNano()
	for _, project := range projects {
		if err := protocol.ValidateComposeProjectRef(project.Ref); err != nil {
			return err
		}
		files, err := json.Marshal(project.Ref.ConfigFiles)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO compose_projects(node_id, project_key, project_name, working_directory,
			config_files_json, config_available, discovered_at, last_seen_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(node_id, project_key) DO UPDATE SET project_name=excluded.project_name,
			working_directory=excluded.working_directory, config_files_json=excluded.config_files_json,
			config_available=excluded.config_available, last_seen_at=excluded.last_seen_at`,
			nodeID, project.Ref.Key, project.Ref.Name, project.Ref.WorkingDirectory, string(files), project.ConfigAvailable, now, now)
		if err != nil {
			return fmt.Errorf("save Compose project: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) Projects(ctx context.Context, nodeID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_key, project_name, working_directory, config_files_json,
		config_available, discovered_at, last_seen_at FROM compose_projects WHERE node_id=? ORDER BY project_name, working_directory`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		var value protocol.ComposeProject
		var filesJSON string
		var available bool
		var discovered, seen int64
		if err := rows.Scan(&value.Ref.Key, &value.Ref.Name, &value.Ref.WorkingDirectory, &filesJSON, &available, &discovered, &seen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(filesJSON), &value.Ref.ConfigFiles); err != nil {
			return nil, errors.New("saved Compose project context is invalid")
		}
		if protocol.ValidateComposeProjectRef(value.Ref) != nil {
			return nil, errors.New("saved Compose project identity is invalid")
		}
		value.ConfigAvailable = available
		if !available {
			value.ConfigReason = "config_missing"
		}
		projects = append(projects, Project{NodeID: nodeID, Value: value,
			DiscoveredAt: time.Unix(0, discovered).UTC(), LastSeenAt: time.Unix(0, seen).UTC()})
	}
	return projects, rows.Err()
}
