// Command benchdb benchmarks the WorkTime SQLite schema on synthetic data.
//
// It answers the Phase 0 questions: how fast are sync-style single-row upserts,
// how slow are report aggregations over month/year/all-time windows, and how
// large the database file grows at realistic and x10 volumes.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const schema = `
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

CREATE TABLE sync_state (
	id  INTEGER PRIMARY KEY CHECK (id = 1),
	seq INTEGER NOT NULL
);
INSERT INTO sync_state (id, seq) VALUES (1, 0);
`

const upsertSQL = `
INSERT INTO time_entries (id, user_id, project_id, description, started_at, stopped_at,
                          created_at, updated_at, deleted_at, server_seq)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	description = excluded.description,
	started_at = excluded.started_at,
	stopped_at = excluded.stopped_at,
	updated_at = excluded.updated_at,
	deleted_at = excluded.deleted_at,
	server_seq = excluded.server_seq
WHERE excluded.updated_at >= time_entries.updated_at`

type generator struct {
	rng      *rand.Rand
	users    []string
	projects map[string][]string
}

func newGenerator(userCount int) *generator {
	rng := rand.New(rand.NewSource(42))
	gen := &generator{rng: rng, projects: make(map[string][]string)}
	for range userCount {
		userID := uuid.NewString()
		gen.users = append(gen.users, userID)
		projectCount := 4 + rng.Intn(5)
		for range projectCount {
			gen.projects[userID] = append(gen.projects[userID], uuid.NewString())
		}
	}
	return gen
}

// entry produces one synthetic time entry roughly matching the solidtime export
// statistics: ~7 entries per workday, 30-90 minute durations, ~30 char descriptions.
func (gen *generator) entry(userID string, day time.Time, indexInDay int, seq int64) []any {
	entryID := mustUUIDv7()
	projects := gen.projects[userID]
	projectID := projects[gen.rng.Intn(len(projects))]
	startedAt := day.Add(9*time.Hour + time.Duration(indexInDay)*70*time.Minute).UnixMilli()
	duration := int64(30+gen.rng.Intn(60)) * time.Minute.Milliseconds()
	stoppedAt := startedAt + duration
	description := fmt.Sprintf("task %d review and implementation", gen.rng.Intn(10000))
	now := time.Now().UnixMilli()
	return []any{entryID, userID, projectID, description, startedAt, stoppedAt, now, now, nil, seq}
}

func mustUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		log.Fatal(err)
	}
	return id.String()
}

func main() {
	entryCount := flag.Int("entries", 100_000, "total number of time entries to seed")
	userCount := flag.Int("users", 10, "number of users")
	dbPath := flag.String("db", "bench.db", "database file path")
	flag.Parse()

	_ = os.Remove(*dbPath)
	_ = os.Remove(*dbPath + "-wal")
	_ = os.Remove(*dbPath + "-shm")

	dsn := "file:" + *dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		log.Fatal(err)
	}

	gen := newGenerator(*userCount)
	fmt.Printf("== benchdb: %d entries, %d users ==\n", *entryCount, *userCount)

	seedStart := time.Now()
	seed(db, gen, *entryCount)
	seedElapsed := time.Since(seedStart)
	fmt.Printf("bulk seed: %d rows in %v (%.0f rows/s)\n",
		*entryCount, seedElapsed.Round(time.Millisecond), float64(*entryCount)/seedElapsed.Seconds())

	benchSyncUpserts(db, gen, 500)

	reportUser := gen.users[len(gen.users)/2]
	nowMs := time.Now().UnixMilli()
	monthAgo := nowMs - 30*24*time.Hour.Milliseconds()
	yearAgo := nowMs - 365*24*time.Hour.Milliseconds()

	benchQuery(db, "report last month", 50,
		`SELECT project_id, SUM(stopped_at - started_at) FROM time_entries
		 WHERE user_id = ? AND deleted_at IS NULL AND started_at >= ? GROUP BY project_id`,
		reportUser, monthAgo)
	benchQuery(db, "report last year", 50,
		`SELECT project_id, SUM(stopped_at - started_at) FROM time_entries
		 WHERE user_id = ? AND deleted_at IS NULL AND started_at >= ? GROUP BY project_id`,
		reportUser, yearAgo)
	benchQuery(db, "report all time", 50,
		`SELECT project_id, SUM(stopped_at - started_at) FROM time_entries
		 WHERE user_id = ? AND deleted_at IS NULL GROUP BY project_id`,
		reportUser)
	benchQuery(db, "sync pull 500", 50,
		`SELECT id, project_id, description, started_at, stopped_at, updated_at, deleted_at, server_seq
		 FROM time_entries WHERE user_id = ? AND server_seq > ? ORDER BY server_seq LIMIT 500`,
		reportUser, 0)
	benchQuery(db, "running timers", 50,
		`SELECT id, started_at FROM time_entries
		 WHERE user_id = ? AND stopped_at IS NULL AND deleted_at IS NULL`,
		reportUser)

	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Fatal(err)
	}
	info, err := os.Stat(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("db file size: %.1f MB (%.0f bytes/entry)\n",
		float64(info.Size())/1024/1024, float64(info.Size())/float64(*entryCount))
}

// seed inserts entryCount rows in batched transactions, spreading entries across
// users and workdays going back from today.
func seed(db *sql.DB, gen *generator, entryCount int) {
	const batchSize = 5_000
	const entriesPerDay = 7

	transaction, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	statement, err := transaction.Prepare(upsertSQL)
	if err != nil {
		log.Fatal(err)
	}

	perUser := entryCount / len(gen.users)
	seq := int64(0)
	inBatch := 0
	for _, userID := range gen.users {
		day := time.Now().Truncate(24 * time.Hour)
		indexInDay := 0
		for range perUser {
			seq++
			if _, err := statement.Exec(gen.entry(userID, day, indexInDay, seq)...); err != nil {
				log.Fatal(err)
			}
			indexInDay++
			if indexInDay == entriesPerDay {
				indexInDay = 0
				day = day.AddDate(0, 0, -1)
				if day.Weekday() == time.Sunday {
					day = day.AddDate(0, 0, -2)
				}
			}
			inBatch++
			if inBatch == batchSize {
				commitAndRestart(&transaction, &statement, db)
				inBatch = 0
			}
		}
	}
	if _, err := transaction.Exec("UPDATE sync_state SET seq = ?", seq); err != nil {
		log.Fatal(err)
	}
	if err := transaction.Commit(); err != nil {
		log.Fatal(err)
	}
}

func commitAndRestart(transaction **sql.Tx, statement **sql.Stmt, db *sql.DB) {
	if err := (*transaction).Commit(); err != nil {
		log.Fatal(err)
	}
	next, err := db.Begin()
	if err != nil {
		log.Fatal(err)
	}
	*transaction = next
	prepared, err := next.Prepare(upsertSQL)
	if err != nil {
		log.Fatal(err)
	}
	*statement = prepared
}

// benchSyncUpserts measures the realistic sync write path: each row lands in its
// own transaction together with the sync_state counter increment.
func benchSyncUpserts(db *sql.DB, gen *generator, iterations int) {
	userID := gen.users[0]
	day := time.Now().Truncate(24 * time.Hour)

	start := time.Now()
	for iteration := range iterations {
		transaction, err := db.Begin()
		if err != nil {
			log.Fatal(err)
		}
		var seq int64
		if err := transaction.QueryRow("UPDATE sync_state SET seq = seq + 1 RETURNING seq").Scan(&seq); err != nil {
			log.Fatal(err)
		}
		if _, err := transaction.Exec(upsertSQL, gen.entry(userID, day, iteration%7, seq)...); err != nil {
			log.Fatal(err)
		}
		if err := transaction.Commit(); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("sync upserts (1 row/tx + seq counter): %d ops in %v (avg %.2f ms/op, %.0f ops/s)\n",
		iterations, elapsed.Round(time.Millisecond),
		float64(elapsed.Milliseconds())/float64(iterations), float64(iterations)/elapsed.Seconds())
}

func benchQuery(db *sql.DB, label string, iterations int, query string, args ...any) {
	// Warm-up run also verifies the query is valid.
	if err := runQuery(db, query, args...); err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	for range iterations {
		if err := runQuery(db, query, args...); err != nil {
			log.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	fmt.Printf("%-18s avg %.2f ms\n", label+":", float64(elapsed.Microseconds())/1000/float64(iterations))
}

func runQuery(db *sql.DB, query string, args ...any) error {
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range values {
		pointers[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return err
		}
	}
	return rows.Err()
}
