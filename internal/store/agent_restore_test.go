package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests cover an agent row the user sets running again after it was
// stopped - the Timer page's Undo, or a late offline push of a row the client
// still has running. The timeline sits in the recent past rather than at
// agentBaseMs: a replaced row is closed at the server's clock, and only a
// timeline near that clock gives the closing moment a meaningful bound.
func restoreTestBase() int64 {
	return time.Now().Add(-3 * time.Hour).UnixMilli()
}

// storedTimeEntry reads a row including its tombstone, which GetTimeEntry hides.
func storedTimeEntry(t *testing.T, testStore *Store, userID, entryID string) TimeEntry {
	t.Helper()
	var entry TimeEntry
	err := testStore.db.QueryRow(`
		SELECT id, project_id, description, tags, started_at, stopped_at, created_at, updated_at, deleted_at,
		       server_seq, agent_session_id
		FROM time_entries WHERE id = ? AND user_id = ?`, entryID, userID,
	).Scan(&entry.ID, &entry.ProjectID, &entry.Description, &entry.Tags, &entry.StartedAt, &entry.StoppedAt,
		&entry.CreatedAt, &entry.UpdatedAt, &entry.DeletedAt, &entry.ServerSeq, &entry.AgentSessionID)
	if err != nil {
		t.Fatalf("read entry %s: %v", entryID, err)
	}
	return entry
}

// runningSessionEntries lists the live running rows produced by one session.
func runningSessionEntries(t *testing.T, testStore *Store, userID, sessionID string) []string {
	t.Helper()
	rows, err := testStore.db.Query(`
		SELECT id FROM time_entries
		WHERE user_id = ? AND agent_session_id = ? AND stopped_at IS NULL AND deleted_at IS NULL
		ORDER BY started_at`, userID, sessionID)
	if err != nil {
		t.Fatalf("list running rows: %v", err)
	}
	ids := []string{}
	for rows.Next() {
		var entryID string
		if err := rows.Scan(&entryID); err != nil {
			rows.Close()
			t.Fatalf("scan running row: %v", err)
		}
		ids = append(ids, entryID)
	}
	if err := closeRows(rows); err != nil {
		t.Fatalf("close running rows: %v", err)
	}
	return ids
}

func currentSyncSeq(t *testing.T, testStore *Store) int64 {
	t.Helper()
	var seq int64
	if err := testStore.db.QueryRow("SELECT seq FROM sync_state").Scan(&seq); err != nil {
		t.Fatalf("read sync cursor: %v", err)
	}
	return seq
}

func getTestAgentSession(t *testing.T, testStore *Store, userID, sessionID string) AgentSession {
	t.Helper()
	session, err := testStore.GetAgentSession(context.Background(), userID, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return session
}

// userStopsEntry is the Timer page's Stop: the stored row with a stop, pushed
// through the normal sync path.
func userStopsEntry(t *testing.T, testStore *Store, userID, entryID string, stoppedAt int64) {
	t.Helper()
	entry := storedTimeEntry(t, testStore, userID, entryID)
	entry.StoppedAt = &stoppedAt
	pushEntry(t, testStore, userID, entry)
}

// userUndoesStop is the Undo in the "Stopped" toast: the same row written back
// with stopped_at cleared and a newer updated_at.
func userUndoesStop(t *testing.T, testStore *Store, userID, entryID string) {
	t.Helper()
	entry := storedTimeEntry(t, testStore, userID, entryID)
	entry.StoppedAt = nil
	pushEntry(t, testStore, userID, entry)
}

// assertSessionRunsOnly checks the invariant the rule exists for: the session is
// active, exactly one of its rows runs, and that row is the one the session owns.
func assertSessionRunsOnly(t *testing.T, testStore *Store, userID, sessionID, entryID string) AgentSession {
	t.Helper()
	if running := runningSessionEntries(t, testStore, userID, sessionID); !reflect.DeepEqual(running, []string{entryID}) {
		t.Fatalf("expected only %s running for the session, got %v", entryID, running)
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if session.Status != agentStatusActive || session.EndedAt != nil {
		t.Fatalf("expected an active session, got %+v", session)
	}
	if session.TimeEntryID == nil || *session.TimeEntryID != entryID {
		t.Fatalf("session must point at %s, got %+v", entryID, session.TimeEntryID)
	}
	if session.EntryServerSeq == nil || *session.EntryServerSeq != entry.ServerSeq {
		t.Fatalf("session must own the row: entry_server_seq %v, row server_seq %d", session.EntryServerSeq, entry.ServerSeq)
	}
	return session
}

// assertSessionEndedOn checks an ended session that took a restored row back:
// nothing of the session runs, the row is closed at the session's end, and the
// session owns it so a later revival continues it instead of opening another.
func assertSessionEndedOn(t *testing.T, testStore *Store, userID, sessionID, entryID string, endedAt int64) AgentSession {
	t.Helper()
	if running := runningSessionEntries(t, testStore, userID, sessionID); len(running) != 0 {
		t.Fatalf("an ended session must leave nothing running, got %v", running)
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	if session.Status != agentStatusClosed || session.EndedAt == nil || *session.EndedAt != endedAt {
		t.Fatalf("expected the session to stay closed at %d, got %+v", endedAt, session)
	}
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if entry.DeletedAt != nil || entry.StoppedAt == nil || *entry.StoppedAt != endedAt {
		t.Fatalf("the restored row must be closed at the session end %d, got %+v", endedAt, entry)
	}
	if session.TimeEntryID == nil || *session.TimeEntryID != entryID {
		t.Fatalf("session must point at %s, got %+v", entryID, session.TimeEntryID)
	}
	if session.EntryServerSeq == nil || *session.EntryServerSeq != entry.ServerSeq {
		t.Fatalf("session must own the row: entry_server_seq %v, row server_seq %d", session.EntryServerSeq, entry.ServerSeq)
	}
	return session
}

func assertTombstoned(t *testing.T, testStore *Store, userID, entryID string, afterSeq int64) {
	t.Helper()
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if entry.DeletedAt == nil {
		t.Fatalf("the replacement must be tombstoned, got %+v", entry)
	}
	if entry.ServerSeq <= afterSeq {
		t.Fatalf("the tombstone needs a fresh server_seq past %d, got %d", afterSeq, entry.ServerSeq)
	}
}

// Order 1: the Undo lands before any heartbeat saw the stop. The session never
// let the row go, so the ordinary adoption keeps it and no second row appears.
func TestAgentUndoBeforeNextHeartbeatKeepsTheRow(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-first@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
	rowID := *started.TimeEntryID
	testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)

	userStopsEntry(t, testStore, user.ID, rowID, base+90_000)
	userUndoesStop(t, testStore, user.ID, rowID)

	undone := getTestAgentSession(t, testStore, user.ID, sessionID)
	if undone.Status != agentStatusActive || undone.TimeEntryID == nil || *undone.TimeEntryID != rowID {
		t.Fatalf("the Undo must leave the session on its row, got %+v", undone)
	}
	if running := runningSessionEntries(t, testStore, user.ID, sessionID); !reflect.DeepEqual(running, []string{rowID}) {
		t.Fatalf("expected only the undone row running, got %v", running)
	}

	testHeartbeat(t, testStore, user.ID, sessionID, base+120_000)
	session := assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if !session.EntryUserEdited {
		t.Fatalf("the stop and the undo are outside writes: %+v", session)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("no replacement may appear, got %d entries", count)
	}
	if entry := storedTimeEntry(t, testStore, user.ID, rowID); entry.StartedAt != base {
		t.Fatalf("the row must keep its start: %+v", entry)
	}
}

// Order 2: a heartbeat saw the stop first and opened a replacement. The Undo
// takes the original back in the same sync write.
func TestAgentUndoAfterHeartbeatTakesTheRowBack(t *testing.T) {
	tests := []struct {
		name string
		// touch runs between the replacement's creation and the Undo.
		touch func(t *testing.T, testStore *Store, userID, sessionID, replacementID string, base int64)
		// tombstoned: an untouched replacement only duplicated time and is
		// removed; one the user touched is kept and closed at the restore.
		tombstoned bool
	}{
		{
			name:       "untouched replacement",
			touch:      func(*testing.T, *Store, string, string, string, int64) {},
			tombstoned: true,
		},
		{
			name: "replacement renamed by the user",
			touch: func(t *testing.T, testStore *Store, userID, _ string, replacementID string, _ int64) {
				entry := storedTimeEntry(t, testStore, userID, replacementID)
				entry.Description = "Kept by hand"
				pushEntry(t, testStore, userID, entry)
			},
		},
		{
			// The adopting heartbeat records the edited row's seq as its own, so
			// the seq alone would call the row untouched; the edit flag must not.
			name: "replacement renamed and adopted by a heartbeat",
			touch: func(t *testing.T, testStore *Store, userID, sessionID, replacementID string, base int64) {
				entry := storedTimeEntry(t, testStore, userID, replacementID)
				entry.Description = "Kept by hand"
				pushEntry(t, testStore, userID, entry)
				testHeartbeat(t, testStore, userID, sessionID, base+75_000)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-undo-after-heartbeat@test.local")
			ctx := context.Background()
			base := restoreTestBase()

			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
			rowID := *started.TimeEntryID
			userStopsEntry(t, testStore, user.ID, rowID, base+30_000)

			replaced := testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
			if replaced.TimeEntryID == nil || *replaced.TimeEntryID == rowID {
				t.Fatalf("the heartbeat must open a replacement after the stop, got %+v", replaced)
			}
			replacementID := *replaced.TimeEntryID
			test.touch(t, testStore, user.ID, sessionID, replacementID, base)

			cursor := currentSyncSeq(t, testStore)
			restoreStarted := time.Now().UnixMilli()
			userUndoesStop(t, testStore, user.ID, rowID)
			restoreFinished := time.Now().UnixMilli()

			session := assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
			if !session.EntryUserEdited || session.EntryUserNamed {
				t.Fatalf("an undo is an outside write that keeps the automatic name: %+v", session)
			}
			if restored := storedTimeEntry(t, testStore, user.ID, rowID); restored.StartedAt != base || restored.StoppedAt != nil {
				t.Fatalf("the restored row must run from its own start: %+v", restored)
			}

			replacement := storedTimeEntry(t, testStore, user.ID, replacementID)
			if test.tombstoned {
				assertTombstoned(t, testStore, user.ID, replacementID, cursor)
			} else {
				if replacement.DeletedAt != nil || replacement.Description != "Kept by hand" {
					t.Fatalf("a replacement the user touched must be kept: %+v", replacement)
				}
				if replacement.StoppedAt == nil || *replacement.StoppedAt < restoreStarted || *replacement.StoppedAt > restoreFinished {
					t.Fatalf("a touched replacement must close at the restore, within [%d, %d]: %+v",
						restoreStarted, restoreFinished, replacement)
				}
				if replacement.ServerSeq <= cursor {
					t.Fatalf("closing the replacement needs a fresh server_seq past %d, got %d", cursor, replacement.ServerSeq)
				}
			}

			// Another device pulls every server-side consequence of the Undo.
			pulled, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: cursor})
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			pulledIDs := map[string]bool{}
			for _, entry := range pulled.Changes.TimeEntries {
				pulledIDs[entry.ID] = true
			}
			if !pulledIDs[rowID] || !pulledIDs[replacementID] {
				t.Fatalf("the pull must carry the restored row and the replacement, got %v", pulledIDs)
			}

			testHeartbeat(t, testStore, user.ID, sessionID, base+90_000)
			assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
			wantEntries := 1
			if !test.tombstoned {
				wantEntries = 2
			}
			if count := countUserEntries(t, testStore, user.ID); count != wantEntries {
				t.Fatalf("the next heartbeat must keep the restored row, expected %d entries, got %d", wantEntries, count)
			}
		})
	}
}

// Order 3: the stop hook closed the session before the Undo arrived. Nothing was
// tracked after the session's end, so the restored row is closed there.
func TestAgentUndoAfterStopHookClosesAtTheSessionEnd(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-after-stop@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
	rowID := *started.TimeEntryID
	testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
	userStopsEntry(t, testStore, user.ID, rowID, base+90_000)
	endedAt := base + 120_000
	testStop(t, testStore, user.ID, sessionID, endedAt, "session_end")

	userUndoesStop(t, testStore, user.ID, rowID)
	session := assertSessionEndedOn(t, testStore, user.ID, sessionID, rowID, endedAt)
	if !session.EntryUserEdited {
		t.Fatalf("the undo is an outside write: %+v", session)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

func TestAgentUndoAfterReplacementAndStopHookTombstonesTheReplacement(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-after-replacement-stop@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
	rowID := *started.TimeEntryID
	userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
	replaced := testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
	replacementID := *replaced.TimeEntryID
	// Long enough that the stop keeps the replacement as real work: the cleanup of
	// sub-30-second technical rows must not be what removes it here.
	testHeartbeat(t, testStore, user.ID, sessionID, base+180_000)
	endedAt := base + 200_000
	testStop(t, testStore, user.ID, sessionID, endedAt, "session_end")
	if replacement := storedTimeEntry(t, testStore, user.ID, replacementID); replacement.DeletedAt != nil ||
		replacement.StoppedAt == nil || *replacement.StoppedAt != endedAt {
		t.Fatalf("the stop must close the replacement at the session end: %+v", replacement)
	}

	cursor := currentSyncSeq(t, testStore)
	userUndoesStop(t, testStore, user.ID, rowID)
	assertSessionEndedOn(t, testStore, user.ID, sessionID, rowID, endedAt)
	assertTombstoned(t, testStore, user.ID, replacementID, cursor)

	// Work resuming inside the idle threshold continues the restored row: the
	// session owns it, so no third row opens.
	testHeartbeat(t, testStore, user.ID, sessionID, endedAt+60_000)
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("expected exactly one entry, got %d", count)
	}
}

// Order 4: an offline client pushes the row it still has running after the
// server already closed or split the session. The push wins last-write-wins, and
// the outcome is the same as an Undo arriving in that state.
func TestAgentLateOfflinePushOfARunningRow(t *testing.T) {
	tests := []struct {
		name string
		// meanwhile is what the server did while the client was offline.
		meanwhile func(t *testing.T, testStore *Store, userID, sessionID string, base int64)
		// endedAt is the session end when the server stopped it, zero when the
		// session is still active after a split.
		endedAt func(base int64) int64
	}{
		{
			name: "stop hook closed the session",
			meanwhile: func(t *testing.T, testStore *Store, userID, sessionID string, base int64) {
				testStop(t, testStore, userID, sessionID, base+120_000, "session_end")
			},
			endedAt: func(base int64) int64 { return base + 120_000 },
		},
		{
			name: "idle gap split the session",
			meanwhile: func(t *testing.T, testStore *Store, userID, sessionID string, base int64) {
				split := testHeartbeat(t, testStore, userID, sessionID, base+60_000+testIdleMs+60_000)
				if split.TimeEntryID == nil {
					t.Fatalf("the split must open a replacement: %+v", split)
				}
			},
			endedAt: func(int64) int64 { return 0 },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-late-offline-push@test.local")
			ctx := context.Background()
			base := restoreTestBase()

			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
			rowID := *started.TimeEntryID
			testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
			clientCopy := storedTimeEntry(t, testStore, user.ID, rowID)

			test.meanwhile(t, testStore, user.ID, sessionID, base)
			before := getTestAgentSession(t, testStore, user.ID, sessionID)
			if closed := storedTimeEntry(t, testStore, user.ID, rowID); closed.StoppedAt == nil {
				t.Fatalf("the server must have closed the row meanwhile: %+v", closed)
			}

			cursor := currentSyncSeq(t, testStore)
			clientCopy.Description = "Edited offline"
			clientCopy.StoppedAt = nil
			clientCopy.UpdatedAt = max(storedTimeEntry(t, testStore, user.ID, rowID).UpdatedAt, time.Now().UnixMilli()) + 1
			if _, err := testStore.Sync(ctx, user.ID, SyncRequest{
				Changes: SyncChanges{TimeEntries: []TimeEntry{clientCopy}},
			}); err != nil {
				t.Fatalf("late push: %v", err)
			}

			var session AgentSession
			if endedAt := test.endedAt(base); endedAt != 0 {
				session = assertSessionEndedOn(t, testStore, user.ID, sessionID, rowID, endedAt)
			} else {
				session = assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
				assertTombstoned(t, testStore, user.ID, *before.TimeEntryID, cursor)
				testHeartbeat(t, testStore, user.ID, sessionID, base+60_000+testIdleMs+120_000)
				assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
			}
			if !session.EntryUserEdited || !session.EntryUserNamed {
				t.Fatalf("a pushed rename marks the row edited and user-named: %+v", session)
			}
			if count := countUserEntries(t, testStore, user.ID); count != 1 {
				t.Fatalf("expected exactly one entry, got %d", count)
			}
		})
	}
}

// Writes that do not restore a released agent row must behave exactly as before
// the rule existed: one reserved server_seq per pushed row and nothing else.
func TestAgentRestoreRuleLeavesOtherWritesAlone(t *testing.T) {
	t.Run("edit of the current running row", func(t *testing.T) {
		for _, edit := range []string{"rename", "project"} {
			t.Run(edit, func(t *testing.T) {
				testStore := openTestStore(t)
				user := testUser(t, testStore, "agent-restore-current-"+edit+"@test.local")
				ctx := context.Background()
				base := restoreTestBase()
				projectID := uuid.NewString()
				if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
					Projects: []Project{{ID: projectID, Name: "Current", Color: "#123456", CreatedAt: 1, UpdatedAt: 1}},
				}}); err != nil {
					t.Fatalf("create project: %v", err)
				}

				sessionID := uuid.NewString()
				started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
				rowID := *started.TimeEntryID
				before := getTestAgentSession(t, testStore, user.ID, sessionID)
				entry := storedTimeEntry(t, testStore, user.ID, rowID)
				want := before
				want.EntryUserEdited = true
				if edit == "rename" {
					entry.Description = "Renamed while running"
					want.EntryUserNamed = true
				} else {
					entry.ProjectID = &projectID
					want.ProjectID = &projectID
				}

				cursor := currentSyncSeq(t, testStore)
				pushEntry(t, testStore, user.ID, entry)
				if after := currentSyncSeq(t, testStore); after != cursor+1 {
					t.Fatalf("an ordinary edit must take exactly its reserved seq, cursor moved %d -> %d", cursor, after)
				}
				if after := getTestAgentSession(t, testStore, user.ID, sessionID); !reflect.DeepEqual(after, want) {
					t.Fatalf("only the existing edit markers may change:\n got %+v\nwant %+v", after, want)
				}
				if running := runningSessionEntries(t, testStore, user.ID, sessionID); !reflect.DeepEqual(running, []string{rowID}) {
					t.Fatalf("expected the edited row still running alone, got %v", running)
				}
			})
		}
	})

	t.Run("non-agent running row", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-restore-manual@test.local")
		base := restoreTestBase()

		sessionID := uuid.NewString()
		startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
		before := getTestAgentSession(t, testStore, user.ID, sessionID)

		manual := TimeEntry{
			ID: uuid.NewString(), Description: "Manual timer", Tags: TagList{},
			StartedAt: base + 10_000, CreatedAt: base + 10_000, UpdatedAt: base + 10_000,
		}
		cursor := currentSyncSeq(t, testStore)
		pushEntry(t, testStore, user.ID, manual)
		if after := currentSyncSeq(t, testStore); after != cursor+1 {
			t.Fatalf("a manual timer must take exactly its reserved seq, cursor moved %d -> %d", cursor, after)
		}
		stored := storedTimeEntry(t, testStore, user.ID, manual.ID)
		if stored.AgentSessionID != nil || stored.StoppedAt != nil || stored.DeletedAt != nil ||
			stored.UpdatedAt != manual.UpdatedAt+1 || stored.ServerSeq != cursor+1 {
			t.Fatalf("a manual running row must be stored exactly as pushed: %+v", stored)
		}
		if after := getTestAgentSession(t, testStore, user.ID, sessionID); !reflect.DeepEqual(after, before) {
			t.Fatalf("a manual timer must not touch agent sessions:\n got %+v\nwant %+v", after, before)
		}
	})

	t.Run("last-write-wins refusal", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-restore-refused@test.local")
		ctx := context.Background()
		base := restoreTestBase()

		sessionID := uuid.NewString()
		started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
		rowID := *started.TimeEntryID
		stale := storedTimeEntry(t, testStore, user.ID, rowID)
		userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
		replaced := testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
		replacementID := *replaced.TimeEntryID
		before := getTestAgentSession(t, testStore, user.ID, sessionID)

		// An older running version of the row loses to the stop and changes nothing.
		cursor := currentSyncSeq(t, testStore)
		if _, err := testStore.Sync(ctx, user.ID, SyncRequest{
			Changes: SyncChanges{TimeEntries: []TimeEntry{stale}},
		}); err != nil {
			t.Fatalf("push stale version: %v", err)
		}
		if after := currentSyncSeq(t, testStore); after != cursor+1 {
			t.Fatalf("a refused write must take only its reserved seq, cursor moved %d -> %d", cursor, after)
		}
		if after := getTestAgentSession(t, testStore, user.ID, sessionID); !reflect.DeepEqual(after, before) {
			t.Fatalf("a refused write must not touch the session:\n got %+v\nwant %+v", after, before)
		}
		assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
		if row := storedTimeEntry(t, testStore, user.ID, rowID); row.StoppedAt == nil || *row.StoppedAt != base+30_000 {
			t.Fatalf("the user's stop must stand: %+v", row)
		}
	})
}
