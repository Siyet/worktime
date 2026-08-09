package store

import (
	"context"
	"testing"
	"time"
)

// Google subjects are stable in practice but not guaranteed: an account deleted and
// recreated, or moved between a personal and a Workspace tenant, arrives with a new
// one. users.email is UNIQUE, so the insert used to fail and the owner was locked out
// of their own data with no way back from inside the product.
func TestFindOrCreateGoogleUserAdoptsARotatedSubject(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	first, err := testStore.FindOrCreateGoogleUser(ctx, "sub-old", "person@test.local", "Person", "old.png")
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	second, err := testStore.FindOrCreateGoogleUser(ctx, "sub-new", "person@test.local", "Person", "old.png")
	if err != nil {
		t.Fatalf("sign-in with a rotated subject: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same account, got %s then %s", first.ID, second.ID)
	}

	// And the new subject is the one that resolves from now on.
	again, err := testStore.FindOrCreateGoogleUser(ctx, "sub-new", "person@test.local", "Person", "old.png")
	if err != nil {
		t.Fatalf("repeat sign-in: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("expected the adopted account, got %s", again.ID)
	}
}

func TestFindOrCreateGoogleUserRefreshesTheProfile(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	created, err := testStore.FindOrCreateGoogleUser(ctx, "sub-1", "who@test.local", "Old Name", "old.png")
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if _, err := testStore.FindOrCreateGoogleUser(ctx, "sub-1", "who@test.local", "New Name", "new.png"); err != nil {
		t.Fatalf("second sign-in: %v", err)
	}

	stored, err := testStore.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if stored.Name != "New Name" || stored.PictureURL != "new.png" {
		t.Fatalf("expected the profile refreshed, got %+v", stored)
	}
}

// A different address is a different account, adoption must not reach across it.
func TestFindOrCreateGoogleUserKeepsDistinctEmailsApart(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	one, err := testStore.FindOrCreateGoogleUser(ctx, "sub-a", "a@test.local", "A", "")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	other, err := testStore.FindOrCreateGoogleUser(ctx, "sub-b", "b@test.local", "B", "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if one.ID == other.ID {
		t.Fatal("two addresses must not collapse into one account")
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	user := testUser(t, testStore, "sweep@test.local")

	live, _, err := testStore.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	expired, _, err := testStore.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := testStore.db.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE id = ?",
		time.Now().Add(-time.Hour).UnixMilli(), expired); err != nil {
		t.Fatalf("age the session: %v", err)
	}

	if err := testStore.DeleteExpiredSessions(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := testStore.GetUserBySession(ctx, live); err != nil {
		t.Fatalf("the live session must survive the sweep: %v", err)
	}
	var remaining int
	if err := testStore.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id = ?", expired).
		Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Fatal("the expired session is still there")
	}
}
