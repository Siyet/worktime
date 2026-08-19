package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestExportUserDB(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	owner := testUser(t, testStore, "owner@test.local")
	other := testUser(t, testStore, "other@test.local")

	projectID := uuid.NewString()
	_, err := testStore.Sync(ctx, owner.ID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{{ID: projectID, Name: "Backend", Color: "#ff0000", CreatedAt: 100, UpdatedAt: 100}},
		TimeEntries: []TimeEntry{
			{
				ID: uuid.NewString(), ProjectID: &projectID, Description: "mine", Tags: TagList{"review"},
				StartedAt: 1000, StoppedAt: msPointer(2000), CreatedAt: 1000, UpdatedAt: 1000,
			},
			{ID: uuid.NewString(), Description: "mine too", StartedAt: 3000, CreatedAt: 3000, UpdatedAt: 3000},
		},
		TimeOff: []TimeOff{{
			ID: uuid.NewString(), Kind: "vacation", DateFrom: "2026-07-01", DateTo: "2026-07-05",
			CreatedAt: 100, UpdatedAt: 100,
		}},
	}})
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	_, err = testStore.Sync(ctx, other.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{ID: uuid.NewString(), Description: "not mine", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000}},
	}})
	if err != nil {
		t.Fatalf("seed other: %v", err)
	}
	// Credentials must never leave the server database.
	if _, _, err := testStore.CreateSession(ctx, owner.ID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := testStore.CreateAPIToken(ctx, owner.ID, "cli"); err != nil {
		t.Fatalf("create token: %v", err)
	}

	path, cleanup, err := testStore.ExportUserDB(ctx, owner.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer cleanup()

	// The export must open like any other WorkTime database.
	exported, err := Open(path)
	if err != nil {
		t.Fatalf("open exported file: %v", err)
	}
	defer exported.Close()

	counts := map[string]int{
		"users":        1,
		"projects":     1,
		"time_entries": 2,
		"time_off":     1,
		"sessions":     0,
		"api_tokens":   0,
	}
	for table, expected := range counts {
		var got int
		if err := exported.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != expected {
			t.Errorf("table %s: expected %d rows, got %d", table, expected, got)
		}
	}

	var foreign int
	if err := exported.db.QueryRow(
		"SELECT count(*) FROM time_entries WHERE user_id = ?", other.ID,
	).Scan(&foreign); err != nil {
		t.Fatalf("count foreign rows: %v", err)
	}
	if foreign != 0 {
		t.Errorf("expected no rows of the second user, got %d", foreign)
	}

	var tags string
	if err := exported.db.QueryRow(
		"SELECT tags FROM time_entries WHERE description = 'mine'",
	).Scan(&tags); err != nil {
		t.Fatalf("read tags: %v", err)
	}
	if tags != `["review"]` {
		t.Errorf("expected tags to survive the export, got %q", tags)
	}
}

func TestExportKeepsAgentColumns(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	user := testUser(t, testStore, "export-agent@test.local")

	// The exported file is a full WorkTime database: an agent entry has to keep
	// both its pause and the session it belongs to, or the durations inside it
	// would not match the instance it came from.
	sessionID := uuid.NewString()
	session := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	gap := testIdleMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+gap)

	path, cleanup, err := testStore.ExportUserDB(ctx, user.ID)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer cleanup()
	exported, err := Open(path)
	if err != nil {
		t.Fatalf("open exported file: %v", err)
	}
	defer exported.Close()

	var pausedMs int64
	var agentSessionID *string
	if err := exported.db.QueryRow(
		"SELECT paused_ms, agent_session_id FROM time_entries WHERE id = ?", *session.TimeEntryID,
	).Scan(&pausedMs, &agentSessionID); err != nil {
		t.Fatalf("read agent columns: %v", err)
	}
	if pausedMs != gap {
		t.Errorf("expected paused_ms %d in the export, got %d", gap, pausedMs)
	}
	if agentSessionID == nil || *agentSessionID != sessionID {
		t.Errorf("expected the session link in the export, got %v", agentSessionID)
	}
}
