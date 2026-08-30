package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Siyet/worktime/internal/lifecycle"
	"github.com/Siyet/worktime/internal/store"
)

var errInjectedExec = errors.New("injected exec return")

func TestExecutableInstallerRollsBackBinaryAndDatabaseWhenExecFails(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "worktime.db")
	dataStore, err := store.Open(database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := dataStore.FindOrCreateGoogleUser(t.Context(), "before", "before@example.com", "Before", "", false); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	executable := filepath.Join(directory, "worktime")
	oldBytes := []byte("old executable")
	newBytes := []byte("new executable")
	writeFixture(t, executable, oldBytes, 0o755)

	preparedUserID := ""
	prepare := func(ctx context.Context) error {
		user, writeErr := dataStore.FindOrCreateGoogleUser(ctx, "after", "after@example.com", "After", "", false)
		if writeErr == nil {
			preparedUserID = user.ID
		}
		return errors.Join(writeErr, dataStore.Close())
	}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, ContentLength: int64(len(newBytes)),
			Body: io.NopCloser(strings.NewReader(string(newBytes))), Header: make(http.Header), Request: request,
		}, nil
	})}
	execCalls := 0
	installer := newExecutableInstaller(NativeRuntime{
		Executable: executable, DatabasePath: database, DataDirectory: directory,
		Store: dataStore, Lifecycle: lifecycle.New(), HTTPClient: client,
		Quiesce: func(context.Context) error { return nil }, Resume: func() {}, Prepare: prepare,
		HandoffFailed: func(error) {},
	}, fakeExchange, func(string, []string, []string) error {
		execCalls++
		return errInjectedExec
	})
	installer.run = func(_ context.Context, _ string, arguments, _ []string, _ string) ([]byte, error) {
		if len(arguments) == 1 && arguments[0] == "--version" {
			return []byte("worktime v1.2.3 (revision " + strings.Repeat("c", 40) + ", built test)\n"), nil
		}
		return nil, nil
	}
	digest := sha256.Sum256(newBytes)
	asset := Asset{
		OS: "linux", Arch: runtime.GOARCH, Name: "worktime-linux-" + runtime.GOARCH,
		URL: "https://github.com/Siyet/worktime/releases/download/v1.2.3/worktime", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(newBytes)),
	}
	manifest := validManifest(1, "v1.2.3")
	if err := installer.Apply(t.Context(), manifest, asset); !errors.Is(err, errInjectedExec) {
		t.Fatalf("expected injected exec failure, got %v", err)
	}
	if execCalls != 2 {
		t.Fatalf("expected updated and rollback exec attempts, got %d", execCalls)
	}
	if got, err := os.ReadFile(executable); err != nil || string(got) != string(oldBytes) {
		t.Fatalf("old executable was not restored: %q, %v", got, err)
	}
	if _, err := os.Stat(transactionPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction journal survived rollback: %v", err)
	}
	restored, err := store.Open(database)
	if err != nil {
		t.Fatalf("open restored store: %v", err)
	}
	defer restored.Close()
	if preparedUserID == "" {
		t.Fatal("prepare fixture did not mutate the real database")
	}
	if _, err := restored.GetUser(t.Context(), preparedUserID); err == nil {
		t.Fatal("write made after rollback backup survived restore")
	}
}

func TestRecoverSwapIntentByHashesAndCommitNeverRollsBack(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-fixture")
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	oldBytes := []byte("old")
	newBytes := []byte("new")
	writeFixture(t, executable, newBytes, 0o755)
	writeFixture(t, staged, oldBytes, 0o755)
	writeFixture(t, database, []byte("database"), 0o600)
	writeFixture(t, backup, []byte("backup"), 0o600)
	transaction := fixtureTransaction(executable, staged, database, backup, oldBytes, newBytes)
	transaction.State = stateSwapIntent
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write intent: %v", err)
	}
	bootstrapping, err := RecoverStartup(executable, database, directory)
	if err != nil || !bootstrapping {
		t.Fatalf("recover exchanged intent: bootstrapping=%v err=%v", bootstrapping, err)
	}
	if err := CommitStartup(directory); err != nil {
		t.Fatalf("commit startup: %v", err)
	}
	if got, err := os.ReadFile(executable); err != nil || string(got) != string(newBytes) {
		t.Fatalf("commit changed current executable: %q, %v", got, err)
	}
	for _, path := range []string{staged, backup, transactionPath(directory)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("committed recovery artifact %s survived: %v", path, err)
		}
	}
}

func TestRollbackRefusesCommittedJournal(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-fixture")
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	oldBytes := []byte("old")
	newBytes := []byte("new")
	writeFixture(t, executable, newBytes, 0o755)
	writeFixture(t, staged, oldBytes, 0o755)
	writeFixture(t, database, []byte("database"), 0o600)
	writeFixture(t, backup, []byte("backup"), 0o600)
	transaction := fixtureTransaction(executable, staged, database, backup, oldBytes, newBytes)
	transaction.State = stateCommitted
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write committed journal: %v", err)
	}
	if err := RollbackStartup(directory); err == nil || !strings.Contains(err.Error(), "committed") {
		t.Fatalf("expected committed rollback refusal, got %v", err)
	}
	if got, _ := os.ReadFile(executable); string(got) != string(newBytes) {
		t.Fatal("committed executable was rolled back")
	}
}

func TestBootstrappingCrashRestoresBackupAndExecutesOldBinary(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	dataStore, err := store.Open(database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := dataStore.FindOrCreateGoogleUser(t.Context(), "before", "before@example.com", "Before", "", false); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	version, err := dataStore.UserVersion(t.Context())
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatalf("create update directory: %v", err)
	}
	if err := dataStore.BackupSQLite(t.Context(), backup); err != nil {
		t.Fatalf("backup store: %v", err)
	}
	partialUser, err := dataStore.FindOrCreateGoogleUser(t.Context(), "partial", "partial@example.com", "Partial", "", false)
	if err != nil {
		t.Fatalf("simulate partial migration write: %v", err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-fixture")
	oldBytes := []byte("old")
	newBytes := []byte("new")
	writeFixture(t, executable, newBytes, 0o755)
	writeFixture(t, staged, oldBytes, 0o755)
	transaction := fixtureTransaction(executable, staged, database, backup, oldBytes, newBytes)
	transaction.DatabaseVersion = version
	transaction.State = stateBootstrapping
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write bootstrapping journal: %v", err)
	}
	execCalls := 0
	bootstrapping, err := recoverStartup(executable, database, directory, fakeExchange, func(path string, _ []string, _ []string) error {
		execCalls++
		if path != executable {
			t.Fatalf("exec path %q, expected %q", path, executable)
		}
		return errInjectedExec
	})
	if bootstrapping || !errors.Is(err, errInjectedExec) {
		t.Fatalf("expected rollback handoff, bootstrapping=%v err=%v", bootstrapping, err)
	}
	if execCalls != 1 {
		t.Fatalf("expected one old-binary exec, got %d", execCalls)
	}
	if got, _ := os.ReadFile(executable); string(got) != string(oldBytes) {
		t.Fatalf("old executable not restored: %q", got)
	}
	restored, err := store.Open(database)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()
	if _, err := restored.GetUser(t.Context(), partialUser.ID); err == nil {
		t.Fatal("partial migration write survived recovery")
	}
}

func TestBootstrappingRecoveryAfterExchangeBackStillRestoresDatabase(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	dataStore, err := store.Open(database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	version, err := dataStore.UserVersion(t.Context())
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		t.Fatalf("create update directory: %v", err)
	}
	if err := dataStore.BackupSQLite(t.Context(), backup); err != nil {
		t.Fatalf("backup store: %v", err)
	}
	partialUser, err := dataStore.FindOrCreateGoogleUser(t.Context(), "partial-back", "partial-back@example.com", "Partial", "", false)
	if err != nil {
		t.Fatalf("simulate partial migration write: %v", err)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-fixture")
	oldBytes := []byte("old")
	newBytes := []byte("new")
	// The exchange-back reached disk, but rollback_started did not.
	writeFixture(t, executable, oldBytes, 0o755)
	writeFixture(t, staged, newBytes, 0o755)
	transaction := fixtureTransaction(executable, staged, database, backup, oldBytes, newBytes)
	transaction.DatabaseVersion = version
	transaction.State = stateBootstrapping
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write bootstrapping journal: %v", err)
	}
	_, err = recoverStartup(executable, database, directory, fakeExchange, func(string, []string, []string) error { return errInjectedExec })
	if !errors.Is(err, errInjectedExec) {
		t.Fatalf("expected old-binary handoff, got %v", err)
	}
	restored, err := store.Open(database)
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	defer restored.Close()
	if _, err := restored.GetUser(t.Context(), partialUser.ID); err == nil {
		t.Fatal("partial write survived exchange-back recovery")
	}
}

func TestSwappedJournalWithOldLiveFinalizesWithoutDatabaseRestore(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-fixture")
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	oldBytes := []byte("old")
	newBytes := []byte("new")
	writeFixture(t, executable, oldBytes, 0o755)
	writeFixture(t, staged, newBytes, 0o755)
	writeFixture(t, database, []byte("database must remain untouched"), 0o600)
	writeFixture(t, backup, []byte("not a sqlite database"), 0o600)
	transaction := fixtureTransaction(executable, staged, database, backup, oldBytes, newBytes)
	transaction.State = stateSwapped
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write swapped journal: %v", err)
	}
	bootstrapping, err := recoverStartup(executable, database, directory, fakeExchange, func(string, []string, []string) error {
		t.Fatal("old-live swapped recovery must not exec or restore")
		return nil
	})
	if err != nil || bootstrapping {
		t.Fatalf("finalize old-live state: bootstrapping=%v err=%v", bootstrapping, err)
	}
	if got, _ := os.ReadFile(database); string(got) != "database must remain untouched" {
		t.Fatalf("database was unexpectedly restored: %q", got)
	}
}

func TestTerminalRecoveryToleratesArtifactsAlreadyMissing(t *testing.T) {
	for _, terminalState := range []string{stateCommitted, stateRolledBack} {
		t.Run(terminalState, func(t *testing.T) {
			directory := t.TempDir()
			executable := filepath.Join(directory, "worktime")
			staged := filepath.Join(directory, ".worktime-update-missing")
			database := filepath.Join(directory, "worktime.db")
			backup := filepath.Join(directory, "update", "rollback-missing.sqlite")
			transaction := fixtureTransaction(executable, staged, database, backup, []byte("old"), []byte("new"))
			transaction.State = terminalState
			if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
				t.Fatalf("write terminal journal: %v", err)
			}
			bootstrapping, err := RecoverStartup(executable, database, directory)
			if err != nil || bootstrapping {
				t.Fatalf("recover terminal state with missing artifacts: bootstrapping=%v err=%v", bootstrapping, err)
			}
			if _, err := os.Stat(transactionPath(directory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("terminal journal survived cleanup: %v", err)
			}
		})
	}
}

func TestTerminalCleanupUnlinksJournalBeforeArtifactFailure(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "worktime")
	staged := filepath.Join(directory, ".worktime-update-nonempty")
	database := filepath.Join(directory, "worktime.db")
	backup := filepath.Join(directory, "update", "rollback-fixture.sqlite")
	if err := os.MkdirAll(filepath.Join(staged, "child"), 0o700); err != nil {
		t.Fatalf("create non-removable staged fixture: %v", err)
	}
	writeFixture(t, backup, []byte("backup"), 0o600)
	transaction := fixtureTransaction(executable, staged, database, backup, []byte("old"), []byte("new"))
	transaction.State = stateRolledBack
	if err := writeJSONAtomic(transactionPath(directory), transaction); err != nil {
		t.Fatalf("write rolled-back journal: %v", err)
	}
	if _, err := RecoverStartup(executable, database, directory); err == nil {
		t.Fatal("expected artifact cleanup failure")
	}
	if _, err := os.Stat(transactionPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal was not unlinked before artifact cleanup: %v", err)
	}
	if bootstrapping, err := RecoverStartup(executable, database, directory); err != nil || bootstrapping {
		t.Fatalf("journal-free restart must proceed despite orphan: bootstrapping=%v err=%v", bootstrapping, err)
	}
}

func TestExchangeProbeFailureDoesNotEnterMaintenanceOrFallback(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "worktime.db")
	dataStore, err := store.Open(database)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer dataStore.Close()
	executable := filepath.Join(directory, "worktime")
	newBytes := []byte("new executable")
	writeFixture(t, executable, []byte("old executable"), 0o755)
	digest := sha256.Sum256(newBytes)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(newBytes)), Body: io.NopCloser(strings.NewReader(string(newBytes))), Header: make(http.Header), Request: request}, nil
	})}
	prepared := false
	installer := newExecutableInstaller(NativeRuntime{
		Executable: executable, DatabasePath: database, DataDirectory: directory,
		Store: dataStore, Lifecycle: lifecycle.New(), HTTPClient: client,
		Quiesce: func(context.Context) error { return nil }, Resume: func() {},
		Prepare: func(context.Context) error { prepared = true; return nil }, HandoffFailed: func(error) {},
	}, func(string, string) error { return errors.New("renameat2 unsupported") }, func(string, []string, []string) error { return nil })
	asset := Asset{OS: "linux", Arch: runtime.GOARCH, URL: "https://github.com/Siyet/worktime/releases/download/v1/test", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(newBytes))}
	if err := installer.Apply(t.Context(), validManifest(1, "v1.2.3"), asset); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected fail-closed exchange probe, got %v", err)
	}
	if prepared {
		t.Fatal("unsupported exchange entered process maintenance")
	}
	if _, err := os.Stat(transactionPath(directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported exchange wrote journal: %v", err)
	}
}

func fixtureTransaction(executable, staged, database, backup string, oldBytes, newBytes []byte) updateTransaction {
	oldDigest := sha256.Sum256(oldBytes)
	newDigest := sha256.Sum256(newBytes)
	return updateTransaction{
		Schema: transactionSchema, Executable: executable, Staged: staged,
		Database: database, Backup: backup, DatabaseVersion: 10,
		OldSHA256: hex.EncodeToString(oldDigest[:]), NewSHA256: hex.EncodeToString(newDigest[:]),
		Version: "v1.2.3", Revision: strings.Repeat("c", 40),
	}
}

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func fakeExchange(left, right string) error {
	temporary := left + ".exchange"
	if err := os.Rename(left, temporary); err != nil {
		return err
	}
	if err := os.Rename(right, left); err != nil {
		_ = os.Rename(temporary, left)
		return err
	}
	if err := os.Rename(temporary, right); err != nil {
		return err
	}
	return nil
}
