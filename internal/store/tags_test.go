package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTagsRoundtripThroughSync(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "tags@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: entryID, Description: "grooming", Tags: TagList{"analysis", "meeting"},
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	pulled, err := testStore.Sync(ctx, user.ID, SyncRequest{Since: 0})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pulled.Changes.TimeEntries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(pulled.Changes.TimeEntries))
	}
	if got := strings.Join(pulled.Changes.TimeEntries[0].Tags, ","); got != "analysis,meeting" {
		t.Fatalf("tags did not survive the roundtrip: %q", got)
	}
}

// The upsert path is the one that silently drops tags: without tags = excluded.tags in
// the ON CONFLICT clause the insert carries them and every later edit blanks them.
func TestTagsSurviveUpdate(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "update@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: entryID, Description: "first", Tags: TagList{"development"},
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: entryID, Description: "edited", Tags: TagList{"development", "review"},
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 2000,
		}},
	}}); err != nil {
		t.Fatalf("update: %v", err)
	}

	entry, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := strings.Join(entry.Tags, ","); got != "development,review" {
		t.Fatalf("update lost tags: %q", got)
	}
}

// GetTimeEntry feeds the MCP stop_timer path, which reads the row and pushes it back
// whole. A tags column missing from that SELECT blanks the entry on every MCP stop and
// broadcasts the blanking to every device with a fresh server_seq.
func TestTagsVisibleToEntryReaders(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "readers@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: entryID, Description: "running", Tags: TagList{"meeting"},
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	single, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(single.Tags) != 1 || single.Tags[0] != "meeting" {
		t.Fatalf("GetTimeEntry dropped tags: %v", single.Tags)
	}

	running, err := testStore.ListRunningEntries(ctx, user.ID)
	if err != nil {
		t.Fatalf("list running: %v", err)
	}
	if len(running) != 1 || len(running[0].Tags) != 1 || running[0].Tags[0] != "meeting" {
		t.Fatalf("ListRunningEntries dropped tags: %+v", running)
	}
}

// Rows written before migration 003 hold the column default, and a client that predates
// tags sends no field at all. Both must read back as an empty list, never as null.
func TestTagsDefaultToEmptyList(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "empty@test.local")
	ctx := context.Background()

	entryID := uuid.NewString()
	if _, err := testStore.Sync(ctx, user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: entryID, Description: "no tags", StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	entry, err := testStore.GetTimeEntry(ctx, user.ID, entryID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.Tags == nil {
		t.Fatal("tags scanned as nil, which marshals to null on the wire")
	}
	if len(entry.Tags) != 0 {
		t.Fatalf("expected no tags, got %v", entry.Tags)
	}
	encoded, err := entry.Tags.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty tags marshalled as %s, want []", encoded)
	}
	var nilList TagList
	encoded, err = nilList.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal nil: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("nil tags marshalled as %s, want []", encoded)
	}
}

func TestTagValidation(t *testing.T) {
	longTag := strings.Repeat("a", maxTagLength+1)
	cases := []struct {
		name string
		tags TagList
		ok   bool
	}{
		{"empty", TagList{}, true},
		{"normal", TagList{"meeting", "review"}, true},
		{"cyrillic", TagList{"анализ"}, true},
		{"max length", TagList{strings.Repeat("a", maxTagLength)}, true},
		{"too many", TagList{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, false},
		{"blank", TagList{""}, false},
		{"too long", TagList{longTag}, false},
		{"untrimmed", TagList{" meeting"}, false},
		{"uppercase", TagList{"Meeting"}, false},
		{"reserved prefix", TagList{"_untagged"}, false},
		{"duplicate", TagList{"meeting", "meeting"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateTags(testCase.tags)
			if testCase.ok && err != nil {
				t.Fatalf("expected %v to be valid, got %v", testCase.tags, err)
			}
			if !testCase.ok && err == nil {
				t.Fatalf("expected %v to be rejected", testCase.tags)
			}
		})
	}
}

func TestSyncRejectsInvalidTags(t *testing.T) {
	testStore := openTestStore(t)
	user := testUser(t, testStore, "reject@test.local")

	_, err := testStore.Sync(context.Background(), user.ID, SyncRequest{Changes: SyncChanges{
		TimeEntries: []TimeEntry{{
			ID: uuid.NewString(), Description: "bad", Tags: TagList{"Meeting"},
			StartedAt: 1000, CreatedAt: 1000, UpdatedAt: 1000,
		}},
	}})
	if err == nil {
		t.Fatal("expected uppercase tag to be rejected by the server")
	}
}
