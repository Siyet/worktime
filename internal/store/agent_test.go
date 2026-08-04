package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

const (
	testIdleMs  = int64(10 * 60 * 1000)
	testGraceMs = int64(10 * 60 * 1000)
	agentBaseMs = int64(1_700_000_000_000)
)

func startTestAgentSession(t *testing.T, testStore *Store, userID, sessionID string, startedAt int64) AgentSession {
	t.Helper()
	session, err := testStore.StartAgentSession(context.Background(), userID, AgentStart{
		SessionID: sessionID, StartedAt: startedAt, Source: "claude-code",
		Cwd: "C:\\Users\\dev\\Projects\\WorkTime", GitBranch: "main",
	}, testIdleMs)
	if err != nil {
		t.Fatalf("start agent session: %v", err)
	}
	return session
}

func TestAgentStartCreatesRunningEntry(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-start@test.local")
	ctx := context.Background()

	session := startTestAgentSession(t, testStore, user.ID, uuid.NewString(), agentBaseMs)
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
	if entry.Description != "Claude Code (main)" {
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

func TestAgentStartReplayIsIdempotent(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-replay@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	first := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	// A --resume replays SessionStart with the same session id a bit later.
	replayed, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{
		SessionID: sessionID, StartedAt: agentBaseMs + 60_000, GitBranch: "feature",
	}, testIdleMs)
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
	ctx := context.Background()

	sessionID := uuid.NewString()
	startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	session, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, agentBaseMs+120_000, testIdleMs)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if session.LastHeartbeatAt != agentBaseMs+120_000 {
		t.Fatalf("expected watermark advance, got %+v", session)
	}

	// A delayed out-of-order heartbeat (offline queue replay) must not rewind it.
	session, err = testStore.AgentHeartbeat(ctx, user.ID, sessionID, agentBaseMs+30_000, testIdleMs)
	if err != nil {
		t.Fatalf("late heartbeat: %v", err)
	}
	if session.LastHeartbeatAt != agentBaseMs+120_000 {
		t.Fatalf("watermark rewound: %+v", session)
	}
}

func TestAgentIdleGapSplitsSegments(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-idle@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	firstEntryID := *started.TimeEntryID

	lastActive := agentBaseMs + 60_000
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, lastActive, testIdleMs); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Silence longer than the idle threshold: the gap must not be billed.
	afterIdle := lastActive + testIdleMs + 300_000
	session, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, afterIdle, testIdleMs)
	if err != nil {
		t.Fatalf("heartbeat after idle: %v", err)
	}
	if *session.TimeEntryID == firstEntryID {
		t.Fatal("expected a new segment entry after the idle gap")
	}

	firstEntry, err := testStore.GetTimeEntry(ctx, user.ID, firstEntryID)
	if err != nil {
		t.Fatalf("get first entry: %v", err)
	}
	if firstEntry.StoppedAt == nil || *firstEntry.StoppedAt != lastActive {
		t.Fatalf("first segment must stop at the last heartbeat, got %+v", firstEntry)
	}
	secondEntry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get second entry: %v", err)
	}
	if secondEntry.StartedAt != afterIdle || secondEntry.StoppedAt != nil {
		t.Fatalf("unexpected second segment: %+v", secondEntry)
	}
}

func TestAgentStopWithinIdleUsesEndedAt(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-stop@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)

	endedAt := agentBaseMs + 120_000
	session, err := testStore.StopAgentSession(ctx, user.ID, sessionID, endedAt, "prompt_input_exit", testIdleMs)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
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
	again, err := testStore.StopAgentSession(ctx, user.ID, sessionID, endedAt+500_000, "other", testIdleMs)
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
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
	if _, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, lastActive, testIdleMs); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// The stop arrives long after the last activity (terminal left open overnight).
	session, err := testStore.StopAgentSession(ctx, user.ID, sessionID, lastActive+testIdleMs+1, "other", testIdleMs)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
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

func TestAgentHeartbeatReopensClosedSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-reopen@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	started := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	if _, err := testStore.StopAgentSession(ctx, user.ID, sessionID, agentBaseMs+60_000, "other", testIdleMs); err != nil {
		t.Fatalf("stop: %v", err)
	}

	reopenedAt := agentBaseMs + 900_000
	session, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, reopenedAt, testIdleMs)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if session.Status != agentStatusActive || session.EndedAt != nil || session.EndReason != nil {
		t.Fatalf("expected a reopened session, got %+v", session)
	}
	if *session.TimeEntryID == *started.TimeEntryID {
		t.Fatal("reopening must create a new entry")
	}
	entry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if entry.StartedAt != reopenedAt || entry.StoppedAt != nil {
		t.Fatalf("unexpected reopened entry: %+v", entry)
	}
}

func TestAgentHeartbeatAutoCreatesSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-auto@test.local")
	ctx := context.Background()

	sessionID := uuid.NewString()
	session, err := testStore.AgentHeartbeat(ctx, user.ID, sessionID, agentBaseMs, testIdleMs)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
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
	if _, err := testStore.StartAgentSession(ctx, user.ID, AgentStart{SessionID: freshID, StartedAt: now - 1000}, testIdleMs); err != nil {
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

	if _, err := testStore.StartAgentSession(ctx, intruder.ID, AgentStart{SessionID: sessionID, StartedAt: agentBaseMs}, testIdleMs); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput on start, got %v", err)
	}
	if _, err := testStore.AgentHeartbeat(ctx, intruder.ID, sessionID, agentBaseMs, testIdleMs); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput on heartbeat, got %v", err)
	}
	if _, err := testStore.StopAgentSession(ctx, intruder.ID, sessionID, agentBaseMs, "other", testIdleMs); !errors.Is(err, ErrNotFound) {
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
	// updated_at must beat the row's wall-clock updated_at or last-write-wins drops the edit.
	entry.UpdatedAt = entry.UpdatedAt + 1
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{entry}}}); err != nil {
		t.Fatalf("manual stop: %v", err)
	}

	if _, err := testStore.StopAgentSession(ctx, user.ID, sessionID, agentBaseMs+90_000, "other", testIdleMs); err != nil {
		t.Fatalf("agent stop: %v", err)
	}
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
	}, testIdleMs)
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
	}, testIdleMs); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for an unknown project, got %v", err)
	}
}
