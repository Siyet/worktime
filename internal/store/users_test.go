package store

import (
	"context"
	"errors"
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

	first, err := testStore.FindOrCreateGoogleUser(ctx, "sub-old", "person@test.local", "Person", "old.png", true)
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}

	second, err := testStore.FindOrCreateGoogleUser(ctx, "sub-new", "person@test.local", "Person", "old.png", true)
	if err != nil {
		t.Fatalf("sign-in with a rotated subject: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same account, got %s then %s", first.ID, second.ID)
	}

	// And the new subject is the one that resolves from now on.
	again, err := testStore.FindOrCreateGoogleUser(ctx, "sub-new", "person@test.local", "Person", "old.png", true)
	if err != nil {
		t.Fatalf("repeat sign-in: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("expected the adopted account, got %s", again.ID)
	}
}

// Adoption hands an existing account to a new credential on the strength of the address
// alone, which only means "the owner invited you" on an instance with an allowlist. An
// open instance must refuse instead, or whoever later acquires a lapsed domain or a
// reassigned Workspace address inherits the account.
func TestFindOrCreateGoogleUserRefusesAdoptionWhenNotAllowed(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	if _, err := testStore.FindOrCreateGoogleUser(ctx, "sub-old", "person@test.local", "Person", "", false); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	_, err := testStore.FindOrCreateGoogleUser(ctx, "sub-attacker", "person@test.local", "Person", "", false)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected the sign-in refused, got %v", err)
	}

	// And the account still belongs to the original subject.
	original, err := testStore.FindOrCreateGoogleUser(ctx, "sub-old", "person@test.local", "Person", "", false)
	if err != nil {
		t.Fatalf("original sign-in: %v", err)
	}
	if original.ID == "" {
		t.Fatal("the original account must still resolve")
	}
}

// The profile refresh must never write the UNIQUE email column: a routine sign-in
// cannot be allowed to fail on another row's address.
func TestFindOrCreateGoogleUserSurvivesAnEmailCollision(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	taken, err := testStore.FindOrCreateGoogleUser(ctx, "sub-a", "taken@test.local", "A", "", false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := testStore.FindOrCreateGoogleUser(ctx, "sub-b", "own@test.local", "B", "", false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Google now reports the address that already belongs to the other account.
	returning, err := testStore.FindOrCreateGoogleUser(ctx, "sub-b", "taken@test.local", "B", "new.png", false)
	if err != nil {
		t.Fatalf("a returning user must still sign in: %v", err)
	}
	if returning.ID == taken.ID {
		t.Fatal("the sign-in must not resolve to the other account")
	}
}

func TestFindOrCreateGoogleUserRefreshesTheProfile(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	created, err := testStore.FindOrCreateGoogleUser(ctx, "sub-1", "who@test.local", "Old Name", "old.png", false)
	if err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if _, err := testStore.FindOrCreateGoogleUser(ctx, "sub-1", "who@test.local", "New Name", "new.png", false); err != nil {
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

	one, err := testStore.FindOrCreateGoogleUser(ctx, "sub-a", "a@test.local", "A", "", false)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	other, err := testStore.FindOrCreateGoogleUser(ctx, "sub-b", "b@test.local", "B", "", false)
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
