package store

// Agent sessions implement crash-resilient time tracking for AI agents
// (Claude Code first). The client sends idempotent start/heartbeat/stop
// signals keyed by the agent's session id; the server materializes them
// into ordinary time_entries rows so they flow to clients over the normal
// sync pull path. A lost stop can never produce a wrong duration: the
// reconciliation job closes any session whose last heartbeat is older than
// a grace period, at the moment of that last heartbeat.
//
// A session owns one *running* time entry at a time (the current segment).
// A heartbeat after an idle gap longer than the idle threshold stops the
// current segment at the previous heartbeat and opens a new one, so idle
// time in the middle of a session is never billed. Trailing idle is trimmed
// the same way on stop and by reconciliation.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	agentStatusActive = "active"
	agentStatusClosed = "closed"

	// AgentEndReasonStale marks sessions closed by the reconciliation job.
	AgentEndReasonStale = "stale_heartbeat"

	defaultAgentSource = "claude-code"

	maxAgentCwdLength    = 500
	maxAgentBranchLength = 200
	maxAgentModelLength  = 100
	maxAgentShortLength  = 64
)

type AgentSession struct {
	ID              string  `json:"id"`
	ProjectID       *string `json:"project_id"`
	Source          string  `json:"source"`
	Status          string  `json:"status"`
	StartedAt       int64   `json:"started_at"`
	LastHeartbeatAt int64   `json:"last_heartbeat_at"`
	EndedAt         *int64  `json:"ended_at"`
	EndReason       *string `json:"end_reason"`
	Cwd             string  `json:"cwd"`
	GitBranch       string  `json:"git_branch"`
	Model           string  `json:"model"`
	TimeEntryID     *string `json:"time_entry_id"`
}

// AgentStart carries the payload of a start signal. Zero StartedAt means "now".
type AgentStart struct {
	SessionID string
	StartedAt int64
	Source    string
	Cwd       string
	GitBranch string
	Model     string
	ProjectID *string
}

// StartAgentSession creates the session and its running time entry, or, when the
// same session id is replayed (--continue / --resume), refreshes metadata and
// treats the call as an activity signal. Replaying never duplicates entries.
func (s *Store) StartAgentSession(ctx context.Context, userID string, params AgentStart, idleMs int64) (AgentSession, error) {
	if err := uuid.Validate(params.SessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, params.SessionID)
	}
	now := time.Now().UnixMilli()
	if params.StartedAt <= 0 {
		params.StartedAt = now
	}
	params.Source = truncateRunes(params.Source, maxAgentShortLength)
	params.Cwd = truncateRunes(params.Cwd, maxAgentCwdLength)
	params.GitBranch = truncateRunes(params.GitBranch, maxAgentBranchLength)
	params.Model = truncateRunes(params.Model, maxAgentModelLength)
	if params.Source == "" {
		params.Source = defaultAgentSource
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentSession{}, err
	}
	defer transaction.Rollback()

	session, err := getAgentSession(ctx, transaction, userID, params.SessionID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.ensureAgentSessionIDFree(ctx, transaction, params.SessionID); err != nil {
			return AgentSession{}, err
		}
		if err := validateAgentProject(ctx, transaction, userID, params.ProjectID); err != nil {
			return AgentSession{}, err
		}
		session = AgentSession{
			ID: params.SessionID, ProjectID: params.ProjectID, Source: params.Source,
			Status: agentStatusActive, StartedAt: params.StartedAt, LastHeartbeatAt: params.StartedAt,
			Cwd: params.Cwd, GitBranch: params.GitBranch, Model: params.Model,
		}
		entryID, err := createAgentEntry(ctx, transaction, userID, session, params.StartedAt, now)
		if err != nil {
			return AgentSession{}, err
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO agent_sessions (id, user_id, project_id, source, status, started_at, last_heartbeat_at,
			                            cwd, git_branch, model, time_entry_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, userID, session.ProjectID, session.Source, session.Status,
			session.StartedAt, session.LastHeartbeatAt, session.Cwd, session.GitBranch, session.Model,
			entryID, now, now)
		if err != nil {
			return AgentSession{}, err
		}
	case err != nil:
		return AgentSession{}, err
	default:
		// Replay: refresh metadata first so a reopened segment gets a fresh description.
		if params.Cwd != "" {
			session.Cwd = params.Cwd
		}
		if params.GitBranch != "" {
			session.GitBranch = params.GitBranch
		}
		if params.Model != "" {
			session.Model = params.Model
		}
		if params.ProjectID != nil {
			if err := validateAgentProject(ctx, transaction, userID, params.ProjectID); err != nil {
				return AgentSession{}, err
			}
			session.ProjectID = params.ProjectID
		}
		_, err = transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET project_id = ?, cwd = ?, git_branch = ?, model = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			session.ProjectID, session.Cwd, session.GitBranch, session.Model, now, session.ID, userID)
		if err != nil {
			return AgentSession{}, err
		}
		if err := advanceAgentSession(ctx, transaction, userID, &session, params.StartedAt, idleMs, now); err != nil {
			return AgentSession{}, err
		}
	}

	return commitAgentSession(ctx, transaction, userID, params.SessionID)
}

// AgentHeartbeat records an activity signal. An unknown session id is created
// implicitly, so heartbeats surviving a lost start still produce a session.
// A heartbeat on a closed session reopens it with a new segment.
func (s *Store) AgentHeartbeat(ctx context.Context, userID, sessionID string, at, idleMs int64) (AgentSession, error) {
	if err := uuid.Validate(sessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, sessionID)
	}
	now := time.Now().UnixMilli()
	if at <= 0 {
		at = now
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentSession{}, err
	}
	defer transaction.Rollback()

	session, err := getAgentSession(ctx, transaction, userID, sessionID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.ensureAgentSessionIDFree(ctx, transaction, sessionID); err != nil {
			return AgentSession{}, err
		}
		session = AgentSession{
			ID: sessionID, Source: defaultAgentSource,
			Status: agentStatusActive, StartedAt: at, LastHeartbeatAt: at,
		}
		entryID, err := createAgentEntry(ctx, transaction, userID, session, at, now)
		if err != nil {
			return AgentSession{}, err
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO agent_sessions (id, user_id, source, status, started_at, last_heartbeat_at,
			                            time_entry_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, userID, session.Source, session.Status, session.StartedAt, session.LastHeartbeatAt,
			entryID, now, now)
		if err != nil {
			return AgentSession{}, err
		}
	case err != nil:
		return AgentSession{}, err
	default:
		if err := advanceAgentSession(ctx, transaction, userID, &session, at, idleMs, now); err != nil {
			return AgentSession{}, err
		}
	}

	return commitAgentSession(ctx, transaction, userID, sessionID)
}

// StopAgentSession closes the session and its running entry. Stopping an already
// closed session is a no-op. The end is trimmed to the last heartbeat when the
// stop arrives after more than the idle threshold of silence, so a stop delivered
// late (e.g. from the offline queue) never inflates the duration.
func (s *Store) StopAgentSession(ctx context.Context, userID, sessionID string, endedAt int64, reason string, idleMs int64) (AgentSession, error) {
	if err := uuid.Validate(sessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, sessionID)
	}
	now := time.Now().UnixMilli()
	if endedAt <= 0 {
		endedAt = now
	}
	reason = truncateRunes(reason, maxAgentShortLength)
	if reason == "" {
		reason = "other"
	}

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentSession{}, err
	}
	defer transaction.Rollback()

	session, err := getAgentSession(ctx, transaction, userID, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	if err != nil {
		return AgentSession{}, err
	}
	if session.Status == agentStatusClosed {
		return session, nil
	}

	effectiveEnd := endedAt
	if endedAt < session.LastHeartbeatAt || endedAt-session.LastHeartbeatAt > idleMs {
		effectiveEnd = session.LastHeartbeatAt
	}
	if err := closeAgentEntry(ctx, transaction, userID, session.TimeEntryID, effectiveEnd, now); err != nil {
		return AgentSession{}, err
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET status = ?, ended_at = ?, end_reason = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		agentStatusClosed, effectiveEnd, reason, now, sessionID, userID)
	if err != nil {
		return AgentSession{}, err
	}

	return commitAgentSession(ctx, transaction, userID, sessionID)
}

// ReconcileAgentSessions closes every active session (across all users) whose
// last heartbeat is older than the grace period, at the last heartbeat. This is
// the layer that survives SIGKILL, OOM and network loss on the agent side.
func (s *Store) ReconcileAgentSessions(ctx context.Context, now, graceMs int64) (int, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback()

	type staleSession struct {
		id              string
		userID          string
		timeEntryID     *string
		lastHeartbeatAt int64
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, user_id, time_entry_id, last_heartbeat_at
		FROM agent_sessions WHERE status = ? AND last_heartbeat_at < ?`,
		agentStatusActive, now-graceMs)
	if err != nil {
		return 0, err
	}
	stale := []staleSession{}
	for rows.Next() {
		var session staleSession
		if err := rows.Scan(&session.id, &session.userID, &session.timeEntryID, &session.lastHeartbeatAt); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, session)
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}

	for _, session := range stale {
		if err := closeAgentEntry(ctx, transaction, session.userID, session.timeEntryID, session.lastHeartbeatAt, now); err != nil {
			return 0, err
		}
		_, err := transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET status = ?, ended_at = last_heartbeat_at, end_reason = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			agentStatusClosed, AgentEndReasonStale, now, session.id, session.userID)
		if err != nil {
			return 0, err
		}
	}

	if err := transaction.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

// GetAgentSession returns a single session owned by the user.
func (s *Store) GetAgentSession(ctx context.Context, userID, sessionID string) (AgentSession, error) {
	session, err := scanAgentSession(s.db.QueryRowContext(ctx, `
		SELECT id, project_id, source, status, started_at, last_heartbeat_at, ended_at, end_reason,
		       cwd, git_branch, model, time_entry_id
		FROM agent_sessions WHERE id = ? AND user_id = ?`, sessionID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	return session, err
}

// --- internals ---

// advanceAgentSession applies an activity signal at the given moment: it reopens
// a closed session, splits the segment after an idle gap, or just advances the
// heartbeat watermark. Out-of-order signals (at < watermark) only touch metadata.
func advanceAgentSession(ctx context.Context, transaction *sql.Tx, userID string, session *AgentSession, at, idleMs, now int64) error {
	switch {
	case session.Status == agentStatusClosed:
		entryID, err := createAgentEntry(ctx, transaction, userID, *session, at, now)
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET status = ?, ended_at = NULL, end_reason = NULL,
			       time_entry_id = ?, last_heartbeat_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			agentStatusActive, entryID, at, now, session.ID, userID)
		return err
	case at-session.LastHeartbeatAt > idleMs:
		if err := closeAgentEntry(ctx, transaction, userID, session.TimeEntryID, session.LastHeartbeatAt, now); err != nil {
			return err
		}
		entryID, err := createAgentEntry(ctx, transaction, userID, *session, at, now)
		if err != nil {
			return err
		}
		_, err = transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET time_entry_id = ?, last_heartbeat_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			entryID, at, now, session.ID, userID)
		return err
	case at > session.LastHeartbeatAt:
		_, err := transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET last_heartbeat_at = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			at, now, session.ID, userID)
		return err
	default:
		return nil
	}
}

// createAgentEntry opens a new running time entry (a segment) for the session
// and returns its id. The entry gets a server_seq so clients pull it as usual.
func createAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, session AgentSession, startedAt, now int64) (string, error) {
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return "", err
	}
	entryID, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO time_entries (id, user_id, project_id, description, tags, started_at, stopped_at,
		                          created_at, updated_at, deleted_at, server_seq)
		VALUES (?, ?, ?, ?, '[]', ?, NULL, ?, ?, NULL, ?)`,
		entryID.String(), userID, session.ProjectID, agentEntryDescription(session), startedAt, now, now, seq)
	if err != nil {
		return "", err
	}
	return entryID.String(), nil
}

// closeAgentEntry stops the segment's entry, but only while it is still running
// and not deleted - a user who already stopped or removed the entry in the PWA
// wins over the agent flow. MAX() guards against a manually edited started_at.
func closeAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, entryID *string, stoppedAt, now int64) error {
	if entryID == nil {
		return nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE time_entries SET stopped_at = MAX(started_at, ?), updated_at = ?, server_seq = ?
		WHERE id = ? AND user_id = ? AND stopped_at IS NULL AND deleted_at IS NULL`,
		stoppedAt, now, seq, *entryID, userID)
	return err
}

func getAgentSession(ctx context.Context, transaction *sql.Tx, userID, sessionID string) (AgentSession, error) {
	return scanAgentSession(transaction.QueryRowContext(ctx, `
		SELECT id, project_id, source, status, started_at, last_heartbeat_at, ended_at, end_reason,
		       cwd, git_branch, model, time_entry_id
		FROM agent_sessions WHERE id = ? AND user_id = ?`, sessionID, userID))
}

func scanAgentSession(row *sql.Row) (AgentSession, error) {
	var session AgentSession
	err := row.Scan(&session.ID, &session.ProjectID, &session.Source, &session.Status,
		&session.StartedAt, &session.LastHeartbeatAt, &session.EndedAt, &session.EndReason,
		&session.Cwd, &session.GitBranch, &session.Model, &session.TimeEntryID)
	return session, err
}

// ensureAgentSessionIDFree rejects a session id already claimed by another user,
// so a leaked or guessed id can never attach entries to a foreign account.
func (s *Store) ensureAgentSessionIDFree(ctx context.Context, transaction *sql.Tx, sessionID string) error {
	var one int
	err := transaction.QueryRowContext(ctx,
		"SELECT 1 FROM agent_sessions WHERE id = ?", sessionID).Scan(&one)
	if err == nil {
		return fmt.Errorf("%w: session %s belongs to another user", ErrInvalidInput, sessionID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func validateAgentProject(ctx context.Context, transaction *sql.Tx, userID string, projectID *string) error {
	if projectID == nil {
		return nil
	}
	if err := uuid.Validate(*projectID); err != nil {
		return fmt.Errorf("%w: project id %q is not a UUID", ErrInvalidInput, *projectID)
	}
	var one int
	err := transaction.QueryRowContext(ctx,
		"SELECT 1 FROM projects WHERE id = ? AND user_id = ? AND deleted_at IS NULL", *projectID, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: project %s not found", ErrInvalidInput, *projectID)
	}
	return err
}

// commitAgentSession re-reads the session inside the transaction and commits.
func commitAgentSession(ctx context.Context, transaction *sql.Tx, userID, sessionID string) (AgentSession, error) {
	session, err := getAgentSession(ctx, transaction, userID, sessionID)
	if err != nil {
		return AgentSession{}, err
	}
	if err := transaction.Commit(); err != nil {
		return AgentSession{}, err
	}
	return session, nil
}

func agentEntryDescription(session AgentSession) string {
	base := session.Source
	if base == "" || base == defaultAgentSource {
		base = "Claude Code"
	}
	context := session.GitBranch
	if context == "" && session.Cwd != "" {
		cwd := strings.TrimRight(session.Cwd, "/\\")
		if separator := strings.LastIndexAny(cwd, "/\\"); separator >= 0 {
			cwd = cwd[separator+1:]
		}
		context = cwd
	}
	if context != "" {
		return base + " (" + context + ")"
	}
	return base
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
