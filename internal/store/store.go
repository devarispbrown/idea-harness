// Package store provides SQLite persistence with atomic state+event appends
// (§32). Relational state is authoritative; events are appended in the same
// transaction as the state mutation they describe.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
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
		if _, err := db.sql.Exec(pragma); err != nil {
			return fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	return nil
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
