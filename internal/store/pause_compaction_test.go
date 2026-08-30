package store

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPauseCompactionMigrationPreservesDurationsAndPublishesRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pause-compaction.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for index := 0; index < 8; index++ {
		if _, err := legacy.Exec(migrations[index]); err != nil {
			legacy.Close()
			t.Fatalf("apply migration %d: %v", index+1, err)
		}
	}
	if _, err := legacy.Exec("PRAGMA user_version = 8"); err != nil {
		legacy.Close()
		t.Fatalf("set legacy version: %v", err)
	}
	if _, err := legacy.Exec("UPDATE sync_state SET seq = 20"); err != nil {
		legacy.Close()
		t.Fatalf("seed sync cursor: %v", err)
	}

	userID := uuid.NewString()
	finishedID := uuid.NewString()
	runningID := uuid.NewString()
	unchangedID := uuid.NewString()
	sessionID := uuid.NewString()
	insertEntry := `INSERT INTO time_entries
		(id, user_id, description, tags, started_at, stopped_at, created_at, updated_at, server_seq, agent_session_id, paused_ms)
		VALUES (?, ?, ?, '[]', ?, ?, 1, 1, ?, ?, ?)`
	if _, err := legacy.Exec(insertEntry, finishedID, userID, "finished", 1_000, 10_000, 10, nil, 3_000); err != nil {
		legacy.Close()
		t.Fatalf("seed finished entry: %v", err)
	}
	if _, err := legacy.Exec(insertEntry, runningID, userID, "running", 2_000, nil, 11, sessionID, 4_000); err != nil {
		legacy.Close()
		t.Fatalf("seed running entry: %v", err)
	}
	if _, err := legacy.Exec(insertEntry, unchangedID, userID, "unchanged", 3_000, 5_000, 12, nil, 0); err != nil {
		legacy.Close()
		t.Fatalf("seed unchanged entry: %v", err)
	}
	if _, err := legacy.Exec(`INSERT INTO agent_sessions
		(id, user_id, source, status, started_at, last_heartbeat_at, time_entry_id, entry_server_seq, created_at, updated_at)
		VALUES (?, ?, 'codex', 'active', 2000, 6000, ?, 11, 1, 1)`, sessionID, userID, runningID); err != nil {
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

	var version int
	if err := testStore.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("schema version = %d, err %v; want %d", version, err, len(migrations))
	}
	columns, err := testStore.db.Query("PRAGMA table_info(time_entries)")
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	for columns.Next() {
		var columnID, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := columns.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			t.Fatalf("scan column: %v", err)
		}
		if name == "paused_ms" {
			columns.Close()
			t.Fatal("paused_ms survived migration 009")
		}
	}
	if err := closeRows(columns); err != nil {
		t.Fatalf("close columns: %v", err)
	}
	var userEditedDefault string
	if err := testStore.db.QueryRow(`
		SELECT dflt_value FROM pragma_table_info('agent_sessions') WHERE name = 'entry_user_edited'`,
	).Scan(&userEditedDefault); err != nil || userEditedDefault != "0" {
		t.Fatalf("migration 010 entry_user_edited default = %q, err %v; want 0", userEditedDefault, err)
	}

	finished, err := testStore.GetTimeEntry(t.Context(), userID, finishedID)
	if err != nil {
		t.Fatalf("get finished entry: %v", err)
	}
	if finished.StoppedAt == nil || *finished.StoppedAt != 7_000 || EntryDurationMs(finished, 99_000) != 6_000 {
		t.Fatalf("finished duration changed: %+v", finished)
	}
	running, err := testStore.GetTimeEntry(t.Context(), userID, runningID)
	if err != nil {
		t.Fatalf("get running entry: %v", err)
	}
	if running.StartedAt != 6_000 || running.StoppedAt != nil || EntryDurationMs(running, 12_000) != 6_000 {
		t.Fatalf("running duration changed: %+v", running)
	}
	unchanged, err := testStore.GetTimeEntry(t.Context(), userID, unchangedID)
	if err != nil {
		t.Fatalf("get unchanged entry: %v", err)
	}
	if unchanged.StartedAt != 3_000 || unchanged.StoppedAt == nil || *unchanged.StoppedAt != 5_000 || unchanged.ServerSeq != 12 {
		t.Fatalf("zero-pause entry changed: %+v", unchanged)
	}
	if finished.ServerSeq <= 20 || running.ServerSeq <= 20 || finished.ServerSeq == running.ServerSeq {
		t.Fatalf("migrated rows need fresh unique cursors: finished %d, running %d", finished.ServerSeq, running.ServerSeq)
	}
	var cursor, ownerSeq int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&cursor); err != nil || cursor != 22 {
		t.Fatalf("sync cursor = %d, err %v; want 22", cursor, err)
	}
	if err := testStore.db.QueryRow("SELECT entry_server_seq FROM agent_sessions WHERE id = ?", sessionID).Scan(&ownerSeq); err != nil {
		t.Fatalf("read agent ownership: %v", err)
	}
	if ownerSeq != running.ServerSeq {
		t.Fatalf("agent owns seq %d, running row has %d", ownerSeq, running.ServerSeq)
	}
	pulled, err := testStore.Sync(t.Context(), userID, SyncRequest{Since: 20})
	if err != nil {
		t.Fatalf("pull migrated rows: %v", err)
	}
	if len(pulled.Changes.TimeEntries) != 2 {
		t.Fatalf("migration must publish two changed rows, got %+v", pulled.Changes.TimeEntries)
	}
}

func TestTimeEntryAcceptsButNeverEmitsLegacyPausedMs(t *testing.T) {
	for _, test := range []struct {
		name      string
		payload   string
		wantStart int64
		wantStop  *int64
	}{
		{name: "finished", payload: `{"started_at":1000,"stopped_at":10000,"paused_ms":3000}`, wantStart: 1_000, wantStop: msPointer(7_000)},
		{name: "running", payload: `{"started_at":2000,"stopped_at":null,"paused_ms":4000}`, wantStart: 6_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			var entry TimeEntry
			if err := json.Unmarshal([]byte(test.payload), &entry); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if entry.StartedAt != test.wantStart || !equalInt64Pointers(entry.StoppedAt, test.wantStop) {
				t.Fatalf("compacted entry: %+v", entry)
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(encoded), "paused_ms") {
				t.Fatalf("removed field leaked onto the wire: %s", encoded)
			}
		})
	}
}

func TestEntryUserEditedMigrationAppliesToDeployedV9(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployed-v9.db")
	deployed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v9 db: %v", err)
	}
	for index := 0; index < 9; index++ {
		if _, err := deployed.Exec(migrations[index]); err != nil {
			deployed.Close()
			t.Fatalf("apply migration %d: %v", index+1, err)
		}
	}
	if _, err := deployed.Exec("PRAGMA user_version = 9"); err != nil {
		deployed.Close()
		t.Fatalf("mark v9: %v", err)
	}
	if err := deployed.Close(); err != nil {
		t.Fatalf("close v9 db: %v", err)
	}

	testStore, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade deployed v9: %v", err)
	}
	defer testStore.Close()

	var version int
	if err := testStore.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatalf("schema version = %d, err %v; want 10", version, err)
	}
	var defaultValue string
	if err := testStore.db.QueryRow(`
		SELECT dflt_value FROM pragma_table_info('agent_sessions') WHERE name = 'entry_user_edited'`,
	).Scan(&defaultValue); err != nil || defaultValue != "0" {
		t.Fatalf("entry_user_edited default = %q, err %v; want 0", defaultValue, err)
	}
}

func equalInt64Pointers(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
