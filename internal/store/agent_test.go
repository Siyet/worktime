package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	testIdleMs  = int64(10 * 60 * 1000)
	testGraceMs = int64(10 * 60 * 1000)
	agentBaseMs = int64(1_700_000_000_000)
)

var testPolicy = AgentPolicy{
	IdleMs:    testIdleMs,
	ToolMaxMs: 30 * 60 * 1000,
}

func startTestAgentSession(t *testing.T, testStore *Store, userID, sessionID string, startedAt int64) AgentSession {
	return startTestAgentSessionAtCwd(t, testStore, userID, sessionID, startedAt, "C:\\Users\\dev\\Projects\\WorkTime")
}

// startWorkingTestAgentSession is a session that has already reported activity.
// The entry opens on the first signal, so a test that needs the row asks for one;
// the signal shares the start moment, which leaves every caller's timeline as it
// was when the start opened the entry itself.
func startWorkingTestAgentSession(t *testing.T, testStore *Store, userID, sessionID string, startedAt int64) AgentSession {
	t.Helper()
	startTestAgentSession(t, testStore, userID, sessionID, startedAt)
	return testHeartbeat(t, testStore, userID, sessionID, startedAt)
}

func startTestAgentSessionAtCwd(t *testing.T, testStore *Store, userID, sessionID string, startedAt int64, cwd string) AgentSession {
	t.Helper()
	session, err := testStore.StartAgentSession(context.Background(), userID, AgentStart{
		SessionID: sessionID, StartedAt: startedAt, Source: "claude-code",
		Cwd: cwd, GitBranch: "main",
	}, testPolicy)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	return session
}

func TestAgentCwdEqual(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "posix lexical and case", left: "/Home/dev/project/./", right: "/home/dev/project", want: true},
		{name: "posix parent", left: "/home/dev/other/../project", right: "/home/dev/project", want: true},
		{name: "posix literal backslash", left: `/srv/a\b`, right: "/srv/a/b", want: false},
		{name: "windows drive", left: `C:\Users\Dev\Project\.`, right: "c:/users/dev/project", want: true},
		{name: "windows drive clamps parent at root", left: `C:\..\x`, right: "c:/x", want: true},
		{name: "windows UNC", left: `\\server\share\foo\..\bar`, right: `\\SERVER\share/bar`, want: true},
		{name: "windows UNC clamps parent at share", left: `\\server\share\..\x`, right: `\\server/share/x`, want: true},
		{name: "UNC and POSIX flavors differ", left: `\\server\share`, right: "/server/share", want: false},
		{name: "UNC and double-slash POSIX flavors differ", left: `\\server\share`, right: "//server/share", want: false},
		{name: "UNC and mixed-prefix POSIX flavors differ", left: `\\server\share`, right: `/\server/share`, want: false},
		{name: "unknown cwd is not dot", left: "", right: ".", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agentCwdEqual(test.left, test.right); got != test.want {
				t.Fatalf("agentCwdEqual(%q, %q) = %v, want %v", test.left, test.right, got, test.want)
			}
		})
	}
}

func testHeartbeat(t *testing.T, testStore *Store, userID, sessionID string, at int64) AgentSession {
	t.Helper()
	session, err := testStore.AgentHeartbeat(context.Background(), userID, sessionID, AgentSignal{At: at}, testPolicy)
	if err != nil {
		t.Fatalf("heartbeat at %d: %v", at, err)
	}
	return session
}

func testStop(t *testing.T, testStore *Store, userID, sessionID string, endedAt int64, reason string) AgentSession {
	t.Helper()
	session, err := testStore.StopAgentSession(context.Background(), userID, sessionID, reason,
		AgentSignal{At: endedAt}, testPolicy)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	return session
}

// pushEntry replays a client edit of an entry through the normal sync path, which
// is what an edit in the PWA looks like to the agent flow.
func pushEntry(t *testing.T, testStore *Store, userID string, entry TimeEntry) {
	t.Helper()
	entry.UpdatedAt++
	if _, err := testStore.Sync(context.Background(), userID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{entry}},
	}); err != nil {
		t.Fatalf("push entry: %v", err)
	}
}

// A start says a process exists, which is not the same as work: the agent binary
// is launched far more often than it is worked in. The entry waits for the first
// activity signal and then covers the session from its beginning, so waiting
// decides whether the row exists and never how much of the session it holds.
func TestAgentEntryWaitsForActivityAndThenCoversTheWholeSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-start@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	session := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if session.Status != agentStatusActive || session.LastHeartbeatAt != agentBaseMs {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.TimeEntryID != nil {
		t.Fatal("a start on its own must not open a time entry")
	}
	pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pull.Changes.TimeEntries) != 0 {
		t.Fatalf("a session with no activity reached the clients: %+v", pull.Changes.TimeEntries)
	}

	session = testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+30_000)
	if session.TimeEntryID == nil {
		t.Fatal("the first activity signal must open the entry")
	}

	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StartedAt != agentBaseMs || entry.StoppedAt != nil {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.Description != "Claude Code #"+AgentSessionTag(sessionID) {
		t.Fatalf("unexpected description: %q", entry.Description)
	}

	// The entry must reach clients over the normal sync pull path.
	pull, err = testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pull.Changes.TimeEntries) != 1 || pull.Changes.TimeEntries[0].ID != entry.ID {
		t.Fatalf("expected the agent entry in the pull, got %+v", pull.Changes.TimeEntries)
	}
}

// The hook falls back to whole seconds where date(1) has no %N, so a session
// quick enough puts its start and its first signal on the same millisecond. That
// signal is not stale - nothing is billed yet - and it has to open the entry.
func TestAgentFirstSignalAtTheStartMomentStillOpensTheEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-same-ms@test.local")

	sessionID := uuid.NewString()
	startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs)
	if session.TimeEntryID == nil {
		t.Fatal("a signal at the start moment left the session without an entry")
	}

	entry, err := testStore.GetTimeEntry(context.Background(), user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StartedAt != agentBaseMs || entry.StoppedAt != nil {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestAgentStopDiscardsOnlyUntouchedTechnicalZeroMinuteEntries(t *testing.T) {
	tests := []struct {
		name       string
		durationMs int64
		prepare    func(*testing.T, *Store, string, string, string)
		wantKept   bool
	}{
		{name: "zero length", durationMs: 0},
		{name: "last duration rendered as zero minutes", durationMs: agentZeroMinuteMs - 1},
		{name: "first duration rendered as one minute", durationMs: agentZeroMinuteMs, wantKept: true},
		{
			name: "task assigned", durationMs: 1_000, wantKept: true,
			prepare: func(t *testing.T, testStore *Store, userID, sessionID, _ string) {
				if _, err := testStore.SetAgentTask(t.Context(), userID,
					AgentTaskSelector{SessionID: sessionID}, "WT-1", "Real task"); err != nil {
					t.Fatalf("set task: %v", err)
				}
			},
		},
		{
			name: "description edited", durationMs: 1_000, wantKept: true,
			prepare: func(t *testing.T, testStore *Store, userID, sessionID, entryID string) {
				entry, err := testStore.GetTimeEntry(t.Context(), userID, entryID)
				if err != nil {
					t.Fatalf("get entry: %v", err)
				}
				entry.Description = "Manual note"
				pushEntry(t, testStore, userID, entry)
				testHeartbeat(t, testStore, userID, sessionID, agentBaseMs+500)
			},
		},
		{
			name: "tags edited", durationMs: 1_000, wantKept: true,
			prepare: func(t *testing.T, testStore *Store, userID, sessionID, entryID string) {
				entry, err := testStore.GetTimeEntry(t.Context(), userID, entryID)
				if err != nil {
					t.Fatalf("get entry: %v", err)
				}
				entry.Tags = TagList{"review"}
				pushEntry(t, testStore, userID, entry)
				testHeartbeat(t, testStore, userID, sessionID, agentBaseMs+500)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-zero-minute@test.local")
			sessionID := uuid.NewString()
			session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
			entryID := *session.TimeEntryID
			if test.prepare != nil {
				test.prepare(t, testStore, user.ID, sessionID, entryID)
			}

			closed := testStop(t, testStore, user.ID, sessionID, agentBaseMs+test.durationMs, "session_end")
			_, err := testStore.GetTimeEntry(t.Context(), user.ID, entryID)
			if test.wantKept && err != nil {
				t.Fatalf("meaningful short entry was removed: %v", err)
			}
			if !test.wantKept && !errors.Is(err, ErrNotFound) {
				t.Fatalf("technical zero-minute entry survived: %v", err)
			}
			if !test.wantKept {
				var deletedAt, serverSeq int64
				if err := testStore.db.QueryRow(
					"SELECT deleted_at, server_seq FROM time_entries WHERE id = ?", entryID,
				).Scan(&deletedAt, &serverSeq); err != nil {
					t.Fatalf("read tombstone: %v", err)
				}
				if deletedAt == 0 || closed.EntryServerSeq == nil || *closed.EntryServerSeq != serverSeq {
					t.Fatalf("session does not own the synced tombstone: deleted=%d seq=%d session=%v",
						deletedAt, serverSeq, closed.EntryServerSeq)
				}
				pull, err := testStore.Sync(t.Context(), user.ID, SyncRequest{Since: serverSeq - 1})
				if err != nil {
					t.Fatalf("pull tombstone: %v", err)
				}
				if len(pull.Changes.TimeEntries) != 1 || pull.Changes.TimeEntries[0].DeletedAt == nil {
					t.Fatalf("cleanup did not sync a tombstone: %+v", pull.Changes.TimeEntries)
				}
			}
		})
	}
}

func TestAgentReconcileDiscardsTechnicalZeroMinuteEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-zero-minute-reconcile@test.local")
	sessionID := uuid.NewString()
	session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	closed, err := testStore.ReconcileAgentSessions(t.Context(), agentBaseMs+testGraceMs+1, testGraceMs)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if closed != 1 {
		t.Fatalf("closed %d sessions, want 1", closed)
	}
	if _, err := testStore.GetTimeEntry(t.Context(), user.ID, *session.TimeEntryID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reconcile left the zero-minute technical entry: %v", err)
	}
}

// Back-dating the entry to the start is right up to the point where the gap to
// the first signal becomes a pause. Nothing is billed before that signal, so a
// zero-length row before the pause would only recreate the artefact deferred
// materialization exists to prevent.
func TestAgentFirstSignalAfterAnIdleGapOpensAtTheSignal(t *testing.T) {
	ctx := context.Background()
	// UTC+3: local midnight is 21:00 UTC, so 23:30 -> 00:30 local crosses it.
	offset := 180
	evening := time.Date(2026, 7, 1, 20, 30, 0, 0, time.UTC).UnixMilli()
	morning := time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC).UnixMilli()

	tests := []struct {
		name      string
		offset    *int
		startedAt int64
		signalAt  int64
	}{
		{name: "same day", startedAt: agentBaseMs, signalAt: agentBaseMs + testIdleMs + 1},
		{name: "across the local midnight", offset: &offset, startedAt: evening, signalAt: morning},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-cutting-gap@test.local")

			sessionID := uuid.NewString()
			if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
				SessionID: sessionID, StartedAt: test.startedAt, TZOffsetMin: test.offset,
			}, testPolicy); err != nil {
				t.Fatalf("start: %v", err)
			}
			session := testHeartbeat(t, testStore, user.ID, sessionID, test.signalAt)

			entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
			if err != nil {
				t.Fatalf("get entry: %v", err)
			}
			if entry.StartedAt != test.signalAt || entry.StoppedAt != nil {
				t.Fatalf("the entry must open at the signal, got %+v", entry)
			}
			// One row and no stub before it: the cut left nothing to close.
			pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if len(pull.Changes.TimeEntries) != 1 {
				t.Fatalf("expected exactly one entry, got %+v", pull.Changes.TimeEntries)
			}
		})
	}
}

// The hook sends heartbeats asynchronously and the stop synchronously, so the
// only signal of a quick session can land after its own stop. A session that
// ended without ever opening an entry stays ended: reviving it would leave a row
// running until reconciliation - and one worth 0 ms when, as here, the straggler
// sits on the start millisecond the whole-second clock produced.
func TestAgentStragglerAfterStopLeavesAnUnworkedSessionClosed(t *testing.T) {
	ctx := context.Background()
	for _, at := range []int64{agentBaseMs, agentBaseMs + 30_000} {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-straggler@test.local")

		sessionID := uuid.NewString()
		startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
		testStop(t, testStore, user.ID, sessionID, agentBaseMs+60_000, "session_end")

		session := testHeartbeat(t, testStore, user.ID, sessionID, at)
		if session.Status != agentStatusClosed || session.TimeEntryID != nil {
			t.Fatalf("a straggler at %d revived the session: %+v", at, session)
		}
		pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		if len(pull.Changes.TimeEntries) != 0 {
			t.Fatalf("a straggler at %d created an entry: %+v", at, pull.Changes.TimeEntries)
		}
	}
}

// The other half of the same rule: a signal that genuinely arrives after the
// stop, at a moment past the end, is new work and revives the session as always.
func TestAgentActivityAfterStopStillRevivesAnUnworkedSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-revive@test.local")

	sessionID := uuid.NewString()
	startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	testStop(t, testStore, user.ID, sessionID, agentBaseMs+60_000, "session_end")

	resumed := agentBaseMs + 120_000
	session := testHeartbeat(t, testStore, user.ID, sessionID, resumed)
	if session.Status != agentStatusActive || session.TimeEntryID == nil {
		t.Fatalf("real activity after the stop must revive the session: %+v", session)
	}
	entry, err := testStore.GetTimeEntry(context.Background(), user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StartedAt != resumed || entry.StoppedAt != nil {
		t.Fatalf("the entry must open at the signal, not back at the start: %+v", entry)
	}
}

func TestAgentEntryNamedBySessionTag(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-name@test.local")
	ctx := context.Background()

	// One session known only from a heartbeat (a lost start), one started normally
	// but without any metadata at all: neither may fall back to a bare "Claude Code".
	fromHeartbeat := uuid.NewString()
	testHeartbeat(t, testStore, user.ID, fromHeartbeat, agentBaseMs)
	bare := uuid.NewString()
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{SessionID: bare, StartedAt: agentBaseMs}, testPolicy); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The entry is opened by activity, so the started session needs a signal
	// before it has a name to check.
	testHeartbeat(t, testStore, user.ID, bare, agentBaseMs+1_000)

	names := map[string]string{}
	for _, sessionID := range []string{fromHeartbeat, bare} {
		session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
		if err != nil {
			t.Fatalf("get entry: %v", err)
		}
		want := "Claude Code #" + AgentSessionTag(sessionID)
		if entry.Description != want {
			t.Fatalf("session %s: got %q, want %q", sessionID, entry.Description, want)
		}
		names[entry.Description] = sessionID
	}
	if len(names) != 2 {
		t.Fatalf("two sessions must produce two distinct names, got %v", names)
	}
}

func TestAgentEntryCarriesSessionID(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-backref@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+1_000)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.AgentSessionID == nil || *entry.AgentSessionID != sessionID {
		t.Fatalf("agent entry must point back at its session, got %+v", entry.AgentSessionID)
	}

	manualID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{{
		ID: manualID, Description: "manual", StartedAt: agentBaseMs, CreatedAt: agentBaseMs, UpdatedAt: agentBaseMs,
	}}}}); err != nil {
		t.Fatalf("push manual entry: %v", err)
	}
	manual, err := testStore.GetTimeEntry(ctx, user.ID, manualID)
	if err != nil {
		t.Fatalf("get manual entry: %v", err)
	}
	if manual.AgentSessionID != nil {
		t.Fatalf("a manual entry must not claim a session, got %v", *manual.AgentSessionID)
	}
}

func TestAgentStartReplayIsIdempotent(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-replay@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	first := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+1_000)
	// A --resume replays SessionStart with the same session id a bit later.
	replayed, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: agentBaseMs + 60_000, GitBranch: "feature",
	}, testPolicy)
	if err != nil {
		t.Fatalf("replay start: %v", err)
	}
	if *replayed.TimeEntryID != *first.TimeEntryID {
		t.Fatal("replay must not create a second entry")
	}
	if replayed.StartedAt != agentBaseMs || replayed.LastHeartbeatAt != agentBaseMs+60_000 {
		t.Fatalf("unexpected replayed session: %+v", replayed)
	}
	if replayed.GitBranch != "feature" {
		t.Fatalf("replay must refresh metadata, got %+v", replayed)
	}

	pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pull.Changes.TimeEntries) != 1 {
		t.Fatalf("expected exactly one entry, got %d", len(pull.Changes.TimeEntries))
	}
}

func TestAgentHeartbeatWatermarkMonotonic(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-hb@test.local")

	sessionID := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+120_000)
	if session.LastHeartbeatAt != agentBaseMs+120_000 {
		t.Fatalf("expected watermark advance, got %+v", session)
	}

	// A delayed out-of-order heartbeat (offline queue replay) must not rewind it.
	session = testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+30_000)
	if session.LastHeartbeatAt != agentBaseMs+120_000 {
		t.Fatalf("watermark rewound: %+v", session)
	}
}

func TestAgentHeartbeatFillsMissingMetadata(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-meta@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs)

	session, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
		AgentSignal{At: agentBaseMs + 1000, Cwd: "/home/dev/worktime", GitBranch: "main"}, testPolicy)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if session.Cwd != "/home/dev/worktime" || session.GitBranch != "main" {
		t.Fatalf("heartbeat metadata must fill the gaps left by a lost start: %+v", session)
	}

	// A later heartbeat must not overwrite what the session already knows.
	session, err = testStore.AgentHeartbeat(ctx, user.ID, sessionID,
		AgentSignal{At: agentBaseMs + 2000, GitBranch: "other"}, testPolicy)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if session.GitBranch != "main" {
		t.Fatalf("heartbeat must not overwrite known metadata: %+v", session)
	}
}

func TestAgentIdleGapStartsANewEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-idle@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entryID := *started.TimeEntryID

	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)

	// Silence longer than the idle threshold ends the current work segment. The
	// same agent session stays active and points to a fresh running entry.
	gap := testIdleMs + 300_000
	afterIdle := lastActive + gap
	session := testHeartbeat(t, testStore, user.ID, sessionID, afterIdle)
	if *session.TimeEntryID == entryID {
		t.Fatal("an idle gap must open a second entry")
	}
	first, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt != lastActive {
		t.Fatalf("the first entry must end at the last activity: %+v", first)
	}
	second, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get second entry: %v", err)
	}
	if second.StartedAt != afterIdle || second.StoppedAt != nil {
		t.Fatalf("the second entry must start at resumed activity: %+v", second)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 2 {
		t.Fatalf("expected exactly two entries, got %d", count)
	}
}

func TestAgentIdleSplitCleansOnlyUntouchedTechnicalZeroMinuteSegments(t *testing.T) {
	tests := []struct {
		name       string
		durationMs int64
		edit       bool
		assignTask bool
		wantKept   bool
	}{
		{name: "zero length"},
		{name: "last duration rendered as zero minutes", durationMs: agentZeroMinuteMs - 1},
		{name: "first duration rendered as one minute", durationMs: agentZeroMinuteMs, wantKept: true},
		{name: "edited technical segment", durationMs: 1_000, edit: true, wantKept: true},
		{name: "task-assigned segment", durationMs: 1_000, assignTask: true, wantKept: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-idle-cleanup@test.local")
			ctx := t.Context()
			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
			entryID := *started.TimeEntryID
			lastActive := agentBaseMs + test.durationMs
			if test.durationMs > 0 {
				testHeartbeat(t, testStore, user.ID, sessionID, lastActive)
			}
			if test.edit {
				entry, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
				if err != nil {
					t.Fatalf("get entry before edit: %v", err)
				}
				entry.Tags = TagList{"review"}
				pushEntry(t, testStore, user.ID, entry)
			}
			if test.assignTask {
				if _, err := testStore.SetAgentTask(ctx, user.ID,
					AgentTaskSelector{SessionID: sessionID}, "WT-1", "Assigned work"); err != nil {
					t.Fatalf("assign task: %v", err)
				}
			}

			var beforeSeq int64
			if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&beforeSeq); err != nil {
				t.Fatalf("read cursor before split: %v", err)
			}
			resumedAt := lastActive + testIdleMs + 1
			resumed := testHeartbeat(t, testStore, user.ID, sessionID, resumedAt)
			if resumed.TimeEntryID == nil || *resumed.TimeEntryID == entryID {
				t.Fatalf("idle split did not open a new segment: %+v", resumed)
			}

			var deletedAt *int64
			var stoppedAt *int64
			var firstSeq int64
			if err := testStore.db.QueryRow(`
				SELECT deleted_at, stopped_at, server_seq FROM time_entries
				WHERE id = ? AND user_id = ?`, entryID, user.ID).
				Scan(&deletedAt, &stoppedAt, &firstSeq); err != nil {
				t.Fatalf("read closed segment: %v", err)
			}
			if stoppedAt == nil || *stoppedAt != lastActive {
				t.Fatalf("segment closed at %v, want %d", stoppedAt, lastActive)
			}
			if test.wantKept && deletedAt != nil {
				t.Fatalf("meaningful segment was deleted at %d", *deletedAt)
			}
			if !test.wantKept && deletedAt == nil {
				t.Fatal("untouched technical zero-minute segment survived the idle split")
			}

			var secondSeq int64
			if err := testStore.db.QueryRow(
				"SELECT server_seq FROM time_entries WHERE id = ?", *resumed.TimeEntryID,
			).Scan(&secondSeq); err != nil {
				t.Fatalf("read resumed segment cursor: %v", err)
			}
			if firstSeq <= beforeSeq || secondSeq <= firstSeq {
				t.Fatalf("split cursors are not fresh and unique: before=%d closed=%d resumed=%d",
					beforeSeq, firstSeq, secondSeq)
			}

			if !test.wantKept {
				pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: beforeSeq})
				if err != nil {
					t.Fatalf("pull split changes: %v", err)
				}
				if len(pull.Changes.TimeEntries) != 2 || pull.Changes.TimeEntries[0].ID != entryID ||
					pull.Changes.TimeEntries[0].DeletedAt == nil ||
					pull.Changes.TimeEntries[1].ID != *resumed.TimeEntryID {
					t.Fatalf("split did not publish one tombstone and one new row: %+v", pull.Changes.TimeEntries)
				}
			}

			var splitSeq int64
			if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&splitSeq); err != nil {
				t.Fatalf("read cursor after split: %v", err)
			}
			testHeartbeat(t, testStore, user.ID, sessionID, resumedAt)
			var replaySeq int64
			if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&replaySeq); err != nil {
				t.Fatalf("read cursor after replay: %v", err)
			}
			if replaySeq != splitSeq {
				t.Fatalf("idle signal replay advanced cursor from %d to %d", splitSeq, replaySeq)
			}
		})
	}
}

func TestAgentDurationIsSplitAroundPause(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-duration@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// A minute of work, twenty minutes of nothing, another minute, then stop.
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	resumed := agentBaseMs + 60_000 + 20*60_000
	testHeartbeat(t, testStore, user.ID, sessionID, resumed)
	testHeartbeat(t, testStore, user.ID, sessionID, resumed+60_000)
	testStop(t, testStore, user.ID, sessionID, resumed+60_000, "prompt_input_exit")

	first, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt-first.StartedAt != 60_000 {
		t.Fatalf("unexpected first segment: %+v", first)
	}
	session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	second, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get second entry: %v", err)
	}
	if second.StoppedAt == nil || *second.StoppedAt-second.StartedAt != 60_000 {
		t.Fatalf("unexpected second segment: %+v", second)
	}
}

func TestAgentLongToolRunBilledUpToCap(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-tool@test.local")
	ctx := context.Background()

	// A twenty minute tool call is work in full: PostToolUse only fires once the
	// tool is done, so without the tool_start marker the gap looks like idling.
	shortRun := uuid.NewString()
	shortStarted := startWorkingTestAgentSession(t, testStore, user.ID, shortRun, agentBaseMs)
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, shortRun,
		AgentSignal{At: agentBaseMs + 60_000, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	testHeartbeat(t, testStore, user.ID, shortRun, agentBaseMs+60_000+20*60_000)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *shortStarted.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}

	// A forty five minute one bills the first thirty and splits before the rest: a
	// hung tool must not bill forever, and one minute over the cap must not lose all.
	longRun := uuid.NewString()
	longStarted := startWorkingTestAgentSession(t, testStore, user.ID, longRun, agentBaseMs)
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, longRun,
		AgentSignal{At: agentBaseMs + 60_000, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	resumed := testHeartbeat(t, testStore, user.ID, longRun, agentBaseMs+60_000+45*60_000)
	entry, err = testStore.GetTimeEntry(ctx, user.ID, *longStarted.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if *resumed.TimeEntryID == *longStarted.TimeEntryID {
		t.Fatal("time past the tool cap must start a new entry")
	}
	wantStoppedAt := agentBaseMs + 60_000 + testPolicy.ToolMaxMs
	if entry.StoppedAt == nil || *entry.StoppedAt != wantStoppedAt {
		t.Fatalf("expected the first entry capped at %d, got %+v", wantStoppedAt, entry)
	}
}

func TestAgentResumeDoesNotPauseBilledTail(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-resume@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	endedAt := agentBaseMs + 5*60_000
	testHeartbeat(t, testStore, user.ID, sessionID, endedAt)
	testStop(t, testStore, user.ID, sessionID, endedAt, "prompt_input_exit")

	// --resume right away: the interval up to ended_at is already billed, so
	// measuring the gap from the watermark would pause it a second time.
	resumed := endedAt + 60_000
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: resumed,
	}, testPolicy); err != nil {
		t.Fatalf("resume: %v", err)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt != nil {
		t.Fatalf("the entry must be running again: %+v", entry)
	}
}

func TestAgentResumeAfterPauseStartsNewEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-resume-pause@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	endedAt := agentBaseMs + 5*60_000
	testHeartbeat(t, testStore, user.ID, sessionID, endedAt)
	testStop(t, testStore, user.ID, sessionID, endedAt, "prompt_input_exit")

	resumedAt := endedAt + testIdleMs + 1
	resumed, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: resumedAt,
	}, testPolicy)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if *resumed.TimeEntryID == *started.TimeEntryID {
		t.Fatal("resuming after a pause must start a new entry")
	}
	first, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt != endedAt {
		t.Fatalf("the first entry must stay stopped before the pause: %+v", first)
	}
	second, err := testStore.GetTimeEntry(ctx, user.ID, *resumed.TimeEntryID)
	if err != nil {
		t.Fatalf("get second entry: %v", err)
	}
	if second.StartedAt != resumedAt || second.StoppedAt != nil {
		t.Fatalf("the resumed entry must start after the pause: %+v", second)
	}
}

func TestAgentMidnightGapSplitsEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-midnight@test.local")
	ctx := context.Background()

	// UTC+3: local midnight is 21:00 UTC, so 23:30 -> 00:30 local crosses it.
	offset := 180
	sessionID := uuid.NewString()
	evening := time.Date(2026, 7, 1, 20, 25, 0, 0, time.UTC).UnixMilli()
	started, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: evening, TZOffsetMin: &offset,
	}, testPolicy)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	lastEvening := time.Date(2026, 7, 1, 20, 30, 0, 0, time.UTC).UnixMilli()
	// The entry opens on this signal, back-dated to the start, so it is the
	// signal rather than the start that reports which row the evening went into.
	started = testHeartbeat(t, testStore, user.ID, sessionID, lastEvening)

	morning := time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC).UnixMilli()
	session := testHeartbeat(t, testStore, user.ID, sessionID, morning)
	if *session.TimeEntryID == *started.TimeEntryID {
		t.Fatal("a pause across the local midnight must open a new entry")
	}
	if session.Status != agentStatusActive {
		t.Fatalf("the session must stay active after the cut: %+v", session)
	}
	first, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt != lastEvening {
		t.Fatalf("the evening entry must end at the last activity: %+v", first)
	}
	second, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get second entry: %v", err)
	}
	if second.StartedAt != morning {
		t.Fatalf("unexpected morning entry: %+v", second)
	}

	// Reconciliation still owns the new entry, which is the point of keeping the
	// session active through the cut.
	closed, err := testStore.ReconcileAgentSessions(ctx, morning+testGraceMs+1000, testGraceMs)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected the session to be reconciled, got %d", closed)
	}
}

func TestAgentIdleGapSplitsWithoutTimezone(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-notz@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	evening := time.Date(2026, 7, 1, 20, 30, 0, 0, time.UTC).UnixMilli()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, evening)
	lastEvening := evening + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastEvening)
	morning := time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC).UnixMilli()
	session := testHeartbeat(t, testStore, user.ID, sessionID, morning)
	if *session.TimeEntryID == *started.TimeEntryID {
		t.Fatal("an idle gap must split even without a known timezone")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != lastEvening {
		t.Fatalf("the first entry must stop before the pause: %+v", entry)
	}
}

func TestAgentLongPauseSplitsEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-maxpause@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)
	// A long pause follows the same rule as every other idle gap.
	afterNight := lastActive + 5*60*60*1000
	session := testHeartbeat(t, testStore, user.ID, sessionID, afterNight)
	if *session.TimeEntryID == *started.TimeEntryID {
		t.Fatal("a long pause must open a new entry")
	}
	first, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt != lastActive {
		t.Fatalf("unexpected first entry: %+v", first)
	}
}

func TestAgentConcurrentHeartbeats(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-concurrent@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	var waitGroup sync.WaitGroup
	for index := int64(0); index < 4; index++ {
		waitGroup.Add(1)
		go func(offset int64) {
			defer waitGroup.Done()
			if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
				AgentSignal{At: agentBaseMs + 30_000 + offset}, testPolicy); err != nil {
				t.Errorf("heartbeat: %v", err)
			}
		}(index * 10)
	}
	waitGroup.Wait()

	session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if *session.TimeEntryID != *started.TimeEntryID {
		t.Fatal("concurrent heartbeats must not fork the entry")
	}
	if session.LastHeartbeatAt != agentBaseMs+30_030 {
		t.Fatalf("the watermark must hold the newest signal, got %d", session.LastHeartbeatAt)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentStopWithinIdleUsesEndedAt(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-stop@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	endedAt := agentBaseMs + 120_000
	session := testStop(t, testStore, user.ID, sessionID, endedAt, "prompt_input_exit")
	if session.Status != agentStatusClosed || session.EndedAt == nil || *session.EndedAt != endedAt {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.EndReason == nil || *session.EndReason != "prompt_input_exit" {
		t.Fatalf("unexpected end reason: %+v", session)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != endedAt {
		t.Fatalf("unexpected entry stop: %+v", entry)
	}

	// A replayed stop is a no-op and keeps the original end.
	again := testStop(t, testStore, user.ID, sessionID, endedAt+500_000, "other")
	if *again.EndedAt != endedAt || *again.EndReason != "prompt_input_exit" {
		t.Fatalf("second stop must not change anything: %+v", again)
	}
}

func TestAgentStopTrimsTrailingIdle(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-trim@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)

	// The stop arrives long after the last activity (terminal left open overnight).
	session := testStop(t, testStore, user.ID, sessionID, lastActive+testIdleMs+1, "other")
	if *session.EndedAt != lastActive {
		t.Fatalf("expected the end trimmed to the last heartbeat, got %+v", session)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != lastActive {
		t.Fatalf("unexpected entry stop: %+v", entry)
	}
}

// A tool call is the one gap that is known to be work: PostToolUse fires only when the
// tool returns, so a long Bash or Task looks like an empty chair from the server. The
// stop path has to apply the same rule the heartbeat path does, or a session whose last
// act is a twenty-minute test run bills those minutes as zero while the identical gap
// followed by a heartbeat bills all of them.
func TestAgentStopBillsTrailingToolRun(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-tool-stop@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	toolStartedAt := agentBaseMs + 60_000
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
		AgentSignal{At: toolStartedAt, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}

	// The tool runs for twenty minutes and the process exits without a PostToolUse.
	toolRunMs := int64(20 * 60 * 1000)
	session := testStop(t, testStore, user.ID, sessionID, toolStartedAt+toolRunMs, "other")
	if *session.EndedAt != toolStartedAt+toolRunMs {
		t.Fatalf("expected the tool run billed to the cap, got end %d (tool started at %d)",
			*session.EndedAt, toolStartedAt)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != toolStartedAt+toolRunMs {
		t.Fatalf("unexpected entry stop: %+v", entry)
	}
}

// The cap still applies: a gap longer than ToolMaxMs is billed up to it, not in full.
func TestAgentStopCapsTrailingToolRun(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-tool-cap@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	toolStartedAt := agentBaseMs + 60_000
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
		AgentSignal{At: toolStartedAt, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}

	session := testStop(t, testStore, user.ID, sessionID, toolStartedAt+3*testPolicy.ToolMaxMs, "other")
	if *session.EndedAt != toolStartedAt+testPolicy.ToolMaxMs {
		t.Fatalf("expected the end capped at ToolMaxMs, got %d", *session.EndedAt)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	// The session row and the entry are separate writes; the cap only means something
	// if the entry followed it.
	if entry.StoppedAt == nil || *entry.StoppedAt != toolStartedAt+testPolicy.ToolMaxMs {
		t.Fatalf("expected the entry capped too, got %+v", entry.StoppedAt)
	}
}

// The PWA stamps updated_at with the browser's clock, this process with the server's.
// A browser running ahead therefore writes a version the agent's own stop would look
// older than - and a stop the client discards leaves a timer that runs forever, because
// the session is closed and reconciliation only walks active ones.
func TestAgentWritesStayNewerThanAClientEdit(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-clock@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}

	// The browser edits the row with a clock two minutes ahead of the server.
	ahead := time.Now().UnixMilli() + 2*60*1000
	edited := entry
	edited.Description = "renamed in the app"
	edited.UpdatedAt = ahead
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{edited}},
	}); err != nil {
		t.Fatalf("client edit: %v", err)
	}

	testStop(t, testStore, user.ID, sessionID, agentBaseMs+60_000, "other")

	stored, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if stored.StoppedAt == nil {
		t.Fatal("the agent stop must have closed the entry")
	}
	if stored.UpdatedAt <= ahead {
		t.Fatalf("the agent write must outrank the client edit: stored %d, client %d", stored.UpdatedAt, ahead)
	}
}

// A stop carries the moment work ended, so StopAgentSession can measure a trailing tool
// run and bill it up to the cap. Reconciliation knows no such moment - only when the
// job happened to run, which is at least the grace period later and after a restart can
// be days. Extending the entry from there would invent time nobody worked and make a
// killed session worth more than one that stopped cleanly, so it closes at the last
// heartbeat whatever the last signal was.
func TestAgentReconcileNeverBillsPastTheLastHeartbeat(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	for _, lastKind := range []string{"", AgentKindToolStart} {
		name := "heartbeat"
		if lastKind != "" {
			name = lastKind
		}
		t.Run(name, func(t *testing.T) {
			user := testUser(t, testStore, "agent-reconcile-"+name+"@test.local")
			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
			lastSignalAt := agentBaseMs + 60_000
			if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
				AgentSignal{At: lastSignalAt, Kind: lastKind}, testPolicy); err != nil {
				t.Fatalf("signal: %v", err)
			}

			// The machine is off overnight, so the job runs many hours later.
			closed, err := testStore.ReconcileAgentSessions(ctx, lastSignalAt+12*60*60*1000, testGraceMs)
			if err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if closed != 1 {
				t.Fatalf("expected one session closed, got %d", closed)
			}

			entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
			if err != nil {
				t.Fatalf("get entry: %v", err)
			}
			if entry.StoppedAt == nil || *entry.StoppedAt != lastSignalAt {
				t.Fatalf("expected the entry closed at the last heartbeat (%d), got %+v",
					lastSignalAt, entry.StoppedAt)
			}
		})
	}
}

func TestAgentReopenKeepsSameEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reopen@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	testStop(t, testStore, user.ID, sessionID, agentBaseMs+60_000, "other")

	reopenedAt := agentBaseMs + 120_000
	session := testHeartbeat(t, testStore, user.ID, sessionID, reopenedAt)
	if session.Status != agentStatusActive || session.EndedAt != nil || session.EndReason != nil {
		t.Fatalf("expected a revived session, got %+v", session)
	}
	if *session.TimeEntryID != *started.TimeEntryID {
		t.Fatal("reviving must continue the session's own entry, not open a second one")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StartedAt != agentBaseMs || entry.StoppedAt != nil {
		t.Fatalf("the revived entry must run again from its original start: %+v", entry)
	}
}

func TestAgentStaleHeartbeatDoesNotReopen(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-stale@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	endedAt := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, endedAt)
	testStop(t, testStore, user.ID, sessionID, endedAt, "clear")

	// The async heartbeat hook loses the race with the synchronous SessionEnd and
	// is delivered afterwards, carrying its original timestamp.
	session := testHeartbeat(t, testStore, user.ID, sessionID, endedAt-5_000)
	if session.Status != agentStatusClosed {
		t.Fatalf("a stale heartbeat must not revive a closed session: %+v", session)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != endedAt {
		t.Fatalf("the entry must stay closed: %+v", entry)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentLateStopClosesAtWatermark(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-late-stop@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)
	testStop(t, testStore, user.ID, sessionID, lastActive+testIdleMs+1, "other")

	// Work resumes inside the idle threshold, so it is still the same segment.
	session := testHeartbeat(t, testStore, user.ID, sessionID, lastActive+60_000)
	if *session.TimeEntryID != *started.TimeEntryID {
		t.Fatal("the revived session must continue its own entry")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt != nil {
		t.Fatalf("expected a running entry, got %+v", entry)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentManualStopStartsNewEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-manual-stop@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	manualStop := agentBaseMs + 30_000
	entry.StoppedAt = &manualStop
	pushEntry(t, testStore, user.ID, entry)

	// The agent keeps working: its time has to land somewhere, so a new entry opens.
	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	if session.TimeEntryID == nil || *session.TimeEntryID == entry.ID {
		t.Fatalf("expected a fresh entry after the manual stop, got %+v", session.TimeEntryID)
	}
	if session.EntryUserNamed {
		t.Fatal("a fresh entry was never named by the user")
	}
	fresh, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get fresh entry: %v", err)
	}
	if fresh.StartedAt != agentBaseMs+60_000 || fresh.StoppedAt != nil {
		t.Fatalf("unexpected fresh entry: %+v", fresh)
	}

	// Two more signals must reuse that entry: losing time_entry_id here would
	// open a new row on every heartbeat.
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+90_000)
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+120_000)
	if count := countUserEntries(t, testStore, user.ID); count != 2 {
		t.Fatalf("expected exactly two entries, got %d", count)
	}

	stopped, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get stopped entry: %v", err)
	}
	if stopped.StoppedAt == nil || *stopped.StoppedAt != manualStop {
		t.Fatalf("the manual stop must survive: %+v", stopped)
	}
}

func TestAgentUserRenameKeepsSameEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-rename@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Description = "Refactoring the sync engine"
	pushEntry(t, testStore, user.ID, entry)

	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	if *session.TimeEntryID != entry.ID {
		t.Fatal("a renamed entry must be adopted, not abandoned")
	}
	if !session.EntryUserNamed {
		t.Fatalf("expected the entry to be marked user-named: %+v", session)
	}
	after, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.Description != "Refactoring the sync engine" {
		t.Fatalf("the user's name must win: %q", after.Description)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentUserNamedEntryNeverRenamed(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-usernamed@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Description = "My own name"
	pushEntry(t, testStore, user.ID, entry)
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+30_000)

	// A late start replay with fresh metadata, then a task assignment: neither
	// may take the name back.
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: agentBaseMs + 60_000, GitBranch: "feature", Cwd: "/tmp/other",
	}, testPolicy); err != nil {
		t.Fatalf("late start: %v", err)
	}
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-1", "Something"); err != nil {
		t.Fatalf("set task: %v", err)
	}
	after, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.Description != "My own name" {
		t.Fatalf("the user's name must survive, got %q", after.Description)
	}
}

func TestAgentNewEntryResetsUserNamedFlag(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reset@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Description = "Renamed by hand"
	stoppedAt := agentBaseMs + 30_000
	entry.StoppedAt = &stoppedAt
	pushEntry(t, testStore, user.ID, entry)

	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	if session.EntryUserNamed {
		t.Fatal("the fresh entry must not inherit the user-named flag")
	}
	fresh, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get fresh entry: %v", err)
	}
	if fresh.Description != "Claude Code #"+AgentSessionTag(sessionID) {
		t.Fatalf("the fresh entry must carry the automatic name, got %q", fresh.Description)
	}
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-77", ""); err != nil {
		t.Fatalf("set task: %v", err)
	}
	renamed, err := testStore.GetTimeEntry(ctx, user.ID, fresh.ID)
	if err != nil {
		t.Fatalf("get renamed entry: %v", err)
	}
	if renamed.Description != "MT-77" {
		t.Fatalf("the fresh entry must still be renamable, got %q", renamed.Description)
	}
}

func TestAgentDeletedEntryNotResurrected(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-deleted@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	deletedAt := agentBaseMs + 10_000
	entry.DeletedAt = &deletedAt
	pushEntry(t, testStore, user.ID, entry)

	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	if session.TimeEntryID == nil || *session.TimeEntryID == entry.ID {
		t.Fatal("a deleted entry must not be picked back up")
	}
	if _, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the tombstone must stay a tombstone, got %v", err)
	}
}

func TestAgentMigratedSessionKeepsEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-migrated@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// A session that existed before ownership tracking: server_seq unknown.
	if _, err := testStore.db.ExecContext(ctx,
		"UPDATE agent_sessions SET entry_server_seq = NULL WHERE id = ?", sessionID); err != nil {
		t.Fatalf("simulate a pre-migration session: %v", err)
	}

	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	if *session.TimeEntryID != *started.TimeEntryID {
		t.Fatal("a session with unknown ownership must keep its entry, not abandon it")
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentDetachClosesOrphanEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-orphan@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	session.LastHeartbeatAt = agentBaseMs + 60_000

	// Detaching is the one branch that could leave a running row behind with
	// nobody left to close it, so the guard is exercised directly.
	transaction, err := testStore.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer transaction.Rollback()
	if err := detachAgentEntry(ctx, transaction, user.ID, &session, agentBaseMs+120_000); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != agentBaseMs+60_000 {
		t.Fatalf("a detached running entry must be closed at the last activity, got %+v", entry)
	}
}

func TestAgentHeartbeatAutoCreatesSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-auto@test.local")

	sessionID := uuid.NewString()
	session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs)
	if session.Status != agentStatusActive || session.StartedAt != agentBaseMs || session.TimeEntryID == nil {
		t.Fatalf("expected an implicitly created session, got %+v", session)
	}
	if session.Source != defaultAgentSource {
		t.Fatalf("unexpected source: %+v", session)
	}
}

func TestAgentReconcileClosesStaleSessions(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reconcile@test.local")
	ctx := context.Background()

	staleID := uuid.NewString()
	stale := startWorkingTestAgentSession(t, testStore, user.ID, staleID, agentBaseMs)
	lastActivity := agentBaseMs + 60_000
	stale = testHeartbeat(t, testStore, user.ID, staleID, lastActivity)
	freshID := uuid.NewString()
	now := lastActivity + testGraceMs + 60_000
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{SessionID: freshID, StartedAt: now - 1000}, testPolicy); err != nil {
		t.Fatalf("start fresh: %v", err)
	}

	closed, err := testStore.ReconcileAgentSessions(ctx, now, testGraceMs)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected 1 closed session, got %d", closed)
	}

	staleSession, err := testStore.GetAgentSession(ctx, user.ID, staleID)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if staleSession.Status != agentStatusClosed || *staleSession.EndedAt != lastActivity {
		t.Fatalf("unexpected stale session: %+v", staleSession)
	}
	if *staleSession.EndReason != AgentEndReasonStale {
		t.Fatalf("unexpected end reason: %+v", staleSession)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *stale.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != lastActivity {
		t.Fatalf("stale entry must stop at the last heartbeat, got %+v", entry)
	}

	freshSession, err := testStore.GetAgentSession(ctx, user.ID, freshID)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if freshSession.Status != agentStatusActive {
		t.Fatalf("fresh session must stay active, got %+v", freshSession)
	}
}

func TestAgentSessionForeignIDRejected(t *testing.T) {
	testStore := openTestStore(t)
	owner := testUser(t, testStore, "agent-owner@test.local")
	intruder := testUser(t, testStore, "agent-intruder@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, owner.ID, sessionID, agentBaseMs)

	if _, err := testStore.StartAgentSession(ctx, intruder.ID, AgentStart{SessionID: sessionID, StartedAt: agentBaseMs}, testPolicy); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput on start, got %v", err)
	}
	if _, err := testStore.AgentHeartbeat(ctx, intruder.ID, sessionID, AgentSignal{At: agentBaseMs}, testPolicy); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput on heartbeat, got %v", err)
	}
	if _, err := testStore.StopAgentSession(ctx, intruder.ID, sessionID, "other", AgentSignal{At: agentBaseMs}, testPolicy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound on stop, got %v", err)
	}

	session, err := testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil || session.Status != agentStatusActive {
		t.Fatalf("owner session must be untouched: %+v (%v)", session, err)
	}
}

func TestAgentManualEntryStopWins(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-manual@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	// The user stops the agent's running entry by hand in the PWA.
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	manualStop := agentBaseMs + 30_000
	entry.StoppedAt = &manualStop
	pushEntry(t, testStore, user.ID, entry)

	testStop(t, testStore, user.ID, sessionID, agentBaseMs+90_000, "other")
	after, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.StoppedAt == nil || *after.StoppedAt != manualStop {
		t.Fatalf("the manual stop must win, got %+v", after)
	}
}

func TestAgentStartValidatesProject(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-project@test.local")
	ctx := context.Background()

	projectID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{{ID: projectID, Name: "Agent", Color: "#123456", CreatedAt: 1, UpdatedAt: 1}},
	}}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	filed := uuid.NewString()
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: filed, StartedAt: agentBaseMs, ProjectID: &projectID,
	}, testPolicy); err != nil {
		t.Fatalf("start: %v", err)
	}
	// The project reaches the entry through the session, so it survives the entry
	// being opened by the first signal rather than by the start.
	session := testHeartbeat(t, testStore, user.ID, filed, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.ProjectID == nil || *entry.ProjectID != projectID {
		t.Fatalf("entry must carry the project, got %+v", entry)
	}

	unknown := uuid.NewString()
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: uuid.NewString(), StartedAt: agentBaseMs, ProjectID: &unknown,
	}, testPolicy); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for an unknown project, got %v", err)
	}
}

// A project chosen mid-session has to reach the session row, not only the entry that
// is running: the next entry after any pause is opened from the session and would
// otherwise land under no project.
func TestSetAgentSessionProjectCarriesToTheNextEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-session-project@test.local")
	ctx := context.Background()

	projectID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{{ID: projectID, Name: "WorkTime", Color: "#123456", CreatedAt: 1, UpdatedAt: 1}},
	}}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if err := testStore.SetAgentSessionProject(ctx, user.ID, sessionID, &projectID); err != nil {
		t.Fatalf("set session project: %v", err)
	}

	afterIdle := agentBaseMs + 5*60*60*1000
	second := testHeartbeat(t, testStore, user.ID, sessionID, afterIdle)
	if *second.TimeEntryID == *started.TimeEntryID {
		t.Fatal("expected the long pause to open a second entry")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *second.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.ProjectID == nil || *entry.ProjectID != projectID {
		t.Fatalf("the next entry of the session must carry the project, got %+v", entry)
	}

	unknown := uuid.NewString()
	if err := testStore.SetAgentSessionProject(ctx, user.ID, sessionID, &unknown); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for an unknown project, got %v", err)
	}
	if err := testStore.SetAgentSessionProject(ctx, user.ID, uuid.NewString(), &projectID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for an unknown session, got %v", err)
	}
}

func TestSyncAcceptedCurrentEntryProjectCarriesToAgentSession(t *testing.T) {
	testStore := openTestStore(t)
	owner := testUser(t, testStore, "agent-sync-project@test.local")
	intruder := testUser(t, testStore, "agent-sync-project-intruder@test.local")
	ctx := t.Context()

	firstProjectID := uuid.NewString()
	secondProjectID := uuid.NewString()
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{
			{ID: firstProjectID, Name: "First", Color: "#123456", CreatedAt: 1, UpdatedAt: 1},
			{ID: secondProjectID, Name: "Second", Color: "#654321", CreatedAt: 1, UpdatedAt: 1},
		},
	}}); err != nil {
		t.Fatalf("create projects: %v", err)
	}

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, owner.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, owner.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get current entry: %v", err)
	}

	accepted := entry
	accepted.ProjectID = &firstProjectID
	accepted.UpdatedAt++
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{accepted}},
	}); err != nil {
		t.Fatalf("push accepted project: %v", err)
	}
	session, err := testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after accepted project: %v", err)
	}
	if session.ProjectID == nil || *session.ProjectID != firstProjectID {
		t.Fatalf("accepted project did not reach session: %+v", session)
	}
	if !session.EntryUserEdited || session.EntryUserNamed {
		t.Fatalf("accepted project edit did not persist the outside-edit marker: %+v", session)
	}

	stale := entry
	stale.ProjectID = &secondProjectID
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{stale}},
	}); err != nil {
		t.Fatalf("push stale project: %v", err)
	}
	session, err = testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after stale project: %v", err)
	}
	if session.ProjectID == nil || *session.ProjectID != firstProjectID {
		t.Fatalf("refused LWW row changed session project: %+v", session)
	}

	// Make the first segment visible at the 30-second boundary, then prove the
	// accepted project is what creates the next segment after idle.
	lastActive := agentBaseMs + agentZeroMinuteMs
	testHeartbeat(t, testStore, owner.ID, sessionID, lastActive)
	resumedAt := lastActive + testIdleMs + 1
	resumed := testHeartbeat(t, testStore, owner.ID, sessionID, resumedAt)
	secondEntry, err := testStore.GetTimeEntry(ctx, owner.ID, *resumed.TimeEntryID)
	if err != nil {
		t.Fatalf("get resumed entry: %v", err)
	}
	if secondEntry.ProjectID == nil || *secondEntry.ProjectID != firstProjectID {
		t.Fatalf("next segment did not inherit accepted project: %+v", secondEntry)
	}

	// An accepted edit to an older segment is not allowed to retarget the session:
	// only the entry currently named by time_entry_id owns future defaults.
	oldEntry, err := testStore.GetTimeEntry(ctx, owner.ID, entry.ID)
	if err != nil {
		t.Fatalf("get old entry: %v", err)
	}
	oldEntry.ProjectID = &secondProjectID
	oldEntry.UpdatedAt++
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{oldEntry}},
	}); err != nil {
		t.Fatalf("push old segment project: %v", err)
	}
	session, err = testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after old segment edit: %v", err)
	}
	if session.ProjectID == nil || *session.ProjectID != firstProjectID {
		t.Fatalf("old segment changed current session project: %+v", session)
	}

	// NULL is a real accepted value: clearing the current entry must clear the
	// session and therefore the next segment as well.
	secondEntry.ProjectID = nil
	secondEntry.UpdatedAt++
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{secondEntry}},
	}); err != nil {
		t.Fatalf("clear current project: %v", err)
	}
	session, err = testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after clearing project: %v", err)
	}
	if session.ProjectID != nil {
		t.Fatalf("accepted null project did not clear session: %+v", session)
	}

	secondLastActive := resumedAt + agentZeroMinuteMs
	testHeartbeat(t, testStore, owner.ID, sessionID, secondLastActive)
	third := testHeartbeat(t, testStore, owner.ID, sessionID, secondLastActive+testIdleMs+1)
	thirdEntry, err := testStore.GetTimeEntry(ctx, owner.ID, *third.TimeEntryID)
	if err != nil {
		t.Fatalf("get segment after cleared project: %v", err)
	}
	if thirdEntry.ProjectID != nil {
		t.Fatalf("segment after cleared project was filed unexpectedly: %+v", thirdEntry)
	}

	// A foreign account cannot claim the server-owned entry/session relationship.
	foreign := thirdEntry
	foreign.ProjectID = &secondProjectID
	foreign.UpdatedAt++
	if _, err := testStore.Sync(ctx, intruder.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{foreign}},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("foreign project push returned %v, want ErrInvalidInput", err)
	}
	session, err = testStore.GetAgentSession(ctx, owner.ID, sessionID)
	if err != nil {
		t.Fatalf("get owner session after foreign push: %v", err)
	}
	if session.ProjectID != nil {
		t.Fatalf("foreign push changed owner session project: %+v", session)
	}
}

func TestSyncBulkGroupMetadataCarriesToEveryCurrentAgentSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-bulk-group@test.local")
	ctx := t.Context()

	projectID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{Projects: []Project{{
		ID: projectID, Name: "WorkTime", Color: "#123456", CreatedAt: 1, UpdatedAt: 1,
	}}}}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sessionIDs := []string{uuid.NewString(), uuid.NewString()}
	entries := make([]TimeEntry, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
		if _, err := testStore.SetAgentTask(ctx, user.ID,
			AgentTaskSelector{SessionID: sessionID}, "GH-8", "Edit a whole task group in one dialog"); err != nil {
			t.Fatalf("set task for %s: %v", sessionID, err)
		}
		entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
		if err != nil {
			t.Fatalf("get entry for %s: %v", sessionID, err)
		}
		entry.Description = "Shared manual correction"
		entry.ProjectID = &projectID
		entry.Tags = TagList{"development", "review"}
		entry.UpdatedAt++
		entries = append(entries, entry)
	}

	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: entries}}); err != nil {
		t.Fatalf("push bulk group edit: %v", err)
	}
	for _, sessionID := range sessionIDs {
		session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
		if err != nil {
			t.Fatalf("get session %s: %v", sessionID, err)
		}
		if session.ProjectID == nil || *session.ProjectID != projectID || !session.EntryUserEdited || !session.EntryUserNamed {
			t.Fatalf("bulk edit was not adopted by session %s: %+v", sessionID, session)
		}

		lastActive := agentBaseMs + agentZeroMinuteMs
		testHeartbeat(t, testStore, user.ID, sessionID, lastActive)
		resumed := testHeartbeat(t, testStore, user.ID, sessionID, lastActive+testIdleMs+1)
		next, err := testStore.GetTimeEntry(ctx, user.ID, *resumed.TimeEntryID)
		if err != nil {
			t.Fatalf("get resumed entry for %s: %v", sessionID, err)
		}
		if next.ProjectID == nil || *next.ProjectID != projectID {
			t.Fatalf("resumed entry for %s did not inherit project: %+v", sessionID, next)
		}
	}
}

func TestSyncAgentUserNamedUsesExactAutomaticDescription(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-user-named-exact@test.local")
	ctx := t.Context()
	sessionID := uuid.NewString()
	session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if _, err := testStore.SetAgentTask(ctx, user.ID,
		AgentTaskSelector{SessionID: sessionID}, "GH-8", "Grouped edit"); err != nil {
		t.Fatalf("set task: %v", err)
	}

	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Tags = TagList{"review"}
	entry.UpdatedAt++
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{entry}}}); err != nil {
		t.Fatalf("push exact automatic description: %v", err)
	}
	afterExact, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get exact session: %v", err)
	}
	if afterExact.EntryUserNamed || !afterExact.EntryUserEdited {
		t.Fatalf("exact automatic description ownership = %+v", afterExact)
	}

	entry, err = testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry after exact edit: %v", err)
	}
	// This remains in the same client-side task group after case/space
	// normalisation, but ownership is intentionally exact on the server.
	entry.Description = "gh-8  grouped edit"
	entry.UpdatedAt++
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{entry}}}); err != nil {
		t.Fatalf("push normalised variant: %v", err)
	}
	afterVariant, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get variant session: %v", err)
	}
	if !afterVariant.EntryUserNamed {
		t.Fatalf("non-exact normalised variant did not become user-named: %+v", afterVariant)
	}
}

func TestAcceptedAgentEntryEditsSurviveImmediateCloseResumeAndSecondShortStop(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*TimeEntry, string)
		wantNamed bool
		verify    func(*testing.T, TimeEntry, string)
	}{
		{
			name: "project",
			mutate: func(entry *TimeEntry, projectID string) {
				entry.ProjectID = &projectID
			},
			verify: func(t *testing.T, entry TimeEntry, projectID string) {
				if entry.ProjectID == nil || *entry.ProjectID != projectID {
					t.Fatalf("project edit was lost: %+v", entry)
				}
			},
		},
		{
			name: "tags",
			mutate: func(entry *TimeEntry, _ string) {
				entry.Tags = TagList{"review"}
			},
			verify: func(t *testing.T, entry TimeEntry, _ string) {
				if !reflect.DeepEqual(entry.Tags, TagList{"review"}) {
					t.Fatalf("tags edit was lost: %+v", entry)
				}
			},
		},
		{
			name: "bounds",
			mutate: func(entry *TimeEntry, _ string) {
				entry.StartedAt = agentBaseMs - 1_000
			},
			verify: func(t *testing.T, entry TimeEntry, _ string) {
				if entry.StartedAt != agentBaseMs-1_000 {
					t.Fatalf("bounds edit was lost: %+v", entry)
				}
			},
		},
		{
			name: "manual description",
			mutate: func(entry *TimeEntry, _ string) {
				entry.Description = "Reviewed short work"
			},
			wantNamed: true,
			verify: func(t *testing.T, entry TimeEntry, _ string) {
				if entry.Description != "Reviewed short work" {
					t.Fatalf("manual description was lost: %+v", entry)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-durable-edit-"+strings.ReplaceAll(test.name, " ", "-")+"@test.local")
			ctx := t.Context()
			projectID := uuid.NewString()
			if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
				Projects: []Project{{
					ID: projectID, Name: "Durable", Color: "#123456", CreatedAt: 1, UpdatedAt: 1,
				}},
			}}); err != nil {
				t.Fatalf("create project: %v", err)
			}

			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
			entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
			if err != nil {
				t.Fatalf("get running entry: %v", err)
			}
			test.mutate(&entry, projectID)
			pushEntry(t, testStore, user.ID, entry)

			editedSession, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
			if err != nil {
				t.Fatalf("get session after edit: %v", err)
			}
			if !editedSession.EntryUserEdited || editedSession.EntryUserNamed != test.wantNamed {
				t.Fatalf("outside-edit flags are not durable before heartbeat: %+v", editedSession)
			}

			firstStop := testStop(t, testStore, user.ID, sessionID, agentBaseMs+1_000, "session_end")
			if !firstStop.EntryUserEdited || firstStop.EntryUserNamed != test.wantNamed {
				t.Fatalf("close lost outside-edit flags: %+v", firstStop)
			}
			resumed := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+2_000)
			if resumed.TimeEntryID == nil || *resumed.TimeEntryID != entry.ID || !resumed.EntryUserEdited {
				t.Fatalf("resume did not adopt the edited row: %+v", resumed)
			}
			testStop(t, testStore, user.ID, sessionID, agentBaseMs+3_000, "session_end")

			stored, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
			if err != nil {
				t.Fatalf("second short stop discarded meaningful entry: %v", err)
			}
			test.verify(t, stored, projectID)
		})
	}
}

func TestRejectedStaleAgentEntryEditDoesNotMarkSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-stale-edit-marker@test.local")
	ctx := t.Context()
	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get running entry: %v", err)
	}
	entry.Tags = TagList{"stale"}
	entry.UpdatedAt--
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{entry}},
	}); err != nil {
		t.Fatalf("push stale edit: %v", err)
	}
	session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after stale edit: %v", err)
	}
	if session.EntryUserEdited || session.EntryUserNamed {
		t.Fatalf("rejected stale edit marked the session: %+v", session)
	}

	testStop(t, testStore, user.ID, sessionID, agentBaseMs+1_000, "session_end")
	if _, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale edit unexpectedly protected technical row: %v", err)
	}
}

func TestSyncAgentProjectPropagationIsAtomicWithEntryUpsert(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-sync-project-atomic@test.local")
	ctx := t.Context()
	projectID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{{
			ID: projectID, Name: "Atomic", Color: "#123456", CreatedAt: 1, UpdatedAt: 1,
		}},
	}}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get current entry: %v", err)
	}
	var beforeSeq int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&beforeSeq); err != nil {
		t.Fatalf("read cursor: %v", err)
	}

	if _, err := testStore.db.Exec(`
		CREATE TRIGGER reject_agent_project
		BEFORE UPDATE OF project_id ON agent_sessions
		WHEN NEW.id = '` + sessionID + `'
		BEGIN
			SELECT RAISE(ABORT, 'forced propagation failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	entry.ProjectID = &projectID
	entry.UpdatedAt++
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{entry}},
	}); err == nil {
		t.Fatal("sync succeeded despite forced session propagation failure")
	}

	stored, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry after rollback: %v", err)
	}
	if stored.ProjectID != nil {
		t.Fatalf("entry upsert escaped failed propagation transaction: %+v", stored)
	}
	session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session after rollback: %v", err)
	}
	if session.ProjectID != nil {
		t.Fatalf("session project changed despite rollback: %+v", session)
	}
	if session.EntryUserEdited || session.EntryUserNamed {
		t.Fatalf("outside-edit markers changed despite rollback: %+v", session)
	}
	var afterSeq int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&afterSeq); err != nil {
		t.Fatalf("read cursor after rollback: %v", err)
	}
	if afterSeq != beforeSeq {
		t.Fatalf("failed propagation advanced cursor from %d to %d", beforeSeq, afterSeq)
	}
}

func TestSetAgentTaskRenamesAllSessionEntries(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	// A pause leaves the session with two entries; both belong to the same task,
	// and half the work would keep the technical name if only the current entry
	// were renamed.
	afterIdle := agentBaseMs + 60_000 + 5*60*60*1000
	second := testHeartbeat(t, testStore, user.ID, sessionID, afterIdle)
	if *second.TimeEntryID == *started.TimeEntryID {
		t.Fatal("expected two segments for this test")
	}

	result, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{}, "MT-12345", "Slow AMaaS quote creation")
	if err != nil {
		t.Fatalf("set task: %v", err)
	}
	if result.RenamedEntries != 2 {
		t.Fatalf("expected both segments renamed, got %d", result.RenamedEntries)
	}
	if result.Session.TaskKey != "MT-12345" || result.Session.TaskTitle != "Slow AMaaS quote creation" {
		t.Fatalf("unexpected session: %+v", result.Session)
	}
	for _, entryID := range []string{*started.TimeEntryID, *second.TimeEntryID} {
		entry, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
		if err != nil {
			t.Fatalf("get entry: %v", err)
		}
		if entry.Description != "MT-12345 Slow AMaaS quote creation" {
			t.Fatalf("unexpected description: %q", entry.Description)
		}
	}

	// The session keeps its entry: a rename must not cost it ownership.
	after := testHeartbeat(t, testStore, user.ID, sessionID, afterIdle+60_000)
	if *after.TimeEntryID != *second.TimeEntryID {
		t.Fatal("the session must keep the entry it renamed")
	}
}

func TestSetAgentTaskWithoutTitleUsesKey(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-key@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-9", ""); err != nil {
		t.Fatalf("set task: %v", err)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Description != "MT-9" {
		t.Fatalf("unexpected description: %q", entry.Description)
	}
}

func TestSetAgentTaskSkipsUserNamedEntries(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-user@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	entry.Description = "Chosen by the user"
	pushEntry(t, testStore, user.ID, entry)

	result, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-1", "Title")
	if err != nil {
		t.Fatalf("set task: %v", err)
	}
	if result.RenamedEntries != 0 {
		t.Fatalf("expected nothing renamed, got %d", result.RenamedEntries)
	}
	after, err := testStore.GetTimeEntry(ctx, user.ID, entry.ID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if after.Description != "Chosen by the user" {
		t.Fatalf("the user's name must survive: %q", after.Description)
	}
}

func TestSetAgentTaskCwdRejectsSoleDifferentSessionWithoutMutation(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-cwd-mismatch@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startTestAgentSessionAtCwd(t, testStore, user.ID, sessionID, agentBaseMs, "/projects/alpha")
	beforeSession := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs)
	beforeEntry, err := testStore.GetTimeEntry(ctx, user.ID, *beforeSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}

	_, err = testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "/projects/beta"}, "B-123", "Other project")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), "/projects/beta") || !strings.Contains(err.Error(), sessionID) {
		t.Fatalf("error must identify the requested cwd and active candidate: %v", err)
	}

	afterSession, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	afterEntry, err := testStore.GetTimeEntry(ctx, user.ID, *beforeSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry after rejection: %v", err)
	}
	if !reflect.DeepEqual(afterSession, beforeSession) || !reflect.DeepEqual(afterEntry, beforeEntry) {
		t.Fatalf("a rejected selector must not mutate session or entry:\nsession before=%+v\nsession after=%+v\nentry before=%+v\nentry after=%+v",
			beforeSession, afterSession, beforeEntry, afterEntry)
	}
}

func TestSetAgentTaskCwdMatchesNormalizedSoleSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-cwd-normalized@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startTestAgentSessionAtCwd(t, testStore, user.ID, sessionID, agentBaseMs, `C:\Users\Dev\WorkTime\.`)
	result, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "c:/users/dev/worktime"}, "MT-1", "Title")
	if err != nil {
		t.Fatalf("set task by normalized cwd: %v", err)
	}
	if result.Session.ID != sessionID || result.Session.TaskKey != "MT-1" {
		t.Fatalf("unexpected selected session: %+v", result.Session)
	}
}

func TestSetAgentTaskCwdFiltersAllActiveSessions(t *testing.T) {
	t.Run("unique match", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-task-cwd-unique@test.local")
		ctx := context.Background()
		first := uuid.NewString()
		second := uuid.NewString()
		startTestAgentSessionAtCwd(t, testStore, user.ID, first, agentBaseMs, "/projects/alpha/./")
		startTestAgentSessionAtCwd(t, testStore, user.ID, second, agentBaseMs+1000, "/projects/beta")

		result, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "/PROJECTS/alpha"}, "MT-1", "")
		if err != nil {
			t.Fatalf("set task by unique cwd: %v", err)
		}
		if result.Session.ID != first {
			t.Fatalf("cwd selected %s, want %s", result.Session.ID, first)
		}
	})

	t.Run("no match lists all candidates", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-task-cwd-zero@test.local")
		ctx := context.Background()
		first := uuid.NewString()
		second := uuid.NewString()
		startTestAgentSessionAtCwd(t, testStore, user.ID, first, agentBaseMs, "/projects/alpha")
		startTestAgentSessionAtCwd(t, testStore, user.ID, second, agentBaseMs+1000, "/projects/beta")

		_, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "/projects/gamma"}, "MT-1", "")
		if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
			t.Fatalf("zero-match error must list all candidates, got %v", err)
		}
		for _, sessionID := range []string{first, second} {
			session, getErr := testStore.GetAgentSession(ctx, user.ID, sessionID)
			if getErr != nil || session.TaskKey != "" {
				t.Fatalf("zero-match call mutated %s: session=%+v err=%v", sessionID, session, getErr)
			}
		}
	})

	t.Run("multiple matches list only matches", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-task-cwd-multiple@test.local")
		ctx := context.Background()
		first := uuid.NewString()
		second := uuid.NewString()
		unrelated := uuid.NewString()
		startTestAgentSessionAtCwd(t, testStore, user.ID, first, agentBaseMs, "/projects/alpha")
		startTestAgentSessionAtCwd(t, testStore, user.ID, second, agentBaseMs+1000, "/PROJECTS/alpha/.")
		startTestAgentSessionAtCwd(t, testStore, user.ID, unrelated, agentBaseMs+2000, "/projects/beta")

		_, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "/projects/alpha"}, "MT-1", "")
		if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
			t.Fatalf("multiple-match error must list matching candidates, got %v", err)
		}
		if strings.Contains(err.Error(), unrelated) {
			t.Fatalf("multiple-match error must omit unrelated candidates: %v", err)
		}
		for _, sessionID := range []string{first, second, unrelated} {
			session, getErr := testStore.GetAgentSession(ctx, user.ID, sessionID)
			if getErr != nil || session.TaskKey != "" {
				t.Fatalf("ambiguous call mutated %s: session=%+v err=%v", sessionID, session, getErr)
			}
		}
	})
}

func TestSetAgentTaskExplicitSessionWinsOverCwd(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-explicit@test.local")
	ctx := context.Background()
	sessionID := uuid.NewString()
	startTestAgentSessionAtCwd(t, testStore, user.ID, sessionID, agentBaseMs, "/projects/alpha")

	result, err := testStore.SetAgentTask(ctx, user.ID,
		AgentTaskSelector{SessionID: sessionID, Cwd: "/projects/beta"}, "MT-1", "")
	if err != nil {
		t.Fatalf("set task by explicit session: %v", err)
	}
	if result.Session.ID != sessionID {
		t.Fatalf("selected %s, want explicit %s", result.Session.ID, sessionID)
	}
}

func TestSetAgentTaskAmbiguousSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-ambiguous@test.local")
	ctx := context.Background()

	first := uuid.NewString()
	second := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, user.ID, first, agentBaseMs)
	startWorkingTestAgentSession(t, testStore, user.ID, second, agentBaseMs+1000)

	_, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{}, "MT-1", "Title")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if !strings.Contains(err.Error(), first) || !strings.Contains(err.Error(), second) {
		t.Fatalf("the error must list the candidates, got %v", err)
	}
	for _, sessionID := range []string{first, second} {
		session, err := testStore.GetAgentSession(ctx, user.ID, sessionID)
		if err != nil {
			t.Fatalf("get session: %v", err)
		}
		if session.TaskKey != "" {
			t.Fatalf("nothing may be attached on an ambiguous call: %+v", session)
		}
	}

	// An explicit id resolves it, and so does a unique working directory.
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: second}, "MT-2", ""); err != nil {
		t.Fatalf("set task by id: %v", err)
	}
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: first, StartedAt: agentBaseMs + 2000, Cwd: "/home/dev/other",
	}, testPolicy); err != nil {
		t.Fatalf("refresh cwd: %v", err)
	}
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{Cwd: "/home/dev/other"}, "MT-3", ""); err != nil {
		t.Fatalf("set task by cwd: %v", err)
	}
	session, err := testStore.GetAgentSession(ctx, user.ID, first)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.TaskKey != "MT-3" {
		t.Fatalf("cwd must pick the matching session, got %+v", session)
	}
}

func TestSetAgentTaskIdempotent(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-idempotent@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-1", "First"); err != nil {
		t.Fatalf("set task: %v", err)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	seqAfterFirst := entry.ServerSeq

	result, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-1", "First")
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if result.RenamedEntries != 0 {
		t.Fatalf("a repeated call must change nothing, got %d renamed", result.RenamedEntries)
	}
	entry, err = testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.ServerSeq != seqAfterFirst {
		t.Fatalf("a repeated call must not reship the row: seq %d -> %d", seqAfterFirst, entry.ServerSeq)
	}

	// A corrected task key renames again.
	if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "MT-2", "Second"); err != nil {
		t.Fatalf("correct the task: %v", err)
	}
	entry, err = testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.Description != "MT-2 Second" {
		t.Fatalf("unexpected description: %q", entry.Description)
	}
}

func TestSetAgentTaskScopedToUser(t *testing.T) {
	testStore := openTestStore(t)
	owner := testUser(t, testStore, "agent-task-owner@test.local")
	intruder := testUser(t, testStore, "agent-task-intruder@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	startWorkingTestAgentSession(t, testStore, owner.ID, sessionID, agentBaseMs)

	if _, err := testStore.SetAgentTask(ctx, intruder.ID, AgentTaskSelector{SessionID: sessionID}, "MT-1", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a foreign session, got %v", err)
	}
}

func countUserEntries(t *testing.T, testStore *Store, userID string) int {
	t.Helper()
	var count int
	if err := testStore.db.QueryRow(
		"SELECT COUNT(*) FROM time_entries WHERE user_id = ? AND deleted_at IS NULL", userID).Scan(&count); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	return count
}

// A session reported by another client is filed under that client's name. The hook
// is shared - Codex delivers the same payload on the same events - so without this
// every Codex session would read "Claude Code #ab12cd34".
func TestAgentEntryNamedAfterItsClient(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-source@test.local")
	ctx := context.Background()

	for source, want := range map[string]string{
		"codex":       "Codex",
		"claude-code": "Claude Code",
		"":            "Claude Code",
		"acme-agent":  "acme-agent", // unknown clients are named after themselves, not after Claude
	} {
		sessionID := uuid.NewString()
		if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
			SessionID: sessionID, StartedAt: agentBaseMs, Source: source,
		}, testPolicy); err != nil {
			t.Fatalf("start %q: %v", source, err)
		}
		session := testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs)
		entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
		if err != nil {
			t.Fatalf("get entry %q: %v", source, err)
		}
		if expected := want + " #" + AgentSessionTag(sessionID); entry.Description != expected {
			t.Fatalf("source %q named the entry %q, want %q", source, entry.Description, expected)
		}
	}
}
