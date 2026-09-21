// Package store provides SQLite persistence with atomic state+event appends
// (§32). Relational state is authoritative; events are appended in the same
// transaction as the state mutation they describe.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DB wraps a SQLite connection pool with the schema and event helpers.
type DB struct {
	sql *sql.DB
}

// Open opens (or creates) the SQLite database at path, applies migrations, and
// configures WAL + foreign keys + busy timeout. The daemon is the single writer
// (§4 D1), so the pool is deliberately small to avoid SQLITE_BUSY.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sqlDB.SetMaxOpenConns(1) // single writer; serializes all access

	db := &DB{sql: sqlDB}
	if err := db.configure(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := db.migrate(migrations); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// OpenGlobal opens the global registry database (projects, daemon_meta).
func OpenGlobal(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open global: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	if err := db.configure(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := db.migrate(globalMigrations); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) configure() error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		// review MEDIUM (concurrent first-open): switching journal mode
		// needs an exclusive lock SQLite briefly cannot get while another
		// connection is concurrently opening the same fresh file, and that
		// specific failure mode is not one the DSN's busy_timeout pragma
		// reliably covers (it applies to the connection's own later
		// statements, not necessarily to this file-level mode change
		// racing another connection's own first PRAGMA calls). A handful of
		// short retries is the standard way around this exact, well-known
		// SQLite quirk; the migration race itself (applyMigration, below in
		// migrate.go) is fixed at the transaction level, not by retrying.
		var err error
		for attempt := 0; attempt < 10; attempt++ {
			if _, err = db.sql.Exec(pragma); err == nil || !isBusy(err) {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
		}
		if err != nil {
			return fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	return nil
}

// isBusy reports whether err is SQLite reporting the database (or a table
// within it) locked by another connection -- transient, and worth a short
// retry -- as opposed to a real failure.
func isBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// Close closes the underlying database.
func (db *DB) Close() error { return db.sql.Close() }

// SQL exposes the underlying *sql.DB for repository methods.
func (db *DB) SQL() *sql.DB { return db.sql }

// InTx runs fn inside a transaction.
func (db *DB) InTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// nowMS returns the current time as unix milliseconds.
func nowMS() int64 { return time.Now().UnixMilli() }
