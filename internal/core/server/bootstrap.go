package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/CST-Cat/NodeDance/internal/core/audit"
	"github.com/CST-Cat/NodeDance/internal/core/auth"
)

// InitialAdminPath contains first-run credentials until the first successful login.
// Only its path is printed during unattended starts.
func (s *Server) InitialAdminPath() string { return filepath.Join(s.dataDir, "initial-admin.json") }

func (s *Server) BootstrapAdministrator(ctx context.Context, username, password string) error {
	username, password, err := auth.InitialCredentials(username, password)
	if err != nil {
		return err
	}
	hash, salt, err := s.hashSetupPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("already initialized")
	}
	now := s.now().UnixNano()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,password_salt,password_version,display_name,updated_at) VALUES(1,?,?,?,?,?,?)`, username, hash, salt, auth.CurrentPasswordVersion, "NodeDance Admin", now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM init_credential`); err != nil {
		return err
	}
	if err := audit.Record(ctx, tx, audit.Event{OccurredAt: s.now(), Action: "setup", Outcome: "succeeded", ActorID: sql.NullInt64{Int64: 1, Valid: true}}); err != nil {
		return err
	}
	credentials, err := json.MarshalIndent(map[string]string{"username": username, "password": password}, "", "  ")
	if err != nil {
		return err
	}
	path := s.InitialAdminPath()
	if err := writeSecretFile(path, string(credentials)+"\n"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Remove(path)
		return err
	}
	if err := os.Remove(setupCredentialPath(s.dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
