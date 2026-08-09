package store

// Agent sessions implement crash-resilient time tracking for AI agents
// (Claude Code first). The client sends idempotent start/heartbeat/stop
// signals keyed by the agent's session id; the server materializes them
// into ordinary time_entries rows so they flow to clients over the normal
// sync pull path. A lost stop can never produce a wrong duration: the
// reconciliation job closes any session whose last heartbeat is older than
// a grace period, at the moment of that last heartbeat.
//
// A session owns exactly one time entry. A gap between signals longer than the
// idle threshold is added to that entry's paused_ms and subtracted from its
// duration, so idle time in the middle of a session is never billed and the
// session still reads as one row; started_at and stopped_at stay real
// timestamps, which is what the printed report, the CSV and the day boundaries
// are built on. Trailing idle is trimmed on stop and by reconciliation. Only a
// pause crossing the agent's local midnight, or one longer than the maximum
// pause, cuts the entry in two - both are about which day the work belongs to,
// not about idling.
//
// The entry is named after the tracker task the session belongs to, set
// explicitly through the set_agent_task MCP tool. Until the task is known the
// entry carries a short session tag, which keeps two concurrent sessions
// visibly different rows. Ownership of the row is tracked through server_seq:
// when it no longer matches what the session wrote last, the row was changed
// outside the agent flow and the session either adopts it (a live row, edited)
// or lets it go and opens a new one (deleted, or stopped by the user).

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

	maxAgentCwdLength       = 500
	maxAgentBranchLength    = 200
	maxAgentModelLength     = 100
	maxAgentShortLength     = 64
	maxAgentTaskTitleLength = 200

	agentSessionColumns = `id, project_id, source, status, started_at, last_heartbeat_at, ended_at, end_reason,
		cwd, git_branch, model, time_entry_id, task_key, task_title, entry_server_seq, entry_user_named,
		tz_offset_min, last_kind`

	// AgentKindToolStart marks the signal sent before a tool runs. It is the only
	// way the server can tell "a 20 minute Bash call" from "nobody was there":
	// PostToolUse and SubagentStop both fire after the gap has already happened.
	AgentKindToolStart = "tool_start"

	msPerDay = int64(24 * 60 * 60 * 1000)
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
	TaskKey         string  `json:"task_key"`
	TaskTitle       string  `json:"task_title"`
	// EntryServerSeq is the server_seq the session itself last wrote to its entry.
	// NULL means "ownership unknown" (a session from before the column existed).
	EntryServerSeq *int64 `json:"entry_server_seq"`
	// EntryUserNamed records that the entry's description was chosen by the user,
	// which stops the session from ever fixing the name again.
	EntryUserNamed bool `json:"entry_user_named"`
	// TZOffsetMin is the agent's UTC offset in minutes, nullable because 0 is a
	// valid offset (UTC) and "unknown" has to stay distinguishable from it.
	TZOffsetMin *int `json:"tz_offset_min"`
	// LastKind is the kind of the previous signal (see AgentKindToolStart).
	LastKind string `json:"last_kind"`
}

// AgentPolicy carries the thresholds an activity signal is judged against. It
// travels as one value so that adding a threshold never grows a parameter list.
type AgentPolicy struct {
	// IdleMs is the largest gap between signals still billed as continuous work;
	// a larger gap is not billed at all.
	IdleMs int64
	// ToolMaxMs caps how much of a gap that started with a tool call is still
	// billed. A ceiling, not a switch: a 45 minute gap after a tool start bills
	// the first 30 and pauses the rest, so a hung tool cannot bill forever and a
	// tool one minute over the limit does not lose everything.
	ToolMaxMs int64
	// MaxPauseMs is the pause after which the entry is cut in two even when the
	// timezone is unknown. Without it a night break would glue yesterday and
	// today into one row dated yesterday.
	MaxPauseMs int64
}

// AgentStart carries the payload of a start signal. Zero StartedAt means "now".
type AgentStart struct {
	SessionID   string
	StartedAt   int64
	Source      string
	Cwd         string
	GitBranch   string
	Model       string
	ProjectID   *string
	TZOffsetMin *int
}

// AgentSignal is one activity report (heartbeat or stop). The metadata fields are
// optional: they only fill values a lost start never delivered, and never
// overwrite what the session already knows.
type AgentSignal struct {
	At          int64
	Kind        string
	Cwd         string
	GitBranch   string
	Model       string
	TZOffsetMin *int
}

// StartAgentSession creates the session and its running time entry, or, when the
// same session id is replayed (--continue / --resume), refreshes metadata and
// treats the call as an activity signal. Replaying never duplicates entries.
func (s *Store) StartAgentSession(ctx context.Context, userID string, params AgentStart, policy AgentPolicy) (AgentSession, error) {
	if err := uuid.Validate(params.SessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, params.SessionID)
	}
	now := time.Now().UnixMilli()
	// Same bound as sanitizeAgentSignal: a start stamped in the future would open an
	// entry no later signal can advance and no reconciliation can close.
	if params.StartedAt <= 0 || params.StartedAt > now+maxAgentSkewMs {
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
		if err := ensureAgentSessionIDFree(ctx, transaction, params.SessionID); err != nil {
			return AgentSession{}, err
		}
		if err := validateAgentProject(ctx, transaction, userID, params.ProjectID); err != nil {
			return AgentSession{}, err
		}
		session = AgentSession{
			ID: params.SessionID, ProjectID: params.ProjectID, Source: params.Source,
			Status: agentStatusActive, StartedAt: params.StartedAt, LastHeartbeatAt: params.StartedAt,
			Cwd: params.Cwd, GitBranch: params.GitBranch, Model: params.Model,
			TZOffsetMin: validTZOffset(params.TZOffsetMin),
		}
		entry, err := createAgentEntry(ctx, transaction, userID, session, params.StartedAt, now)
		if err != nil {
			return AgentSession{}, err
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO agent_sessions (id, user_id, project_id, source, status, started_at, last_heartbeat_at,
			                            cwd, git_branch, model, tz_offset_min, time_entry_id, entry_server_seq,
			                            created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, userID, session.ProjectID, session.Source, session.Status,
			session.StartedAt, session.LastHeartbeatAt, session.Cwd, session.GitBranch, session.Model,
			session.TZOffsetMin, entry.id, entry.serverSeq, now, now)
		if err != nil {
			return AgentSession{}, err
		}
	case err != nil:
		return AgentSession{}, err
	default:
		// Replay: start is the authoritative source of metadata, so it overwrites.
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
		if offset := validTZOffset(params.TZOffsetMin); offset != nil {
			session.TZOffsetMin = offset
		}
		_, err = transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET project_id = ?, cwd = ?, git_branch = ?, model = ?, tz_offset_min = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			session.ProjectID, session.Cwd, session.GitBranch, session.Model, session.TZOffsetMin,
			now, session.ID, userID)
		if err != nil {
			return AgentSession{}, err
		}
		signal := AgentSignal{At: params.StartedAt, Kind: "start", TZOffsetMin: session.TZOffsetMin}
		if err := advanceAgentSession(ctx, transaction, userID, &session, signal, policy, now); err != nil {
			return AgentSession{}, err
		}
	}

	return commitAgentSession(ctx, transaction, userID, params.SessionID)
}

// AgentHeartbeat records an activity signal. An unknown session id is created
// implicitly, so heartbeats surviving a lost start still produce a session.
// A heartbeat on a closed session revives it.
func (s *Store) AgentHeartbeat(ctx context.Context, userID, sessionID string, signal AgentSignal, policy AgentPolicy) (AgentSession, error) {
	if err := uuid.Validate(sessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, sessionID)
	}
	now := time.Now().UnixMilli()
	signal = sanitizeAgentSignal(signal, now)

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentSession{}, err
	}
	defer transaction.Rollback()

	session, err := getAgentSession(ctx, transaction, userID, sessionID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := ensureAgentSessionIDFree(ctx, transaction, sessionID); err != nil {
			return AgentSession{}, err
		}
		session = AgentSession{
			ID: sessionID, Source: defaultAgentSource,
			Status: agentStatusActive, StartedAt: signal.At, LastHeartbeatAt: signal.At,
			Cwd: signal.Cwd, GitBranch: signal.GitBranch, Model: signal.Model,
			TZOffsetMin: signal.TZOffsetMin, LastKind: signal.Kind,
		}
		entry, err := createAgentEntry(ctx, transaction, userID, session, signal.At, now)
		if err != nil {
			return AgentSession{}, err
		}
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO agent_sessions (id, user_id, source, status, started_at, last_heartbeat_at,
			                            cwd, git_branch, model, tz_offset_min, last_kind,
			                            time_entry_id, entry_server_seq, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, userID, session.Source, session.Status, session.StartedAt, session.LastHeartbeatAt,
			session.Cwd, session.GitBranch, session.Model, session.TZOffsetMin, session.LastKind,
			entry.id, entry.serverSeq, now, now)
		if err != nil {
			return AgentSession{}, err
		}
	case err != nil:
		return AgentSession{}, err
	default:
		if err := applyAgentMetadata(ctx, transaction, userID, &session, signal, now); err != nil {
			return AgentSession{}, err
		}
		if err := advanceAgentSession(ctx, transaction, userID, &session, signal, policy, now); err != nil {
			return AgentSession{}, err
		}
	}

	return commitAgentSession(ctx, transaction, userID, sessionID)
}

// StopAgentSession closes the session and its running entry. Stopping an already
// closed session is a no-op. When the stop arrives after more than the idle threshold
// of silence the end is trimmed back to the last heartbeat, so a stop delivered late
// (e.g. from the offline queue) cannot inflate the duration - except that silence which
// began with a tool_start is billed up to ToolMaxMs, because a running tool is the one
// gap that is known to be work. See trailingEnd, which reconciliation shares.
func (s *Store) StopAgentSession(ctx context.Context, userID, sessionID, reason string, signal AgentSignal, policy AgentPolicy) (AgentSession, error) {
	if err := uuid.Validate(sessionID); err != nil {
		return AgentSession{}, fmt.Errorf("%w: session id %q is not a UUID", ErrInvalidInput, sessionID)
	}
	now := time.Now().UnixMilli()
	signal = sanitizeAgentSignal(signal, now)
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
	if err := applyAgentMetadata(ctx, transaction, userID, &session, signal, now); err != nil {
		return AgentSession{}, err
	}

	effectiveEnd := signal.At
	if signal.At < session.LastHeartbeatAt || signal.At-session.LastHeartbeatAt > policy.IdleMs {
		effectiveEnd = trailingEnd(session.LastHeartbeatAt, signal.At, session.LastKind, policy)
	}
	seq, closed, err := closeAgentEntry(ctx, transaction, userID, session.TimeEntryID, effectiveEnd, now)
	if err != nil {
		return AgentSession{}, err
	}
	if closed {
		session.EntryServerSeq = &seq
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET status = ?, ended_at = ?, end_reason = ?, entry_server_seq = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		agentStatusClosed, effectiveEnd, reason, session.EntryServerSeq, now, sessionID, userID)
	if err != nil {
		return AgentSession{}, err
	}

	return commitAgentSession(ctx, transaction, userID, sessionID)
}

// ReconcileAgentSessions closes every active session (across all users) whose last
// heartbeat is older than the grace period. This is the layer that survives SIGKILL,
// OOM and network loss on the agent side.
//
// The end is the last heartbeat, with the same tool_start allowance StopAgentSession
// makes - a session killed mid-tool must not be worth less than one that managed to
// send its stop.
func (s *Store) ReconcileAgentSessions(ctx context.Context, now, graceMs int64, policy AgentPolicy) (int, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback()

	type staleSession struct {
		id              string
		userID          string
		timeEntryID     *string
		entryServerSeq  *int64
		lastHeartbeatAt int64
		lastKind        string
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, user_id, time_entry_id, entry_server_seq, last_heartbeat_at, last_kind
		FROM agent_sessions WHERE status = ? AND last_heartbeat_at < ?`,
		agentStatusActive, now-graceMs)
	if err != nil {
		return 0, err
	}
	stale := []staleSession{}
	for rows.Next() {
		var session staleSession
		if err := rows.Scan(&session.id, &session.userID, &session.timeEntryID,
			&session.entryServerSeq, &session.lastHeartbeatAt, &session.lastKind); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, session)
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}

	for _, session := range stale {
		endedAt := trailingEnd(session.lastHeartbeatAt, now, session.lastKind, policy)
		seq, closed, err := closeAgentEntry(ctx, transaction, session.userID, session.timeEntryID, endedAt, now)
		if err != nil {
			return 0, err
		}
		if closed {
			session.entryServerSeq = &seq
		}
		_, err = transaction.ExecContext(ctx, `
			UPDATE agent_sessions SET status = ?, ended_at = ?, end_reason = ?,
			       entry_server_seq = ?, updated_at = ?
			WHERE id = ? AND user_id = ?`,
			agentStatusClosed, endedAt, AgentEndReasonStale, session.entryServerSeq, now, session.id, session.userID)
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
	session, err := scanAgentSession(s.db.QueryRowContext(ctx,
		"SELECT "+agentSessionColumns+" FROM agent_sessions WHERE id = ? AND user_id = ?", sessionID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentSession{}, ErrNotFound
	}
	return session, err
}

// LatestAgentTZOffset returns the UTC offset of the user's most recent agent session,
// or nil when no session ever reported one. It is the only record this server keeps of
// what "today" means to the user, which is what the MCP report falls back to when the
// caller does not say.
func (s *Store) LatestAgentTZOffset(ctx context.Context, userID string) (*int, error) {
	var offset *int
	err := s.db.QueryRowContext(ctx, `
		SELECT tz_offset_min FROM agent_sessions
		WHERE user_id = ? AND tz_offset_min IS NOT NULL
		ORDER BY started_at DESC LIMIT 1`, userID).Scan(&offset)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return offset, err
}

// AgentTaskSelector picks the session a task is attached to. An explicit session
// id wins; otherwise the only active session is used, or the only active session
// with a matching working directory. Attaching the wrong session silently is
// worse than asking, so anything else is an error listing the candidates.
type AgentTaskSelector struct {
	SessionID string
	Cwd       string
}

type AgentTaskResult struct {
	Session        AgentSession
	RenamedEntries int
}

// SetAgentTask names the session after a tracker task and renames every entry the
// session has produced - not just the current one, because a session split by an
// idle gap would otherwise leave half its work under the technical name. Entries
// the user renamed by hand keep their name. Calling it again with the same key
// changes nothing; with a different key it renames (the task can be corrected).
func (s *Store) SetAgentTask(ctx context.Context, userID string, selector AgentTaskSelector, taskKey, taskTitle string) (AgentTaskResult, error) {
	taskKey = strings.TrimSpace(truncateRunes(taskKey, maxAgentShortLength))
	taskTitle = strings.TrimSpace(truncateRunes(taskTitle, maxAgentTaskTitleLength))
	if taskKey == "" {
		return AgentTaskResult{}, fmt.Errorf("%w: task_key is required", ErrInvalidInput)
	}
	now := time.Now().UnixMilli()

	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentTaskResult{}, err
	}
	defer transaction.Rollback()

	session, err := selectAgentSession(ctx, transaction, userID, selector)
	if err != nil {
		return AgentTaskResult{}, err
	}
	if session.TaskKey == taskKey && session.TaskTitle == taskTitle {
		if err := transaction.Commit(); err != nil {
			return AgentTaskResult{}, err
		}
		return AgentTaskResult{Session: session}, nil
	}

	previousName := agentEntryDescription(session)
	session.TaskKey = taskKey
	session.TaskTitle = taskTitle
	newName := agentEntryDescription(session)

	renamed, lastSeq, err := renameSessionEntries(ctx, transaction, userID, session, previousName, newName, now)
	if err != nil {
		return AgentTaskResult{}, err
	}
	if lastSeq != nil {
		session.EntryServerSeq = lastSeq
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET task_key = ?, task_title = ?, entry_server_seq = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		session.TaskKey, session.TaskTitle, session.EntryServerSeq, now, session.ID, userID)
	if err != nil {
		return AgentTaskResult{}, err
	}

	stored, err := commitAgentSession(ctx, transaction, userID, session.ID)
	if err != nil {
		return AgentTaskResult{}, err
	}
	return AgentTaskResult{Session: stored, RenamedEntries: renamed}, nil
}

// --- internals ---

// agentEntry is the session's current entry as a signal handler sees it.
type agentEntry struct {
	id          string
	description string
	stoppedAt   *int64
	serverSeq   int64
	userNamed   bool
}

// advanceAgentSession applies an activity signal at the given moment: it revives
// a closed session, subtracts an idle gap from the entry instead of cutting it,
// adopts or replaces an entry that changed outside the agent flow, and always
// leaves the session active with its watermark moved forward. Every branch runs
// through the same tail, so the session can never end up closed while still
// holding a running entry - the reconciliation job only looks at active sessions
// and nothing else would ever close that row.
func advanceAgentSession(ctx context.Context, transaction *sql.Tx, userID string, session *AgentSession, signal AgentSignal, policy AgentPolicy, now int64) error {
	at := signal.At
	// The last moment already paid for. Measuring the gap from the watermark alone
	// would count the tail of a normally stopped session twice: after a stop the
	// entry is already billed up to ended_at.
	mark := session.LastHeartbeatAt
	if session.EndedAt != nil && *session.EndedAt > mark {
		mark = *session.EndedAt
	}
	// A stale signal (offline queue replay, an async heartbeat overtaken by the
	// synchronous stop) only refreshes metadata: reviving a session at a moment
	// already billed would open a second running entry out of thin air.
	if at <= mark {
		return nil
	}

	current, err := resolveAgentEntry(ctx, transaction, userID, session, now)
	if err != nil {
		return err
	}

	opened := false
	if current != nil && at-mark > policy.IdleMs {
		// A gap that started with a tool call is work up to the cap: PostToolUse
		// only fires once the tool is done, so a long Bash or Task looks exactly
		// like an empty chair from here.
		billable := int64(0)
		if session.LastKind == AgentKindToolStart {
			billable = min(at-mark, policy.ToolMaxMs)
		}
		workedUntil := mark + billable
		if idle := at - workedUntil; idle > 0 {
			switch {
			case crossesLocalMidnight(workedUntil, at, session.TZOffsetMin) || idle > policy.MaxPauseMs:
				// A pause across the local midnight has to cut: reports put an entry
				// on the day it started, so gluing them would move this morning's work
				// into yesterday and inflate that day's total.
				if _, _, err := closeAgentEntry(ctx, transaction, userID, &current.id, workedUntil, now); err != nil {
					return err
				}
				current, err = createAgentEntry(ctx, transaction, userID, *session, at, now)
				if err != nil {
					return err
				}
				opened = true
			default:
				if err := addAgentPause(ctx, transaction, userID, current, idle, now); err != nil {
					return err
				}
			}
		}
	}
	if current == nil {
		current, err = createAgentEntry(ctx, transaction, userID, *session, at, now)
		if err != nil {
			return err
		}
		opened = true
	}
	if !opened {
		// The session was stopped and is now active again: it continues writing into
		// the row it already owns rather than leaving a second one behind.
		if err := reopenAgentEntry(ctx, transaction, userID, current, now); err != nil {
			return err
		}
	}
	if opened && signal.TZOffsetMin != nil {
		// The offset is fixed when the entry opens and not touched again while it
		// runs: a flight or a DST switch mid-entry would otherwise redefine which
		// day the pause crossed.
		session.TZOffsetMin = signal.TZOffsetMin
	}
	if err := renameAgentEntry(ctx, transaction, userID, *session, current, policy, now); err != nil {
		return err
	}

	if at > session.LastHeartbeatAt {
		session.LastHeartbeatAt = at
	}
	session.Status = agentStatusActive
	session.EndedAt = nil
	session.EndReason = nil
	session.TimeEntryID = &current.id
	session.EntryServerSeq = &current.serverSeq
	session.EntryUserNamed = current.userNamed
	session.LastKind = truncateRunes(signal.Kind, maxAgentShortLength)
	_, err = transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET status = ?, ended_at = NULL, end_reason = NULL,
		       time_entry_id = ?, entry_server_seq = ?, entry_user_named = ?,
		       last_heartbeat_at = ?, last_kind = ?, tz_offset_min = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		agentStatusActive, current.id, current.serverSeq, current.userNamed,
		session.LastHeartbeatAt, session.LastKind, session.TZOffsetMin, now, session.ID, userID)
	return err
}

// crossesLocalMidnight reports whether the interval spans a local calendar day
// boundary. An unknown offset never counts as a crossing: guessing UTC would cut
// entries at the wrong moment for most of the planet, and MaxPauseMs is the
// timezone-independent guard for that case.
func crossesLocalMidnight(fromMs, toMs int64, offsetMin *int) bool {
	if offsetMin == nil {
		return false
	}
	shift := int64(*offsetMin) * 60_000
	return floorDiv(fromMs+shift, msPerDay) != floorDiv(toMs+shift, msPerDay)
}

func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}

// validTZOffset drops values outside the range real timezones live in, so a
// broken hook cannot move an entry to a different day.
func validTZOffset(offsetMin *int) *int {
	if offsetMin == nil || *offsetMin < -12*60 || *offsetMin > 14*60 {
		return nil
	}
	return offsetMin
}

// addAgentPause records idle time inside the entry instead of cutting the entry
// in two. The guard on server_seq keeps a row someone else has just changed from
// being written blind; the caller has already checked ownership.
func addAgentPause(ctx context.Context, transaction *sql.Tx, userID string, entry *agentEntry, pauseMs, now int64) error {
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries SET paused_ms = paused_ms + ?, updated_at = ?, server_seq = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND server_seq = ?`,
		pauseMs, now, seq, entry.id, userID, entry.serverSeq)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		entry.serverSeq = seq
	}
	return nil
}

// resolveAgentEntry decides what happens to the row the session points at.
// Owning it (server_seq is still the one the session wrote) keeps it as is. A
// changed row is adopted while it is alive - a project or tag edit must not cost
// the session its entry - and only the right to fix the name is given up. A row
// the user deleted or stopped is let go: continuing to write into a stopped row
// would silently lose the rest of the session. Returns nil when the session has
// to open a new entry.
func resolveAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, session *AgentSession, now int64) (*agentEntry, error) {
	if session.TimeEntryID == nil {
		return nil, nil
	}
	var (
		description string
		stoppedAt   *int64
		deletedAt   *int64
		serverSeq   int64
	)
	err := transaction.QueryRowContext(ctx, `
		SELECT description, stopped_at, deleted_at, server_seq FROM time_entries
		WHERE id = ? AND user_id = ?`, *session.TimeEntryID, userID).
		Scan(&description, &stoppedAt, &deletedAt, &serverSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if deletedAt != nil {
		// A tombstone is never resurrected, and closing it would only rewrite it.
		return nil, nil
	}
	// An unknown server_seq (a session predating the column) counts as ownership
	// confirmed, otherwise the deploy would abandon every live session's entry.
	owns := session.EntryServerSeq == nil || *session.EntryServerSeq == serverSeq
	if owns && session.EntryServerSeq != nil {
		return &agentEntry{
			id: *session.TimeEntryID, description: description, stoppedAt: stoppedAt,
			serverSeq: serverSeq, userNamed: session.EntryUserNamed,
		}, nil
	}
	if !owns && stoppedAt != nil {
		// Stopped by the user: detaching is what keeps the rest of the session
		// tracked at all, and the row is already closed.
		return nil, detachAgentEntry(ctx, transaction, userID, session, now)
	}
	// Adopted: whether the name is now the user's is decided by the name itself,
	// so changing a project or a tag never permanently blocks renaming.
	return &agentEntry{
		id: *session.TimeEntryID, description: description, stoppedAt: stoppedAt,
		serverSeq: serverSeq, userNamed: description != agentEntryDescription(*session),
	}, nil
}

// detachAgentEntry is the guard on letting an entry go: a row that is somehow
// still running is closed at the session's last activity first. Nothing closes
// it afterwards - reconciliation walks sessions, and the session is about to
// point somewhere else - so an orphan here would run forever. Deleted and
// already stopped rows are left exactly as they are.
func detachAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, session *AgentSession, now int64) error {
	_, _, err := closeAgentEntry(ctx, transaction, userID, session.TimeEntryID, session.LastHeartbeatAt, now)
	return err
}

// createAgentEntry opens a new running time entry (a segment) for the session.
// The entry gets a server_seq so clients pull it as usual, and agent_session_id
// so a later set_agent_task can find it.
func createAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, session AgentSession, startedAt, now int64) (*agentEntry, error) {
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return nil, err
	}
	entryID, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	description := agentEntryDescription(session)
	_, err = transaction.ExecContext(ctx, `
		INSERT INTO time_entries (id, user_id, project_id, description, tags, started_at, stopped_at,
		                          created_at, updated_at, deleted_at, server_seq, agent_session_id)
		VALUES (?, ?, ?, ?, '[]', ?, NULL, ?, ?, NULL, ?, ?)`,
		entryID.String(), userID, session.ProjectID, description, startedAt, now, now, seq, session.ID)
	if err != nil {
		return nil, err
	}
	return &agentEntry{id: entryID.String(), description: description, serverSeq: seq}, nil
}

// closeAgentEntry stops the segment's entry, but only while it is still running
// and not deleted - a user who already stopped or removed the entry in the PWA
// wins over the agent flow. MAX() guards against a manually edited started_at.
// The reported flag says whether a row was actually written, which is what makes
// the returned seq usable as the session's ownership marker.
func closeAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, entryID *string, stoppedAt, now int64) (int64, bool, error) {
	if entryID == nil {
		return 0, false, nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return 0, false, err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries SET stopped_at = MAX(started_at, ?), updated_at = ?, server_seq = ?
		WHERE id = ? AND user_id = ? AND stopped_at IS NULL AND deleted_at IS NULL`,
		stoppedAt, now, seq, *entryID, userID)
	if err != nil {
		return 0, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	return seq, affected > 0, nil
}

// reopenAgentEntry lets a revived session continue writing into the row it
// already owns instead of opening a second one for the same work.
func reopenAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, entry *agentEntry, now int64) error {
	if entry.stoppedAt == nil {
		return nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries SET stopped_at = NULL, updated_at = ?, server_seq = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND server_seq = ?`,
		now, seq, entry.id, userID, entry.serverSeq)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		entry.stoppedAt = nil
		entry.serverSeq = seq
	}
	return nil
}

// renameAgentEntry fixes the entry's name when, and only when, it actually
// changes: every write bumps server_seq and ships the row to every client, and
// signals arrive far more often than names change. Entries closed long ago are
// left alone - silently rewriting yesterday's row is not a rename anyone asked for.
func renameAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, session AgentSession, entry *agentEntry, policy AgentPolicy, now int64) error {
	label := agentEntryDescription(session)
	if entry.userNamed || label == entry.description {
		return nil
	}
	if entry.stoppedAt != nil && *entry.stoppedAt < now-policy.IdleMs {
		return nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries SET description = ?, updated_at = ?, server_seq = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND server_seq = ?`,
		label, now, seq, entry.id, userID, entry.serverSeq)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		entry.description = label
		entry.serverSeq = seq
	}
	return nil
}

// renameSessionEntries applies a new task name to every entry the session
// produced. Only rows still carrying the previous automatic name are touched;
// anything the user renamed keeps their wording.
func renameSessionEntries(ctx context.Context, transaction *sql.Tx, userID string, session AgentSession, previousName, newName string, now int64) (int, *int64, error) {
	if previousName == newName {
		return 0, nil, nil
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, description FROM time_entries
		WHERE user_id = ? AND agent_session_id = ? AND deleted_at IS NULL
		ORDER BY started_at`, userID, session.ID)
	if err != nil {
		return 0, nil, err
	}
	targets := []string{}
	for rows.Next() {
		var entryID, description string
		if err := rows.Scan(&entryID, &description); err != nil {
			rows.Close()
			return 0, nil, err
		}
		if description != previousName {
			continue
		}
		if session.TimeEntryID != nil && *session.TimeEntryID == entryID && session.EntryUserNamed {
			continue
		}
		targets = append(targets, entryID)
	}
	if err := closeRows(rows); err != nil {
		return 0, nil, err
	}
	if len(targets) == 0 {
		return 0, nil, nil
	}

	seq, err := allocateServerSeq(transaction, len(targets))
	if err != nil {
		return 0, nil, err
	}
	var currentSeq *int64
	for _, entryID := range targets {
		if _, err := transaction.ExecContext(ctx, `
			UPDATE time_entries SET description = ?, updated_at = ?, server_seq = ?
			WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
			newName, now, seq, entryID, userID); err != nil {
			return 0, nil, err
		}
		if session.TimeEntryID != nil && *session.TimeEntryID == entryID {
			written := seq
			currentSeq = &written
		}
		seq++
	}
	return len(targets), currentSeq, nil
}

// selectAgentSession resolves the session a task applies to, or explains why it
// cannot: guessing between two live sessions would attach the work to the wrong one.
func selectAgentSession(ctx context.Context, transaction *sql.Tx, userID string, selector AgentTaskSelector) (AgentSession, error) {
	if selector.SessionID != "" {
		session, err := getAgentSession(ctx, transaction, userID, selector.SessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return AgentSession{}, ErrNotFound
		}
		return session, err
	}

	rows, err := transaction.QueryContext(ctx,
		"SELECT "+agentSessionColumns+` FROM agent_sessions
		 WHERE user_id = ? AND status = ? ORDER BY started_at DESC`, userID, agentStatusActive)
	if err != nil {
		return AgentSession{}, err
	}
	candidates := []AgentSession{}
	for rows.Next() {
		session, err := scanAgentSession(rows)
		if err != nil {
			rows.Close()
			return AgentSession{}, err
		}
		candidates = append(candidates, session)
	}
	if err := closeRows(rows); err != nil {
		return AgentSession{}, err
	}

	if len(candidates) == 0 {
		return AgentSession{}, fmt.Errorf("%w: no active agent session; pass session_id", ErrInvalidInput)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if selector.Cwd != "" {
		matched := []AgentSession{}
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.Cwd, selector.Cwd) {
				matched = append(matched, candidate)
			}
		}
		if len(matched) == 1 {
			return matched[0], nil
		}
		if len(matched) > 1 {
			candidates = matched
		}
	}
	listed := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		listed = append(listed, fmt.Sprintf("%s (cwd %q, started %s)",
			candidate.ID, candidate.Cwd, time.UnixMilli(candidate.StartedAt).UTC().Format(time.RFC3339)))
	}
	return AgentSession{}, fmt.Errorf("%w: %d active agent sessions, pass session_id: %s",
		ErrInvalidInput, len(candidates), strings.Join(listed, "; "))
}

// applyAgentMetadata fills session fields a lost start never delivered. Heartbeat
// and stop are secondary sources: they never overwrite a known value.
func applyAgentMetadata(ctx context.Context, transaction *sql.Tx, userID string, session *AgentSession, signal AgentSignal, now int64) error {
	changed := false
	if session.Cwd == "" && signal.Cwd != "" {
		session.Cwd = signal.Cwd
		changed = true
	}
	if session.GitBranch == "" && signal.GitBranch != "" {
		session.GitBranch = signal.GitBranch
		changed = true
	}
	if session.Model == "" && signal.Model != "" {
		session.Model = signal.Model
		changed = true
	}
	if session.TZOffsetMin == nil && signal.TZOffsetMin != nil {
		session.TZOffsetMin = signal.TZOffsetMin
		changed = true
	}
	if !changed {
		return nil
	}
	_, err := transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET cwd = ?, git_branch = ?, model = ?, tz_offset_min = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		session.Cwd, session.GitBranch, session.Model, session.TZOffsetMin, now, session.ID, userID)
	return err
}

// maxAgentSkewMs bounds how far ahead of the server an agent's clock may be. A signal
// past it is not merely odd: it moves the session watermark into the future, and from
// then on every real signal is discarded as stale while reconciliation - which selects
// on last_heartbeat_at being old - can never pick the session up. The running entry
// would then stay open forever with nothing able to close it.
const maxAgentSkewMs = int64(24 * 60 * 60 * 1000)

// trailingEnd decides where an entry ends when the session goes quiet for longer than
// the idle threshold. The silence is not billed, because nothing proves anyone was
// there - except when it opened with a tool call: PostToolUse fires only once the tool
// returns, so a long Bash or Task looks exactly like an empty chair from here, and its
// run is billed up to the cap.
//
// Both ways a session can end share this, or the same twenty-minute test run would be
// worth twenty minutes when SessionEnd arrives and zero when the process was killed and
// reconciliation closed the session instead.
func trailingEnd(lastHeartbeatAt, silentUntil int64, lastKind string, policy AgentPolicy) int64 {
	if lastKind != AgentKindToolStart || silentUntil <= lastHeartbeatAt {
		return lastHeartbeatAt
	}
	return lastHeartbeatAt + min(silentUntil-lastHeartbeatAt, policy.ToolMaxMs)
}

func sanitizeAgentSignal(signal AgentSignal, now int64) AgentSignal {
	if signal.At <= 0 || signal.At > now+maxAgentSkewMs {
		signal.At = now
	}
	signal.Kind = truncateRunes(signal.Kind, maxAgentShortLength)
	signal.Cwd = truncateRunes(signal.Cwd, maxAgentCwdLength)
	signal.GitBranch = truncateRunes(signal.GitBranch, maxAgentBranchLength)
	signal.Model = truncateRunes(signal.Model, maxAgentModelLength)
	signal.TZOffsetMin = validTZOffset(signal.TZOffsetMin)
	return signal
}

func getAgentSession(ctx context.Context, transaction *sql.Tx, userID, sessionID string) (AgentSession, error) {
	return scanAgentSession(transaction.QueryRowContext(ctx,
		"SELECT "+agentSessionColumns+" FROM agent_sessions WHERE id = ? AND user_id = ?", sessionID, userID))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAgentSession(row rowScanner) (AgentSession, error) {
	var session AgentSession
	err := row.Scan(&session.ID, &session.ProjectID, &session.Source, &session.Status,
		&session.StartedAt, &session.LastHeartbeatAt, &session.EndedAt, &session.EndReason,
		&session.Cwd, &session.GitBranch, &session.Model, &session.TimeEntryID,
		&session.TaskKey, &session.TaskTitle, &session.EntryServerSeq, &session.EntryUserNamed,
		&session.TZOffsetMin, &session.LastKind)
	return session, err
}

// ensureAgentSessionIDFree rejects a session id already claimed by another user,
// so a leaked or guessed id can never attach entries to a foreign account.
func ensureAgentSessionIDFree(ctx context.Context, transaction *sql.Tx, sessionID string) error {
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

// agentEntryDescription names an agent entry after the task it belongs to.
// Until the task is known the entry carries a short, stable session tag, so two
// concurrent Claude Code sessions never look like one row and never collide.
// Nothing here depends on the working directory or the git branch: both are
// unknown when a lost start makes a heartbeat create the session, and the branch
// changes inside a session, which is exactly how one session used to end up
// under two different names.
func agentEntryDescription(session AgentSession) string {
	if session.TaskKey != "" {
		if session.TaskTitle != "" {
			return session.TaskKey + " " + session.TaskTitle
		}
		return session.TaskKey
	}
	base := session.Source
	if base == "" || base == defaultAgentSource {
		base = "Claude Code"
	}
	return base + " #" + AgentSessionTag(session.ID)
}

// AgentSessionTag is the short form of a session id used in entry names and in
// the UI: the first eight hex characters of the UUID.
func AgentSessionTag(sessionID string) string {
	tag := strings.ToLower(strings.ReplaceAll(sessionID, "-", ""))
	if len(tag) > 8 {
		tag = tag[:8]
	}
	return tag
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
