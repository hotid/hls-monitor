package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// migrate applies migrations up to and including version upTo; zero means all.
func migrate(ctx context.Context, db *sql.DB, upTo int64) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub, goose.WithGoMigrations(
		goose.NewGoMigration(3, &goose.GoFunc{RunDB: dropStreamLevelCheck}, nil),
	))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if upTo > 0 {
		_, err = p.UpTo(ctx, upTo)
	} else {
		_, err = p.Up(ctx)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// dropStreamLevelCheck (migration 3) removes the CHECK constraint that
// listed the allowed check levels in the streams table: levels are
// validated by the application, and a new one must not need a schema change.
//
// SQLite cannot drop a constraint, so the table is rebuilt. The rebuild
// drops the old table, which with foreign keys enforced would delete every
// row that refers to a stream; enforcement is therefore switched off for
// the duration. That setting belongs to a connection and cannot be changed
// inside a transaction, hence one dedicated connection and manual
// transaction control.
func dropStreamLevelCheck(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	exec := func(q string) error {
		_, e := conn.ExecContext(ctx, q)
		return e
	}
	if err := exec("PRAGMA foreign_keys = OFF"); err != nil {
		return err
	}
	defer func() {
		if e := exec("PRAGMA foreign_keys = ON"); err == nil {
			err = e
		}
	}()
	if err := exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = exec("ROLLBACK")
		}
	}()
	for _, q := range []string{
		`CREATE TABLE streams_new (
			id           INTEGER PRIMARY KEY,
			name         TEXT    NOT NULL UNIQUE,
			url          TEXT    NOT NULL,
			enabled      INTEGER NOT NULL DEFAULT 1,
			level        TEXT    NOT NULL DEFAULT 'segments',
			interval_sec REAL    NOT NULL DEFAULT 0,
			timeout_sec  REAL    NOT NULL DEFAULT 0,
			max_segments INTEGER NOT NULL DEFAULT 0,
			headers      TEXT    NOT NULL DEFAULT '{}',
			insecure     INTEGER NOT NULL DEFAULT 0,
			notes        TEXT    NOT NULL DEFAULT '',
			revision     INTEGER NOT NULL DEFAULT 1,
			created_at   INTEGER NOT NULL,
			updated_at   INTEGER NOT NULL
		)`,
		`INSERT INTO streams_new SELECT id, name, url, enabled, level, interval_sec, timeout_sec, max_segments,
			headers, insecure, notes, revision, created_at, updated_at FROM streams`,
		`DROP TABLE streams`,
		`ALTER TABLE streams_new RENAME TO streams`,
	} {
		if err := exec(q); err != nil {
			return err
		}
	}
	// Nothing may be left pointing at a stream that is gone.
	rows, err := conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	broken := rows.Next()
	rows.Close()
	if broken {
		return fmt.Errorf("rebuilding the streams table left dangling references")
	}
	return exec("COMMIT")
}
