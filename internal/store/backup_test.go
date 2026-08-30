package store

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestBackupAndNonMigratingRestorePreserveWALData(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.db")
	backupPath := filepath.Join(directory, "backup.db")
	dataStore, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	user, err := dataStore.FindOrCreateGoogleUser(t.Context(), "before", "before@example.com", "Before", "", false)
	if err != nil {
		t.Fatalf("write source: %v", err)
	}
	version, err := dataStore.UserVersion(t.Context())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := dataStore.BackupSQLite(t.Context(), backupPath); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := dataStore.FindOrCreateGoogleUser(t.Context(), "after", "after@example.com", "After", "", false); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	if _, err := dataStore.db.Exec("CREATE TABLE partial_new_migration (id INTEGER)"); err != nil {
		t.Fatalf("simulate partial migration: %v", err)
	}
	if _, err := dataStore.db.Exec("PRAGMA user_version = " + fmt.Sprint(version+1)); err != nil {
		t.Fatalf("advance simulated migration version: %v", err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}
	if err := RestoreSQLite(t.Context(), sourcePath, backupPath, version); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// Recovery re-enters rollback_started after a crash. Restoring from the same
	// immutable backup a second time must therefore be safe and deterministic.
	if err := RestoreSQLite(t.Context(), sourcePath, backupPath, version); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
	restored, err := Open(sourcePath)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer restored.Close()
	if got, err := restored.GetUser(t.Context(), user.ID); err != nil || got.Email != user.Email {
		t.Fatalf("backup row missing: %+v, %v", got, err)
	}
	if _, err := restored.GetUserBySession(t.Context(), "not-a-session"); err == nil {
		t.Fatal("sanity check expected missing session")
	}
	var count int
	if err := restored.db.QueryRow("SELECT COUNT(*) FROM users WHERE email = 'after@example.com'").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("post-backup row survived restore: %d", count)
	}
	if err := restored.db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'partial_new_migration'",
	).Scan(&count); err != nil {
		t.Fatalf("inspect restored schema: %v", err)
	}
	if count != 0 {
		t.Fatal("partially applied migration survived restore")
	}
}
