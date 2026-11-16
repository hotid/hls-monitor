// Package store keeps configuration, state, incidents and the short-lived
// segment journal in SQLite. Numeric time series live elsewhere.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// LocalAgentID is the agent that represents the server itself.
const LocalAgentID int64 = 1

// Store is the SQLite-backed storage. It is safe for concurrent use.
type Store struct {
	db      *sql.DB
	journal *sql.DB
	path    string
	opts    Options
}

// Open opens the storage with default options.
func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWith(ctx, path, Options{})
}

// OpenWith opens (creating if needed) the database file and the segment
// journal next to it, and applies migrations.
func OpenWith(ctx context.Context, path string, opts Options) (*Store, error) {
	opts = opts.withDefaults()
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", walLimit))
	// Every transaction takes the write lock up front: a deferred transaction
	// that upgrades from read to write fails with SQLITE_BUSY_SNAPSHOT, which
	// busy_timeout does not retry.
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}

	if err := migrate(ctx, db, 0); err != nil {
		db.Close()
		return nil, err
	}
	if err := reclaim(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	journal, err := openJournal(ctx, journalPath(path), opts.JournalMaxBytes)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, journal: journal, path: path, opts: opts}, nil
}

// reclaim gives back to the file system the room left by a table that was
// dropped: without it the file stays as large as it once was. Rebuilding a
// database costs as much as its live data, so this is done only when most
// of the file is free pages and what remains is small.
func reclaim(ctx context.Context, db *sql.DB) error {
	var pages, free, size int64
	for name, dst := range map[string]*int64{"page_count": &pages, "freelist_count": &free, "page_size": &size} {
		if err := db.QueryRowContext(ctx, "PRAGMA "+name).Scan(dst); err != nil {
			return err
		}
	}
	const minFree, maxLive = 64 << 20, 512 << 20
	if free*size < minFree || free*2 < pages || (pages-free)*size > maxLive {
		return nil
	}
	if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("reclaim free space: %w", err)
	}
	_, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Close closes the database and the journal.
func (s *Store) Close() error {
	return errors.Join(s.journal.Close(), s.db.Close())
}

// querier is implemented by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Tx is a write transaction.
type Tx struct {
	q querier
}

// Tx runs fn in a transaction, committing when it returns nil.
func (s *Store) Tx(ctx context.Context, fn func(tx *Tx) error) error {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{q: t}); err != nil {
		_ = t.Rollback()
		return err
	}
	return t.Commit()
}

func (s *Store) tx() *Tx { return &Tx{q: s.db} }

func toMs(t time.Time) int64 { return t.UnixMilli() }

func fromMs(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromMsPtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMs(v.Int64)
	return &t
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// placeholders returns "?,?,?" for n values.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
