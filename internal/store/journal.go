package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/owner"
)

// The segment journal lives in a file of its own, next to the main
// database. It is by far the busiest table — a row per checked segment —
// and nothing in it is worth more than a look at recent history, so it is
// bounded by design:
//
//   - per variant and agent only the newest rows are kept, counted
//     separately for ordinary segments and for segments with problems;
//   - rows are trimmed as new ones arrive, a handful at a time, so there is
//     never a backlog to delete and the file settles at a steady size;
//   - the file cannot grow past a fixed number of pages, whatever happens.
//
// Losing the journal loses nothing but that recent history: it holds no
// configuration and no state.

// Options tune the storage. Zero values mean the defaults.
type Options struct {
	// SegmentKeep is how many ordinary journal rows are kept per variant
	// and agent.
	SegmentKeep int
	// SegmentKeepProblems is the same for segments that had problems.
	SegmentKeepProblems int
	// JournalMaxBytes is the size the journal file cannot exceed.
	JournalMaxBytes int64
}

const (
	defaultSegmentKeep         = 100
	defaultSegmentKeepProblems = 500
	defaultJournalMaxBytes     = 512 << 20
	// walLimit is what a write-ahead log is cut back to after a checkpoint.
	walLimit = 32 << 20
	// purgeBatch is how many rows one delete statement removes: small
	// enough to hold the write lock for a moment only.
	purgeBatch = 2000
	pageSize   = 4096
)

func (o Options) withDefaults() Options {
	if o.SegmentKeep <= 0 {
		o.SegmentKeep = defaultSegmentKeep
	}
	if o.SegmentKeepProblems <= 0 {
		o.SegmentKeepProblems = defaultSegmentKeepProblems
	}
	if o.JournalMaxBytes <= 0 {
		o.JournalMaxBytes = defaultJournalMaxBytes
	}
	return o
}

const journalSchema = `
CREATE TABLE IF NOT EXISTS segments (
    id          INTEGER PRIMARY KEY,
    stream_id   INTEGER NOT NULL,
    agent_id    INTEGER NOT NULL,
    variant_id  INTEGER NOT NULL,
    seq         INTEGER NOT NULL,
    time        INTEGER NOT NULL,
    url         TEXT    NOT NULL,
    duration    REAL    NOT NULL,
    status      INTEGER NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    remote_addr TEXT    NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    total_ms    REAL    NOT NULL DEFAULT 0,
    problems    TEXT    NOT NULL DEFAULT '',
    flagged     INTEGER NOT NULL DEFAULT 0,
    detail      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS segments_variant ON segments (variant_id, agent_id, flagged, id);
CREATE INDEX IF NOT EXISTS segments_stream ON segments (stream_id, time);
CREATE INDEX IF NOT EXISTS segments_agent ON segments (agent_id);
CREATE INDEX IF NOT EXISTS segments_time ON segments (time);
`

// journalPath is where the journal of the database at path is kept.
func journalPath(path string) string {
	return strings.TrimSuffix(path, ".db") + "-journal.db"
}

func openJournal(ctx context.Context, path string, maxBytes int64) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(10000)")
	q.Add("_pragma", fmt.Sprintf("journal_size_limit(%d)", walLimit))
	// The limit belongs to a connection, so every connection gets it.
	q.Add("_pragma", fmt.Sprintf("max_page_count(%d)", max(maxBytes/pageSize, 256)))
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.ExecContext(ctx, journalSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("open journal %s: %w", path, err)
	}
	return db, nil
}

// JournalEntry is a checked segment to record.
type JournalEntry struct {
	VariantID id.Variant
	Segment   *probe.Segment
}

// AppendSegments records checked segments of one stream as seen by one
// agent and drops the rows they push out. When the journal is full it makes
// room by dropping its oldest rows and tries once more.
func (s *Store) AppendSegments(ctx context.Context, streamID id.Stream, agentID id.Agent, entries []JournalEntry) error {
	if len(entries) == 0 {
		return nil
	}
	err := s.appendSegments(ctx, streamID, agentID, entries)
	if err != nil && isFull(err) {
		if ferr := s.makeRoom(ctx); ferr != nil {
			return fmt.Errorf("journal is full and cannot be trimmed: %w", ferr)
		}
		err = s.appendSegments(ctx, streamID, agentID, entries)
	}
	return err
}

func (s *Store) appendSegments(ctx context.Context, streamID id.Stream, agentID id.Agent, entries []JournalEntry) error {
	tx, err := s.journal.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	type class struct {
		variantID id.Variant
		flagged   int
	}
	touched := map[class]bool{}
	for _, e := range entries {
		seg := e.Segment
		detail, err := json.Marshal(seg)
		if err != nil {
			return err
		}
		flagged := 0
		if len(seg.Problems) > 0 {
			flagged = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO segments (stream_id, agent_id, variant_id, seq, time, url, duration, status, error, remote_addr, size, total_ms, problems, flagged, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			streamID, agentID, e.VariantID, int64(seg.Seq), toMs(seg.Time), seg.URL, seg.Duration, seg.Status, seg.Error,
			addrPortText(seg.RemoteAddr), seg.Size, seg.TotalMs, kindsText(seg.Problems), flagged, string(detail)); err != nil {
			return err
		}
		touched[class{e.VariantID, flagged}] = true
	}
	// Everything beyond the newest rows of each kind goes: about as many
	// rows as were just added.
	for c := range touched {
		keep := s.opts.SegmentKeep
		if c.flagged == 1 {
			keep = s.opts.SegmentKeepProblems
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM segments WHERE variant_id = ? AND agent_id = ? AND flagged = ? AND id <= (
				SELECT id FROM segments WHERE variant_id = ? AND agent_id = ? AND flagged = ?
				ORDER BY id DESC LIMIT 1 OFFSET ?)`,
			c.variantID, agentID, c.flagged, c.variantID, agentID, c.flagged, keep); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// isFull: there is no room left for the database to grow into.
func isFull(err error) bool { return refusedWith(err, codeFull) }

// makeRoom drops the oldest fifth of the journal. Deleting needs no new
// pages, so it works on a full file.
func (s *Store) makeRoom(ctx context.Context) error {
	var lo, hi sql.NullInt64
	if err := s.journal.QueryRowContext(ctx, "SELECT min(id), max(id) FROM segments").Scan(&lo, &hi); err != nil {
		return err
	}
	if !lo.Valid {
		return nil
	}
	_, err := deleteInBatches(ctx, s.journal, "segments", "id <= ?", lo.Int64+(hi.Int64-lo.Int64)/5)
	return err
}

// deleteInBatches deletes the rows of a table matching cond a batch at a
// time, each batch in its own short transaction, so that other writers get
// their turn in between. It returns how many rows went.
func deleteInBatches(ctx context.Context, db *sql.DB, table, cond string, args ...any) (int64, error) {
	var total int64
	q := fmt.Sprintf("DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s LIMIT %d)", table, table, cond, purgeBatch)
	for {
		res, err := db.ExecContext(ctx, q, args...)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += n
		if n < purgeBatch {
			return total, nil
		}
	}
}

// dropJournal forgets the journal rows of a stream or an agent that is gone.
// dropJournal deletes the journal of a stream, or what an agent wrote to
// it, by the number of the one or the other.
func (s *Store) dropJournal(ctx context.Context, of owner.Kind, n int64) error {
	_, err := deleteInBatches(ctx, s.journal, "segments", of.String()+"_id = ?", n)
	return err
}

// SegmentFilter selects journal rows.
type SegmentFilter struct {
	StreamID     id.Stream
	AgentID      id.Agent
	VariantID    id.Variant
	OnlyProblems bool
	Before       time.Time
	Limit        int
}

// ListSegments returns journal rows, newest first.
func (s *Store) ListSegments(ctx context.Context, f SegmentFilter) ([]*SegmentRecord, error) {
	where := []string{"stream_id = ?"}
	args := []any{f.StreamID}
	if !f.AgentID.IsZero() {
		where = append(where, "agent_id = ?")
		args = append(args, f.AgentID)
	}
	if !f.VariantID.IsZero() {
		where = append(where, "variant_id = ?")
		args = append(args, f.VariantID)
	}
	if f.OnlyProblems {
		where = append(where, "flagged = 1")
	}
	if !f.Before.IsZero() {
		where = append(where, "time < ?")
		args = append(args, toMs(f.Before))
	}
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	rows, err := s.journal.QueryContext(ctx, `
		SELECT id, stream_id, agent_id, variant_id, seq, time, url, duration, status, error, remote_addr, size, total_ms, problems, detail
		FROM segments WHERE `+strings.Join(where, " AND ")+` ORDER BY time DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SegmentRecord{}
	for rows.Next() {
		var r SegmentRecord
		var seq, t int64
		var problems, detail string
		if err := rows.Scan(&r.ID, &r.StreamID, &r.AgentID, &r.VariantID, &seq, &t, &r.URL, &r.Duration, &r.Status, &r.Error,
			(*storedAddrPort)(&r.RemoteAddr), &r.Size, &r.TotalMs, &problems, &detail); err != nil {
			return nil, err
		}
		r.Seq = uint64(seq)
		r.Time = fromMs(t)
		r.Problems = readKinds(problems)
		r.Detail = json.RawMessage(detail)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Usage is how much room the storage takes and how much is left.
type Usage struct {
	// DatabaseBytes and JournalBytes include the write-ahead logs.
	DatabaseBytes   int64 `json:"database_bytes"`
	JournalBytes    int64 `json:"journal_bytes"`
	JournalMaxBytes int64 `json:"journal_max_bytes"`
	JournalRows     int64 `json:"journal_rows"`
	// DiskFreeBytes and DiskTotalBytes describe the volume the database is
	// on; zero when that cannot be told.
	DiskFreeBytes  int64 `json:"disk_free_bytes"`
	DiskTotalBytes int64 `json:"disk_total_bytes"`
}

// Usage reports the size of the storage.
func (s *Store) Usage(ctx context.Context) Usage {
	size := func(path string) int64 {
		var n int64
		for _, suffix := range []string{"", "-wal"} {
			if fi, err := os.Stat(path + suffix); err == nil {
				n += fi.Size()
			}
		}
		return n
	}
	u := Usage{
		DatabaseBytes:   size(s.path),
		JournalBytes:    size(journalPath(s.path)),
		JournalMaxBytes: s.opts.JournalMaxBytes,
	}
	// The newest id minus the oldest is close enough and costs nothing.
	var lo, hi sql.NullInt64
	if err := s.journal.QueryRowContext(ctx, "SELECT min(id), max(id) FROM segments").Scan(&lo, &hi); err == nil && lo.Valid {
		_ = s.journal.QueryRowContext(ctx, "SELECT count(*) FROM segments").Scan(&u.JournalRows)
	}
	u.DiskFreeBytes, u.DiskTotalBytes = diskSpace(s.path)
	return u
}
