package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

type backupSource interface {
	NewBackup(string) (*sqlite.Backup, error)
}

type restoreDestination interface {
	NewRestore(string) (*sqlite.Backup, error)
}

// BackupSQLite creates a transactionally consistent standalone backup through
// SQLite's backup API. It works while the source is in WAL mode.
func (s *Store) BackupSQLite(ctx context.Context, destination string) error {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.Raw(func(driverConnection any) error {
		source, ok := driverConnection.(backupSource)
		if !ok {
			return fmt.Errorf("sqlite driver does not expose backup API")
		}
		backup, err := source.NewBackup(destination)
		if err != nil {
			return err
		}
		_, stepErr := backup.Step(-1)
		finishErr := backup.Finish()
		return errors.Join(stepErr, finishErr)
	}); err != nil {
		return fmt.Errorf("backup sqlite: %w", err)
	}
	return fsyncFileAndDirectory(destination)
}

// RestoreSQLite restores without invoking Store.Open, so a failed new-version
// migration cannot be re-applied before the old database has been recovered.
func RestoreSQLite(ctx context.Context, destination, source string, expectedUserVersion int) error {
	dsn := "file:" + destination + "?_pragma=busy_timeout(5000)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	connection, err := database.Conn(ctx)
	if err != nil {
		database.Close()
		return err
	}
	_, _ = connection.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	err = connection.Raw(func(driverConnection any) error {
		destinationConnection, ok := driverConnection.(restoreDestination)
		if !ok {
			return fmt.Errorf("sqlite driver does not expose restore API")
		}
		restore, err := destinationConnection.NewRestore(source)
		if err != nil {
			return err
		}
		_, stepErr := restore.Step(-1)
		finishErr := restore.Finish()
		return errors.Join(stepErr, finishErr)
	})
	if err == nil {
		_, err = connection.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	}
	if err == nil {
		var integrity string
		var version int
		if scanErr := connection.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); scanErr != nil {
			err = scanErr
		} else if integrity != "ok" {
			err = fmt.Errorf("restored database integrity: %s", integrity)
		} else if scanErr := connection.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); scanErr != nil {
			err = scanErr
		} else if version != expectedUserVersion {
			err = fmt.Errorf("restored database user_version %d, expected %d", version, expectedUserVersion)
		}
	}
	closeErr := connection.Close()
	databaseErr := database.Close()
	if err = errors.Join(err, closeErr, databaseErr); err != nil {
		return fmt.Errorf("restore sqlite: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if removeErr := os.Remove(destination + suffix); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove sqlite sidecar: %w", removeErr)
		}
	}
	return fsyncFileAndDirectory(destination)
}

func (s *Store) UserVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}

func (s *Store) CheckIntegrity(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("database integrity: %s", result)
	}
	return nil
}

func fsyncFileAndDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	fileErr := file.Sync()
	closeErr := file.Close()
	directory, dirErr := os.Open(filepath.Dir(path))
	if dirErr != nil {
		return errors.Join(fileErr, closeErr, dirErr)
	}
	dirSyncErr := directory.Sync()
	dirCloseErr := directory.Close()
	return errors.Join(fileErr, closeErr, dirSyncErr, dirCloseErr)
}
