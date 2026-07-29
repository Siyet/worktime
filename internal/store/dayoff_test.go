package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSyncDayoffKindRoundtrip(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "dayoff@test.local")
	ctx := context.Background()

	push := SyncRequest{Changes: SyncChanges{TimeOff: []TimeOff{{
		ID: uuid.NewString(), Kind: "dayoff", DateFrom: "2026-07-20", DateTo: "2026-07-21",
		CreatedAt: 1, UpdatedAt: 1,
	}}}}
	if _, err := testStore.Sync(ctx, user.ID, push); err != nil {
		t.Fatalf("push dayoff: %v", err)
	}

	state, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(state.Changes.TimeOff) != 1 || state.Changes.TimeOff[0].Kind != "dayoff" {
		t.Fatalf("expected dayoff row back, got %+v", state.Changes.TimeOff)
	}
}

func TestBuildReportCountsDayoff(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "dayoff-report@test.local")
	ctx := context.Background()

	push := SyncRequest{Changes: SyncChanges{TimeOff: []TimeOff{
		{ID: uuid.NewString(), Kind: "dayoff", DateFrom: "2026-07-20", DateTo: "2026-07-21", CreatedAt: 1, UpdatedAt: 1},
		{ID: uuid.NewString(), Kind: "sick", DateFrom: "2026-07-22", DateTo: "2026-07-22", CreatedAt: 1, UpdatedAt: 1},
	}}}
	if _, err := testStore.Sync(ctx, user.ID, push); err != nil {
		t.Fatalf("push: %v", err)
	}

	windowFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	windowTo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	report, err := testStore.BuildReport(ctx, user.ID, windowFrom, windowTo)
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	days := map[string]int{}
	for _, item := range report.TimeOff {
		days[item.Kind] = item.Days
	}
	if days["dayoff"] != 2 || days["sick"] != 1 {
		t.Fatalf("expected 2 dayoff and 1 sick day, got %+v", report.TimeOff)
	}
}

// TestMigration002PreservesTimeOff builds a database at schema version 1,
// inserts a time_off row, then reopens through Open so migration 002 recreates
// the table. The row must survive and the new 'dayoff' kind must be accepted.
func TestMigration002PreservesTimeOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := legacy.Exec(migrations[0]); err != nil {
		legacy.Close()
		t.Fatalf("apply migration 001: %v", err)
	}
	legacyID := uuid.NewString()
	if _, err := legacy.Exec(`
		INSERT INTO time_off (id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq)
		VALUES (?, 'legacy-user', 'vacation', '2026-07-05', '2026-07-07', 'summer', 1, 1, NULL, 1)`, legacyID); err != nil {
		legacy.Close()
		t.Fatalf("insert legacy row: %v", err)
	}
	if _, err := legacy.Exec(fmt.Sprintf("PRAGMA user_version = %d", 1)); err != nil {
		legacy.Close()
		t.Fatalf("set user_version: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer migrated.Close()

	var kind, note string
	if err := migrated.db.QueryRow(
		"SELECT kind, note FROM time_off WHERE id = ?", legacyID,
	).Scan(&kind, &note); err != nil {
		t.Fatalf("legacy row lost in migration: %v", err)
	}
	if kind != "vacation" || note != "summer" {
		t.Fatalf("legacy row corrupted: kind=%q note=%q", kind, note)
	}

	// The recreated CHECK must accept the new kind and still reject unknown ones.
	if _, err := migrated.db.Exec(`
		INSERT INTO time_off (id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq)
		VALUES (?, 'legacy-user', 'dayoff', '2026-07-20', '2026-07-20', '', 1, 1, NULL, 2)`, uuid.NewString()); err != nil {
		t.Fatalf("dayoff insert rejected after migration: %v", err)
	}
	if _, err := migrated.db.Exec(`
		INSERT INTO time_off (id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq)
		VALUES (?, 'legacy-user', 'holiday', '2026-07-21', '2026-07-21', '', 1, 1, NULL, 3)`, uuid.NewString()); err == nil {
		t.Fatal("unknown kind must still be rejected by CHECK")
	}
}
