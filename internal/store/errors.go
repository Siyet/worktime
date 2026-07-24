package store

import (
	"database/sql"
	"errors"
)

// ErrInvalidInput marks validation failures so the API layer can map them to 400.
var ErrInvalidInput = errors.New("invalid input")

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
