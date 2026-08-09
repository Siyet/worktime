package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	testStore, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { testStore.Close() })
	return testStore
}

func testUser(t *testing.T, testStore *Store, email string) User {
	t.Helper()
	user, err := testStore.FindOrCreateGoogleUser(context.Background(), "sub-"+email, email, "Test", "")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func msPointer(value int64) *int64 { return &value }

func TestSyncPushPullRoundtrip(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "one@test.local")
	ctx := context.Background()

	projectID := uuid.NewString()
	entryID := uuid.NewString()
	push := SyncRequest{Changes: SyncChanges{
		Projects: []Project{{ID: projectID, Name: "Backend", Color: "#ff0000", CreatedAt: 100, UpdatedAt: 100}},
		TimeEntries: []TimeEntry{{
			ID: entryID, ProjectID: &projectID, Description: "coding",
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}}
	pushResponse, err := testStore.Sync(ctx, user.ID, push)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResponse.Seq != 2 {
		t.Fatalf("expected seq 2 after two rows, got %d", pushResponse.Seq)
	}
	// The same client sees its own rows echoed because it pulled with since=0.
	if len(pushResponse.Changes.Projects) != 1 || len(pushResponse.Changes.TimeEntries) != 1 {
		t.Fatalf("expected echo of pushed rows, got %+v", pushResponse.Changes)
	}

	// A second device starting from zero receives everything.
	pullResponse, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pullResponse.Changes.Projects) != 1 || len(pullResponse.Changes.TimeEntries) != 1 {
		t.Fatalf("second device should receive all rows, got %+v", pullResponse.Changes)
	}
	if pullResponse.Changes.TimeEntries[0].Description != "coding" {
		t.Fatalf("unexpected entry: %+v", pullResponse.Changes.TimeEntries[0])
	}

	// Pulling from the latest cursor returns nothing.
	emptyResponse, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: pullResponse.Seq})
	if err != nil {
		t.Fatalf("empty pull: %v", err)
	}
	if len(emptyResponse.Changes.Projects)+len(emptyResponse.Changes.TimeEntries) != 0 {
		t.Fatalf("expected no changes past cursor, got %+v", emptyResponse.Changes)
	}
}

func TestSyncLastWriteWins(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "lww@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	base := TimeEntry{ID: entryID, Description: "v1", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000}
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{base}}}); err != nil {
		t.Fatalf("initial push: %v", err)
	}

	// A newer update wins.
	newer := base
	newer.Description = "v2"
	newer.UpdatedAt = 2000
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{newer}}}); err != nil {
		t.Fatalf("newer push: %v", err)
	}

	// A stale update (older updated_at) must be ignored.
	stale := base
	stale.Description = "stale"
	stale.UpdatedAt = 1500
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{stale}}}); err != nil {
		t.Fatalf("stale push: %v", err)
	}

	state, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(state.Changes.TimeEntries) != 1 || state.Changes.TimeEntries[0].Description != "v2" {
		t.Fatalf("expected v2 to win, got %+v", state.Changes.TimeEntries)
	}
}

func TestSyncDeletePropagates(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "del@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	entry := TimeEntry{ID: entryID, Description: "to delete", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000}
	first, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{entry}}})
	if err != nil {
		t.Fatalf("push: %v", err)
	}

	deleted := entry
	deleted.DeletedAt = msPointer(3000)
	deleted.UpdatedAt = 3000
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{deleted}}}); err != nil {
		t.Fatalf("delete push: %v", err)
	}

	// Another device that synced before the delete receives the tombstone.
	incremental, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: first.Seq})
	if err != nil {
		t.Fatalf("incremental pull: %v", err)
	}
	if len(incremental.Changes.TimeEntries) != 1 || incremental.Changes.TimeEntries[0].DeletedAt == nil {
		t.Fatalf("expected tombstone, got %+v", incremental.Changes.TimeEntries)
	}
}

func TestSyncUserIsolation(t *testing.T) {
	testStore := openTestStore(t)
	owner := testUser(t, testStore, "owner@test.local")
	attacker := testUser(t, testStore, "attacker@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	entry := TimeEntry{ID: entryID, Description: "private", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000}
	if _, err := testStore.Sync(ctx, owner.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{entry}}}); err != nil {
		t.Fatalf("owner push: %v", err)
	}

	// The attacker reuses the same row ID with a newer timestamp.
	hijack := entry
	hijack.Description = "hijacked"
	hijack.UpdatedAt = 9000
	attackerView, err := testStore.Sync(ctx, attacker.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{hijack}}})
	if err != nil {
		t.Fatalf("attacker push: %v", err)
	}
	if len(attackerView.Changes.TimeEntries) != 0 {
		t.Fatalf("attacker must not see foreign rows, got %+v", attackerView.Changes.TimeEntries)
	}

	ownerView, err := testStore.Sync(ctx, owner.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("owner pull: %v", err)
	}
	if len(ownerView.Changes.TimeEntries) != 1 || ownerView.Changes.TimeEntries[0].Description != "private" {
		t.Fatalf("owner row must be untouched, got %+v", ownerView.Changes.TimeEntries)
	}
}

func TestSyncValidation(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "val@test.local")
	ctx := context.Background()

	invalid := []SyncRequest{
		{Changes: SyncChanges{TimeEntries: []TimeEntry{{ID: "not-a-uuid", StartedAt: 1, UpdatedAt: 1}}}},
		{Changes: SyncChanges{TimeEntries: []TimeEntry{{ID: uuid.NewString(), StartedAt: 0, UpdatedAt: 1}}}},
		{Changes: SyncChanges{TimeEntries: []TimeEntry{{
			ID: uuid.NewString(), StartedAt: 2000, StoppedAt: msPointer(1000), UpdatedAt: 1,
		}}}},
		{Changes: SyncChanges{Projects: []Project{{ID: uuid.NewString(), Name: "", UpdatedAt: 1}}}},
		{Changes: SyncChanges{TimeOff: []TimeOff{{
			ID: uuid.NewString(), Kind: "holiday", DateFrom: "2026-01-01", DateTo: "2026-01-02", UpdatedAt: 1,
		}}}},
		{Changes: SyncChanges{TimeOff: []TimeOff{{
			ID: uuid.NewString(), Kind: "sick", DateFrom: "2026-01-05", DateTo: "2026-01-01", UpdatedAt: 1,
		}}}},
	}
	for index, request := range invalid {
		if _, err := testStore.Sync(ctx, user.ID, request); err == nil {
			t.Errorf("case %d: expected validation error", index)
		}
	}
}

func TestSyncMultipleRunningTimers(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "run@test.local")
	ctx := context.Background()

	running := []TimeEntry{
		{ID: uuid.NewString(), Description: "first", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000},
		{ID: uuid.NewString(), Description: "second", StartedAt: 2000, CreatedAt: 2000, UpdatedAt: 2000},
		{ID: uuid.NewString(), Description: "third", StartedAt: 3000, CreatedAt: 3000, UpdatedAt: 3000},
	}
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: running}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	state, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	openCount := 0
	for _, entry := range state.Changes.TimeEntries {
		if entry.StoppedAt == nil {
			openCount++
		}
	}
	if openCount != 3 {
		t.Fatalf("expected 3 concurrent running timers, got %d", openCount)
	}
}

func TestSyncCannotClaimAgentSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "claim@test.local")
	ctx := context.Background()

	agentSession := startTestAgentSession(t, testStore, user.ID, uuid.NewString(), agentBaseMs)

	// A client creating a row cannot hand it a session: ids are generated on the
	// client, so a pushed value would let it attach the row to a session it does
	// not own. The column is server-owned in both directions.
	foreign := agentSession.ID
	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{{
		ID: entryID, Description: "mine", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		AgentSessionID: &foreign,
	}}}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	created, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if created.AgentSessionID != nil {
		t.Fatalf("insert must ignore agent_session_id, got %v", *created.AgentSessionID)
	}

	// Editing the agent's own row does not clear or move the link either.
	agentEntry, err := testStore.GetTimeEntry(ctx, user.ID, *agentSession.TimeEntryID)
	if err != nil {
		t.Fatalf("get agent entry: %v", err)
	}
	agentEntry.AgentSessionID = nil
	agentEntry.Description = "edited"
	agentEntry.UpdatedAt++
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{agentEntry},
	}}); err != nil {
		t.Fatalf("push edit: %v", err)
	}
	after, err := testStore.GetTimeEntry(ctx, user.ID, agentEntry.ID)
	if err != nil {
		t.Fatalf("get agent entry: %v", err)
	}
	if after.AgentSessionID == nil || *after.AgentSessionID != agentSession.ID {
		t.Fatalf("an update must not change agent_session_id, got %v", after.AgentSessionID)
	}
	if after.Description != "edited" {
		t.Fatalf("the edit itself must apply, got %q", after.Description)
	}
}

func TestSyncPausedMsIsServerOwned(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "paused@test.local")
	ctx := context.Background()

	// A client creating a row cannot hand itself a pause: it would be free
	// unbilled time on a row nobody but the client ever touched.
	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{{
		ID: entryID, Description: "mine", StartedAt: 1000, StoppedAt: msPointer(4000),
		CreatedAt: 1000, UpdatedAt: 1000, PausedMs: 999,
	}}}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	created, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if created.PausedMs != 0 {
		t.Fatalf("insert must ignore paused_ms, got %d", created.PausedMs)
	}

	// The agent's own row keeps its pause across an ordinary edit, and the value
	// reaches clients through the normal pull.
	sessionID := uuid.NewString()
	session := startTestAgentSession(t, testStore, user.ID, sessionID, agentBaseMs)
	gap := testIdleMs + 60_000
	testHeartbeat(t, testStore, user.ID, sessionID, agentBaseMs+gap)

	agentEntry, err := testStore.GetTimeEntry(ctx, user.ID, *session.TimeEntryID)
	if err != nil {
		t.Fatalf("get agent entry: %v", err)
	}
	if agentEntry.PausedMs != gap {
		t.Fatalf("expected the gap paused, got %d", agentEntry.PausedMs)
	}
	agentEntry.PausedMs = 0
	agentEntry.Description = "edited"
	pushEntry(t, testStore, user.ID, agentEntry)

	pulled, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	var seen *TimeEntry
	for index := range pulled.Changes.TimeEntries {
		if pulled.Changes.TimeEntries[index].ID == agentEntry.ID {
			seen = &pulled.Changes.TimeEntries[index]
		}
	}
	if seen == nil {
		t.Fatal("the agent entry must come back in the pull")
	}
	if seen.PausedMs != gap {
		t.Fatalf("an update must not change paused_ms, got %d", seen.PausedMs)
	}
	if seen.Description != "edited" {
		t.Fatalf("the edit itself must apply, got %q", seen.Description)
	}
}
