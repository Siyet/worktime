package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxTextLength   = 2000
	maxNameLength   = 200
	maxColorLength  = 32
	maxBatchRows    = 10000
	maxTagsPerEntry = 8
	maxTagLength    = 24
)

// Sync applies client changes (last-write-wins by updated_at) and returns all rows
// of this user changed after request.Since, together with the new cursor value.
// Push and pull run in one transaction, so the returned cursor is consistent.
func (s *Store) Sync(ctx context.Context, userID string, request SyncRequest) (SyncResponse, error) {
	if err := validateChanges(request.Changes); err != nil {
		return SyncResponse{}, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SyncResponse{}, err
	}
	defer transaction.Rollback()

	totalRows := len(request.Changes.Projects) + len(request.Changes.TimeEntries) + len(request.Changes.TimeOff)
	nextSeq := int64(0)
	if totalRows > 0 {
		var lastSeq int64
		if err := transaction.QueryRow(
			"UPDATE sync_state SET seq = seq + ? RETURNING seq", totalRows,
		).Scan(&lastSeq); err != nil {
			return SyncResponse{}, err
		}
		nextSeq = lastSeq - int64(totalRows) + 1
	}

	// Projects go first so that pulled entries never reference a project the client
	// has not seen yet within the same batch.
	for _, project := range request.Changes.Projects {
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO projects (id, user_id, name, color, archived, created_at, updated_at, deleted_at, server_seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				name = excluded.name, color = excluded.color, archived = excluded.archived,
				updated_at = excluded.updated_at, deleted_at = excluded.deleted_at, server_seq = excluded.server_seq
			WHERE excluded.updated_at >= projects.updated_at AND projects.user_id = excluded.user_id`,
			project.ID, userID, project.Name, project.Color, project.Archived,
			project.CreatedAt, project.UpdatedAt, project.DeletedAt, nextSeq)
		if err != nil {
			return SyncResponse{}, err
		}
		nextSeq++
	}
	for _, entry := range request.Changes.TimeEntries {
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO time_entries (id, user_id, project_id, description, tags, started_at, stopped_at,
			                          created_at, updated_at, deleted_at, server_seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				project_id = excluded.project_id, description = excluded.description, tags = excluded.tags,
				started_at = excluded.started_at, stopped_at = excluded.stopped_at,
				updated_at = excluded.updated_at, deleted_at = excluded.deleted_at, server_seq = excluded.server_seq
			WHERE excluded.updated_at >= time_entries.updated_at AND time_entries.user_id = excluded.user_id`,
			entry.ID, userID, entry.ProjectID, entry.Description, entry.Tags, entry.StartedAt, entry.StoppedAt,
			entry.CreatedAt, entry.UpdatedAt, entry.DeletedAt, nextSeq)
		if err != nil {
			return SyncResponse{}, err
		}
		nextSeq++
	}
	for _, timeOff := range request.Changes.TimeOff {
		_, err := transaction.ExecContext(ctx, `
			INSERT INTO time_off (id, user_id, kind, date_from, date_to, note,
			                      created_at, updated_at, deleted_at, server_seq)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				kind = excluded.kind, date_from = excluded.date_from, date_to = excluded.date_to,
				note = excluded.note,
				updated_at = excluded.updated_at, deleted_at = excluded.deleted_at, server_seq = excluded.server_seq
			WHERE excluded.updated_at >= time_off.updated_at AND time_off.user_id = excluded.user_id`,
			timeOff.ID, userID, timeOff.Kind, timeOff.DateFrom, timeOff.DateTo, timeOff.Note,
			timeOff.CreatedAt, timeOff.UpdatedAt, timeOff.DeletedAt, nextSeq)
		if err != nil {
			return SyncResponse{}, err
		}
		nextSeq++
	}

	response := SyncResponse{}

	projectRows, err := transaction.QueryContext(ctx, `
		SELECT id, name, color, archived, created_at, updated_at, deleted_at, server_seq
		FROM projects WHERE user_id = ? AND server_seq > ? ORDER BY server_seq`, userID, request.Since)
	if err != nil {
		return SyncResponse{}, err
	}
	for projectRows.Next() {
		var project Project
		if err := projectRows.Scan(&project.ID, &project.Name, &project.Color, &project.Archived,
			&project.CreatedAt, &project.UpdatedAt, &project.DeletedAt, &project.ServerSeq); err != nil {
			projectRows.Close()
			return SyncResponse{}, err
		}
		response.Changes.Projects = append(response.Changes.Projects, project)
	}
	if err := closeRows(projectRows); err != nil {
		return SyncResponse{}, err
	}

	entryRows, err := transaction.QueryContext(ctx, `
		SELECT id, project_id, description, tags, started_at, stopped_at, created_at, updated_at, deleted_at, server_seq
		FROM time_entries WHERE user_id = ? AND server_seq > ? ORDER BY server_seq`, userID, request.Since)
	if err != nil {
		return SyncResponse{}, err
	}
	for entryRows.Next() {
		var entry TimeEntry
		if err := entryRows.Scan(&entry.ID, &entry.ProjectID, &entry.Description, &entry.Tags, &entry.StartedAt, &entry.StoppedAt,
			&entry.CreatedAt, &entry.UpdatedAt, &entry.DeletedAt, &entry.ServerSeq); err != nil {
			entryRows.Close()
			return SyncResponse{}, err
		}
		response.Changes.TimeEntries = append(response.Changes.TimeEntries, entry)
	}
	if err := closeRows(entryRows); err != nil {
		return SyncResponse{}, err
	}

	timeOffRows, err := transaction.QueryContext(ctx, `
		SELECT id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq
		FROM time_off WHERE user_id = ? AND server_seq > ? ORDER BY server_seq`, userID, request.Since)
	if err != nil {
		return SyncResponse{}, err
	}
	for timeOffRows.Next() {
		var timeOff TimeOff
		if err := timeOffRows.Scan(&timeOff.ID, &timeOff.Kind, &timeOff.DateFrom, &timeOff.DateTo, &timeOff.Note,
			&timeOff.CreatedAt, &timeOff.UpdatedAt, &timeOff.DeletedAt, &timeOff.ServerSeq); err != nil {
			timeOffRows.Close()
			return SyncResponse{}, err
		}
		response.Changes.TimeOff = append(response.Changes.TimeOff, timeOff)
	}
	if err := closeRows(timeOffRows); err != nil {
		return SyncResponse{}, err
	}

	if err := transaction.QueryRow("SELECT seq FROM sync_state").Scan(&response.Seq); err != nil {
		return SyncResponse{}, err
	}
	if err := transaction.Commit(); err != nil {
		return SyncResponse{}, err
	}
	return response, nil
}

// validateTags enforces the tag rules server-side. Three writers reach this endpoint -
// the PWA, the MCP server and raw API tokens - so the limits cannot live in the client.
// Names are values here, not ids, which is what makes rename and merge possible at all,
// and it is also why the normalised form has to be enforced rather than assumed.
func validateTags(tags TagList) error {
	if len(tags) > maxTagsPerEntry {
		return fmt.Errorf("at most %d tags allowed, got %d", maxTagsPerEntry, len(tags))
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		length := utf8.RuneCountInString(tag)
		if length == 0 || length > maxTagLength {
			return fmt.Errorf("tag %q must be 1..%d characters", tag, maxTagLength)
		}
		if tag != strings.TrimSpace(tag) {
			return fmt.Errorf("tag %q must be trimmed", tag)
		}
		if tag != strings.ToLower(tag) {
			return fmt.Errorf("tag %q must be lowercase", tag)
		}
		// The reports use "__untagged" as the bucket for entries with no tags, so the
		// underscore prefix stays reserved and a user tag can never collide with it.
		if strings.HasPrefix(tag, "_") {
			return fmt.Errorf("tag %q must not start with an underscore", tag)
		}
		if seen[tag] {
			return fmt.Errorf("tag %q is duplicated", tag)
		}
		seen[tag] = true
	}
	return nil
}

func validateChanges(changes SyncChanges) error {
	total := len(changes.Projects) + len(changes.TimeEntries) + len(changes.TimeOff)
	if total > maxBatchRows {
		return fmt.Errorf("batch of %d rows exceeds limit %d", total, maxBatchRows)
	}
	for _, project := range changes.Projects {
		if err := uuid.Validate(project.ID); err != nil {
			return fmt.Errorf("project id %q is not a UUID", project.ID)
		}
		if project.Name == "" || len(project.Name) > maxNameLength {
			return fmt.Errorf("project %s: name must be 1..%d chars", project.ID, maxNameLength)
		}
		if len(project.Color) > maxColorLength {
			return fmt.Errorf("project %s: color too long", project.ID)
		}
		if project.UpdatedAt <= 0 {
			return fmt.Errorf("project %s: updated_at is required", project.ID)
		}
	}
	for _, entry := range changes.TimeEntries {
		if err := uuid.Validate(entry.ID); err != nil {
			return fmt.Errorf("time entry id %q is not a UUID", entry.ID)
		}
		if entry.ProjectID != nil {
			if err := uuid.Validate(*entry.ProjectID); err != nil {
				return fmt.Errorf("time entry %s: project_id is not a UUID", entry.ID)
			}
		}
		if len(entry.Description) > maxTextLength {
			return fmt.Errorf("time entry %s: description too long", entry.ID)
		}
		if entry.StartedAt <= 0 || entry.UpdatedAt <= 0 {
			return fmt.Errorf("time entry %s: started_at and updated_at are required", entry.ID)
		}
		if entry.StoppedAt != nil && *entry.StoppedAt < entry.StartedAt {
			return fmt.Errorf("time entry %s: stopped_at is before started_at", entry.ID)
		}
		if err := validateTags(entry.Tags); err != nil {
			return fmt.Errorf("time entry %s: %w", entry.ID, err)
		}
	}
	for _, timeOff := range changes.TimeOff {
		if err := uuid.Validate(timeOff.ID); err != nil {
			return fmt.Errorf("time off id %q is not a UUID", timeOff.ID)
		}
		if timeOff.Kind != "sick" && timeOff.Kind != "vacation" && timeOff.Kind != "dayoff" {
			return fmt.Errorf("time off %s: kind must be sick, vacation or dayoff", timeOff.ID)
		}
		fromDate, err := time.Parse(time.DateOnly, timeOff.DateFrom)
		if err != nil {
			return fmt.Errorf("time off %s: date_from must be YYYY-MM-DD", timeOff.ID)
		}
		toDate, err := time.Parse(time.DateOnly, timeOff.DateTo)
		if err != nil {
			return fmt.Errorf("time off %s: date_to must be YYYY-MM-DD", timeOff.ID)
		}
		if toDate.Before(fromDate) {
			return fmt.Errorf("time off %s: date_to is before date_from", timeOff.ID)
		}
		if len(timeOff.Note) > maxTextLength {
			return fmt.Errorf("time off %s: note too long", timeOff.ID)
		}
		if timeOff.UpdatedAt <= 0 {
			return fmt.Errorf("time off %s: updated_at is required", timeOff.ID)
		}
	}
	return nil
}
