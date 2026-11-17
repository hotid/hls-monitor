package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rec(i int) []byte { return []byte(fmt.Sprintf(`{"n":%04d,"pad":"0123456789"}`, i)) }

func drain(t *testing.T, s *spool) (got []string) {
	t.Helper()
	for {
		seq, batch := s.next()
		if len(batch) == 0 {
			return got
		}
		for _, b := range batch {
			got = append(got, string(b))
		}
		if err := s.done(seq, batch, len(batch)); err != nil {
			t.Fatal(err)
		}
	}
}

// Results written before a restart are delivered after it, in order.
func TestSpoolSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpool(dir, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if err := s.add(rec(i)); err != nil {
			t.Fatal(err)
		}
	}
	// No close: the agent was killed. A record cut short ends its file.
	files, _ := filepath.Glob(filepath.Join(dir, "*"+spoolExt))
	if len(files) < 3 {
		t.Fatalf("records of 30 bytes with files of 100 must take several files, got %d", len(files))
	}
	f, _ := os.OpenFile(files[len(files)-1], os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.Write([]byte{0, 0, 0, 200, 'x'})
	_ = f.Close()

	s, err = openSpool(dir, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.queued(); n != 10 {
		t.Fatalf("after reopening %d results are queued, want 10", n)
	}
	if err := s.add(rec(10)); err != nil {
		t.Fatal(err)
	}
	got := drain(t, s)
	if len(got) != 11 {
		t.Fatalf("delivered %d results, want 11", len(got))
	}
	for i, g := range got {
		if g != string(rec(i)) {
			t.Fatalf("result %d is %s: order is broken", i, g)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("files left after everything was delivered: %v", left)
	}
}

// The server stored a part of a batch: only the rest is sent again.
func TestSpoolPartialDelivery(t *testing.T) {
	s, err := openSpool(t.TempDir(), 1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		_ = s.add(rec(i))
	}
	seq, batch := s.next()
	if len(batch) != 5 {
		t.Fatalf("batch of %d, want 5", len(batch))
	}
	if err := s.done(seq, batch, 2); err != nil {
		t.Fatal(err)
	}
	_ = s.add(rec(5)) // arrives while the rest waits
	got := drain(t, s)
	if len(got) != 4 || got[0] != string(rec(2)) || got[3] != string(rec(5)) {
		t.Fatalf("after 2 of 5 were stored the queue gave %v", got)
	}
}

// Over the size limit the oldest results are dropped, the newest kept.
func TestSpoolSizeLimit(t *testing.T) {
	s, err := openSpool(t.TempDir(), 300, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 40 {
		_ = s.add(rec(i))
	}
	if _, size := s.queued(); size > 300 {
		t.Errorf("the queue takes %d bytes, limit 300", size)
	}
	dropped := s.takeDropped()
	got := drain(t, s)
	if dropped == 0 || dropped+len(got) != 40 || got[len(got)-1] != string(rec(39)) {
		t.Errorf("dropped %d, kept %d of 40, last kept %s", dropped, len(got), got[len(got)-1])
	}
}

func TestSpoolStatistics(t *testing.T) {
	s, err := openSpool(t.TempDir(), 250, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if n, size, oldest, dropped := s.stats(); n != 0 || size != 0 || oldest != 0 || dropped != 0 {
		t.Errorf("empty spool: %d %d %s %d", n, size, oldest, dropped)
	}
	rec := make([]byte, 60)
	for range 6 {
		if err := s.add(rec); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	n, size, oldest, dropped := s.stats()
	if n+int(dropped) != 6 || dropped == 0 || size != int64(n*60) || oldest < 20*time.Millisecond || oldest > 5*time.Second {
		t.Errorf("stats: %d results, %d bytes, oldest %s, dropped %d", n, size, oldest, dropped)
	}
	// What was dropped stays counted after the log line about it.
	s.takeDropped()
	if _, _, _, again := s.stats(); again != dropped {
		t.Errorf("dropped total = %d, want %d", again, dropped)
	}
}

// A queue file may be cut anywhere by a crash or damaged by the disk.
// Reading it must give records that are really in it, and never fail.
func FuzzSpoolFile(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}', 0, 0, 0, 1, 'x'})
	f.Add([]byte{0, 0, 0, 9, 'c', 'u', 't'})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 1, 2, 3})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "0000000000000001.q")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		records, err := readSpoolFile(path)
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for _, r := range records {
			if len(r) == 0 {
				t.Fatal("an empty record")
			}
			total += 4 + len(r)
		}
		if total > len(data) {
			t.Fatalf("records of %d bytes read from a file of %d", total, len(data))
		}
	})
}
