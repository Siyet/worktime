package store

// Agent sessions implement crash-resilient time tracking for AI agents
// (Claude Code first). The client sends idempotent start/heartbeat/stop
// signals keyed by the agent's session id; the server materializes them
// into ordinary time_entries rows so they flow to clients over the normal
// sync pull path. A lost stop can never produce a wrong duration: the
// reconciliation job closes any session whose last heartbeat is older than
// a grace period, at the moment of that last heartbeat.
//
// A session may produce several time entries. A gap between signals longer than
// the idle threshold closes the current entry at the last billable activity and
// opens a new one at the next signal. Every row therefore represents one
// continuous work segment, and an idle gap can never turn into a multi-day row.
// Trailing idle is trimmed on stop and by reconciliation.
//
// Every write here stamps updated_at as MAX(updated_at + 1, now). That column is the
// version key last-write-wins compares, and it carries the *browser's* clock on rows
// the PWA has touched while this process carries the server's. A browser running even a
// minute ahead would otherwise make the agent's stop look older than the row it closes:
// the client would discard it on merge, show the timer running forever, and its next
// edit would push stopped_at back to null and un-stop it on the server too. Stepping
// past the stored value keeps the agent's writes newest without trusting either clock.
//
// The entry is named after the tracker task the session belongs to, set
// explicitly through the set_agent_task MCP tool. Until the task is known the
// entry carries a short session tag, which keeps two concurrent sessions
// visibly different rows. Ownership of the row is tracked through server_seq:
// when it no longer matches what the session wrote last, the row was changed
// outside the agent flow and the session either adopts it (a live row, edited)
// or lets it go and opens a new one (deleted, or stopped by the user). A row a
// sync write sets running again is settled in the same transaction (see
// settleRestoredAgentEntry): the session takes it back or it is closed again,
// so a let-go row can never run unowned.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"sort"
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
		entry_user_edited, tz_offset_min, last_kind`

	// AgentKindToolStart marks the signal sent before a tool runs. It is the only
	// way the server can tell "a 20 minute Bash call" from "nobody was there":
	// PostToolUse and SubagentStop both fire after the gap has already happened.
	AgentKindToolStart = "tool_start"

	// formatDurationShort rounds to the nearest minute, so anything below this
	// boundary is rendered as 0m. Only untouched, unassigned technical entries
	// are discarded at that boundary; real user entries keep sub-minute precision.
	agentZeroMinuteMs = int64(30 * 1000)
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
	// EntryUserEdited records any outside write without blocking task renames.
	// It protects deliberate short entries from automatic technical cleanup.
	EntryUserEdited bool `json:"entry_user_edited"`
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
	// the first 30 and starts a new segment after the rest, so a hung tool cannot
	// bill forever and a tool one minute over the limit does not lose everything.
	ToolMaxMs int64
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

// StartAgentSession creates the session, or, when the same session id is
// replayed (--continue / --resume), refreshes metadata and treats the call as an
// activity signal. Replaying never duplicates entries. The time entry is not
// opened here: it waits for the first activity signal and then covers the
// session from this moment, so a launch that never works leaves no row.
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
		// The entry is not opened here. A start only says a process exists, and
		// the agent binary is launched far more often than it is worked in: on
		// the first machine to measure it, 222 of 249 sessions in a week reported
		// no activity at all and still left a row. The first activity signal
		// opens the entry, back-dated to this moment, so nothing is lost by
		// waiting for it.
		_, err = transaction.ExecContext(ctx, `
			INSERT INTO agent_sessions (id, user_id, project_id, source, status, started_at, last_heartbeat_at,
			                            cwd, git_branch, model, tz_offset_min, time_entry_id, entry_server_seq,
			                            created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
			session.ID, userID, session.ProjectID, session.Source, session.Status,
			session.StartedAt, session.LastHeartbeatAt, session.Cwd, session.GitBranch, session.Model,
			session.TZOffsetMin, now, now)
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
		entry, err := createAgentEntry(ctx, transaction, userID, session, signal.At, nil, now)
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
	seq, closed, err := closeAgentEntryWithTechnicalCleanup(
		ctx, transaction, userID, session, effectiveEnd, now,
	)
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
// heartbeat is older than the grace period, at that last heartbeat. This is the layer
// that survives SIGKILL, OOM and network loss on the agent side.
//
// It deliberately does NOT extend a trailing tool_start the way StopAgentSession does,
// even though both close a session that went quiet mid-tool. The difference is what
// each one knows. A stop carries the moment work actually ended, so the gap before it
// can be measured and capped. Reconciliation has no such moment - only "now", which is
// whenever the job happened to run: at least the grace period later, and after a server
// restart possibly days later. Billing to the cap from there would invent time nobody
// worked, and would make a killed session worth *more* than one that stopped cleanly.
// Under-counting an interrupted tool run is the lesser error, and the only one that
// keeps the promise this whole mechanism is built on: a lost stop can never inflate a
// duration.
func (s *Store) ReconcileAgentSessions(ctx context.Context, now, graceMs int64) (int, error) {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer transaction.Rollback()

	type staleSession struct {
		id              string
		userID          string
		source          string
		timeEntryID     *string
		entryServerSeq  *int64
		entryUserNamed  bool
		entryUserEdited bool
		taskKey         string
		taskTitle       string
		lastHeartbeatAt int64
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, user_id, source, time_entry_id, entry_server_seq, entry_user_named, entry_user_edited,
		       task_key, task_title, last_heartbeat_at
		FROM agent_sessions WHERE status = ? AND last_heartbeat_at < ?`,
		agentStatusActive, now-graceMs)
	if err != nil {
		return 0, err
	}
	stale := []staleSession{}
	for rows.Next() {
		var session staleSession
		if err := rows.Scan(&session.id, &session.userID, &session.source, &session.timeEntryID,
			&session.entryServerSeq, &session.entryUserNamed, &session.entryUserEdited,
			&session.taskKey, &session.taskTitle,
			&session.lastHeartbeatAt); err != nil {
			rows.Close()
			return 0, err
		}
		stale = append(stale, session)
	}
	if err := closeRows(rows); err != nil {
		return 0, err
	}

	for _, session := range stale {
		endedAt := session.lastHeartbeatAt
		agentSession := AgentSession{
			ID: session.id, Source: session.source, TimeEntryID: session.timeEntryID,
			EntryServerSeq: session.entryServerSeq, EntryUserNamed: session.entryUserNamed,
			EntryUserEdited: session.entryUserEdited,
			TaskKey:         session.taskKey, TaskTitle: session.taskTitle,
		}
		seq, closed, err := closeAgentEntryWithTechnicalCleanup(
			ctx, transaction, session.userID, agentSession, endedAt, now,
		)
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
// id wins. Otherwise a supplied working directory constrains the choice to exactly
// one normalized active-session match; only without either selector may the sole
// active session be chosen automatically. Attaching the wrong session silently is
// worse than asking, so anything else is an error listing the relevant candidates.
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

// SetAgentSessionProject records which project the session's entries belong to. The
// entry that is running gets moved by the ordinary sync path, but a session split by
// a pause opens its next entry from the session row, so without this the project an
// agent picked would be forgotten after the next idle gap.
// A nil project detaches future entries, which is the state a session starts in.
func (s *Store) SetAgentSessionProject(ctx context.Context, userID, sessionID string, projectID *string) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()

	if err := validateAgentProject(ctx, transaction, userID, projectID); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET project_id = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		projectID, time.Now().UnixMilli(), sessionID, userID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return transaction.Commit()
}

// --- internals ---

// agentEntry is the session's current entry as a signal handler sees it.
type agentEntry struct {
	id          string
	description string
	stoppedAt   *int64
	serverSeq   int64
	userNamed   bool
	userEdited  bool
}

// advanceAgentSession applies an activity signal at the given moment: it revives
// a closed session, splits the current entry at an idle gap,
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
	// A session whose entry is still unopened has nothing billed yet, so the
	// staleness guard below must not apply to it: the hook's clock falls back to
	// whole seconds where date(1) has no %N, which puts the start and the first
	// signal of a quick session on the same millisecond and would leave that
	// session with no entry at all. A session that has already ended is not in
	// that position and keeps the guard: the hook sends heartbeats asynchronously
	// and the stop synchronously, so the only signal of a quick session can land
	// after its stop, and letting it through would flip a finished session back to
	// active and leave a row running until reconciliation - a row worth 0 ms when
	// that straggler sits on the start millisecond. Real activity after a stop
	// carries a moment past the end and revives the session further down as usual.
	unopened := session.TimeEntryID == nil && session.Status != agentStatusClosed
	// A stale signal (offline queue replay, an async heartbeat overtaken by the
	// synchronous stop) only refreshes metadata: reviving a session at a moment
	// already billed would open a second running entry out of thin air.
	if at <= mark && !unopened {
		return nil
	}

	current, err := resolveAgentEntry(ctx, transaction, userID, session, now)
	if err != nil {
		return err
	}

	// pausedFrom is set when this signal ends a pause: it is the last billable
	// moment before the gap, where an idle split closes the row that ran until
	// then. A gap that started with a tool call is work up to the cap, because
	// PostToolUse only fires once the tool is done and a long Bash or Task looks
	// exactly like an empty chair from here. Every row this signal opens records
	// it, so a later restore of an earlier row can close that row where the split
	// would have, had the row kept running (see settleRestoredAgentEntry).
	var pausedFrom *int64
	if at-mark > policy.IdleMs {
		billable := int64(0)
		if session.LastKind == AgentKindToolStart {
			billable = min(at-mark, policy.ToolMaxMs)
		}
		if workedUntil := mark + billable; workedUntil < at {
			pausedFrom = &workedUntil
		}
	}

	opened := false
	if current == nil && unopened {
		// Deferred materialization. The row appears with the first activity but
		// covers the session from its start when that signal arrives before the
		// idle threshold. A later first signal starts the first segment itself.
		//
		// Nothing is billed before a first signal that arrives after the idle
		// threshold. Opening a zero-length segment before that pause would recreate
		// the artefact deferred materialization exists to prevent, so the first
		// entry starts at the signal instead.
		from, fromPause := session.StartedAt, (*int64)(nil)
		if at-mark > policy.IdleMs {
			from, fromPause = at, pausedFrom
			opened = true
		}
		current, err = createAgentEntry(ctx, transaction, userID, *session, from, fromPause, now)
		if err != nil {
			return err
		}
	}
	if !opened && current != nil && pausedFrom != nil {
		if _, _, err := closeAgentEntryWithTechnicalCleanup(
			ctx, transaction, userID, *session, *pausedFrom, now,
		); err != nil {
			return err
		}
		current, err = createAgentEntry(ctx, transaction, userID, *session, at, pausedFrom, now)
		if err != nil {
			return err
		}
		opened = true
	}
	if current == nil {
		// The session lost its entry - deleted or stopped by the user - so the
		// replacement opens at the signal: everything before it is already on the
		// row that was let go. When the signal also ends a pause, the replacement
		// records it like any row opened after one.
		current, err = createAgentEntry(ctx, transaction, userID, *session, at, pausedFrom, now)
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
	session.EntryUserEdited = current.userEdited
	session.LastKind = truncateRunes(signal.Kind, maxAgentShortLength)
	_, err = transaction.ExecContext(ctx, `
		UPDATE agent_sessions SET status = ?, ended_at = NULL, end_reason = NULL,
		       time_entry_id = ?, entry_server_seq = ?, entry_user_named = ?, entry_user_edited = ?,
		       last_heartbeat_at = ?, last_kind = ?, tz_offset_min = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		agentStatusActive, current.id, current.serverSeq, current.userNamed, current.userEdited,
		session.LastHeartbeatAt, session.LastKind, session.TZOffsetMin, now, session.ID, userID)
	return err
}

// validTZOffset drops values outside the range real timezones live in, so a
// broken hook cannot distort report day boundaries.
func validTZOffset(offsetMin *int) *int {
	if offsetMin == nil || *offsetMin < -12*60 || *offsetMin > 14*60 {
		return nil
	}
	return offsetMin
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
			serverSeq: serverSeq, userNamed: session.EntryUserNamed, userEdited: session.EntryUserEdited,
		}, nil
	}
	if !owns && stoppedAt != nil {
		// Stopped by the user: detaching is what keeps the rest of the session
		// tracked at all, and the row is already closed.
		return nil, detachAgentEntry(ctx, transaction, userID, session, now)
	}
	// Adopted: whether the name is now the user's is decided by the name itself,
	// while every outside edit is remembered separately for cleanup safety. A
	// stale automatic name (a device that had not pulled the task rename yet) is
	// not a choice, so the rename below brings it up to date.
	return &agentEntry{
		id: *session.TimeEntryID, description: description, stoppedAt: stoppedAt,
		serverSeq: serverSeq, userNamed: !isAutomaticAgentName(*session, description), userEdited: true,
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

// agentRestores settles, once every row of a push is written, the rows a push
// brings back after the server had already ended them:
//
//   - A server tombstone: agent_deleted_at holds the deleted_at the agent flow
//     wrote (a zero-minute technical row, a replacement a restore made redundant).
//     A device that still shows such a row and writes it - renames it, stops it,
//     pushes it running - would resurrect it by last-write-wins and bill its time
//     twice next to the row that covers it. The tombstone is put back and the
//     pushed fields are kept. This holds for every pushed row, running or not.
//   - A restored agent row: one the push leaves running and not deleted while
//     its stored version before the push was stopped or deleted - the Timer
//     page's Undo after a Stop or a delete, or a device pushing a row it still
//     shows running after the server closed or split the session. Reconciliation
//     walks sessions, not entries, so such a row must end up owned by its session
//     or closed again; see settleRestoredAgentEntry.
//
// Both run on the state the whole push left, never per pushed version, so the
// order of rows inside a push cannot change the outcome.
type agentRestores struct {
	// wanted holds the ids the push sends running and not deleted at least once.
	// Only those can end the push running, so only those are read beforehand.
	wanted map[string]bool
	// previous is each wanted row as stored before this push first wrote it, and
	// order lists those rows in push order.
	previous map[string]restoredAgentEntry
	order    []string
	// accepted holds every row the push wrote. The rule never tombstones one.
	accepted map[string]bool
}

// restoredAgentEntry is a pushed row as it was stored before the push first
// wrote it.
type restoredAgentEntry struct {
	id        string
	sessionID *string
	// prevStoppedAt and prevDeletedAt are the row's end before the push. With
	// neither set the row was already running, and a push only edits it.
	prevStoppedAt *int64
	prevDeletedAt *int64
	// prevAgentEnd is the stop the agent flow wrote, when it wrote one.
	prevAgentEnd *int64
}

// sessionEntryRow is one row of a session as the settle sees it, deleted or not:
// a deleted row still tells where the session paused.
type sessionEntryRow struct {
	id          string
	startedAt   int64
	stoppedAt   *int64
	deleted     bool
	serverSeq   int64
	description string
	projectID   *string
	// pausedFrom is set on a row the session opened after an idle pause: the last
	// billable moment before that pause.
	pausedFrom *int64
}

// sessionTimeline is every row of one session, loaded once per push in start
// order and kept up to date with the settle's own writes, so each restored row
// is decided in memory: one query per session, not one per restored row.
type sessionTimeline struct {
	rows      []sessionEntryRow
	positions map[string]int
}

func newAgentRestores(entries []TimeEntry) *agentRestores {
	restores := &agentRestores{
		wanted: map[string]bool{}, previous: map[string]restoredAgentEntry{}, accepted: map[string]bool{},
	}
	for _, entry := range entries {
		if entry.StoppedAt == nil && entry.DeletedAt == nil {
			restores.wanted[entry.ID] = true
		}
	}
	return restores
}

// remember records the stored state of a row before the push first writes it,
// so it must run before the row's upsert.
func (restores *agentRestores) remember(ctx context.Context, transaction *sql.Tx, userID, entryID string) error {
	if !restores.wanted[entryID] {
		return nil
	}
	if _, seen := restores.previous[entryID]; seen {
		return nil
	}
	previous := restoredAgentEntry{id: entryID}
	err := transaction.QueryRowContext(ctx, `
		SELECT agent_session_id, stopped_at, deleted_at, agent_end
		FROM time_entries WHERE id = ? AND user_id = ?`, entryID, userID).
		Scan(&previous.sessionID, &previous.prevStoppedAt, &previous.prevDeletedAt, &previous.prevAgentEnd)
	if errors.Is(err, sql.ErrNoRows) {
		// A new row: nothing before the push to restore, and nothing to remember
		// again for a second version of it later in the same push.
		restores.previous[entryID] = restoredAgentEntry{id: entryID}
		return nil
	}
	if err != nil {
		return err
	}
	restores.previous[entryID] = previous
	restores.order = append(restores.order, entryID)
	return nil
}

// accept notes a row the push wrote.
func (restores *agentRestores) accept(entryID string) {
	restores.accepted[entryID] = true
}

// settle puts back every server tombstone the push cleared, then applies
// settleRestoredAgentEntry to every restored agent row, session by session and
// in start order within a session: an earlier row's outcome is what a later one
// of the same session sees. pushedFrom and pushedTo bound the server_seq block
// Sync reserved for the pushed time entries, which is exactly where every row
// this push wrote sits.
func (restores *agentRestores) settle(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	pushedFrom, pushedTo int64,
	now int64,
) error {
	if pushedFrom > pushedTo {
		return nil
	}
	if err := restores.keepServerTombstones(ctx, transaction, userID, pushedFrom, pushedTo, now); err != nil {
		return err
	}

	bySession := map[string][]restoredAgentEntry{}
	sessions := []string{}
	for _, entryID := range restores.order {
		previous := restores.previous[entryID]
		if !restores.accepted[entryID] || previous.sessionID == nil ||
			(previous.prevStoppedAt == nil && previous.prevDeletedAt == nil) {
			continue
		}
		// agent_session_id is server-owned and never pushed, so it comes from the row.
		sessionID := *previous.sessionID
		if _, known := bySession[sessionID]; !known {
			sessions = append(sessions, sessionID)
		}
		bySession[sessionID] = append(bySession[sessionID], previous)
	}
	for _, sessionID := range sessions {
		session, err := getAgentSession(ctx, transaction, userID, sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		timeline, err := loadSessionTimeline(ctx, transaction, userID, sessionID)
		if err != nil {
			return err
		}
		restored := []restoredAgentEntry{}
		for _, previous := range bySession[sessionID] {
			position, found := timeline.positions[previous.id]
			if !found || timeline.rows[position].stoppedAt != nil || timeline.rows[position].deleted {
				continue
			}
			restored = append(restored, previous)
		}
		// The timeline is in start order, so its positions are the settle order.
		sort.Slice(restored, func(left, right int) bool {
			return timeline.positions[restored[left].id] < timeline.positions[restored[right].id]
		})
		for _, previous := range restored {
			if err := settleRestoredAgentEntry(ctx, transaction, userID, &session, timeline, previous, restores.accepted, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// keepServerTombstones puts back the tombstone of every row this push brought
// back to life after the agent flow had deleted it. The pushed fields are kept;
// a row pushed running also gets its previous stop back, so it ends deleted
// exactly as it was ended.
func (restores *agentRestores) keepServerTombstones(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	pushedFrom, pushedTo int64,
	now int64,
) error {
	type resurrected struct {
		id             string
		stoppedAt      *int64
		agentDeletedAt int64
		serverSeq      int64
	}
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, stopped_at, agent_deleted_at, server_seq FROM time_entries
		WHERE user_id = ? AND server_seq BETWEEN ? AND ?
		  AND deleted_at IS NULL AND agent_deleted_at IS NOT NULL`, userID, pushedFrom, pushedTo)
	if err != nil {
		return err
	}
	found := []resurrected{}
	for rows.Next() {
		var row resurrected
		if err := rows.Scan(&row.id, &row.stoppedAt, &row.agentDeletedAt, &row.serverSeq); err != nil {
			rows.Close()
			return err
		}
		found = append(found, row)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	for _, row := range found {
		correction := restoredEntryCorrection{deletedAt: &row.agentDeletedAt}
		if previous := restores.previous[row.id]; row.stoppedAt == nil && previous.prevStoppedAt != nil {
			correction.stoppedAt = previous.prevStoppedAt
		}
		if _, err := correctRestoredAgentEntry(ctx, transaction, userID, row.id, row.serverSeq, correction, now); err != nil {
			return err
		}
	}
	return nil
}

// settleRestoredAgentEntry decides one restored row P of session S. prevEnd is
// the end P had before the push: its stop, or its tombstone when it was deleted
// while running. A tombstone the server wrote never gets here: keepServerTombstones
// has already put it back.
//
// A row that was stopped and then deleted keeps both. No Undo produces a running
// copy of it - the Undo of a delete writes the stopped row back - so the copy is
// a device that saw neither the stop nor the delete, and its edit must not bring
// the row back. The pushed fields are kept, S and every other row are left alone.
//
// When the server wrote the stop - agent_end holds that very value - an idle
// split, a stop hook, reconciliation or this rule ended the row, and that end
// stands: the pushed fields are kept, the stop is put back, and S and every other
// row are left alone. This is the stale offline client whose row the server cut
// while it was away; reopening the row would bill the idle gap the cut removed.
//
// Otherwise the end was the user's - a Stop, a delete, or an Undo of either. The
// Undo says the row kept running, so it gets the end the session would have
// given it. First, S may have paused after the user's end: the rows S opened
// after P, deleted ones included, are scanned in start order up to the first
// live one, and a row among them opened after an idle pause records where that
// pause began. Had P kept running, the idle split would have closed it there, so
// it closes at that moment - or at the user's end when that came first - and
// everything after is left as it is. This keeps a pause unbilled whether the
// Undo came before the heartbeat that ended the pause or after it, and whether
// the rows after it survived or were zero-minute noise the server removed.
//
// Without a pause, S's activity ran on from before the user's end into whatever
// it opened next - that is exactly what the absence of agent_paused_from records,
// including a gap a running tool call covered - so the outcome depends only on
// what S billed after the user's end. later is S's live rows other than P that
// start at or after P (rows of one session do not overlap); current is the row S
// points at when that is not P.
//
//   - a: S is active and already on P. Nothing changes; the next signal adopts
//     the edit as it adopts any other.
//   - b: nothing later. S takes P back: running while S is active, so S's own
//     machinery closes it at its last activity and no idle time is billed; closed
//     at max(prevEnd, S's end) when S has ended, so never before the user's own
//     stop, and a resume continues it.
//   - c: the only later row is current and nobody touched it. It continues the
//     work P was doing and only duplicates time P covers, so it is tombstoned and
//     S takes P back as in (b).
//   - d: as (c), but current was touched - edited, or written by this very push.
//     It stays as it is and S stays on it, and P is closed where current begins,
//     so the two neither overlap nor leave a hole between them.
//   - e: anything else - several later rows, a later row S no longer points at,
//     or S still running a row that began before P. What came after is work of
//     its own, so P is closed again at prevEnd with the pushed edits kept.
//
// Every write takes a fresh server_seq past the block Sync reserved and steps
// updated_at past the stored value, so other devices pull it and the pushing
// client's copy loses to it.
func settleRestoredAgentEntry(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	session *AgentSession,
	timeline *sessionTimeline,
	previous restoredAgentEntry,
	accepted map[string]bool,
	now int64,
) error {
	position := timeline.positions[previous.id]
	row := &timeline.rows[position]
	prevEnd := previous.prevStoppedAt
	if prevEnd == nil {
		prevEnd = previous.prevDeletedAt
	}

	// A restore never keeps a stale automatic name. An Undo from a device that
	// had not pulled set_agent_task's rename carries the old session tag, and
	// under that name no later set_agent_task would ever find the row again.
	correction := restoredEntryCorrection{}
	description := row.description
	if automatic := agentEntryDescription(*session); description != automatic && isAutomaticAgentName(*session, description) {
		correction.description = &automatic
		description = automatic
	}
	write := func() error {
		_, err := timeline.correct(ctx, transaction, userID, position, correction, now)
		return err
	}
	closeAgain := func(closeAt int64) error {
		correction.closeAt = &closeAt
		return write()
	}

	if previous.prevStoppedAt != nil && previous.prevDeletedAt != nil {
		correction.stoppedAt, correction.deletedAt = previous.prevStoppedAt, previous.prevDeletedAt
		return write()
	}
	if previous.prevStoppedAt != nil && previous.prevAgentEnd != nil && *previous.prevAgentEnd == *previous.prevStoppedAt {
		correction.stoppedAt = previous.prevStoppedAt
		return write()
	}

	active := session.Status == agentStatusActive
	if active && session.TimeEntryID != nil && *session.TimeEntryID == row.id {
		return write()
	}

	var current *sessionEntryRow
	if session.TimeEntryID != nil && *session.TimeEntryID != row.id {
		if currentPosition, found := timeline.positions[*session.TimeEntryID]; found && !timeline.rows[currentPosition].deleted {
			current = &timeline.rows[currentPosition]
		}
	}
	pausedFrom, firstLive, secondLive := timeline.after(position)

	switch {
	case pausedFrom != nil:
		return closeAgain(min(*prevEnd, *pausedFrom))
	case current != nil && current.stoppedAt == nil && current.startedAt < row.startedAt:
		// S still runs a row that began before P, which only an edited start can
		// produce. Taking P back would leave that row running with no session,
		// so P is closed again as in (e).
	case firstLive == nil:
		return takeBackRestoredAgentEntry(ctx, transaction, userID, timeline, position, session, description, correction, *prevEnd, now)
	case secondLive == nil && current != nil && firstLive.id == current.id:
		untouched := session.EntryServerSeq != nil && *session.EntryServerSeq == current.serverSeq &&
			!session.EntryUserEdited && !accepted[current.id]
		if untouched {
			seq, err := tombstoneReplacedAgentEntry(ctx, transaction, userID, current.id, current.serverSeq, now)
			if err != nil {
				return err
			}
			current.deleted, current.serverSeq = true, seq
			return takeBackRestoredAgentEntry(ctx, transaction, userID, timeline, position, session, description, correction, *prevEnd, now)
		}
		return closeAgain(max(*prevEnd, current.startedAt))
	}
	return closeAgain(*prevEnd)
}

// after describes what follows the row at position in its session: where the
// session paused, when a row opened after an idle pause comes before any live
// row, and otherwise the first two live rows that start at or after it.
func (timeline *sessionTimeline) after(position int) (*int64, *sessionEntryRow, *sessionEntryRow) {
	startedAt := timeline.rows[position].startedAt
	// Rows starting at the same moment sort by id, so some may sit just before.
	from := position
	for from > 0 && timeline.rows[from-1].startedAt >= startedAt {
		from--
	}
	var firstLive *sessionEntryRow
	for index := from; index < len(timeline.rows); index++ {
		if index == position {
			continue
		}
		other := &timeline.rows[index]
		if firstLive == nil && other.pausedFrom != nil {
			return other.pausedFrom, nil, nil
		}
		if other.deleted {
			continue
		}
		if firstLive != nil {
			return nil, firstLive, other
		}
		firstLive = other
	}
	return nil, firstLive, nil
}

// correct applies a correction to the row at position and keeps the timeline in
// step with what was written.
func (timeline *sessionTimeline) correct(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	position int,
	correction restoredEntryCorrection,
	now int64,
) (int64, error) {
	row := &timeline.rows[position]
	seq, err := correctRestoredAgentEntry(ctx, transaction, userID, row.id, row.serverSeq, correction, now)
	if err != nil || seq == row.serverSeq {
		return seq, err
	}
	row.serverSeq = seq
	if correction.description != nil {
		row.description = *correction.description
	}
	end := correction.closeAt
	if end == nil {
		end = correction.stoppedAt
	}
	if end != nil {
		stoppedAt := max(row.startedAt, *end)
		row.stoppedAt = &stoppedAt
	}
	if correction.deletedAt != nil {
		row.deleted = true
	}
	return seq, nil
}

// takeBackRestoredAgentEntry points the session at the restored row as the
// outside write it is: edited, user-named only under a name the session would
// not give it, and carrying its project into later segments. An ended session
// closes the row at its end, because nothing after that moment was tracked - but
// never before prevEnd, the user's own stop. The in-memory session follows the
// write, so the next restored row of the same session sees it.
func takeBackRestoredAgentEntry(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	timeline *sessionTimeline,
	position int,
	session *AgentSession,
	description string,
	correction restoredEntryCorrection,
	prevEnd, now int64,
) error {
	if session.Status != agentStatusActive {
		sessionEnd := session.LastHeartbeatAt
		if session.EndedAt != nil {
			sessionEnd = *session.EndedAt
		}
		closeAt := max(prevEnd, sessionEnd)
		correction.closeAt = &closeAt
	}
	ownedSeq, err := timeline.correct(ctx, transaction, userID, position, correction, now)
	if err != nil {
		return err
	}
	row := timeline.rows[position]
	userNamed := !isAutomaticAgentName(*session, description)
	if _, err := transaction.ExecContext(ctx, `
		UPDATE agent_sessions
		SET time_entry_id = ?, entry_server_seq = ?, entry_user_edited = 1, entry_user_named = ?,
		    project_id = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		row.id, ownedSeq, userNamed, row.projectID, now, session.ID, userID); err != nil {
		return err
	}
	session.TimeEntryID, session.EntryServerSeq = &row.id, &ownedSeq
	session.EntryUserEdited, session.EntryUserNamed, session.ProjectID = true, userNamed, row.projectID
	return nil
}

// restoredEntryCorrection is what the server changes on a row a push brought
// back. Every field is optional, and an empty correction writes nothing.
type restoredEntryCorrection struct {
	// description replaces a stale automatic name with the session's current one.
	description *string
	// closeAt closes the row as the server does, so agent_end records the stop.
	closeAt *int64
	// stoppedAt and deletedAt put the row's previous end back as it was. The
	// agent_* markers survived the push untouched and still describe that end,
	// so they are left alone - a user's end stays the user's.
	stoppedAt *int64
	deletedAt *int64
}

// correctRestoredAgentEntry applies a correction as one write with a fresh
// server_seq and returns the row's server_seq afterwards, which is the given one
// when nothing was written. MAX() guards against an edited started_at, as in
// closeAgentEntry; a close stores the same value in agent_end, so the value
// comparison of a later restore holds. closeAt wins over stoppedAt, so no column
// is ever assigned twice in one statement.
func correctRestoredAgentEntry(
	ctx context.Context,
	transaction *sql.Tx,
	userID, entryID string,
	serverSeq int64,
	correction restoredEntryCorrection,
	now int64,
) (int64, error) {
	assignments := []string{}
	arguments := []any{}
	if correction.description != nil {
		assignments = append(assignments, "description = ?")
		arguments = append(arguments, *correction.description)
	}
	switch {
	case correction.closeAt != nil:
		assignments = append(assignments, "stopped_at = MAX(started_at, ?)", "agent_end = MAX(started_at, ?)")
		arguments = append(arguments, *correction.closeAt, *correction.closeAt)
	case correction.stoppedAt != nil:
		assignments = append(assignments, "stopped_at = MAX(started_at, ?)")
		arguments = append(arguments, *correction.stoppedAt)
	}
	if correction.deletedAt != nil {
		assignments = append(assignments, "deleted_at = ?")
		arguments = append(arguments, *correction.deletedAt)
	}
	if len(assignments) == 0 {
		return serverSeq, nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return 0, err
	}
	assignments = append(assignments, "updated_at = MAX(updated_at + 1, ?)", "server_seq = ?")
	arguments = append(arguments, now, seq, entryID, userID, serverSeq)
	result, err := transaction.ExecContext(ctx, "UPDATE time_entries SET "+strings.Join(assignments, ", ")+`
		WHERE id = ? AND user_id = ? AND server_seq = ?`, arguments...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return serverSeq, nil
	}
	return seq, nil
}

// tombstoneReplacedAgentEntry removes the replacement a session opened after the
// user's stop and still owns untouched: the restored row covers its time.
// agent_deleted_at marks the tombstone as the server's, so a device that still
// shows the replacement and writes it later gets the tombstone put back. Returns
// the tombstone's server_seq.
func tombstoneReplacedAgentEntry(ctx context.Context, transaction *sql.Tx, userID, entryID string, serverSeq, now int64) (int64, error) {
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return 0, err
	}
	_, err = transaction.ExecContext(ctx, `
		UPDATE time_entries
		SET deleted_at = ?, agent_deleted_at = ?, updated_at = MAX(updated_at + 1, ?), server_seq = ?
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND server_seq = ?`,
		now, now, now, seq, entryID, userID, serverSeq)
	return seq, err
}

// loadSessionTimeline reads every row of one session, deleted or not, in start
// order.
func loadSessionTimeline(ctx context.Context, transaction *sql.Tx, userID, sessionID string) (*sessionTimeline, error) {
	rows, err := transaction.QueryContext(ctx, `
		SELECT id, started_at, stopped_at, deleted_at IS NOT NULL, server_seq, description, project_id, agent_paused_from
		FROM time_entries WHERE user_id = ? AND agent_session_id = ?
		ORDER BY started_at, id`, userID, sessionID)
	if err != nil {
		return nil, err
	}
	timeline := &sessionTimeline{positions: map[string]int{}}
	for rows.Next() {
		var entry sessionEntryRow
		if err := rows.Scan(&entry.id, &entry.startedAt, &entry.stoppedAt, &entry.deleted, &entry.serverSeq,
			&entry.description, &entry.projectID, &entry.pausedFrom); err != nil {
			rows.Close()
			return nil, err
		}
		timeline.positions[entry.id] = len(timeline.rows)
		timeline.rows = append(timeline.rows, entry)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return timeline, nil
}

// isAutomaticAgentName reports whether a description is a name the session itself
// gives its rows: the current one, or the session tag every row carries until a
// task is set. The tag comes back after set_agent_task from any device that had
// not pulled the rename yet - an Undo, an edit of another field - and that is not
// the user choosing a name.
func isAutomaticAgentName(session AgentSession, description string) bool {
	if description == agentEntryDescription(session) {
		return true
	}
	untitled := session
	untitled.TaskKey, untitled.TaskTitle = "", ""
	return description == agentEntryDescription(untitled)
}

// createAgentEntry opens a new running time entry (a segment) for the session.
// The entry gets a server_seq so clients pull it as usual, and agent_session_id
// so a later set_agent_task can find it. pausedFrom is set when the row opens
// after an idle pause: the last billable moment before it, kept server-side in
// agent_paused_from.
func createAgentEntry(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	session AgentSession,
	startedAt int64,
	pausedFrom *int64,
	now int64,
) (*agentEntry, error) {
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
		                          created_at, updated_at, deleted_at, server_seq, agent_session_id, agent_paused_from)
		VALUES (?, ?, ?, ?, '[]', ?, NULL, ?, ?, NULL, ?, ?, ?)`,
		entryID.String(), userID, session.ProjectID, description, startedAt, now, now, seq, session.ID, pausedFrom)
	if err != nil {
		return nil, err
	}
	return &agentEntry{id: entryID.String(), description: description, serverSeq: seq}, nil
}

// closeAgentEntry stops the segment's entry, but only while it is still running
// and not deleted - a user who already stopped or removed the entry in the PWA
// wins over the agent flow. MAX() guards against a manually edited started_at.
// The reported flag says whether a row was actually written, which is what makes
// the returned seq usable as the session's ownership marker. agent_end records
// the stop as the server's own, so a later write that sets the row running again
// puts this end back instead of reopening the row (see settleRestoredAgentEntry).
func closeAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, entryID *string, stoppedAt, now int64) (int64, bool, error) {
	if entryID == nil {
		return 0, false, nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return 0, false, err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries
		SET stopped_at = MAX(started_at, ?), agent_end = MAX(started_at, ?),
		    updated_at = MAX(updated_at + 1, ?), server_seq = ?
		WHERE id = ? AND user_id = ? AND stopped_at IS NULL AND deleted_at IS NULL`,
		stoppedAt, stoppedAt, now, seq, *entryID, userID)
	if err != nil {
		return 0, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	return seq, affected > 0, nil
}

// closeAgentEntryWithTechnicalCleanup is the single close path for an entry the
// lifecycle currently owns. The cleanup predicate is intentionally evaluated
// before closeAgentEntry advances server_seq: that marker, together with the
// technical name and durable edit flags, proves the user has not touched the row.
// This applies equally to final closes and to the segment just closed by an idle
// split, so a sub-30-second technical fragment cannot survive merely because the
// same agent session later resumed.
func closeAgentEntryWithTechnicalCleanup(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	session AgentSession,
	stoppedAt, now int64,
) (int64, bool, error) {
	discardable, err := technicalEntryStillOwned(ctx, transaction, userID, session)
	if err != nil {
		return 0, false, err
	}
	seq, closed, err := closeAgentEntry(ctx, transaction, userID, session.TimeEntryID, stoppedAt, now)
	if err != nil || !closed || !discardable {
		return seq, closed, err
	}
	session.EntryServerSeq = &seq
	tombstoneSeq, discarded, err := discardZeroMinuteTechnicalEntry(
		ctx, transaction, userID, session, seq, now,
	)
	if err != nil {
		return 0, false, err
	}
	if discarded {
		seq = tombstoneSeq
	}
	return seq, true, nil
}

// technicalEntryStillOwned is evaluated before closeAgentEntry changes the row's
// server_seq. An outside edit to any field is meaningful: even if the user left
// the automatic description intact, a project or tag assignment must keep the row.
func technicalEntryStillOwned(ctx context.Context, transaction *sql.Tx, userID string, session AgentSession) (bool, error) {
	if session.TimeEntryID == nil || session.EntryServerSeq == nil || session.EntryUserNamed || session.EntryUserEdited ||
		session.TaskKey != "" || session.TaskTitle != "" {
		return false, nil
	}
	var one int
	err := transaction.QueryRowContext(ctx, `
		SELECT 1 FROM time_entries
		WHERE id = ? AND user_id = ? AND agent_session_id = ?
		  AND description = ? AND server_seq = ?
		  AND stopped_at IS NULL AND deleted_at IS NULL`,
		*session.TimeEntryID, userID, session.ID, agentEntryDescription(session), *session.EntryServerSeq).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// discardZeroMinuteTechnicalEntry soft-deletes the closed artefact and gives its
// tombstone a fresh cursor. It is deliberately separate from closeAgentEntry:
// ordinary entries, renamed agent work and anything edited outside the agent flow
// must remain visible no matter how short it is.
func discardZeroMinuteTechnicalEntry(
	ctx context.Context,
	transaction *sql.Tx,
	userID string,
	session AgentSession,
	closedSeq, now int64,
) (int64, bool, error) {
	if session.TimeEntryID == nil {
		return closedSeq, false, nil
	}
	var entry TimeEntry
	err := transaction.QueryRowContext(ctx, `
		SELECT started_at, stopped_at
		FROM time_entries
		WHERE id = ? AND user_id = ? AND agent_session_id = ?
		  AND description = ? AND server_seq = ? AND deleted_at IS NULL`,
		*session.TimeEntryID, userID, session.ID, agentEntryDescription(session), closedSeq).
		Scan(&entry.StartedAt, &entry.StoppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return closedSeq, false, nil
	}
	if err != nil {
		return closedSeq, false, err
	}
	if entry.StoppedAt == nil || *entry.StoppedAt-entry.StartedAt >= agentZeroMinuteMs {
		return closedSeq, false, nil
	}

	tombstoneSeq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return closedSeq, false, err
	}
	// agent_deleted_at marks the tombstone as the server's, so a device that still
	// shows the row and writes it later gets the tombstone put back instead of
	// resurrecting it (see agentRestores.settle).
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries
		SET deleted_at = ?, agent_deleted_at = ?, updated_at = MAX(updated_at + 1, ?), server_seq = ?
		WHERE id = ? AND user_id = ? AND stopped_at IS NOT NULL
		  AND deleted_at IS NULL AND server_seq = ?`,
		now, now, now, tombstoneSeq, *session.TimeEntryID, userID, closedSeq)
	if err != nil {
		return closedSeq, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return closedSeq, false, err
	}
	if affected == 0 {
		return closedSeq, false, nil
	}
	return tombstoneSeq, true, nil
}

// reopenAgentEntry lets a revived session continue writing into the row it
// already owns instead of opening a second one for the same work. A running row
// has no end, so the record of who wrote the last one goes with it.
func reopenAgentEntry(ctx context.Context, transaction *sql.Tx, userID string, entry *agentEntry, now int64) error {
	if entry.stoppedAt == nil {
		return nil
	}
	seq, err := allocateServerSeq(transaction, 1)
	if err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `
		UPDATE time_entries SET stopped_at = NULL, agent_end = NULL, updated_at = MAX(updated_at + 1, ?), server_seq = ?
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
		UPDATE time_entries SET description = ?, updated_at = MAX(updated_at + 1, ?), server_seq = ?
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
			UPDATE time_entries SET description = ?, updated_at = MAX(updated_at + 1, ?), server_seq = ?
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

	if selector.Cwd != "" {
		matched := []AgentSession{}
		for _, candidate := range candidates {
			if agentCwdEqual(candidate.Cwd, selector.Cwd) {
				matched = append(matched, candidate)
			}
		}
		switch len(matched) {
		case 1:
			return matched[0], nil
		case 0:
			if len(candidates) == 0 {
				return AgentSession{}, fmt.Errorf("%w: no active agent session matches cwd %q; pass session_id",
					ErrInvalidInput, selector.Cwd)
			}
			return AgentSession{}, fmt.Errorf("%w: no active agent session matches cwd %q; pass session_id: %s",
				ErrInvalidInput, selector.Cwd, formatAgentSessionCandidates(candidates))
		default:
			return AgentSession{}, fmt.Errorf("%w: %d active agent sessions match cwd %q; pass session_id: %s",
				ErrInvalidInput, len(matched), selector.Cwd, formatAgentSessionCandidates(matched))
		}
	}
	if len(candidates) == 0 {
		return AgentSession{}, fmt.Errorf("%w: no active agent session; pass session_id", ErrInvalidInput)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return AgentSession{}, fmt.Errorf("%w: %d active agent sessions; pass session_id: %s",
		ErrInvalidInput, len(candidates), formatAgentSessionCandidates(candidates))
}

func formatAgentSessionCandidates(candidates []AgentSession) string {
	listed := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		listed = append(listed, fmt.Sprintf("%s (cwd %q, started %s)",
			candidate.ID, candidate.Cwd, time.UnixMilli(candidate.StartedAt).UTC().Format(time.RFC3339)))
	}
	return strings.Join(listed, "; ")
}

type agentCwdFlavor uint8

const (
	agentCwdPOSIX agentCwdFlavor = iota
	agentCwdWindowsDrive
	agentCwdWindowsUNC
)

// agentCwdEqual compares paths reported by the agent, not paths on this server.
// The server is commonly Linux even when the hook runs on Windows, so filepath.Clean
// and filesystem lookups would apply the wrong machine's rules. Keep POSIX backslashes
// literal, preserve Windows volume boundaries, and never turn an unknown cwd into ".".
func agentCwdEqual(left, right string) bool {
	leftFlavor, leftPath, leftOK := normalizeAgentCwd(left)
	rightFlavor, rightPath, rightOK := normalizeAgentCwd(right)
	return leftOK && rightOK && leftFlavor == rightFlavor && strings.EqualFold(leftPath, rightPath)
}

func normalizeAgentCwd(value string) (agentCwdFlavor, string, bool) {
	if value == "" {
		return 0, "", false
	}
	if len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' && isWindowsSeparator(value[2]) {
		return agentCwdWindowsDrive, value[:2] + "/" + cleanWindowsPath(value[3:]), true
	}
	// A leading // is a valid POSIX spelling with implementation-defined meaning,
	// so only two backslashes prove that the remote path uses UNC syntax. Once the
	// flavor is known, both Windows separators are accepted inside the path.
	if strings.HasPrefix(value, `\\`) {
		parts := splitWindowsPath(strings.TrimLeft(value, `/\`))
		if len(parts) >= 2 && parts[0] != ".." && parts[1] != ".." {
			root := "//" + parts[0] + "/" + parts[1]
			cleaned := cleanWindowsParts(parts[2:])
			if cleaned != "" {
				root += "/" + cleaned
			}
			return agentCwdWindowsUNC, root, true
		}
	}
	return agentCwdPOSIX, path.Clean(value), true
}

func cleanWindowsPath(value string) string {
	return cleanWindowsParts(splitWindowsPath(value))
}

func splitWindowsPath(value string) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '\\'
	})
}

func cleanWindowsParts(parts []string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(cleaned) > 0 {
				cleaned = cleaned[:len(cleaned)-1]
			}
		default:
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/")
}

func isWindowsSeparator(character byte) bool {
	return character == '/' || character == '\\'
}

func isASCIIAlpha(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
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
		&session.TaskKey, &session.TaskTitle, &session.EntryServerSeq, &session.EntryUserNamed, &session.EntryUserEdited,
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
	return agentSourceLabel(session.Source) + " #" + AgentSessionTag(session.ID)
}

// agentSourceLabel turns the client id the hook reports into the name a person
// reads in the list. Anything unknown is shown as it arrived: a client this
// build has never heard of is still better named after itself than after Claude.
func agentSourceLabel(source string) string {
	switch source {
	case "", defaultAgentSource:
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		return source
	}
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

// purgeEmptyAgentEntries is the body of migration 008 and is frozen: editing it
// rewrites a migration that has already run on live databases. The test re-runs
// it against rows inserted by hand, which is the only way to reach the shape the
// old rule produced now that no code path can still create it.
//
// Until the entry started waiting for activity, a start opened it, so every
// launch that never worked wrote a row nobody asked for - 222 of 249 sessions in
// the week that was measured. The new rule only governs what happens from here;
// this is the backlog it left.
//
// Three conditions have to hold together, and each one alone is too weak:
//
//   - the session never reported activity. Only advanceAgentSession's tail moves
//     last_heartbeat_at past the start or writes last_kind, and it always writes
//     both, so a session where neither moved has been silent since it began.
//   - the agent flow still owns the entry. server_seq is the value the session
//     recorded, which is the same test resolveAgentEntry uses to decide whether a
//     row was edited outside the agent flow.
//   - the description is still this session's own tag, so a row renamed by hand,
//     or after a tracker task, survives even where the two tests above pass.
//
// The agent_sessions rows are deliberately left alone: the launch did happen, and
// a deleted session could no longer tell "was empty" from "went missing" the next
// time something breaks. A row still running is taken too - a session that never
// worked has no entry under the new rule whether or not it was closed, and its
// next signal opens a fresh one at that signal, because a tombstone is never
// resurrected.
const purgeEmptyAgentEntries = `
CREATE TEMP TABLE agent_empty_entries AS
SELECT e.id AS id, ROW_NUMBER() OVER (ORDER BY e.server_seq) AS position
FROM time_entries e
JOIN agent_sessions s ON s.id = e.agent_session_id
WHERE e.deleted_at IS NULL
  AND e.server_seq = s.entry_server_seq
  AND s.last_heartbeat_at <= s.started_at
  AND s.last_kind = ''
  AND e.description GLOB ('* #' || lower(substr(replace(s.id, '-', ''), 1, 8)));

-- Every tombstone needs a server_seq of its own: two rows sharing one cursor
-- value would let a client acknowledge both and pull only the first.
UPDATE sync_state SET seq = seq + (SELECT COUNT(*) FROM agent_empty_entries);

UPDATE time_entries
SET deleted_at = CAST(strftime('%s', 'now') AS INTEGER) * 1000,
    updated_at = MAX(updated_at + 1, CAST(strftime('%s', 'now') AS INTEGER) * 1000),
    server_seq = (SELECT seq FROM sync_state)
                 - (SELECT COUNT(*) FROM agent_empty_entries)
                 + (SELECT position FROM agent_empty_entries WHERE agent_empty_entries.id = time_entries.id)
WHERE id IN (SELECT id FROM agent_empty_entries);

DROP TABLE agent_empty_entries;
`
