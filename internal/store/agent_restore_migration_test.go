package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// Migration 012 is the first to record who ended an agent row, so every agent
// row already stopped or deleted when it runs counts as ended by the server. A
// stale device that later pushes such a row running gets the stop and the
// tombstone back, exactly as for an end the server writes after the upgrade.
// Running rows and rows outside the agent flow get no marker, and no client
// pulls anything because of the backfill.
func TestAgentMarkerMigrationCountsEarlierEndsAsTheServers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-markers.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	const legacyVersion = 11
	for index := 0; index < legacyVersion; index++ {
		if _, err := legacy.Exec(migrations[index]); err != nil {
			legacy.Close()
			t.Fatalf("apply migration %d: %v", index+1, err)
		}
	}
	if _, err := legacy.Exec("PRAGMA user_version = 11"); err != nil {
		legacy.Close()
		t.Fatalf("set legacy version: %v", err)
	}
	if _, err := legacy.Exec("UPDATE sync_state SET seq = 20"); err != nil {
		legacy.Close()
		t.Fatalf("seed sync cursor: %v", err)
	}

	base := restoreTestBase()
	const minute = int64(60_000)
	userID := uuid.NewString()
	sessionID := uuid.NewString()
	otherSessionID := uuid.NewString()
	type seeded struct {
		id         string
		sessionID  *string
		stoppedAt  *int64
		deletedAt  *int64
		serverSeq  int64
		wantEnd    *int64
		wantDelete *int64
	}
	closed := seeded{id: uuid.NewString(), sessionID: &sessionID, stoppedAt: msPointer(base + 10*minute), serverSeq: 10}
	closed.wantEnd = closed.stoppedAt
	running := seeded{id: uuid.NewString(), sessionID: &otherSessionID, serverSeq: 11}
	stoppedAndDeleted := seeded{id: uuid.NewString(), sessionID: &sessionID, stoppedAt: msPointer(base - 20*minute),
		deletedAt: msPointer(base - 10*minute), serverSeq: 12}
	stoppedAndDeleted.wantEnd, stoppedAndDeleted.wantDelete = stoppedAndDeleted.stoppedAt, stoppedAndDeleted.deletedAt
	deletedRunning := seeded{id: uuid.NewString(), sessionID: &sessionID, deletedAt: msPointer(base - 5*minute), serverSeq: 13}
	deletedRunning.wantDelete = deletedRunning.deletedAt
	manual := seeded{id: uuid.NewString(), stoppedAt: msPointer(base - 50*minute), deletedAt: msPointer(base - 40*minute), serverSeq: 14}
	rows := []seeded{closed, running, stoppedAndDeleted, deletedRunning, manual}

	startOf := map[string]int64{
		closed.id: base, running.id: base, stoppedAndDeleted.id: base - 30*minute,
		deletedRunning.id: base - 6*minute, manual.id: base - 60*minute,
	}
	for _, row := range rows {
		if _, err := legacy.Exec(`INSERT INTO time_entries
			(id, user_id, description, tags, started_at, stopped_at, created_at, updated_at, deleted_at, server_seq, agent_session_id)
			VALUES (?, ?, 'Claude Code #legacy', '[]', ?, ?, 1, 1, ?, ?, ?)`,
			row.id, userID, startOf[row.id], row.stoppedAt, row.deletedAt, row.serverSeq, row.sessionID); err != nil {
			legacy.Close()
			t.Fatalf("seed entry: %v", err)
		}
	}
	if _, err := legacy.Exec(`INSERT INTO agent_sessions
		(id, user_id, source, status, started_at, last_heartbeat_at, ended_at, end_reason, time_entry_id, entry_server_seq, created_at, updated_at)
		VALUES (?, ?, 'claude-code', 'closed', ?, ?, ?, 'session_end', ?, 10, 1, 1)`,
		sessionID, userID, base, base+10*minute, base+10*minute, closed.id); err != nil {
		legacy.Close()
		t.Fatalf("seed agent session: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	testStore, err := Open(path)
	if err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	defer testStore.Close()

	for _, row := range rows {
		stored := storedTimeEntry(t, testStore, userID, row.id)
		if stored.ServerSeq != row.serverSeq || stored.UpdatedAt != 1 {
			t.Fatalf("the backfill must not publish %s: server_seq %d, updated_at %d", row.id, stored.ServerSeq, stored.UpdatedAt)
		}
		for column, want := range map[string]*int64{"agent_end": row.wantEnd, "agent_deleted_at": row.wantDelete, "agent_paused_from": nil} {
			got := storedAgentMarker(t, testStore, userID, row.id, column)
			if (got == nil) != (want == nil) || (got != nil && *got != *want) {
				t.Fatalf("%s of %s = %v, want %v", column, row.id, got, want)
			}
		}
	}
	if seq := currentSyncSeq(t, testStore); seq != 20 {
		t.Fatalf("the backfill must not advance the sync cursor, got %d", seq)
	}

	// A device that last saw the closed row running renames it; another pushes
	// its running copy of the row deleted while it ran.
	sessionBefore := getTestAgentSession(t, testStore, userID, sessionID)
	renamed := storedTimeEntry(t, testStore, userID, closed.id)
	renamed.Description = "Renamed on the stale device"
	pushRunning(t, testStore, userID, renamed)
	pushRunning(t, testStore, userID, storedTimeEntry(t, testStore, userID, deletedRunning.id))

	assertClosedAt(t, testStore, userID, closed.id, *closed.stoppedAt)
	if stored := storedTimeEntry(t, testStore, userID, closed.id); stored.Description != renamed.Description {
		t.Fatalf("the rename must be kept, got %q", stored.Description)
	}
	if stored := storedTimeEntry(t, testStore, userID, deletedRunning.id); stored.DeletedAt == nil || *stored.DeletedAt != *deletedRunning.deletedAt {
		t.Fatalf("the tombstone must be put back, got %+v", stored)
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	if session.Status != sessionBefore.Status || *session.EndedAt != *sessionBefore.EndedAt || *session.TimeEntryID != closed.id {
		t.Fatalf("the ended session must stay as it was:\n got %+v\nwant %+v", session, sessionBefore)
	}
	if count := countUserEntries(t, testStore, userID); count != 2 {
		t.Fatalf("expected the closed and the running row live, got %d", count)
	}
}
