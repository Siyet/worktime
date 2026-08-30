package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Siyet/worktime/internal/store"
)

const (
	transactionSchema      = 1
	stateVerified          = "verified"
	stateDraining          = "draining"
	stateBackupComplete    = "backup_complete"
	statePreflightComplete = "preflight_complete"
	stateSwapIntent        = "swap_intent"
	stateSwapped           = "swapped"
	stateBootstrapping     = "bootstrapping"
	stateCommitted         = "committed"
	stateRollbackStarted   = "rollback_started"
	stateRolledBack        = "rolled_back"
)

type exchangeFunction func(string, string) error
type execFunction func(string, []string, []string) error
type commandFunction func(context.Context, string, []string, []string, string) ([]byte, error)

type updateTransaction struct {
	Schema          int    `json:"schema"`
	State           string `json:"state"`
	Executable      string `json:"executable"`
	Staged          string `json:"staged"`
	Database        string `json:"database"`
	Backup          string `json:"backup"`
	DatabaseVersion int    `json:"database_version"`
	OldSHA256       string `json:"old_sha256"`
	NewSHA256       string `json:"new_sha256"`
	Version         string `json:"version"`
	Revision        string `json:"revision"`
}

type executableInstaller struct {
	runtime  NativeRuntime
	exchange exchangeFunction
	exec     execFunction
	run      commandFunction
}

func newExecutableInstaller(nativeRuntime NativeRuntime, exchange exchangeFunction, execute execFunction) *executableInstaller {
	if nativeRuntime.HTTPClient == nil {
		nativeRuntime.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return &executableInstaller{
		runtime:  nativeRuntime,
		exchange: exchange,
		exec:     execute,
		run:      runCommand,
	}
}

func (installer *executableInstaller) Supported() (bool, string) { return true, "" }

func (installer *executableInstaller) Apply(ctx context.Context, manifest Manifest, asset Asset) (result error) {
	if asset.OS != "linux" || asset.Arch != runtime.GOARCH {
		return fmt.Errorf("release asset targets %s/%s, current runtime is linux/%s", asset.OS, asset.Arch, runtime.GOARCH)
	}
	decodedDigest, err := hex.DecodeString(asset.SHA256)
	if err != nil || len(decodedDigest) != sha256.Size {
		return fmt.Errorf("release asset has an invalid SHA-256 digest")
	}
	journal := transactionPath(installer.runtime.DataDirectory)
	if _, err := os.Lstat(journal); err == nil {
		return fmt.Errorf("an unfinished update transaction already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect update transaction: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
		return fmt.Errorf("create update directory: %w", err)
	}

	staged, err := installer.download(ctx, asset)
	if err != nil {
		return err
	}
	backup := filepath.Join(filepath.Dir(journal), "rollback-"+strconvTimestamp()+".sqlite")
	removeStaged := true
	removeBackup := true
	journalRecorded := false
	preHandoff := true
	admissionClosed := false
	quiesced := false
	var transaction updateTransaction
	defer func() {
		if preHandoff {
			if journalRecorded {
				result = errors.Join(result, finalizeTransaction(&transaction, journal, stateRolledBack))
			} else {
				if removeStaged {
					_ = os.Remove(staged)
				}
				if removeBackup {
					_ = os.Remove(backup)
				}
			}
			if admissionClosed {
				installer.runtime.Lifecycle.OpenAdmission()
			}
			if quiesced {
				installer.runtime.Resume()
			}
		} else {
			if result == nil {
				result = fmt.Errorf("same-process update handoff returned without replacing the process")
			}
			installer.runtime.HandoffFailed(result)
		}
	}()

	if err := probeAtomicExchange(filepath.Dir(installer.runtime.Executable), installer.exchange); err != nil {
		return fmt.Errorf("filesystem does not support atomic executable exchange: %w", err)
	}
	if err := installer.verifyExecutable(ctx, staged, manifest); err != nil {
		return err
	}
	databaseVersion, err := installer.runtime.Store.UserVersion(ctx)
	if err != nil {
		return fmt.Errorf("read database version: %w", err)
	}
	oldDigest, _, err := hashFile(installer.runtime.Executable)
	if err != nil {
		return fmt.Errorf("hash current executable: %w", err)
	}
	transaction = updateTransaction{
		Schema: transactionSchema, State: stateVerified,
		Executable: installer.runtime.Executable, Staged: staged,
		Database: installer.runtime.DatabasePath, Backup: backup,
		DatabaseVersion: databaseVersion, OldSHA256: oldDigest,
		NewSHA256: strings.ToLower(asset.SHA256), Version: manifest.Version, Revision: manifest.Revision,
	}
	if err := writeJSONAtomic(journal, transaction); err != nil {
		return fmt.Errorf("record verified update: %w", err)
	}
	journalRecorded = true
	removeStaged = false
	removeBackup = false

	installer.runtime.Lifecycle.CloseAdmission()
	admissionClosed = true
	if err := recordTransactionState(journal, &transaction, stateDraining); err != nil {
		return err
	}
	drainContext, cancelDrain := context.WithTimeout(ctx, 30*time.Second)
	drainErr := installer.runtime.Lifecycle.WaitDrained(drainContext)
	cancelDrain()
	if drainErr != nil {
		return fmt.Errorf("drain active requests: %w", drainErr)
	}
	if err := installer.runtime.Quiesce(ctx); err != nil {
		return fmt.Errorf("quiesce background jobs: %w", err)
	}
	quiesced = true
	if err := installer.runtime.Store.BackupSQLite(ctx, backup); err != nil {
		return fmt.Errorf("create update rollback backup: %w", err)
	}
	if err := recordTransactionState(journal, &transaction, stateBackupComplete); err != nil {
		return err
	}
	if err := installer.selfCheck(ctx, staged, backup); err != nil {
		return err
	}
	if err := recordTransactionState(journal, &transaction, statePreflightComplete); err != nil {
		return err
	}
	if err := recordTransactionState(journal, &transaction, stateSwapIntent); err != nil {
		return err
	}
	// Prepare may have partially stopped the runtime even when it reports an error;
	// from its first instruction onward recovery is by exec/supervisor, never resume.
	preHandoff = false
	if err := installer.runtime.Prepare(ctx); err != nil {
		return installer.restartWithoutSwap(transaction, fmt.Errorf("prepare process handoff: %w", err))
	}
	// The old server and Store cannot be resumed beyond this point. Recovery is an
	// executable handoff, never a return to the now-partially-stopped process.
	if err := installer.exchange(transaction.Executable, transaction.Staged); err != nil {
		return installer.restartWithoutSwap(transaction, fmt.Errorf("exchange executable: %w", err))
	}
	if err := syncExchangedFiles(transaction.Executable, transaction.Staged); err != nil {
		return installer.rollbackAndRestart(transaction, fmt.Errorf("sync exchanged executables: %w", err))
	}
	transaction.State = stateSwapped
	if err := writeJSONAtomic(journal, transaction); err != nil {
		// Hash-based startup recovery recognizes the completed exchange even if the
		// state update itself was interrupted.
		if execErr := installer.exec(transaction.Executable, executableArgs(transaction.Executable), os.Environ()); execErr != nil {
			return errors.Join(fmt.Errorf("record completed executable swap: %w", err), execErr)
		}
		return nil
	}
	if err := installer.exec(transaction.Executable, executableArgs(transaction.Executable), os.Environ()); err != nil {
		return installer.rollbackAndRestart(transaction, fmt.Errorf("exec updated executable: %w", err))
	}
	return nil
}

func (installer *executableInstaller) download(ctx context.Context, asset Asset) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	response, err := installer.runtime.HTTPClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("download update asset: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download update asset: unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != asset.Size {
		return "", fmt.Errorf("download update asset: content length %d, expected %d", response.ContentLength, asset.Size)
	}
	file, err := os.CreateTemp(filepath.Dir(installer.runtime.Executable), ".worktime-update-*")
	if err != nil {
		return "", fmt.Errorf("stage update beside executable: %w", err)
	}
	path := file.Name()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, asset.Size+1))
	chmodErr := file.Chmod(0o755)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(copyErr, chmodErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("stage update asset: %w", err)
	}
	if written != asset.Size {
		_ = os.Remove(path)
		return "", fmt.Errorf("download update asset: received %d bytes, expected %d", written, asset.Size)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, asset.SHA256) {
		_ = os.Remove(path)
		return "", fmt.Errorf("download update asset: SHA-256 mismatch")
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("sync staged update: %w", err)
	}
	return path, nil
}

func (installer *executableInstaller) verifyExecutable(ctx context.Context, staged string, manifest Manifest) error {
	privateDirectory, err := os.MkdirTemp(filepath.Dir(transactionPath(installer.runtime.DataDirectory)), "verify-*")
	if err != nil {
		return fmt.Errorf("create private executable verification directory: %w", err)
	}
	defer os.RemoveAll(privateDirectory)
	output, err := installer.run(ctx, staged, []string{"--version"}, isolatedEnvironment(privateDirectory, ""), privateDirectory)
	if err != nil {
		return fmt.Errorf("run staged executable: %w: %s", err, bytes.TrimSpace(output))
	}
	expected := "worktime " + manifest.Version + " (revision " + manifest.Revision + ","
	if !strings.HasPrefix(string(output), expected) {
		return fmt.Errorf("staged executable identity does not match verified release")
	}
	return nil
}

func (installer *executableInstaller) selfCheck(ctx context.Context, staged, backup string) error {
	privateDirectory, err := os.MkdirTemp(filepath.Dir(backup), "preflight-*")
	if err != nil {
		return fmt.Errorf("create private update preflight directory: %w", err)
	}
	defer os.RemoveAll(privateDirectory)
	checkDatabase := filepath.Join(privateDirectory, "worktime.db")
	if err := copyFileDurable(backup, checkDatabase, 0o600); err != nil {
		return fmt.Errorf("prepare isolated update self-check: %w", err)
	}
	output, err := installer.run(ctx, staged, []string{"--update-self-check"}, isolatedEnvironment(privateDirectory, checkDatabase), privateDirectory)
	if err != nil {
		return fmt.Errorf("updated executable rejected isolated database: %w: %s", err, bytes.TrimSpace(output))
	}
	return nil
}

func (installer *executableInstaller) restartWithoutSwap(transaction updateTransaction, cause error) error {
	cleanupErr := finalizeTransaction(&transaction, transactionPath(installer.runtime.DataDirectory), stateRolledBack)
	execErr := installer.exec(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
	return errors.Join(cause, cleanupErr, execErr)
}

func (installer *executableInstaller) rollbackAndRestart(transaction updateTransaction, cause error) error {
	rollbackErr := rollbackTransaction(transaction, transactionPath(installer.runtime.DataDirectory), installer.exchange)
	execErr := installer.exec(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
	return errors.Join(cause, rollbackErr, execErr)
}

// RecoverStartup resolves a crash at every durable transaction boundary. It never
// guesses: both executable hashes and all journal-owned paths must match first.
func RecoverStartup(executable, database, dataDirectory string) (bool, error) {
	return recoverStartup(executable, database, dataDirectory, atomicExchange, processExec)
}

func recoverStartup(executable, database, dataDirectory string, exchange exchangeFunction, execute execFunction) (bool, error) {
	journal := transactionPath(dataDirectory)
	transaction, err := readTransaction(journal)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := validateTransaction(transaction, executable, database, dataDirectory); err != nil {
		return false, err
	}
	if transaction.State == stateCommitted || transaction.State == stateRolledBack {
		return false, cleanupTransaction(transaction, journal)
	}
	liveHash, _, liveErr := hashFile(transaction.Executable)
	stagedHash, _, stagedErr := hashFile(transaction.Staged)
	if liveErr != nil || stagedErr != nil {
		return false, fmt.Errorf("inspect interrupted executable swap: %w", errors.Join(liveErr, stagedErr))
	}
	swapped := liveHash == transaction.NewSHA256 && stagedHash == transaction.OldSHA256
	unswapped := liveHash == transaction.OldSHA256 && stagedHash == transaction.NewSHA256
	switch transaction.State {
	case stateVerified, stateDraining, stateBackupComplete, statePreflightComplete:
		if unswapped {
			return false, finalizeTransaction(&transaction, journal, stateRolledBack)
		}
	case stateSwapIntent:
		if unswapped {
			return false, finalizeTransaction(&transaction, journal, stateRolledBack)
		}
		if swapped {
			if err := syncExchangedFiles(transaction.Executable, transaction.Staged); err != nil {
				return false, err
			}
			if err := recordTransactionState(journal, &transaction, stateBootstrapping); err != nil {
				return false, fmt.Errorf("recover completed swap intent: %w", err)
			}
			return true, nil
		}
	case stateSwapped:
		if swapped {
			if err := syncExchangedFiles(transaction.Executable, transaction.Staged); err != nil {
				return false, err
			}
			if err := recordTransactionState(journal, &transaction, stateBootstrapping); err != nil {
				return false, err
			}
			return true, nil
		}
		if unswapped {
			return false, finalizeTransaction(&transaction, journal, stateRolledBack)
		}
	case stateBootstrapping:
		if swapped {
			if err := rollbackTransaction(transaction, journal, exchange); err != nil {
				return false, err
			}
			return false, execute(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
		}
		if unswapped {
			if err := rollbackTransaction(transaction, journal, exchange); err != nil {
				return false, err
			}
			return false, execute(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
		}
	case stateRollbackStarted:
		if swapped {
			if err := rollbackTransaction(transaction, journal, exchange); err != nil {
				return false, err
			}
			return false, execute(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
		}
		if unswapped {
			if err := rollbackTransaction(transaction, journal, exchange); err != nil {
				return false, err
			}
			return false, execute(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
		}
	default:
		return false, fmt.Errorf("update transaction has unknown state %q", transaction.State)
	}
	return false, fmt.Errorf("update transaction executable hashes do not match either durable state")
}

// CommitStartup crosses the no-auto-rollback boundary before deleting recovery
// artifacts. Recovery of a committed journal performs cleanup only.
func CommitStartup(dataDirectory string) error {
	journal := transactionPath(dataDirectory)
	transaction, err := readTransaction(journal)
	if err != nil {
		return err
	}
	if err := validateTransactionOwnership(transaction, dataDirectory); err != nil {
		return err
	}
	if transaction.State == stateCommitted {
		return cleanupTransaction(transaction, journal)
	}
	if transaction.State != stateBootstrapping {
		return fmt.Errorf("cannot commit update transaction in state %q", transaction.State)
	}
	liveHash, _, liveErr := hashFile(transaction.Executable)
	stagedHash, _, stagedErr := hashFile(transaction.Staged)
	if liveErr != nil || stagedErr != nil || liveHash != transaction.NewSHA256 || stagedHash != transaction.OldSHA256 {
		return fmt.Errorf("cannot commit update transaction with mismatched executable hashes: %w", errors.Join(liveErr, stagedErr))
	}
	if err := recordTransactionState(journal, &transaction, stateCommitted); err != nil {
		return fmt.Errorf("record committed update: %w", err)
	}
	return cleanupTransaction(transaction, journal)
}

// RollbackStartup is called only when the new executable cannot open/migrate the
// real database. A committed transaction is never rolled back automatically.
func RollbackStartup(dataDirectory string) error {
	journal := transactionPath(dataDirectory)
	transaction, err := readTransaction(journal)
	if err != nil {
		return err
	}
	if err := validateTransactionOwnership(transaction, dataDirectory); err != nil {
		return err
	}
	if transaction.State == stateCommitted {
		return fmt.Errorf("refusing to roll back a committed update")
	}
	if transaction.State == stateRolledBack {
		return cleanupTransaction(transaction, journal)
	}
	if err := rollbackTransaction(transaction, journal, atomicExchange); err != nil {
		return err
	}
	return processExec(transaction.Executable, executableArgs(transaction.Executable), os.Environ())
}

func rollbackTransaction(transaction updateTransaction, journal string, exchange exchangeFunction) error {
	if err := recordTransactionState(journal, &transaction, stateRollbackStarted); err != nil {
		return fmt.Errorf("record update rollback: %w", err)
	}
	liveHash, _, liveErr := hashFile(transaction.Executable)
	stagedHash, _, stagedErr := hashFile(transaction.Staged)
	if liveErr != nil || stagedErr != nil {
		return fmt.Errorf("inspect executables for rollback: %w", errors.Join(liveErr, stagedErr))
	}
	if liveHash == transaction.NewSHA256 && stagedHash == transaction.OldSHA256 {
		if err := exchange(transaction.Executable, transaction.Staged); err != nil {
			return fmt.Errorf("roll back executable: %w", err)
		}
		if err := syncExchangedFiles(transaction.Executable, transaction.Staged); err != nil {
			return fmt.Errorf("sync rolled-back executables: %w", err)
		}
	} else if liveHash != transaction.OldSHA256 || stagedHash != transaction.NewSHA256 {
		return fmt.Errorf("refusing rollback because executable hashes are unexpected")
	}
	if err := store.RestoreSQLite(context.Background(), transaction.Database, transaction.Backup, transaction.DatabaseVersion); err != nil {
		return err
	}
	if err := recordTransactionState(journal, &transaction, stateRolledBack); err != nil {
		return err
	}
	return cleanupTransaction(transaction, journal)
}

func finalizeTransaction(transaction *updateTransaction, journal, terminalState string) error {
	if transaction.State != terminalState {
		if err := recordTransactionState(journal, transaction, terminalState); err != nil {
			return err
		}
	}
	return cleanupTransaction(*transaction, journal)
}

func recordTransactionState(journal string, transaction *updateTransaction, state string) error {
	transaction.State = state
	if err := writeJSONAtomic(journal, *transaction); err != nil {
		return fmt.Errorf("record update state %s: %w", state, err)
	}
	return nil
}

func readTransaction(path string) (updateTransaction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return updateTransaction{}, err
	}
	var transaction updateTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&transaction); err != nil {
		return updateTransaction{}, fmt.Errorf("decode update transaction: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return updateTransaction{}, fmt.Errorf("decode update transaction: trailing data")
	}
	return transaction, nil
}

func validateTransaction(transaction updateTransaction, executable, database, dataDirectory string) error {
	if transaction.Schema != transactionSchema || transaction.Executable != executable || transaction.Database != database {
		return fmt.Errorf("update transaction does not belong to this installation")
	}
	return validateTransactionOwnership(transaction, dataDirectory)
}

func validateTransactionOwnership(transaction updateTransaction, dataDirectory string) error {
	if transaction.Schema != transactionSchema {
		return fmt.Errorf("update transaction has unsupported schema %d", transaction.Schema)
	}
	updateDirectory := filepath.Join(dataDirectory, "update")
	paths := []string{dataDirectory, transaction.Executable, transaction.Staged, transaction.Database, transaction.Backup}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("update transaction contains non-canonical paths")
		}
	}
	oldDigest, oldDigestErr := hex.DecodeString(transaction.OldSHA256)
	newDigest, newDigestErr := hex.DecodeString(transaction.NewSHA256)
	if filepath.Dir(transaction.Staged) != filepath.Dir(transaction.Executable) || filepath.Dir(transaction.Backup) != updateDirectory ||
		transaction.Staged == transaction.Executable || transaction.Backup == transaction.Database ||
		!strings.HasPrefix(filepath.Base(transaction.Staged), ".worktime-update-") ||
		!strings.HasPrefix(filepath.Base(transaction.Backup), "rollback-") || !strings.HasSuffix(transaction.Backup, ".sqlite") ||
		oldDigestErr != nil || newDigestErr != nil || len(oldDigest) != sha256.Size || len(newDigest) != sha256.Size || transaction.DatabaseVersion < 0 {
		return fmt.Errorf("update transaction contains unsafe paths or metadata")
	}
	return nil
}

func cleanupTransaction(transaction updateTransaction, journal string) error {
	// The journal is the recovery authority. Once a durable terminal state exists,
	// unlink and fsync it before touching artifacts. A crash after this point can at
	// worst leave inert files; it can never leave a live journal pointing at files
	// that cleanup already removed.
	if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(filepath.Dir(journal)); err != nil {
		return err
	}
	var cleanupErrors []error
	for _, path := range []string{transaction.Staged, transaction.Backup, transaction.Backup + ".self-check"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	cleanupErrors = append(cleanupErrors, syncDirectory(filepath.Dir(journal)), syncDirectory(filepath.Dir(transaction.Executable)))
	return errors.Join(cleanupErrors...)
}

func probeAtomicExchange(directory string, exchange exchangeFunction) error {
	left, err := os.CreateTemp(directory, ".worktime-exchange-probe-a-*")
	if err != nil {
		return err
	}
	leftPath := left.Name()
	defer os.Remove(leftPath)
	right, err := os.CreateTemp(directory, ".worktime-exchange-probe-b-*")
	if err != nil {
		left.Close()
		return err
	}
	rightPath := right.Name()
	defer os.Remove(rightPath)
	if _, err = left.WriteString("left"); err == nil {
		err = left.Sync()
	}
	leftClose := left.Close()
	if _, rightErr := right.WriteString("right"); err == nil {
		err = rightErr
	}
	if rightSync := right.Sync(); err == nil {
		err = rightSync
	}
	rightClose := right.Close()
	if err = errors.Join(err, leftClose, rightClose); err != nil {
		return err
	}
	if err := exchange(leftPath, rightPath); err != nil {
		return err
	}
	leftData, leftErr := os.ReadFile(leftPath)
	rightData, rightErr := os.ReadFile(rightPath)
	if err := errors.Join(leftErr, rightErr); err != nil {
		return err
	}
	if string(leftData) != "right" || string(rightData) != "left" {
		return fmt.Errorf("atomic exchange probe returned inconsistent contents")
	}
	if err := exchange(leftPath, rightPath); err != nil {
		return fmt.Errorf("restore atomic exchange probe: %w", err)
	}
	return syncDirectory(directory)
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = fmt.Errorf("not a regular file")
		}
		return "", 0, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func copyFileDurable(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		_ = os.Remove(destination)
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	return errors.Join(handle.Sync(), handle.Close())
}

func syncExchangedFiles(left, right string) error {
	var syncErrors []error
	for _, path := range []string{left, right} {
		file, err := os.Open(path)
		if err != nil {
			syncErrors = append(syncErrors, err)
			continue
		}
		syncErrors = append(syncErrors, file.Sync(), file.Close())
	}
	syncErrors = append(syncErrors, syncDirectory(filepath.Dir(left)))
	return errors.Join(syncErrors...)
}

func transactionPath(dataDirectory string) string {
	return filepath.Join(dataDirectory, "update", "transaction.json")
}

func executableArgs(executable string) []string {
	arguments := append([]string(nil), os.Args...)
	if len(arguments) == 0 {
		return []string{executable}
	}
	arguments[0] = executable
	return arguments
}

func isolatedEnvironment(privateDirectory, database string) []string {
	environment := []string{
		"HOME=" + privateDirectory,
		"TMPDIR=" + privateDirectory,
		"WORKTIME_UPDATE_CHECKS=0",
		"WORKTIME_ADDR=127.0.0.1:0",
		"WORKTIME_BASE_URL=http://127.0.0.1",
	}
	if database != "" {
		environment = append(environment, "WORKTIME_DB="+database)
	}
	return environment
}

func strconvTimestamp() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
