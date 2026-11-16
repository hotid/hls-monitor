package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
)

func entries(variant int64, from, n int, problem bool, at time.Time) []JournalEntry {
	out := make([]JournalEntry, 0, n)
	for i := range n {
		seg := &probe.Segment{Seq: uint64(from + i), URL: "http://x/seg.ts", Time: at, Duration: 5, Status: 200, Size: 1000}
		if problem {
			seg.Problems = []string{probe.ProbBitrateExceeds}
		}
		out = append(out, JournalEntry{VariantID: variant, Segment: seg})
	}
	return out
}

// The journal keeps the newest rows of each variant, counting ordinary
// segments and segments with problems apart, however many are written.
func TestJournalIsBoundedByCount(t *testing.T) {
	s, err := OpenWith(ctx, filepath.Join(t.TempDir(), "test.db"), Options{SegmentKeep: 10, SegmentKeepProblems: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for i := range 50 {
		if err := s.AppendSegments(ctx, 1, LocalAgentID, entries(7, i*3, 3, false, now)); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendSegments(ctx, 1, LocalAgentID, entries(7, 1000+i, 1, true, now)); err != nil {
			t.Fatal(err)
		}
	}
	// Another variant and another agent are counted on their own.
	if err := s.AppendSegments(ctx, 1, LocalAgentID, entries(8, 0, 5, false, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendSegments(ctx, 1, 2, entries(7, 0, 5, false, now)); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListSegments(ctx, SegmentFilter{StreamID: 1, VariantID: 7, AgentID: LocalAgentID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 14 {
		t.Fatalf("rows of the busy variant = %d, want 10 ordinary + 4 with problems", len(all))
	}
	flagged, _ := s.ListSegments(ctx, SegmentFilter{StreamID: 1, VariantID: 7, AgentID: LocalAgentID, OnlyProblems: true, Limit: 1000})
	if len(flagged) != 4 || flagged[0].Seq != 1049 || flagged[3].Seq != 1046 {
		t.Fatalf("segments with problems = %d (newest %d), want the last 4", len(flagged), flagged[0].Seq)
	}
	if rest, _ := s.ListSegments(ctx, SegmentFilter{StreamID: 1, Limit: 1000}); len(rest) != 24 {
		t.Errorf("rows in all = %d, want 24: other variants and agents keep their own", len(rest))
	}
	if u := s.Usage(ctx); u.JournalRows != 24 || u.JournalBytes == 0 || u.DatabaseBytes == 0 {
		t.Errorf("usage = %+v", u)
	}

	// A deleted stream takes its journal along.
	st, err := s.CreateStream(ctx, StreamInput{Name: "one", URL: "http://x/a.m3u8", Enabled: true, Level: "segments", AgentIDs: []int64{LocalAgentID}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStream(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if rest, _ := s.ListSegments(ctx, SegmentFilter{StreamID: st.ID, Limit: 1000}); len(rest) != 0 {
		t.Errorf("journal of a deleted stream still has %d rows", len(rest))
	}
}

// A journal that reaches its size limit drops its oldest rows and goes on.
func TestJournalCannotOutgrowItsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	const limit = 2 << 20
	s, err := OpenWith(ctx, path, Options{SegmentKeep: 1 << 30, JournalMaxBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for i := range 400 {
		batch := entries(int64(i%5+1), i*50, 50, false, now)
		for _, e := range batch {
			e.Segment.Error = strings.Repeat("x", 500)
		}
		if err := s.AppendSegments(ctx, 1, LocalAgentID, batch); err != nil {
			t.Fatalf("batch %d: %v", i, err)
		}
	}
	fi, err := os.Stat(journalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > limit {
		t.Errorf("journal file is %d bytes, limit is %d", fi.Size(), limit)
	}
	newest, _ := s.ListSegments(ctx, SegmentFilter{StreamID: 1, Limit: 1})
	if len(newest) != 1 || newest[0].Seq != 399*50+49 {
		t.Errorf("the newest row must survive, got %+v", newest)
	}
}

// Expired rows go in batches: more than one batch is deleted completely.
func TestPurgeDeletesInBatches(t *testing.T) {
	s, err := OpenWith(ctx, filepath.Join(t.TempDir(), "test.db"), Options{SegmentKeep: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	for i := range 5 {
		if err := s.AppendSegments(ctx, 1, LocalAgentID, entries(1, i*1000, 1000, false, old)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendSegments(ctx, 1, LocalAgentID, entries(1, 9000, 10, false, now)); err != nil {
		t.Fatal(err)
	}
	keep := Retention{Segments: time.Hour, Snapshots: time.Hour, Incidents: time.Hour, Events: time.Hour}
	deleted, err := s.Purge(ctx, keep, now)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5000 {
		t.Errorf("purged %d rows, want 5000", deleted)
	}
	if left, _ := s.ListSegments(ctx, SegmentFilter{StreamID: 1, Limit: 1000}); len(left) != 10 {
		t.Errorf("rows left = %d, want the 10 fresh ones", len(left))
	}
}

// A database that carries the old journal table gives its room back.
func TestDroppedJournalTableIsReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, 5); err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("d", 4000)
	tx, _ := db.Begin()
	if _, err := tx.Exec("INSERT INTO streams (id, name, url, created_at, updated_at) VALUES (1, 's', 'http://x', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("INSERT INTO variants (id, stream_id, key, kind, first_seen, last_seen) VALUES (1, 1, 'media', 'media', 0, 0)"); err != nil {
		t.Fatal(err)
	}
	for i := range 20000 {
		if _, err := tx.Exec("INSERT INTO segments (stream_id, agent_id, variant_id, seq, time, url, duration, status, detail) VALUES (1, 1, 1, ?, 0, 'u', 1, 200, ?)", i, blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, _ := os.Stat(path)

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, _ := os.Stat(path)
	if before.Size() < 64<<20 || after.Size() > before.Size()/10 {
		t.Errorf("database was %d bytes, is %d after the journal table was dropped: the room must be given back", before.Size(), after.Size())
	}
	if list, err := s.ListStreams(ctx); err != nil || len(list) != 1 {
		t.Errorf("streams after the migration = %v, %v", list, err)
	}
}
