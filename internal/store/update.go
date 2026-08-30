package store

import (
	"context"
	"time"
)

func (s *Store) AutoUpdate(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, "SELECT auto_update FROM instance_settings WHERE id = 1").Scan(&enabled)
	return enabled, err
}

func (s *Store) SetAutoUpdate(ctx context.Context, enabled bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE instance_settings SET auto_update = ?, updated_at = ? WHERE id = 1",
		enabled, time.Now().UnixMilli())
	return err
}
