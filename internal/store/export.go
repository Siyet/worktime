package store

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
)

// Unique ATTACH alias per export: two concurrent exports on the shared single
// connection must not collide on the same schema name.
var exportSequence atomic.Int64

// ExportUserDB writes a standalone SQLite database containing only the given
// user's data: their users row, projects, time_entries and time_off. Sessions
// and api_tokens are credentials and stay out; sync_state keeps its initial
// zero row - the counter is global to the server, not part of the user's data.
// The file is produced by running the normal migration set and copying rows
// through ATTACH, so it opens with store.Open like any other WorkTime database.
// The caller must invoke cleanup once the file has been served.
func (s *Store) ExportUserDB(ctx context.Context, userID string) (string, func(), error) {
	tempFile, err := os.CreateTemp("", "worktime-export-*.sqlite")
	if err != nil {
		return "", nil, err
	}
	path := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		os.Remove(path)
		return "", nil, err
	}
	cleanup := func() { os.Remove(path) }

	exportStore, err := Open(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	if err := exportStore.Close(); err != nil {
		cleanup()
		return "", nil, err
	}

	alias := fmt.Sprintf("export_%d", exportSequence.Add(1))
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE ? AS %s", alias), path); err != nil {
		cleanup()
		return "", nil, err
	}
	copyErr := s.copyUserRows(ctx, alias, userID)
	// DETACH must run even after a failed copy, or the temp file stays locked
	// and cannot be removed on Windows.
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf("DETACH DATABASE %s", alias)); err != nil && copyErr == nil {
		copyErr = err
	}
	if copyErr != nil {
		cleanup()
		return "", nil, copyErr
	}
	return path, cleanup, nil
}

func (s *Store) copyUserRows(ctx context.Context, alias, userID string) error {
	statements := []string{
		`INSERT INTO %[1]s.users (id, google_sub, email, name, picture_url, created_at)
		 SELECT id, google_sub, email, name, picture_url, created_at FROM users WHERE id = ?`,
		`INSERT INTO %[1]s.projects (id, user_id, name, color, archived, created_at, updated_at, deleted_at, server_seq)
		 SELECT id, user_id, name, color, archived, created_at, updated_at, deleted_at, server_seq
		 FROM projects WHERE user_id = ?`,
		`INSERT INTO %[1]s.time_entries (id, user_id, project_id, description, tags, started_at, stopped_at,
		                                 created_at, updated_at, deleted_at, server_seq)
		 SELECT id, user_id, project_id, description, tags, started_at, stopped_at,
		        created_at, updated_at, deleted_at, server_seq
		 FROM time_entries WHERE user_id = ?`,
		`INSERT INTO %[1]s.time_off (id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq)
		 SELECT id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq
		 FROM time_off WHERE user_id = ?`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(statement, alias), userID); err != nil {
			return err
		}
	}
	return nil
}
