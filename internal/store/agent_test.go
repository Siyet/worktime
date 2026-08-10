package store

import (
	"context"
	"errors"
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
	IdleMs:     testIdleMs,
	ToolMaxMs:  30 * 60 * 1000,
	MaxPauseMs: 4 * 60 * 60 * 1000,
}

func startTestAgentSession(t *testing.T, testStore *Store, userID, sessionID string, startedAt int64) AgentSession {
	t.Helper()
	session, err := testStore.StartAgentSession(context.Background(), userID, AgentStart{
		SessionID: sessionID, StartedAt: startedAt, Source: "claude-code",
		Cwd: "C:\\Users\\dev\\Projects\\WorkTime", GitBranch: "main",
	}, testPolicy)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	return session
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

func TestAgentStartCreatesRunningEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-start@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	session := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if session.Status != agentStatusActive || session.LastHeartbeatAt != agentBaseMs {
		t.Fatalf("unexpected session: %+v", session)
	}
	if session.TimeEntryID == nil {
		t.Fatal("expected a running time entry")
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
	pull, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pull.Changes.TimeEntries) != 1 || pull.Changes.TimeEntries[0].ID != entry.ID {
		t.Fatalf("expected the agent entry in the pull, got %+v", pull.Changes.TimeEntries)
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
	session := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	first := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

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

func TestAgentIdleGapKeepsOneEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-idle@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	entryID := *started.TimeEntryID

	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)

	// Silence longer than the idle threshold: the gap must not be billed, but it
	// must not cost the session its entry either - one session is one row.
	gap := testIdleMs + 300_000
	afterIdle := lastActive + gap
	session := testHeartbeat(t, testStore, user.ID, sessionID, afterIdle)
	if *session.TimeEntryID != entryID {
		t.Fatal("an idle gap must not open a second entry")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt != nil {
		t.Fatalf("the entry must still be running: %+v", entry)
	}
	if entry.PausedMs != gap {
		t.Fatalf("expected the whole gap paused, got %d of %d", entry.PausedMs, gap)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentDurationExcludesPause(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-duration@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// A minute of work, twenty minutes of nothing, another minute, then stop.
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+60_000)
	resumed := agentBaseMs + 60_000 + 20*60_000
	testHeartbeat(t, testStore, user.ID, sessionID, resumed)
	testHeartbeat(t, testStore, user.ID, sessionID, resumed+60_000)
	testStop(t, testStore, user.ID, sessionID, resumed+60_000, "prompt_input_exit")

	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	billable := *entry.StoppedAt - entry.StartedAt - entry.PausedMs
	if billable != 120_000 {
		t.Fatalf("expected two billed minutes, got %d ms (paused %d)", billable, entry.PausedMs)
	}
}

func TestAgentPausedMsNeverExceedsSpan(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-pause-bounds@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// Out of order, repeated and far apart signals in one go.
	moments := []int64{60_000, 61_000, 20 * 60_000, 20*60_000 + 1, 90 * 60_000, 30 * 60_000, 95 * 60_000}
	for _, offset := range moments {
		if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
			AgentSignal{At: agentBaseMs + offset}, testPolicy); err != nil {
			t.Fatalf("heartbeat at +%d: %v", offset, err)
		}
	}
	testStop(t, testStore, user.ID, sessionID, agentBaseMs+95*60_000, "other")

	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	span := *entry.StoppedAt - entry.StartedAt
	if entry.PausedMs < 0 || entry.PausedMs > span {
		t.Fatalf("paused_ms %d is outside 0..%d", entry.PausedMs, span)
	}
}

func TestAgentLongToolRunBilledUpToCap(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-tool@test.local")
	ctx := context.Background()

	// A twenty minute tool call is work in full: PostToolUse only fires once the
	// tool is done, so without the tool_start marker the gap looks like idling.
	shortRun := uuid.NewString()
	shortStarted := startTestAgentSession(t, testStore, user.ID, shortRun, agentBaseMs)
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, shortRun,
		AgentSignal{At: agentBaseMs + 60_000, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	testHeartbeat(t, testStore, user.ID, shortRun, agentBaseMs+60_000+20*60_000)
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *shortStarted.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.PausedMs != 0 {
		t.Fatalf("a tool run inside the cap must be billed in full, paused %d", entry.PausedMs)
	}

	// A forty five minute one bills the first thirty and pauses the rest: a hung
	// tool must not bill forever, and one minute over the cap must not lose all.
	longRun := uuid.NewString()
	longStarted := startTestAgentSession(t, testStore, user.ID, longRun, agentBaseMs)
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, longRun,
		AgentSignal{At: agentBaseMs + 60_000, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}
	testHeartbeat(t, testStore, user.ID, longRun, agentBaseMs+60_000+45*60_000)
	entry, err = testStore.GetTimeEntry(ctx, user.ID, *longStarted.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.PausedMs != 15*60_000 {
		t.Fatalf("expected 15 minutes paused past the cap, got %d", entry.PausedMs)
	}
}

func TestAgentResumeDoesNotPauseBilledTail(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-resume@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	if entry.PausedMs != 0 {
		t.Fatalf("a one minute resume gap is under the idle threshold, paused %d", entry.PausedMs)
	}
	if entry.StoppedAt != nil {
		t.Fatalf("the entry must be running again: %+v", entry)
	}
}

func TestAgentMidnightGapSplitsEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-midnight@test.local")
	ctx := context.Background()

	// UTC+3: local midnight is 21:00 UTC, so 23:30 -> 00:30 local crosses it.
	offset := 180
	sessionID := uuid.NewString()
	evening := time.Date(2026, 7, 1, 20, 0, 0, 0, time.UTC).UnixMilli()
	started, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: evening, TZOffsetMin: &offset,
	}, testPolicy)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	lastEvening := time.Date(2026, 7, 1, 20, 30, 0, 0, time.UTC).UnixMilli()
	testHeartbeat(t, testStore, user.ID, sessionID, lastEvening)

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
	if second.StartedAt != morning || second.PausedMs != 0 {
		t.Fatalf("unexpected morning entry: %+v", second)
	}

	// Reconciliation still owns the new entry, which is the point of keeping the
	// session active through the cut.
	closed, err := testStore.ReconcileAgentSessions(ctx, morning+testGraceMs+1000, testGraceMs, testPolicy)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if closed != 1 {
		t.Fatalf("expected the session to be reconciled, got %d", closed)
	}
}

func TestAgentUnknownTimezoneNeverSplits(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-notz@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	evening := time.Date(2026, 7, 1, 20, 30, 0, 0, time.UTC).UnixMilli()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, evening)
	morning := time.Date(2026, 7, 1, 21, 30, 0, 0, time.UTC).UnixMilli()
	session := testHeartbeat(t, testStore, user.ID, sessionID, morning)
	if *session.TimeEntryID != *started.TimeEntryID {
		t.Fatal("without a known offset there is no local midnight to cut at")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.PausedMs != morning-evening {
		t.Fatalf("the whole gap must be paused, got %d", entry.PausedMs)
	}
}

func TestAgentMaxPauseSplitsEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-maxpause@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// Longer than the maximum pause and with no timezone known: the
	// timezone-independent guard has to cut the entry anyway.
	afterNight := agentBaseMs + 5*60*60*1000
	session := testHeartbeat(t, testStore, user.ID, sessionID, afterNight)
	if *session.TimeEntryID == *started.TimeEntryID {
		t.Fatal("a pause longer than the maximum must open a new entry")
	}
	first, err := testStore.GetTimeEntry(ctx, user.ID, *started.TimeEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if first.StoppedAt == nil || *first.StoppedAt != agentBaseMs {
		t.Fatalf("unexpected first entry: %+v", first)
	}
}

func TestAgentConcurrentHeartbeats(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-concurrent@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	session := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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

// A session killed mid-tool is closed by reconciliation rather than by a stop, and must
// be worth the same as one that managed to send SessionEnd - otherwise the value of
// twenty minutes of work depends on whether the process died cleanly.
func TestAgentReconcileBillsTrailingToolRun(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reconcile-tool@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	toolStartedAt := agentBaseMs + 60_000
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID,
		AgentSignal{At: toolStartedAt, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start: %v", err)
	}

	// The agent is SIGKILLed during the run, so no stop ever arrives.
	toolRunMs := int64(20 * 60 * 1000)
	closedAt := toolStartedAt + toolRunMs
	closed, err := testStore.ReconcileAgentSessions(ctx, closedAt, testGraceMs, testPolicy)
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
	if entry.StoppedAt == nil || *entry.StoppedAt != closedAt {
		t.Fatalf("expected the tool run billed, got %+v", entry.StoppedAt)
	}
}

func TestAgentReopenKeepsSameEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reopen@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	lastActive := agentBaseMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, lastActive)
	testStop(t, testStore, user.ID, sessionID, lastActive+testIdleMs+1, "other")

	// Work resumes: the session must pick its own entry back up rather than
	// scatter the rest of the session over new rows.
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	stale := startTestAgentSession(t, testStore, user.ID, staleID, agentBaseMs)
	freshID := uuid.NewString()
	now := agentBaseMs + testGraceMs + 60_000
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{SessionID: freshID, StartedAt: now - 1000}, testPolicy); err != nil {
		t.Fatalf("start fresh: %v", err)
	}

	closed, err := testStore.ReconcileAgentSessions(ctx, now, testGraceMs, testPolicy)
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
	if staleSession.Status != agentStatusClosed || *staleSession.EndedAt != agentBaseMs {
		t.Fatalf("unexpected stale session: %+v", staleSession)
	}
	if *staleSession.EndReason != AgentEndReasonStale {
		t.Fatalf("unexpected end reason: %+v", staleSession)
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *stale.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StoppedAt == nil || *entry.StoppedAt != agentBaseMs {
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
	startTestAgentSession(t, testStore, owner.ID, sessionID, agentBaseMs)

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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

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

	session, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: uuid.NewString(), StartedAt: agentBaseMs, ProjectID: &projectID,
	}, testPolicy)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
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

func TestSetAgentTaskRenamesAllSessionEntries(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// A pause past the maximum leaves the session with two entries; both belong
	// to the same task, and half the work would keep the technical name if only
	// the current entry were renamed.
	afterIdle := agentBaseMs + 5*60*60*1000
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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

func TestSetAgentTaskAmbiguousSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-task-ambiguous@test.local")
	ctx := context.Background()

	first := uuid.NewString()
	second := uuid.NewString()
	startTestAgentSession(t, testStore, user.ID, first, agentBaseMs)
	startTestAgentSession(t, testStore, user.ID, second, agentBaseMs+1000)

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
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
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
	startTestAgentSession(t, testStore, owner.ID, sessionID, agentBaseMs)

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
