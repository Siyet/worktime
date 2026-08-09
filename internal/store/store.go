// Package store implements SQLite persistence for WorkTime.
//
// All timestamps are unix milliseconds UTC. Row IDs are UUIDv7 strings generated
// by clients (offline-first). Every synced table carries updated_at (client clock,
// used for last-write-wins) and server_seq (server-side monotonic counter, used
// as the sync pull cursor).
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

var migrations = []string{
	`
CREATE TABLE users (
	id          TEXT PRIMARY KEY,
	google_sub  TEXT UNIQUE,
	email       TEXT NOT NULL UNIQUE,
	name        TEXT NOT NULL DEFAULT '',
	picture_url TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL
);

CREATE TABLE sessions (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE api_tokens (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL,
	name         TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER
);

CREATE TABLE projects (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	color      TEXT NOT NULL DEFAULT '#888888',
	archived   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	server_seq INTEGER NOT NULL
);
CREATE INDEX idx_projects_user_seq ON projects(user_id, server_seq);

CREATE TABLE time_entries (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL,
	project_id  TEXT,
	description TEXT NOT NULL DEFAULT '',
	started_at  INTEGER NOT NULL,
	stopped_at  INTEGER,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL,
	deleted_at  INTEGER,
	server_seq  INTEGER NOT NULL
);
CREATE INDEX idx_entries_user_seq ON time_entries(user_id, server_seq);
CREATE INDEX idx_entries_user_started ON time_entries(user_id, started_at);
CREATE INDEX idx_entries_running ON time_entries(user_id) WHERE stopped_at IS NULL AND deleted_at IS NULL;

CREATE TABLE time_off (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	kind       TEXT NOT NULL CHECK (kind IN ('sick', 'vacation')),
	date_from  TEXT NOT NULL,
	date_to    TEXT NOT NULL,
	note       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	server_seq INTEGER NOT NULL
);
CREATE INDEX idx_time_off_user_seq ON time_off(user_id, server_seq);
CREATE INDEX idx_time_off_user_dates ON time_off(user_id, date_from);

CREATE TABLE sync_state (
	id  INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL
);
INSERT INTO sync_state (id, seq) VALUES (1, 0);
`,
	// 002: allow the 'dayoff' time-off kind. SQLite cannot alter a CHECK
	// constraint, so the table is recreated and data copied over.
	`
CREATE TABLE time_off_new (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	kind       TEXT NOT NULL CHECK (kind IN ('sick', 'vacation', 'dayoff')),
	date_from  TEXT NOT NULL,
	date_to    TEXT NOT NULL,
	note       TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	deleted_at INTEGER,
	server_seq INTEGER NOT NULL
);
INSERT INTO time_off_new SELECT id, user_id, kind, date_from, date_to, note, created_at, updated_at, deleted_at, server_seq FROM time_off;
DROP TABLE time_off;
ALTER TABLE time_off_new RENAME TO time_off;
CREATE INDEX idx_time_off_user_seq ON time_off(user_id, server_seq);
CREATE INDEX idx_time_off_user_dates ON time_off(user_id, date_from);
`,
	// 003: tags on time entries, stored as a JSON array in one column. NOT NULL with a
	// default so rows written before this migration, and the ON CONFLICT upsert path,
	// both see a valid empty list instead of NULL.
	`
ALTER TABLE time_entries ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
`,
	// 004: agent sessions for crash-resilient Claude Code time tracking. The id is the
	// agent's session_id and doubles as the idempotency key for start/heartbeat/stop.
	// time_entry_id points at the current (running) segment's time_entries row; a session
	// spans several entries when idle gaps split it. Not a synced table: rows live only
	// on the server, clients see the produced time_entries through the normal pull path.
	`
CREATE TABLE agent_sessions (
	id                TEXT PRIMARY KEY,
	user_id           TEXT NOT NULL,
	project_id        TEXT,
	source            TEXT NOT NULL DEFAULT 'claude-code',
	status            TEXT NOT NULL DEFAULT 'active',
	started_at        INTEGER NOT NULL,
	last_heartbeat_at INTEGER NOT NULL,
	ended_at          INTEGER,
	end_reason        TEXT,
	cwd               TEXT NOT NULL DEFAULT '',
	git_branch        TEXT NOT NULL DEFAULT '',
	model             TEXT NOT NULL DEFAULT '',
	time_entry_id     TEXT,
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL
);
CREATE INDEX idx_agent_sessions_open ON agent_sessions(user_id, status, last_heartbeat_at);
`,
	// 005: an agent entry is named after the tracker task the session belongs to
	// (set through the set_agent_task MCP tool), and until that is known after the
	// session itself, so two concurrent sessions never share a name. Ownership of the
	// produced row is tracked by server_seq: a mismatch means someone edited the row
	// outside the agent flow. agent_session_id is the back reference an entry needs
	// to be renamed later and to be labelled in the UI; it is the only column of this
	// migration that reaches clients.
	`
ALTER TABLE agent_sessions ADD COLUMN entry_server_seq INTEGER;
ALTER TABLE agent_sessions ADD COLUMN entry_user_named INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN task_key   TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN task_title TEXT NOT NULL DEFAULT '';

ALTER TABLE time_entries ADD COLUMN agent_session_id TEXT;
CREATE INDEX idx_time_entries_agent_session ON time_entries(agent_session_id);

-- Live sessions must stay owners of their entries: without the backfill the first
-- signal after the deploy would abandon a running row nobody could close anymore.
UPDATE agent_sessions
SET entry_server_seq = (SELECT server_seq FROM time_entries WHERE time_entries.id = agent_sessions.time_entry_id)
WHERE time_entry_id IS NOT NULL;
`,
	// 006: an idle gap inside an agent session no longer cuts the entry in two;
	// it is accumulated in paused_ms and subtracted from the duration, so one
	// session is one row. started_at and stopped_at stay real timestamps, which
	// is what the printed report, the CSV and the day boundaries are built on.
	// tz_offset_min is nullable on purpose: 0 is UTC, not "unknown", and only a
	// known offset can tell whether a pause crossed the user's local midnight.
	// last_kind remembers what the previous signal was, so the gap after a long
	// tool run can be billed instead of silently vanishing into the pause.
	`
ALTER TABLE time_entries   ADD COLUMN paused_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_sessions ADD COLUMN tz_offset_min INTEGER;
ALTER TABLE agent_sessions ADD COLUMN last_kind TEXT NOT NULL DEFAULT '';
`,
}

type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database and applies pending migrations.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection serializes all access: with ~10 users and sub-millisecond
	// queries (see docs/benchmark.md) this is simpler and safer than juggling
	// reader/writer pools, and it eliminates SQLITE_BUSY entirely.
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for index := version; index < len(migrations); index++ {
		transaction, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := transaction.Exec(migrations[index]); err != nil {
			transaction.Rollback()
			return fmt.Errorf("migration %d: %w", index+1, err)
		}
		if _, err := transaction.Exec(fmt.Sprintf("PRAGMA user_version = %d", index+1)); err != nil {
			transaction.Rollback()
			return err
		}
		if err := transaction.Commit(); err != nil {
			return err
		}
	}
	return nil
}
