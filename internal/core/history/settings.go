package history

import (
	"context"
	"errors"
	"time"
)

type Settings struct {
	RetentionDays int `json:"retentionDays"`
}

type SettingsView struct {
	Settings
	Choices []int `json:"choices"`
}

var ErrInvalidSettings = errors.New("monitoring retention must be 1, 7, 30, 90, 180 or 365 days")

func (s Store) Settings(ctx context.Context) (SettingsView, error) {
	result := SettingsView{Choices: []int{1, 7, 30, 90, 180, 365}}
	err := s.DB.QueryRowContext(ctx, `SELECT retention_days FROM metric_history_settings WHERE id=1`).Scan(&result.RetentionDays)
	return result, err
}

func (s Store) SaveSettings(ctx context.Context, settings Settings, now time.Time) error {
	switch settings.RetentionDays {
	case 1, 7, 30, 90, 180, 365:
	default:
		return ErrInvalidSettings
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE metric_history_settings SET retention_days=? WHERE id=1`, settings.RetentionDays); err != nil {
		return err
	}
	if err = cleanupHistory(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}
