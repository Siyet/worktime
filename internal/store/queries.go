package store

import (
	"context"
	"database/sql"
	"errors"
)

// ListProjects returns the user's non-deleted projects ordered by name.
func (s *Store) ListProjects(ctx context.Context, userID string) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, color, archived, created_at, updated_at, deleted_at, server_seq
		FROM projects WHERE user_id = ? AND deleted_at IS NULL ORDER BY name`, userID)
	if err != nil {
		return nil, err
	}
	projects := []Project{}
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name, &project.Color, &project.Archived,
			&project.CreatedAt, &project.UpdatedAt, &project.DeletedAt, &project.ServerSeq); err != nil {
			rows.Close()
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, closeRows(rows)
}

// ListRunningEntries returns entries without stopped_at, newest first.
func (s *Store) ListRunningEntries(ctx context.Context, userID string) ([]TimeEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, description, started_at, stopped_at, created_at, updated_at, deleted_at, server_seq
		FROM time_entries
		WHERE user_id = ? AND stopped_at IS NULL AND deleted_at IS NULL
		ORDER BY started_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	entries := []TimeEntry{}
	for rows.Next() {
		var entry TimeEntry
		if err := rows.Scan(&entry.ID, &entry.ProjectID, &entry.Description, &entry.StartedAt, &entry.StoppedAt,
			&entry.CreatedAt, &entry.UpdatedAt, &entry.DeletedAt, &entry.ServerSeq); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, closeRows(rows)
}

// GetTimeEntry returns a single non-deleted entry owned by the user.
func (s *Store) GetTimeEntry(ctx context.Context, userID, entryID string) (TimeEntry, error) {
	var entry TimeEntry
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, description, started_at, stopped_at, created_at, updated_at, deleted_at, server_seq
		FROM time_entries WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, entryID, userID,
	).Scan(&entry.ID, &entry.ProjectID, &entry.Description, &entry.StartedAt, &entry.StoppedAt,
		&entry.CreatedAt, &entry.UpdatedAt, &entry.DeletedAt, &entry.ServerSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return TimeEntry{}, ErrNotFound
	}
	return entry, err
}

// ListTimeOff returns the user's non-deleted time off records, newest first.
func (s *Store) ListTimeOff(ctx context.Context, userID string) ([]TimeOff, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq
		FROM time_off WHERE user_id = ? AND deleted_at IS NULL ORDER BY date_from DESC`, userID)
	if err != nil {
		return nil, err
	}
	records := []TimeOff{}
	for rows.Next() {
		var timeOff TimeOff
		if err := rows.Scan(&timeOff.ID, &timeOff.Kind, &timeOff.DateFrom, &timeOff.DateTo, &timeOff.Note,
			&timeOff.CreatedAt, &timeOff.UpdatedAt, &timeOff.DeletedAt, &timeOff.ServerSeq); err != nil {
			rows.Close()
			return nil, err
		}
		records = append(records, timeOff)
	}
	return records, closeRows(rows)
}
