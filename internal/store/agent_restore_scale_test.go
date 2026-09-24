package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedStoppedSessionRows writes count finished rows of one agent session
// directly, each stopped by the user (no agent_end), one minute long and two
// minutes apart, and returns them as a device would push them running again.
func seedStoppedSessionRows(t *testing.T, testStore *Store, userID, sessionID string, base int64, count int) []TimeEntry {
	t.Helper()
	transaction, err := testStore.db.Begin()
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer transaction.Rollback()
	firstSeq, err := allocateServerSeq(transaction, count)
	if err != nil {
		t.Fatalf("allocate seed seqs: %v", err)
	}
	pushed := make([]TimeEntry, 0, count)
	lastID := ""
	for index := 0; index < count; index++ {
		entryID := uuid.NewString()
		startedAt := base + int64(index)*120_000
		stoppedAt := startedAt + 60_000
		if _, err := transaction.Exec(`
			INSERT INTO time_entries (id, user_id, description, tags, started_at, stopped_at,
			                          created_at, updated_at, server_seq, agent_session_id)
			VALUES (?, ?, 'Seeded', '[]', ?, ?, ?, ?, ?, ?)`,
			entryID, userID, startedAt, stoppedAt, startedAt, stoppedAt, firstSeq+int64(index), sessionID); err != nil {
			t.Fatalf("seed row %d: %v", index, err)
		}
		pushed = append(pushed, TimeEntry{
			ID: entryID, Description: "Seeded", Tags: TagList{}, StartedAt: startedAt,
			CreatedAt: startedAt, UpdatedAt: time.Now().UnixMilli() + 1,
		})
		lastID = entryID
	}
	if _, err := transaction.Exec(`
		UPDATE agent_sessions SET time_entry_id = ?, entry_server_seq = ? WHERE id = ?`,
		lastID, firstSeq+int64(count)-1, sessionID); err != nil {
		t.Fatalf("point the session at the last row: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	return pushed
}

// A push that sets every row of a long session running again settles each row
// against what follows it. That must cost a pass over the session, not a pass
// per row: the settle runs inside the sync transaction on the single SQLite
// connection, and a quadratic settle of ten thousand rows held every other
// request for over half a minute. The bound is relative to a push of the same
// rows that settles nothing, measured in the same run, because what a CI machine
// needs for ten thousand upserts alone varies more than the settle may add: a
// linear settle costs about twice that floor, the quadratic one over a hundred
// times.
func TestAgentLargeRestorePushStaysFast(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	const rowCount = 10_000
	push := func(t *testing.T, restore bool) (time.Duration, *Store, string, []TimeEntry) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-restore-scale@test.local")
		base := time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
		sessionID := uuid.NewString()
		startTestAgentSession(t, testStore, user.ID, sessionID, base)
		pushed := seedStoppedSessionRows(t, testStore, user.ID, sessionID, base, rowCount)
		if !restore {
			// The floor: the same rows renamed and left finished.
			for index := range pushed {
				stoppedAt := pushed[index].StartedAt + 60_000
				pushed[index].StoppedAt = &stoppedAt
				pushed[index].Description = "Renamed"
			}
		}
		started := time.Now()
		if _, err := testStore.Sync(context.Background(), user.ID, SyncRequest{
			Changes: SyncChanges{TimeEntries: pushed},
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
		return time.Since(started), testStore, user.ID, pushed
	}

	floor, _, _, _ := push(t, false)
	elapsed, testStore, userID, pushed := push(t, true)
	t.Logf("%d rows: restored and settled in %s, renamed without settling in %s", rowCount, elapsed, floor)

	var sessionID string
	if err := testStore.db.QueryRow("SELECT agent_session_id FROM time_entries WHERE id = ?", pushed[0].ID).Scan(&sessionID); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if running := runningSessionEntries(t, testStore, userID, sessionID); len(running) != 1 || running[0] != pushed[rowCount-1].ID {
		t.Fatalf("only the session's own row may run, got %d running", len(running))
	}
	if elapsed > 8*floor {
		t.Fatalf("settling %d restored rows took %s, more than eight times the %s the push needs without it",
			rowCount, elapsed, floor)
	}
}
