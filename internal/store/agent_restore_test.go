package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests cover an agent row a sync write sets running again after it was
// stopped or deleted - the Timer page's Undo, or a late push from a device that
// still shows the row running - and the rule that settles it
// (settleRestoredAgentEntry). The timeline sits in the recent past rather than
// at agentBaseMs, because the rule's writes are stamped with the server clock.
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

func storedAgentEnd(t *testing.T, testStore *Store, userID, entryID string) *int64 {
	t.Helper()
	var agentEnd *int64
	if err := testStore.db.QueryRow(
		"SELECT agent_end FROM time_entries WHERE id = ? AND user_id = ?", entryID, userID,
	).Scan(&agentEnd); err != nil {
		t.Fatalf("read agent_end of %s: %v", entryID, err)
	}
	return agentEnd
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

// pushRunning pushes a device's copy of a row as running and not deleted, newer
// than anything the server stored meanwhile: the edit happened on that device
// after the server's own writes, so it wins last-write-wins.
func pushRunning(t *testing.T, testStore *Store, userID string, copied TimeEntry) {
	t.Helper()
	copied.StoppedAt = nil
	copied.DeletedAt = nil
	copied.UpdatedAt = max(storedTimeEntry(t, testStore, userID, copied.ID).UpdatedAt, time.Now().UnixMilli()) + 1
	if _, err := testStore.Sync(context.Background(), userID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{copied}},
	}); err != nil {
		t.Fatalf("push running copy: %v", err)
	}
}

// workUntil is continuous work: a heartbeat every five minutes, well inside the
// idle threshold, from just after from up to and including until.
func workUntil(t *testing.T, testStore *Store, userID, sessionID string, from, until int64) {
	t.Helper()
	for at := from + 5*60_000; at < until; at += 5 * 60_000 {
		testHeartbeat(t, testStore, userID, sessionID, at)
	}
	testHeartbeat(t, testStore, userID, sessionID, until)
}

func testToolStart(t *testing.T, testStore *Store, userID, sessionID string, at int64) {
	t.Helper()
	if _, err := testStore.AgentHeartbeat(context.Background(), userID, sessionID,
		AgentSignal{At: at, Kind: AgentKindToolStart}, testPolicy); err != nil {
		t.Fatalf("tool start at %d: %v", at, err)
	}
}

func createTestProject(t *testing.T, testStore *Store, userID, name string) string {
	t.Helper()
	projectID := uuid.NewString()
	if _, err := testStore.Sync(context.Background(), userID, SyncRequest{Changes: SyncChanges{
		Projects: []Project{{ID: projectID, Name: name, Color: "#123456", CreatedAt: 1, UpdatedAt: 1}},
	}}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return projectID
}

// assertSessionOn checks the invariant the rule exists for: the session is
// active, exactly one of its rows runs, and the session points at that row. An
// outside edit the session has not adopted yet leaves the ownership marker
// behind, which the next signal settles.
func assertSessionOn(t *testing.T, testStore *Store, userID, sessionID, entryID string) AgentSession {
	t.Helper()
	if running := runningSessionEntries(t, testStore, userID, sessionID); !reflect.DeepEqual(running, []string{entryID}) {
		t.Fatalf("expected only %s running for the session, got %v", entryID, running)
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	if session.Status != agentStatusActive || session.EndedAt != nil {
		t.Fatalf("expected an active session, got %+v", session)
	}
	if session.TimeEntryID == nil || *session.TimeEntryID != entryID {
		t.Fatalf("session must point at %s, got %+v", entryID, session.TimeEntryID)
	}
	return session
}

// assertSessionRunsOnly is assertSessionOn for a session that also owns the row:
// its ownership marker is the row's current server_seq.
func assertSessionRunsOnly(t *testing.T, testStore *Store, userID, sessionID, entryID string) AgentSession {
	t.Helper()
	session := assertSessionOn(t, testStore, userID, sessionID, entryID)
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if session.EntryServerSeq == nil || *session.EntryServerSeq != entry.ServerSeq {
		t.Fatalf("session must own the row: entry_server_seq %v, row server_seq %d", session.EntryServerSeq, entry.ServerSeq)
	}
	return session
}

// assertSessionEndedOn checks an ended session that took a restored row back:
// nothing of the session runs, the row is closed at stoppedAt, and the session
// owns it so a later revival continues it instead of opening another.
func assertSessionEndedOn(t *testing.T, testStore *Store, userID, sessionID, entryID string, endedAt, stoppedAt int64) AgentSession {
	t.Helper()
	if running := runningSessionEntries(t, testStore, userID, sessionID); len(running) != 0 {
		t.Fatalf("an ended session must leave nothing running, got %v", running)
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	if session.Status != agentStatusClosed || session.EndedAt == nil || *session.EndedAt != endedAt {
		t.Fatalf("expected the session to stay closed at %d, got %+v", endedAt, session)
	}
	assertClosedAt(t, testStore, userID, entryID, stoppedAt)
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if session.TimeEntryID == nil || *session.TimeEntryID != entryID {
		t.Fatalf("session must point at %s, got %+v", entryID, session.TimeEntryID)
	}
	if session.EntryServerSeq == nil || *session.EntryServerSeq != entry.ServerSeq {
		t.Fatalf("session must own the row: entry_server_seq %v, row server_seq %d", session.EntryServerSeq, entry.ServerSeq)
	}
	return session
}

// assertClosedAt checks a live row stopped exactly at stoppedAt, with the stop
// recorded as the server's own so a later push cannot reopen it.
func assertClosedAt(t *testing.T, testStore *Store, userID, entryID string, stoppedAt int64) {
	t.Helper()
	entry := storedTimeEntry(t, testStore, userID, entryID)
	if entry.DeletedAt != nil || entry.StoppedAt == nil || *entry.StoppedAt != stoppedAt {
		t.Fatalf("expected %s live and stopped at %d, got %+v", entryID, stoppedAt, entry)
	}
	if agentEnd := storedAgentEnd(t, testStore, userID, entryID); agentEnd == nil || *agentEnd != stoppedAt {
		t.Fatalf("the stop of %s must be recorded as the server's, agent_end %v", entryID, agentEnd)
	}
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

func assertUnchanged(t *testing.T, testStore *Store, userID string, before TimeEntry) {
	t.Helper()
	if after := storedTimeEntry(t, testStore, userID, before.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("row %s must be left alone:\n got %+v\nwant %+v", before.ID, after, before)
	}
}

func assertSessionUnchanged(t *testing.T, testStore *Store, userID string, before AgentSession) {
	t.Helper()
	if after := getTestAgentSession(t, testStore, userID, before.ID); !reflect.DeepEqual(after, before) {
		t.Fatalf("the session must be left alone:\n got %+v\nwant %+v", after, before)
	}
}

// assertPulledFrom is a second device pulling from cursor: it must receive every
// listed row exactly as stored, and a cursor covering every server write.
func assertPulledFrom(t *testing.T, testStore *Store, userID string, cursor int64, entryIDs ...string) {
	t.Helper()
	pulled, err := testStore.Sync(context.Background(), userID, SyncRequest{Since: cursor})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	byID := map[string]TimeEntry{}
	for _, entry := range pulled.Changes.TimeEntries {
		byID[entry.ID] = entry
	}
	for _, entryID := range entryIDs {
		got, found := byID[entryID]
		if !found {
			t.Fatalf("the pull from %d must carry %s, got %d rows", cursor, entryID, len(byID))
		}
		if want := storedTimeEntry(t, testStore, userID, entryID); !reflect.DeepEqual(got, want) {
			t.Fatalf("the pull must carry the stored version of %s:\n got %+v\nwant %+v", entryID, got, want)
		}
	}
	if seq := currentSyncSeq(t, testStore); pulled.Seq != seq {
		t.Fatalf("the pulled cursor %d must cover every write up to %d", pulled.Seq, seq)
	}
}

// entryShape is what a person sees of a row; sequence numbers and update stamps
// are left out, because a replayed push may legitimately move them.
type entryShape struct {
	StartedAt   int64
	StoppedAt   *int64
	Deleted     bool
	Description string
	ProjectID   *string
}

type sessionShape struct {
	Status          string
	EndedAt         *int64
	TimeEntryID     *string
	EntryUserEdited bool
	EntryUserNamed  bool
	ProjectID       *string
}

func userEntryShapes(t *testing.T, testStore *Store, userID string) map[string]entryShape {
	t.Helper()
	rows, err := testStore.db.Query(`
		SELECT id, started_at, stopped_at, deleted_at, description, project_id
		FROM time_entries WHERE user_id = ?`, userID)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	shapes := map[string]entryShape{}
	for rows.Next() {
		var (
			entryID   string
			shape     entryShape
			deletedAt *int64
		)
		if err := rows.Scan(&entryID, &shape.StartedAt, &shape.StoppedAt, &deletedAt, &shape.Description, &shape.ProjectID); err != nil {
			rows.Close()
			t.Fatalf("scan entry: %v", err)
		}
		shape.Deleted = deletedAt != nil
		shapes[entryID] = shape
	}
	if err := closeRows(rows); err != nil {
		t.Fatalf("close entries: %v", err)
	}
	return shapes
}

func sessionShapeOf(session AgentSession) sessionShape {
	return sessionShape{
		Status: session.Status, EndedAt: session.EndedAt, TimeEntryID: session.TimeEntryID,
		EntryUserEdited: session.EntryUserEdited, EntryUserNamed: session.EntryUserNamed, ProjectID: session.ProjectID,
	}
}

// (a) The Undo lands before any heartbeat saw the stop. The session still points
// at the row, so the rule changes nothing and the ordinary adoption keeps it.
func TestAgentUndoBeforeNextHeartbeatKeepsTheRow(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-first@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
	rowID := *started.TimeEntryID
	testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)

	userStopsEntry(t, testStore, user.ID, rowID, base+90_000)
	stopped := getTestAgentSession(t, testStore, user.ID, sessionID)
	cursor := currentSyncSeq(t, testStore)
	userUndoesStop(t, testStore, user.ID, rowID)

	if after := currentSyncSeq(t, testStore); after != cursor+1 {
		t.Fatalf("the Undo must take only its reserved seq, cursor moved %d -> %d", cursor, after)
	}
	// The stop already marked the row edited; taking the row "back" would rewrite
	// the ownership marker and hide the Undo from the next heartbeat.
	assertSessionUnchanged(t, testStore, user.ID, stopped)
	if running := runningSessionEntries(t, testStore, user.ID, sessionID); !reflect.DeepEqual(running, []string{rowID}) {
		t.Fatalf("expected only the undone row running, got %v", running)
	}

	testHeartbeat(t, testStore, user.ID, sessionID, base+120_000)
	session := assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if !session.EntryUserEdited || session.EntryUserNamed {
		t.Fatalf("the stop and the undo are outside writes that keep the automatic name: %+v", session)
	}
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("no replacement may appear, got %d entries", count)
	}
	if entry := storedTimeEntry(t, testStore, user.ID, rowID); entry.StartedAt != base {
		t.Fatalf("the row must keep its start: %+v", entry)
	}
}

// (c) A heartbeat saw the stop first and opened a replacement nobody touched. It
// only duplicates the restored row's time: it is tombstoned in the same sync
// write, and the session writes into the original again.
func TestAgentUndoAfterHeartbeatTombstonesTheUntouchedReplacement(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-after-heartbeat@test.local")
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

	cursor := currentSyncSeq(t, testStore)
	userUndoesStop(t, testStore, user.ID, rowID)

	session := assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if !session.EntryUserEdited || session.EntryUserNamed {
		t.Fatalf("an undo is an outside write that keeps the automatic name: %+v", session)
	}
	if restored := storedTimeEntry(t, testStore, user.ID, rowID); restored.StartedAt != base || restored.StoppedAt != nil {
		t.Fatalf("the restored row must run from its own start: %+v", restored)
	}
	assertTombstoned(t, testStore, user.ID, replacementID, cursor)
	replacement := storedTimeEntry(t, testStore, user.ID, replacementID)
	if agentEnd := storedAgentEnd(t, testStore, user.ID, replacementID); agentEnd == nil || *agentEnd != *replacement.DeletedAt {
		t.Fatalf("a running row the server deletes records the tombstone as its end, agent_end %v", agentEnd)
	}
	assertPulledFrom(t, testStore, user.ID, cursor, rowID, replacementID)

	testHeartbeat(t, testStore, user.ID, sessionID, base+90_000)
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if count := countUserEntries(t, testStore, user.ID); count != 1 {
		t.Fatalf("the next heartbeat must continue the restored row, got %d entries", count)
	}
}

// (d) The replacement was touched - renamed by the user, or moved to a project by
// the agent's own update_time_entry, which goes through Sync like any edit. It is
// work somebody cares about, so it stays and keeps running, and the restored row
// closes where it begins: no overlap and no hole between them.
func TestAgentUndoWithAnEditedReplacementClosesAtItsStart(t *testing.T) {
	tests := []struct {
		name  string
		touch func(t *testing.T, testStore *Store, userID, sessionID, replacementID string, base int64)
	}{
		{
			name: "renamed by the user",
			touch: func(t *testing.T, testStore *Store, userID, _ string, replacementID string, _ int64) {
				entry := storedTimeEntry(t, testStore, userID, replacementID)
				entry.Description = "Kept by hand"
				pushEntry(t, testStore, userID, entry)
			},
		},
		{
			// The adopting heartbeat records the edited row's seq as its own, so
			// the seq alone would call the row untouched; the edit flag must not.
			name: "renamed and adopted by a heartbeat",
			touch: func(t *testing.T, testStore *Store, userID, sessionID, replacementID string, base int64) {
				entry := storedTimeEntry(t, testStore, userID, replacementID)
				entry.Description = "Kept by hand"
				pushEntry(t, testStore, userID, entry)
				testHeartbeat(t, testStore, userID, sessionID, base+75_000)
			},
		},
		{
			name: "project set by the agent's update path",
			touch: func(t *testing.T, testStore *Store, userID, sessionID, replacementID string, _ int64) {
				projectID := createTestProject(t, testStore, userID, "Agent project")
				entry := storedTimeEntry(t, testStore, userID, replacementID)
				entry.ProjectID = &projectID
				pushEntry(t, testStore, userID, entry)
				if err := testStore.SetAgentSessionProject(context.Background(), userID, sessionID, &projectID); err != nil {
					t.Fatalf("set session project: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-undo-edited-replacement@test.local")
			base := restoreTestBase()

			sessionID := uuid.NewString()
			started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
			rowID := *started.TimeEntryID
			userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
			replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+60_000).TimeEntryID
			test.touch(t, testStore, user.ID, sessionID, replacementID, base)
			touched := storedTimeEntry(t, testStore, user.ID, replacementID)

			cursor := currentSyncSeq(t, testStore)
			undoCopy := storedTimeEntry(t, testStore, user.ID, rowID)
			userUndoesStop(t, testStore, user.ID, rowID)

			assertClosedAt(t, testStore, user.ID, rowID, touched.StartedAt)
			assertUnchanged(t, testStore, user.ID, touched)
			assertSessionOn(t, testStore, user.ID, sessionID, replacementID)
			assertNoOverlap(t, testStore, user.ID, sessionID)
			assertPulledFrom(t, testStore, user.ID, cursor, rowID)

			// The close is the server's: pushing the row running again from a device
			// that still holds the Undo does not reopen it.
			pushRunning(t, testStore, user.ID, undoCopy)
			assertClosedAt(t, testStore, user.ID, rowID, touched.StartedAt)
			assertSessionOn(t, testStore, user.ID, sessionID, replacementID)

			testHeartbeat(t, testStore, user.ID, sessionID, base+90_000)
			assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
			if count := countUserEntries(t, testStore, user.ID); count != 2 {
				t.Fatalf("expected the restored row and the replacement, got %d entries", count)
			}
		})
	}
}

// restoreOutcome is a settled timeline in terms two runs can compare: rows by
// role, times relative to the run's base, the session tag cut out of names.
type restoreOutcome struct {
	Entries   map[string]entryShape
	SessionOn string
	Edited    bool
	Named     bool
}

func restoreOutcomeOf(t *testing.T, testStore *Store, userID, sessionID string, base int64, roles map[string]string) restoreOutcome {
	t.Helper()
	outcome := restoreOutcome{Entries: map[string]entryShape{}}
	for entryID, shape := range userEntryShapes(t, testStore, userID) {
		shape.StartedAt -= base
		if shape.StoppedAt != nil {
			offset := *shape.StoppedAt - base
			shape.StoppedAt = &offset
		}
		shape.Description = strings.ReplaceAll(shape.Description, AgentSessionTag(sessionID), "")
		outcome.Entries[roles[entryID]] = shape
	}
	session := getTestAgentSession(t, testStore, userID, sessionID)
	if session.TimeEntryID != nil {
		outcome.SessionOn = roles[*session.TimeEntryID]
	}
	outcome.Edited, outcome.Named = session.EntryUserEdited, session.EntryUserNamed
	return outcome
}

// assertNoOverlap checks that the session's live rows follow one another: each
// one ends no later than the next begins, and only the last may still run.
func assertNoOverlap(t *testing.T, testStore *Store, userID, sessionID string) {
	t.Helper()
	rows, err := testStore.db.Query(`
		SELECT id, started_at, stopped_at FROM time_entries
		WHERE user_id = ? AND agent_session_id = ? AND deleted_at IS NULL
		ORDER BY started_at, id`, userID, sessionID)
	if err != nil {
		t.Fatalf("list session rows: %v", err)
	}
	type interval struct {
		id        string
		startedAt int64
		stoppedAt *int64
	}
	intervals := []interval{}
	for rows.Next() {
		var row interval
		if err := rows.Scan(&row.id, &row.startedAt, &row.stoppedAt); err != nil {
			rows.Close()
			t.Fatalf("scan session row: %v", err)
		}
		intervals = append(intervals, row)
	}
	if err := closeRows(rows); err != nil {
		t.Fatalf("close session rows: %v", err)
	}
	for index := 0; index+1 < len(intervals); index++ {
		earlier, later := intervals[index], intervals[index+1]
		if earlier.stoppedAt == nil || *earlier.stoppedAt > later.startedAt {
			t.Fatalf("row %s (%d..%v) overlaps row %s starting at %d",
				earlier.id, earlier.startedAt, earlier.stoppedAt, later.id, later.startedAt)
		}
	}
}

// One push can carry the Undo and an edit of the replacement. Settling runs on
// the state the whole push left, so the order of the two rows cannot matter.
func TestAgentRestoreAndReplacementEditInOnePushIsOrderIndependent(t *testing.T) {
	run := func(t *testing.T, restoreFirst bool) restoreOutcome {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-restore-one-push@test.local")
		base := restoreTestBase()

		sessionID := uuid.NewString()
		started := startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base)
		rowID := *started.TimeEntryID
		userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
		replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+60_000).TimeEntryID

		restored := storedTimeEntry(t, testStore, user.ID, rowID)
		restored.StoppedAt = nil
		restored.UpdatedAt++
		edited := storedTimeEntry(t, testStore, user.ID, replacementID)
		edited.Description = "Kept by hand"
		edited.UpdatedAt++
		push := []TimeEntry{restored, edited}
		if !restoreFirst {
			push = []TimeEntry{edited, restored}
		}
		if _, err := testStore.Sync(context.Background(), user.ID, SyncRequest{
			Changes: SyncChanges{TimeEntries: push},
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
		assertNoOverlap(t, testStore, user.ID, sessionID)
		return restoreOutcomeOf(t, testStore, user.ID, sessionID, base,
			map[string]string{rowID: "restored", replacementID: "replacement"})
	}

	closedAt := int64(60_000)
	want := restoreOutcome{
		Entries: map[string]entryShape{
			"restored":    {StartedAt: 0, StoppedAt: &closedAt, Description: "Claude Code #"},
			"replacement": {StartedAt: 60_000, Description: "Kept by hand"},
		},
		SessionOn: "replacement", Edited: true, Named: true,
	}
	restoreFirst := run(t, true)
	editFirst := run(t, false)
	if !reflect.DeepEqual(restoreFirst, editFirst) {
		t.Fatalf("the push order changed the outcome:\nrestore first %+v\n   edit first %+v", restoreFirst, editFirst)
	}
	if !reflect.DeepEqual(restoreFirst, want) {
		t.Fatalf("unexpected outcome:\n got %+v\nwant %+v", restoreFirst, want)
	}
}

// The Timer page undoes stops made in a quick burst together. The user stopped A,
// a heartbeat opened B, the user stopped B too, and one Undo restarts both. A
// settles first: B is the session's row and was written by this very push, so it
// counts as touched and A closes where B begins. B is the session's row, so it
// simply runs on. Either order of the two rows gives that one timeline.
func TestAgentUndoOfTwoStopsInOnePushIsOrderIndependent(t *testing.T) {
	run := func(t *testing.T, firstRowFirst bool) restoreOutcome {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-undo-two-stops@test.local")
		base := restoreTestBase()

		sessionID := uuid.NewString()
		firstID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
		userStopsEntry(t, testStore, user.ID, firstID, base+30_000)
		secondID := *testHeartbeat(t, testStore, user.ID, sessionID, base+60_000).TimeEntryID
		userStopsEntry(t, testStore, user.ID, secondID, base+90_000)

		cursor := currentSyncSeq(t, testStore)
		first := storedTimeEntry(t, testStore, user.ID, firstID)
		first.StoppedAt = nil
		first.UpdatedAt++
		second := storedTimeEntry(t, testStore, user.ID, secondID)
		second.StoppedAt = nil
		second.UpdatedAt++
		push := []TimeEntry{first, second}
		if !firstRowFirst {
			push = []TimeEntry{second, first}
		}
		if _, err := testStore.Sync(context.Background(), user.ID, SyncRequest{
			Changes: SyncChanges{TimeEntries: push},
		}); err != nil {
			t.Fatalf("push: %v", err)
		}

		assertClosedAt(t, testStore, user.ID, firstID, base+60_000)
		assertNoOverlap(t, testStore, user.ID, sessionID)
		assertPulledFrom(t, testStore, user.ID, cursor, firstID, secondID)
		testHeartbeat(t, testStore, user.ID, sessionID, base+120_000)
		assertSessionRunsOnly(t, testStore, user.ID, sessionID, secondID)
		return restoreOutcomeOf(t, testStore, user.ID, sessionID, base,
			map[string]string{firstID: "first", secondID: "second"})
	}

	closedAt := int64(60_000)
	want := restoreOutcome{
		Entries: map[string]entryShape{
			"first":  {StartedAt: 0, StoppedAt: &closedAt, Description: "Claude Code #"},
			"second": {StartedAt: 60_000, Description: "Claude Code #"},
		},
		SessionOn: "second", Edited: true, Named: false,
	}
	firstRowFirst := run(t, true)
	secondRowFirst := run(t, false)
	if !reflect.DeepEqual(firstRowFirst, secondRowFirst) {
		t.Fatalf("the push order changed the outcome:\n first row first %+v\nsecond row first %+v", firstRowFirst, secondRowFirst)
	}
	if !reflect.DeepEqual(firstRowFirst, want) {
		t.Fatalf("unexpected outcome:\n got %+v\nwant %+v", firstRowFirst, want)
	}
}

// The stop hook ended the session before the Undo arrived. Nothing after its end
// was tracked, so the restored row closes there - but never before the user's
// own stop - and the session points at it, so a resume continues it.
func TestAgentUndoAfterStopHook(t *testing.T) {
	tests := []struct {
		name string
		// arrange builds the timeline and returns the session end, the moment the
		// restored row must close at, and the replacement (empty when none).
		arrange func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string)
	}{
		{
			name: "without a replacement",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string) {
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				userStopsEntry(t, testStore, userID, rowID, base+90_000)
				testStop(t, testStore, userID, sessionID, base+120_000, "session_end")
				return base + 120_000, base + 120_000, ""
			},
		},
		{
			name: "with an untouched replacement",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string) {
				userStopsEntry(t, testStore, userID, rowID, base+30_000)
				replacementID := *testHeartbeat(t, testStore, userID, sessionID, base+60_000).TimeEntryID
				// Long enough that the stop keeps the replacement as real work: the
				// cleanup of sub-30-second technical rows must not be what removes it.
				testHeartbeat(t, testStore, userID, sessionID, base+180_000)
				testStop(t, testStore, userID, sessionID, base+200_000, "session_end")
				assertClosedAt(t, testStore, userID, replacementID, base+200_000)
				return base + 200_000, base + 200_000, replacementID
			},
		},
		{
			// A stop hook delivered after more than the idle threshold of silence is
			// trimmed back to the last heartbeat, which lies before the user's stop.
			name: "session end trimmed below the user's stop",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string) {
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				userStopsEntry(t, testStore, userID, rowID, base+300_000)
				stopped := testStop(t, testStore, userID, sessionID, base+60_000+testIdleMs+300_000, "session_end")
				if stopped.EndedAt == nil || *stopped.EndedAt != base+60_000 {
					t.Fatalf("the stop must be trimmed to the last heartbeat: %+v", stopped)
				}
				return base + 60_000, base + 300_000, ""
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-undo-after-stop@test.local")
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			endedAt, closedAt, replacementID := test.arrange(t, testStore, user.ID, sessionID, rowID, base)

			cursor := currentSyncSeq(t, testStore)
			userUndoesStop(t, testStore, user.ID, rowID)
			session := assertSessionEndedOn(t, testStore, user.ID, sessionID, rowID, endedAt, closedAt)
			if !session.EntryUserEdited || session.EntryUserNamed {
				t.Fatalf("the undo is an outside write that keeps the automatic name: %+v", session)
			}
			pulled := []string{rowID}
			if replacementID != "" {
				assertTombstoned(t, testStore, user.ID, replacementID, cursor)
				pulled = append(pulled, replacementID)
			}
			assertPulledFrom(t, testStore, user.ID, cursor, pulled...)
			if count := countUserEntries(t, testStore, user.ID); count != 1 {
				t.Fatalf("expected exactly one entry, got %d", count)
			}

			// Work resuming inside the idle threshold continues the restored row:
			// the session owns it, so no second row opens.
			testHeartbeat(t, testStore, user.ID, sessionID, max(endedAt, closedAt)+60_000)
			assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
			if count := countUserEntries(t, testStore, user.ID); count != 1 {
				t.Fatalf("expected exactly one entry after the resume, got %d", count)
			}
		})
	}
}

// Reconciliation ended the session at its last heartbeat before the Undo came.
func TestAgentUndoAfterReconciliation(t *testing.T) {
	tests := []struct {
		name string
		// arrange returns the moment of the last heartbeat, the moment the restored
		// row must close at, and the replacement (empty when none).
		arrange func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string)
	}{
		{
			name: "user stopped after the last heartbeat",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string) {
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				userStopsEntry(t, testStore, userID, rowID, base+120_000)
				return base + 60_000, base + 120_000, ""
			},
		},
		{
			name: "replacement closed by reconciliation",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (int64, int64, string) {
				userStopsEntry(t, testStore, userID, rowID, base+30_000)
				replacementID := *testHeartbeat(t, testStore, userID, sessionID, base+60_000).TimeEntryID
				testHeartbeat(t, testStore, userID, sessionID, base+180_000)
				return base + 180_000, base + 180_000, replacementID
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-undo-after-reconcile@test.local")
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			lastHeartbeat, closedAt, replacementID := test.arrange(t, testStore, user.ID, sessionID, rowID, base)
			if closed, err := testStore.ReconcileAgentSessions(context.Background(), lastHeartbeat+testGraceMs+1, testGraceMs); err != nil || closed != 1 {
				t.Fatalf("reconcile: closed %d, err %v", closed, err)
			}

			cursor := currentSyncSeq(t, testStore)
			userUndoesStop(t, testStore, user.ID, rowID)
			assertSessionEndedOn(t, testStore, user.ID, sessionID, rowID, lastHeartbeat, closedAt)
			if replacementID != "" {
				assertTombstoned(t, testStore, user.ID, replacementID, cursor)
				assertPulledFrom(t, testStore, user.ID, cursor, rowID, replacementID)
			}
			if count := countUserEntries(t, testStore, user.ID); count != 1 {
				t.Fatalf("expected exactly one entry, got %d", count)
			}
		})
	}
}

// A device that was away pushes a row the server itself ended meanwhile - an idle
// split, including one after a capped tool run. The server's end stands: the
// row keeps the pushed edits, but is not reopened across a gap nobody worked,
// and the session and its current row are left exactly as they were.
func TestAgentStalePushOfAServerEndedRowKeepsTheServerEnd(t *testing.T) {
	tests := []struct {
		name string
		// ago is how far in the past the timeline starts.
		ago time.Duration
		// arrange works the session until the server ends the first row, and
		// returns the moment it ended it at.
		arrange func(t *testing.T, testStore *Store, userID, sessionID string, base int64) int64
		rename  string
	}{
		{
			name: "overnight idle split, renamed on the stale device",
			ago:  20 * time.Hour,
			arrange: func(t *testing.T, testStore *Store, userID, sessionID string, base int64) int64 {
				workUntil(t, testStore, userID, sessionID, base, base+30*60_000)
				testHeartbeat(t, testStore, userID, sessionID, base+15*60*60_000)
				return base + 30*60_000
			},
			rename: "Renamed offline",
		},
		{
			// The tool cap bills the first ToolMaxMs of the gap and opens the next
			// row right after it, well inside the idle threshold of the close.
			name: "split after a capped tool run",
			ago:  3 * time.Hour,
			arrange: func(t *testing.T, testStore *Store, userID, sessionID string, base int64) int64 {
				testToolStart(t, testStore, userID, sessionID, base+60_000)
				testHeartbeat(t, testStore, userID, sessionID, base+60_000+testPolicy.ToolMaxMs+120_000)
				return base + 60_000 + testPolicy.ToolMaxMs
			},
		},
		{
			// Stop and Undo both happened offline, after the agent came back from a
			// pause the server had already cut: the device pushes only the result.
			name: "offline Stop and Undo after the agent came back",
			ago:  5 * time.Hour,
			arrange: func(t *testing.T, testStore *Store, userID, sessionID string, base int64) int64 {
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				testHeartbeat(t, testStore, userID, sessionID, base+60_000+testIdleMs+2*60*60_000)
				return base + 60_000
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-stale-server-end@test.local")
			base := time.Now().Add(-test.ago).UnixMilli()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			deviceCopy := storedTimeEntry(t, testStore, user.ID, rowID)
			serverEnd := test.arrange(t, testStore, user.ID, sessionID, base)
			assertClosedAt(t, testStore, user.ID, rowID, serverEnd)
			sessionBefore := getTestAgentSession(t, testStore, user.ID, sessionID)
			replacementBefore := storedTimeEntry(t, testStore, user.ID, *sessionBefore.TimeEntryID)

			cursor := currentSyncSeq(t, testStore)
			wantDescription := deviceCopy.Description
			if test.rename != "" {
				deviceCopy.Description = test.rename
				wantDescription = test.rename
			}
			pushRunning(t, testStore, user.ID, deviceCopy)

			assertClosedAt(t, testStore, user.ID, rowID, serverEnd)
			if stored := storedTimeEntry(t, testStore, user.ID, rowID); stored.Description != wantDescription {
				t.Fatalf("the pushed edit must be kept: %+v", stored)
			}
			assertUnchanged(t, testStore, user.ID, replacementBefore)
			assertSessionUnchanged(t, testStore, user.ID, sessionBefore)
			assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementBefore.ID)
			assertPulledFrom(t, testStore, user.ID, cursor, rowID)
		})
	}
}

// The overnight case measured end to end: 30 minutes before the pause and 5
// after it are 35 minutes of work, whatever the stale device pushed in between.
func TestAgentStalePushAcrossAnOvernightSplitBillsOnlyTheWork(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-stale-overnight@test.local")
	base := time.Now().Add(-20 * time.Hour).UnixMilli()
	resumed := base + 15*60*60_000

	sessionID := uuid.NewString()
	rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	deviceCopy := storedTimeEntry(t, testStore, user.ID, rowID)
	workUntil(t, testStore, user.ID, sessionID, base, base+30*60_000)
	testHeartbeat(t, testStore, user.ID, sessionID, resumed)
	deviceCopy.Description = "Renamed offline"
	pushRunning(t, testStore, user.ID, deviceCopy)
	workUntil(t, testStore, user.ID, sessionID, resumed, resumed+5*60_000)
	testStop(t, testStore, user.ID, sessionID, resumed+5*60_000, "session_end")

	total := int64(0)
	for _, shape := range userEntryShapes(t, testStore, user.ID) {
		if shape.Deleted {
			continue
		}
		if shape.StoppedAt == nil {
			t.Fatalf("nothing may run after the stop hook: %+v", shape)
		}
		total += *shape.StoppedAt - shape.StartedAt
	}
	if total != 35*60_000 {
		t.Fatalf("expected 35 minutes billed, got %d ms", total)
	}
}

// Rows A, B and C of an ended session, and a late push of A running. Nothing
// after A may be deleted or billed twice.
func TestAgentLatePushWithSeveralLaterRowsClosesItAgain(t *testing.T) {
	tests := []struct {
		name string
		// arrange works the session into three rows and ends it. It returns the
		// device's copy of A and the moment A must stay closed at.
		arrange func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (TimeEntry, int64)
	}{
		{
			name: "ends written by the server",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (TimeEntry, int64) {
				deviceCopy := storedTimeEntry(t, testStore, userID, rowID)
				at := base
				for segment := range 3 {
					if segment > 0 {
						// A pause beyond the idle threshold: the server closes the
						// previous row and this signal opens the next one.
						at += testIdleMs + 60_000
						testHeartbeat(t, testStore, userID, sessionID, at)
					}
					at += 5 * 60_000
					testHeartbeat(t, testStore, userID, sessionID, at)
				}
				testStop(t, testStore, userID, sessionID, at, "session_end")
				return deviceCopy, base + 5*60_000
			},
		},
		{
			name: "A stopped by the user",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) (TimeEntry, int64) {
				userStopsEntry(t, testStore, userID, rowID, base+5*60_000)
				deviceCopy := storedTimeEntry(t, testStore, userID, rowID)
				testHeartbeat(t, testStore, userID, sessionID, base+6*60_000)
				testHeartbeat(t, testStore, userID, sessionID, base+10*60_000)
				resumed := base + 10*60_000 + testIdleMs + 60_000
				testHeartbeat(t, testStore, userID, sessionID, resumed)
				testHeartbeat(t, testStore, userID, sessionID, resumed+5*60_000)
				testStop(t, testStore, userID, sessionID, resumed+5*60_000, "session_end")
				return deviceCopy, base + 5*60_000
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-late-push-several@test.local")
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			deviceCopy, closedAt := test.arrange(t, testStore, user.ID, sessionID, rowID, base)
			sessionBefore := getTestAgentSession(t, testStore, user.ID, sessionID)
			if sessionBefore.Status != agentStatusClosed {
				t.Fatalf("the session must have ended: %+v", sessionBefore)
			}
			others := []TimeEntry{}
			for entryID, shape := range userEntryShapes(t, testStore, user.ID) {
				if entryID != rowID && !shape.Deleted {
					others = append(others, storedTimeEntry(t, testStore, user.ID, entryID))
				}
			}
			if len(others) != 2 {
				t.Fatalf("expected two later rows, got %d", len(others))
			}

			pushRunning(t, testStore, user.ID, deviceCopy)
			assertClosedAt(t, testStore, user.ID, rowID, closedAt)
			for _, other := range others {
				assertUnchanged(t, testStore, user.ID, other)
			}
			assertSessionUnchanged(t, testStore, user.ID, sessionBefore)
		})
	}
}

// The Stop was pushed, the agent came back long after, and only then the Undo
// arrived. The replacement opened beyond the idle threshold is work of its own.
func TestAgentUndoAfterTheAgentCameBackBeyondIdle(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-beyond-idle@test.local")
	base := time.Now().Add(-5 * time.Hour).UnixMilli()

	sessionID := uuid.NewString()
	rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
	userStopsEntry(t, testStore, user.ID, rowID, base+120_000)
	replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+2*60*60_000).TimeEntryID
	replacement := storedTimeEntry(t, testStore, user.ID, replacementID)
	sessionBefore := getTestAgentSession(t, testStore, user.ID, sessionID)

	userUndoesStop(t, testStore, user.ID, rowID)
	assertClosedAt(t, testStore, user.ID, rowID, base+120_000)
	assertUnchanged(t, testStore, user.ID, replacement)
	assertSessionUnchanged(t, testStore, user.ID, sessionBefore)
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
}

// set_agent_task renamed the row between the Stop and the Undo, and the Undo
// comes from a device that had not pulled that rename. The old session tag it
// carries is not a name the user chose: the row gets the task name back, stays
// automatic, and the next task rename still reaches it.
func TestAgentUndoAfterSetAgentTaskKeepsTheTaskName(t *testing.T) {
	for _, heartbeatBetween := range []bool{false, true} {
		name := "no heartbeat between"
		if heartbeatBetween {
			name = "replacement opened between"
		}
		t.Run(name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-undo-after-task@test.local")
			ctx := context.Background()
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
			undoCopy := storedTimeEntry(t, testStore, user.ID, rowID)
			if heartbeatBetween {
				testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
			}
			if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "GH-1", "First task"); err != nil {
				t.Fatalf("set task: %v", err)
			}
			if renamed := storedTimeEntry(t, testStore, user.ID, rowID); renamed.Description != "GH-1 First task" {
				t.Fatalf("set_agent_task must rename the stopped row: %+v", renamed)
			}

			pushRunning(t, testStore, user.ID, undoCopy)
			session := assertSessionOn(t, testStore, user.ID, sessionID, rowID)
			if session.EntryUserNamed {
				t.Fatalf("an automatic name pushed back is not the user's: %+v", session)
			}
			if restored := storedTimeEntry(t, testStore, user.ID, rowID); restored.Description != "GH-1 First task" {
				t.Fatalf("the restored row must carry the task name: %+v", restored)
			}
			adopted := testHeartbeat(t, testStore, user.ID, sessionID, base+90_000)
			if adopted.EntryUserNamed || adopted.TimeEntryID == nil || *adopted.TimeEntryID != rowID {
				t.Fatalf("the next heartbeat must adopt the row without making the name the user's: %+v", adopted)
			}

			if _, err := testStore.SetAgentTask(ctx, user.ID, AgentTaskSelector{SessionID: sessionID}, "GH-2", "Second task"); err != nil {
				t.Fatalf("set second task: %v", err)
			}
			if renamed := storedTimeEntry(t, testStore, user.ID, rowID); renamed.Description != "GH-2 Second task" {
				t.Fatalf("a later task rename must still reach the restored row: %+v", renamed)
			}
		})
	}
}

// The restored row is the session's again, so its project is what the next
// segment after a pause opens under - the same carry-over an edit of the current
// row gets.
func TestAgentRestoredRowCarriesItsProjectIntoTheSession(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-restore-project@test.local")
	base := restoreTestBase()
	projectID := createTestProject(t, testStore, user.ID, "Restored")

	sessionID := uuid.NewString()
	rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
	testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)

	undone := storedTimeEntry(t, testStore, user.ID, rowID)
	undone.ProjectID = &projectID
	pushRunning(t, testStore, user.ID, undone)
	session := assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	if session.ProjectID == nil || *session.ProjectID != projectID {
		t.Fatalf("the session must take the restored row's project: %+v", session.ProjectID)
	}

	next := testHeartbeat(t, testStore, user.ID, sessionID, base+60_000+testIdleMs+60_000)
	if next.TimeEntryID == nil || *next.TimeEntryID == rowID {
		t.Fatalf("the pause must open a new segment: %+v", next)
	}
	if segment := storedTimeEntry(t, testStore, user.ID, *next.TimeEntryID); segment.ProjectID == nil || *segment.ProjectID != projectID {
		t.Fatalf("the next segment must open under the restored row's project: %+v", segment)
	}
}

// Undo of a delete is a restore like Undo of a Stop, with the tombstone as the
// user's end. The replacement it removes was running, so its tombstone is the
// server's end: a device that still shows it running and renames it gets the
// tombstone back rather than a second running row.
func TestAgentUndoOfADelete(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-undo-delete@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	deleted := storedTimeEntry(t, testStore, user.ID, rowID)
	deletedAt := base + 30_000
	deleted.DeletedAt = &deletedAt
	pushEntry(t, testStore, user.ID, deleted)
	replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+60_000).TimeEntryID
	secondDevice := storedTimeEntry(t, testStore, user.ID, replacementID)

	cursor := currentSyncSeq(t, testStore)
	pushRunning(t, testStore, user.ID, storedTimeEntry(t, testStore, user.ID, rowID))
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
	assertTombstoned(t, testStore, user.ID, replacementID, cursor)
	tombstone := storedTimeEntry(t, testStore, user.ID, replacementID)

	secondDevice.Description = "Renamed on the other device"
	pushRunning(t, testStore, user.ID, secondDevice)
	if stored := storedTimeEntry(t, testStore, user.ID, replacementID); stored.DeletedAt == nil || *stored.DeletedAt != *tombstone.DeletedAt {
		t.Fatalf("the server's tombstone must be put back: %+v", stored)
	}
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
}

// Several restored rows of one session are settled in started_at order, not in
// push order: an earlier row's outcome is what a later one sees.
func TestAgentSeveralRestoredRowsOfOneSessionSettleInStartOrder(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-restore-several@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	firstID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	userStopsEntry(t, testStore, user.ID, firstID, base+5*60_000)
	secondID := *testHeartbeat(t, testStore, user.ID, sessionID, base+6*60_000).TimeEntryID
	userStopsEntry(t, testStore, user.ID, secondID, base+8*60_000)
	thirdID := *testHeartbeat(t, testStore, user.ID, sessionID, base+9*60_000).TimeEntryID

	cursor := currentSyncSeq(t, testStore)
	first := storedTimeEntry(t, testStore, user.ID, firstID)
	first.StoppedAt = nil
	first.UpdatedAt++
	second := storedTimeEntry(t, testStore, user.ID, secondID)
	second.StoppedAt = nil
	second.UpdatedAt++
	// The later row first: settling in push order would let the second row take
	// the session back before the first is decided, and then close the first at
	// the second's start instead of at the user's own stop.
	if _, err := testStore.Sync(context.Background(), user.ID, SyncRequest{
		Changes: SyncChanges{TimeEntries: []TimeEntry{second, first}},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	assertClosedAt(t, testStore, user.ID, firstID, base+5*60_000)
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, secondID)
	assertTombstoned(t, testStore, user.ID, thirdID, cursor)
}

// A restored row whose start was edited past the session's running row finds
// nothing "later", but taking it back would leave that running row with no
// session. It is closed again instead, and the session keeps its row.
func TestAgentRestoreNeverOrphansTheSessionRunningRow(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "agent-restore-orphan@test.local")
	base := restoreTestBase()

	sessionID := uuid.NewString()
	rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
	userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
	replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+60_000).TimeEntryID
	replacement := storedTimeEntry(t, testStore, user.ID, replacementID)

	undone := storedTimeEntry(t, testStore, user.ID, rowID)
	undone.StartedAt = base + 90_000
	pushRunning(t, testStore, user.ID, undone)

	if stored := storedTimeEntry(t, testStore, user.ID, rowID); stored.StoppedAt == nil || stored.DeletedAt != nil {
		t.Fatalf("the restored row must be closed again: %+v", stored)
	}
	assertUnchanged(t, testStore, user.ID, replacement)
	assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
}

// The idle threshold the rule compares with is the store's, set from
// WORKTIME_AGENT_IDLE: the same replacement two minutes after the stop is a
// duplicate under the default ten minutes and work of its own under one minute.
func TestAgentRestoreUsesTheStoreIdleThreshold(t *testing.T) {
	tests := []struct {
		name       string
		options    []Option
		tombstoned bool
	}{
		{name: "default threshold", tombstoned: true},
		{name: "one minute threshold", options: []Option{WithAgentIdle(time.Minute)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore, err := Open(t.TempDir()+"/idle.db", test.options...)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { testStore.Close() })
			user := testUser(t, testStore, "agent-restore-idle@test.local")
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			userStopsEntry(t, testStore, user.ID, rowID, base+30_000)
			replacementID := *testHeartbeat(t, testStore, user.ID, sessionID, base+150_000).TimeEntryID

			cursor := currentSyncSeq(t, testStore)
			userUndoesStop(t, testStore, user.ID, rowID)
			if test.tombstoned {
				assertTombstoned(t, testStore, user.ID, replacementID, cursor)
				assertSessionRunsOnly(t, testStore, user.ID, sessionID, rowID)
			} else {
				assertClosedAt(t, testStore, user.ID, rowID, base+30_000)
				assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
			}
		})
	}
}

// Replaying a push the server already settled changes nothing: the rule's own
// writes step updated_at past the pushed value, so the replay either loses
// last-write-wins or finds the row already running.
func TestAgentRestoreReplayIsANoOp(t *testing.T) {
	tests := []struct {
		name string
		// arrange returns the push to replay.
		arrange func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) TimeEntry
	}{
		{
			name: "untouched replacement tombstoned",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) TimeEntry {
				userStopsEntry(t, testStore, userID, rowID, base+30_000)
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				return storedTimeEntry(t, testStore, userID, rowID)
			},
		},
		{
			name: "server end put back",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) TimeEntry {
				deviceCopy := storedTimeEntry(t, testStore, userID, rowID)
				testHeartbeat(t, testStore, userID, sessionID, base+60_000)
				testHeartbeat(t, testStore, userID, sessionID, base+60_000+testIdleMs+60_000)
				return deviceCopy
			},
		},
		{
			name: "closed again at the user's stop",
			arrange: func(t *testing.T, testStore *Store, userID, sessionID, rowID string, base int64) TimeEntry {
				userStopsEntry(t, testStore, userID, rowID, base+30_000)
				testHeartbeat(t, testStore, userID, sessionID, base+30_000+testIdleMs+60_000)
				return storedTimeEntry(t, testStore, userID, rowID)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testStore := openTestStore(t)
			user := testUser(t, testStore, "agent-restore-replay@test.local")
			ctx := context.Background()
			base := restoreTestBase()

			sessionID := uuid.NewString()
			rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
			replayed := test.arrange(t, testStore, user.ID, sessionID, rowID, base)
			replayed.StoppedAt = nil
			replayed.DeletedAt = nil
			replayed.UpdatedAt = max(storedTimeEntry(t, testStore, user.ID, rowID).UpdatedAt, time.Now().UnixMilli()) + 1
			push := SyncRequest{Changes: SyncChanges{TimeEntries: []TimeEntry{replayed}}}
			if _, err := testStore.Sync(ctx, user.ID, push); err != nil {
				t.Fatalf("push: %v", err)
			}
			entries := userEntryShapes(t, testStore, user.ID)
			session := sessionShapeOf(getTestAgentSession(t, testStore, user.ID, sessionID))

			cursor := currentSyncSeq(t, testStore)
			if _, err := testStore.Sync(ctx, user.ID, push); err != nil {
				t.Fatalf("replay: %v", err)
			}
			if after := currentSyncSeq(t, testStore); after != cursor+1 {
				t.Fatalf("a replay must take only its reserved seq, cursor moved %d -> %d", cursor, after)
			}
			if after := userEntryShapes(t, testStore, user.ID); !reflect.DeepEqual(after, entries) {
				t.Fatalf("a replay must not change any row:\n got %+v\nwant %+v", after, entries)
			}
			if after := sessionShapeOf(getTestAgentSession(t, testStore, user.ID, sessionID)); !reflect.DeepEqual(after, session) {
				t.Fatalf("a replay must not change the session:\n got %+v\nwant %+v", after, session)
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
				base := restoreTestBase()
				projectID := createTestProject(t, testStore, user.ID, "Current")

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

	t.Run("edit of a finished row that stays finished", func(t *testing.T) {
		testStore := openTestStore(t)
		user := testUser(t, testStore, "agent-restore-finished@test.local")
		base := restoreTestBase()

		sessionID := uuid.NewString()
		rowID := *startWorkingTestAgentSession(t, testStore, user.ID, sessionID, base).TimeEntryID
		testHeartbeat(t, testStore, user.ID, sessionID, base+60_000)
		testHeartbeat(t, testStore, user.ID, sessionID, base+60_000+testIdleMs+60_000)
		before := getTestAgentSession(t, testStore, user.ID, sessionID)
		finished := storedTimeEntry(t, testStore, user.ID, rowID)
		finished.Description = "Renamed later"

		cursor := currentSyncSeq(t, testStore)
		pushEntry(t, testStore, user.ID, finished)
		if after := currentSyncSeq(t, testStore); after != cursor+1 {
			t.Fatalf("an edit of a finished row must take exactly its reserved seq, cursor moved %d -> %d", cursor, after)
		}
		assertSessionUnchanged(t, testStore, user.ID, before)
		assertClosedAt(t, testStore, user.ID, rowID, base+60_000)
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
		assertSessionUnchanged(t, testStore, user.ID, before)

		// Restarting a stopped manual row is not an agent restore either.
		stopped := storedTimeEntry(t, testStore, user.ID, manual.ID)
		stoppedAt := base + 20_000
		stopped.StoppedAt = &stoppedAt
		pushEntry(t, testStore, user.ID, stopped)
		cursor = currentSyncSeq(t, testStore)
		pushRunning(t, testStore, user.ID, storedTimeEntry(t, testStore, user.ID, manual.ID))
		if after := currentSyncSeq(t, testStore); after != cursor+1 {
			t.Fatalf("restarting a manual row must take exactly its reserved seq, cursor moved %d -> %d", cursor, after)
		}
		if restarted := storedTimeEntry(t, testStore, user.ID, manual.ID); restarted.StoppedAt != nil {
			t.Fatalf("a manual row restarts as pushed: %+v", restarted)
		}
		assertSessionUnchanged(t, testStore, user.ID, before)
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
		assertSessionUnchanged(t, testStore, user.ID, before)
		assertSessionRunsOnly(t, testStore, user.ID, sessionID, replacementID)
		if row := storedTimeEntry(t, testStore, user.ID, rowID); row.StoppedAt == nil || *row.StoppedAt != base+30_000 {
			t.Fatalf("the user's stop must stand: %+v", row)
		}
	})
}
