package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// insertLegacyAgentSession reproduces what the old rule wrote: a session and the
// entry its start opened. No code path can still produce this shape, so the rows
// are built by hand - which is also the only way to test the migration, since it
// has already run by the time openTestStore returns.
type legacyAgentSession struct {
	lastHeartbeatOffset int64  // how far the watermark moved past the start
	lastKind            string // what the last signal was, empty when there was none
	description         string // empty means the session's own tag
	editedOutsideAgent  bool   // someone changed the row, so server_seq moved on
	running             bool
}

func insertLegacyAgentSession(t *testing.T, testStore *Store, userID string, session legacyAgentSession) string {
	t.Helper()
	sessionID := uuid.NewString()
	entryID := uuid.NewString()
	description := session.description
	if description == "" {
		description = "Claude Code #" + AgentSessionTag(sessionID)
	}
	var seq int64
	if err := testStore.db.QueryRow("UPDATE sync_state SET seq = seq + 1 RETURNING seq").Scan(&seq); err != nil {
		t.Fatalf("allocate seq: %v", err)
	}
	var stoppedAt any
	if !session.running {
		stoppedAt = agentBaseMs + session.lastHeartbeatOffset
	}
	if _, err := testStore.db.Exec(`
		INSERT INTO time_entries (id, user_id, description, tags, started_at, stopped_at,
		                          created_at, updated_at, server_seq, agent_session_id)
		VALUES (?, ?, ?, '[]', ?, ?, ?, ?, ?, ?)`,
		entryID, userID, description, agentBaseMs, stoppedAt, agentBaseMs, agentBaseMs, seq, sessionID); err != nil {
		t.Fatalf("insert entry: %v", err)
	}
	// An edit outside the agent flow is exactly what moves the row's server_seq
	// past the one the session recorded.
	ownedSeq := seq
	if session.editedOutsideAgent {
		ownedSeq = seq - 1
	}
	if _, err := testStore.db.Exec(`
		INSERT INTO agent_sessions (id, user_id, source, status, started_at, last_heartbeat_at,
		                            last_kind, time_entry_id, entry_server_seq, created_at, updated_at)
		VALUES (?, ?, 'claude-code', 'closed', ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, userID, agentBaseMs, agentBaseMs+session.lastHeartbeatOffset, session.lastKind,
		entryID, ownedSeq, agentBaseMs, agentBaseMs); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return entryID
}

func entryIsDeleted(t *testing.T, testStore *Store, entryID string) bool {
	t.Helper()
	var deletedAt *int64
	if err := testStore.db.QueryRow("SELECT deleted_at FROM time_entries WHERE id = ?", entryID).Scan(&deletedAt); err != nil {
		t.Fatalf("read entry: %v", err)
	}
	return deletedAt != nil
}

// The migration takes the rows the old rule wrote and nothing else. Every
// survivor here is held back by exactly one clause of the predicate and would be
// deleted without it, so a condition dropped or loosened by mistake shows up as a
// named row rather than as a count that happens to still add up.
func TestPurgeEmptyAgentEntriesTakesOnlyTheUntouchedSilentOnes(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "purge@test.local")
	ctx := context.Background()

	silent := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{})
	silentRunning := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{running: true})
	worked := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{
		lastHeartbeatOffset: 30_000, lastKind: "prompt",
	})
	// A signal that landed on the start millisecond moves no watermark, so
	// last_kind is the only witness that the session was ever worked in.
	workedOnTheStartMs := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{lastKind: "tool_start"})
	// And the mirror image, which the production database really holds: last_kind
	// arrived in migration 006, so every session worked before that deploy has an
	// empty one. There the watermark is the only witness left.
	workedBeforeLastKindExisted := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{
		lastHeartbeatOffset: 30_000,
	})
	renamed := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{description: "MT-1234 the real work"})
	edited := insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{editedOutsideAgent: true})

	before, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull before: %v", err)
	}
	if len(before.Changes.TimeEntries) != 7 {
		t.Fatalf("expected the seven planted rows, got %d", len(before.Changes.TimeEntries))
	}

	if _, err := testStore.db.Exec(purgeEmptyAgentEntries); err != nil {
		t.Fatalf("purge: %v", err)
	}

	taken := map[string]string{silent: "a silent session", silentRunning: "a silent session still running"}
	kept := map[string]string{
		worked:                      "a session that reported activity",
		workedOnTheStartMs:          "a session whose only signal shared the start millisecond",
		workedBeforeLastKindExisted: "a session worked before last_kind existed",
		renamed:                     "an entry named after a tracker task",
		edited:                      "an entry edited outside the agent flow",
	}
	for id, what := range taken {
		if !entryIsDeleted(t, testStore, id) {
			t.Errorf("%s was left behind", what)
		}
	}
	for id, what := range kept {
		if entryIsDeleted(t, testStore, id) {
			t.Errorf("%s was deleted", what)
		}
	}
}

// The tombstones have to reach the clients that already pulled the rows, and each
// one needs a cursor value of its own: two rows sharing a server_seq would let a
// client acknowledge both and receive only the first.
func TestPurgeEmptyAgentEntriesShipsDistinctTombstones(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "purge-sync@test.local")
	ctx := context.Background()

	for range 3 {
		insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{})
	}
	before, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull before: %v", err)
	}
	if _, err := testStore.db.Exec(purgeEmptyAgentEntries); err != nil {
		t.Fatalf("purge: %v", err)
	}

	after, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: before.Seq})
	if err != nil {
		t.Fatalf("pull after: %v", err)
	}
	if len(after.Changes.TimeEntries) != 3 {
		t.Fatalf("expected three tombstones past the old cursor, got %d", len(after.Changes.TimeEntries))
	}
	seqs := map[int64]bool{}
	for _, entry := range after.Changes.TimeEntries {
		if entry.DeletedAt == nil {
			t.Fatalf("entry %s came back alive: %+v", entry.ID, entry)
		}
		if seqs[entry.ServerSeq] {
			t.Fatalf("two tombstones share server_seq %d", entry.ServerSeq)
		}
		seqs[entry.ServerSeq] = true
	}
}

// Running it twice must take nothing the second time: the migration ships once,
// but a database restored from a backup taken mid-deploy would see it again.
func TestPurgeEmptyAgentEntriesIsIdempotent(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "purge-twice@test.local")

	insertLegacyAgentSession(t, testStore, user.ID, legacyAgentSession{})
	if _, err := testStore.db.Exec(purgeEmptyAgentEntries); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	var seqBefore int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&seqBefore); err != nil {
		t.Fatalf("read seq: %v", err)
	}
	if _, err := testStore.db.Exec(purgeEmptyAgentEntries); err != nil {
		t.Fatalf("second purge: %v", err)
	}
	var seqAfter int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&seqAfter); err != nil {
		t.Fatalf("read seq: %v", err)
	}
	if seqAfter != seqBefore {
		t.Fatalf("the second run burned cursor values: %d -> %d", seqBefore, seqAfter)
	}
}
