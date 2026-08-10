package store

import (
	"context"
	"database/sql"
	"errors"
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
	// Timestamps are unix milliseconds, so a client that ships seconds or nanoseconds
	// instead writes a row that is either prehistoric or centuries out. The bound is
	// deliberately loose - a year covers any offline device and any plausible clock
	// drift. It exists for two failure modes that have no other floor: a poisoned
	// duration overflowing SUM(stopped_at - started_at) in the reports, and an
	// updated_at in the far future, which under last-write-wins freezes the row against
	// every honest edit from every device, permanently and with no way to repair it
	// from inside the product.
	maxClockSkewMs = 366 * 24 * 60 * 60 * 1000
	maxEntrySpanMs = maxClockSkewMs
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
		if nextSeq, err = allocateServerSeq(transaction, totalRows); err != nil {
			return SyncResponse{}, err
		}
	}

	// A row the last-write-wins guard refuses writes nothing and leaves server_seq
	// where it was, so the pull below - which selects on server_seq > since - would not
	// carry the winning version back when the client cursor is already past it. The
	// client would then keep its refused version forever, believing it synced. Collect
	// those ids and echo them explicitly. In the normal case nothing lands here.
	refused := refusedIDs{}

	// A refusal has two causes and they need opposite answers. Losing last-write-wins
	// is ordinary: echo the winner and the client converges. An id that belongs to
	// another user can never converge - the echo below is scoped to this user, so the
	// row would be dropped from the push queue and exist nowhere but that one browser.
	// The client can only surface what the server calls an error, so it is one.
	claimed := func(table, id string) error {
		var owned int
		err := transaction.QueryRowContext(ctx,
			fmt.Sprintf("SELECT 1 FROM %s WHERE id = ? AND user_id <> ?", table), id, userID).Scan(&owned)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s %s belongs to another account", ErrInvalidInput, table, id)
	}

	// Projects go first so that pulled entries never reference a project the client
	// has not seen yet within the same batch.
	for _, project := range request.Changes.Projects {
		result, err := transaction.ExecContext(ctx, `
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
		if written, err := result.RowsAffected(); err != nil {
			return SyncResponse{}, err
		} else if written == 0 {
			if err := claimed("projects", project.ID); err != nil {
				return SyncResponse{}, err
			}
			refused.projects = append(refused.projects, project.ID)
		}
		nextSeq++
	}
	for _, entry := range request.Changes.TimeEntries {
		// agent_session_id and paused_ms are server-owned: literal values on insert
		// (ids come from the client, so a pushed value could claim a foreign session
		// or hand the row a pause it never had) and absent from the update list, so
		// a push never rewrites them.
		result, err := transaction.ExecContext(ctx, `
			INSERT INTO time_entries (id, user_id, project_id, description, tags, started_at, stopped_at,
			                          created_at, updated_at, deleted_at, server_seq, agent_session_id, paused_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0)
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
		if written, err := result.RowsAffected(); err != nil {
			return SyncResponse{}, err
		} else if written == 0 {
			if err := claimed("time_entries", entry.ID); err != nil {
				return SyncResponse{}, err
			}
			refused.timeEntries = append(refused.timeEntries, entry.ID)
		}
		nextSeq++
	}
	for _, timeOff := range request.Changes.TimeOff {
		result, err := transaction.ExecContext(ctx, `
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
		if written, err := result.RowsAffected(); err != nil {
			return SyncResponse{}, err
		} else if written == 0 {
			if err := claimed("time_off", timeOff.ID); err != nil {
				return SyncResponse{}, err
			}
			refused.timeOff = append(refused.timeOff, timeOff.ID)
		}
		nextSeq++
	}

	response := SyncResponse{}

	projectQuery, projectArgs := pullQuery(`
		SELECT id, name, color, archived, created_at, updated_at, deleted_at, server_seq
		FROM projects`, userID, request.Since, refused.projects)
	projectRows, err := transaction.QueryContext(ctx, projectQuery, projectArgs...)
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

	entryQuery, entryArgs := pullQuery(`
		SELECT id, project_id, description, tags, started_at, stopped_at, created_at, updated_at, deleted_at,
		       server_seq, agent_session_id, paused_ms
		FROM time_entries`, userID, request.Since, refused.timeEntries)
	entryRows, err := transaction.QueryContext(ctx, entryQuery, entryArgs...)
	if err != nil {
		return SyncResponse{}, err
	}
	for entryRows.Next() {
		var entry TimeEntry
		if err := entryRows.Scan(&entry.ID, &entry.ProjectID, &entry.Description, &entry.Tags, &entry.StartedAt, &entry.StoppedAt,
			&entry.CreatedAt, &entry.UpdatedAt, &entry.DeletedAt, &entry.ServerSeq, &entry.AgentSessionID,
			&entry.PausedMs); err != nil {
			entryRows.Close()
			return SyncResponse{}, err
		}
		response.Changes.TimeEntries = append(response.Changes.TimeEntries, entry)
	}
	if err := closeRows(entryRows); err != nil {
		return SyncResponse{}, err
	}

	timeOffQuery, timeOffArgs := pullQuery(`
		SELECT id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq
		FROM time_off`, userID, request.Since, refused.timeOff)
	timeOffRows, err := transaction.QueryContext(ctx, timeOffQuery, timeOffArgs...)
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

// refusedIDs holds the ids of pushed rows the last-write-wins guard left untouched,
// per table, so the pull can hand the winning version back to the client.
type refusedIDs struct {
	projects    []string
	timeEntries []string
	timeOff     []string
}

// pullQuery completes a pull SELECT with the user filter, the cursor and the explicit
// echo of refused ids. The id list is empty on every ordinary sync, in which case the
// query is exactly the plain cursor scan.
func pullQuery(selectClause string, userID string, since int64, refused []string) (string, []any) {
	args := []any{userID, since}
	condition := "server_seq > ?"
	if len(refused) > 0 {
		placeholders := strings.Repeat(",?", len(refused))[1:]
		condition = fmt.Sprintf("(server_seq > ? OR id IN (%s))", placeholders)
		for _, id := range refused {
			args = append(args, id)
		}
	}
	return fmt.Sprintf("%s WHERE user_id = ? AND %s ORDER BY server_seq", selectClause, condition), args
}

// allocateServerSeq reserves a contiguous block of server_seq values inside the
// given transaction and returns the first value of the block.
func allocateServerSeq(transaction *sql.Tx, rows int) (int64, error) {
	var lastSeq int64
	if err := transaction.QueryRow(
		"UPDATE sync_state SET seq = seq + ? RETURNING seq", rows,
	).Scan(&lastSeq); err != nil {
		return 0, err
	}
	return lastSeq - int64(rows) + 1, nil
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
	// Text limits count runes, matching validateTags and the character counts the
	// clients enforce. Counting bytes would silently halve every limit for Cyrillic
	// input and reject text the editor accepted.
	maxTimestamp := time.Now().UnixMilli() + maxClockSkewMs
	for _, project := range changes.Projects {
		if err := uuid.Validate(project.ID); err != nil {
			return fmt.Errorf("project id %q is not a UUID", project.ID)
		}
		if project.Name == "" || utf8.RuneCountInString(project.Name) > maxNameLength {
			return fmt.Errorf("project %s: name must be 1..%d chars", project.ID, maxNameLength)
		}
		if utf8.RuneCountInString(project.Color) > maxColorLength {
			return fmt.Errorf("project %s: color too long", project.ID)
		}
		if project.UpdatedAt <= 0 || project.UpdatedAt > maxTimestamp {
			return fmt.Errorf("project %s: updated_at is required and must not be in the future", project.ID)
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
		if utf8.RuneCountInString(entry.Description) > maxTextLength {
			return fmt.Errorf("time entry %s: description too long", entry.ID)
		}
		if entry.StartedAt <= 0 || entry.UpdatedAt <= 0 {
			return fmt.Errorf("time entry %s: started_at and updated_at are required", entry.ID)
		}
		if entry.UpdatedAt > maxTimestamp {
			return fmt.Errorf("time entry %s: updated_at is too far in the future", entry.ID)
		}
		if entry.StartedAt > maxTimestamp {
			return fmt.Errorf("time entry %s: started_at is too far in the future", entry.ID)
		}
		if entry.StoppedAt != nil {
			if *entry.StoppedAt < entry.StartedAt {
				return fmt.Errorf("time entry %s: stopped_at is before started_at", entry.ID)
			}
			if *entry.StoppedAt > maxTimestamp {
				return fmt.Errorf("time entry %s: stopped_at is too far in the future", entry.ID)
			}
			if *entry.StoppedAt-entry.StartedAt > maxEntrySpanMs {
				return fmt.Errorf("time entry %s: spans more than %d days", entry.ID, maxEntrySpanMs/(24*60*60*1000))
			}
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
		if utf8.RuneCountInString(timeOff.Note) > maxTextLength {
			return fmt.Errorf("time off %s: note too long", timeOff.ID)
		}
		if timeOff.UpdatedAt <= 0 || timeOff.UpdatedAt > maxTimestamp {
			return fmt.Errorf("time off %s: updated_at is required and must not be in the future", timeOff.ID)
		}
	}
	return nil
}
