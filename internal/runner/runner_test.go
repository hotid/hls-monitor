package runner

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/probe"
)

type collector struct {
	mu   sync.Mutex
	seen map[int64]int
}

func (c *collector) sink(_ context.Context, r *probe.Result) {
	c.mu.Lock()
	c.seen[r.TargetID]++
	c.mu.Unlock()
}

func (c *collector) count(id int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[id]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSyncAndCheckNow(t *testing.T) {
	origin := hlstest.New(hlstest.Options{})
	origin.Publish(4)
	// A finished playlist: for a live one the checker would shorten the
	// interval to fit the playlist window, and cycles would come on their own.
	origin.End()
	srv := httptest.NewServer(origin.Handler())
	defer srv.Close()

	col := &collector{seen: map[int64]int{}}
	r := New(col.sink, "test", nil)
	defer r.Stop()

	// A long interval: after the first cycle nothing happens unless asked.
	target := func(id, rev int64) probe.Target {
		return probe.Target{ID: id, URL: srv.URL + "/v0/index.m3u8", Level: probe.LevelPlaylist, IntervalSec: 3600, Revision: rev}
	}
	r.Sync([]probe.Target{target(1, 1), target(2, 1)})
	waitFor(t, "first cycles", func() bool { return col.count(1) == 1 && col.count(2) == 1 })
	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2", r.Count())
	}

	if !r.CheckNow(1) {
		t.Fatal("CheckNow must find a running stream")
	}
	waitFor(t, "triggered cycle", func() bool { return col.count(1) == 2 })
	if col.count(2) != 1 {
		t.Errorf("stream 2 ran %d cycles, want 1: the trigger was for stream 1 only", col.count(2))
	}
	if r.CheckNow(99) {
		t.Error("CheckNow must report an unknown stream")
	}

	// Same revision: nothing restarts. New revision: the loop starts over.
	r.Sync([]probe.Target{target(1, 1), target(2, 2)})
	waitFor(t, "restart of the changed stream", func() bool { return col.count(2) == 2 })
	time.Sleep(50 * time.Millisecond)
	if col.count(1) != 2 {
		t.Errorf("unchanged stream restarted: %d cycles", col.count(1))
	}

	// Removed streams stop.
	r.Sync([]probe.Target{target(2, 2)})
	if r.Count() != 1 || r.CheckNow(1) {
		t.Errorf("stream 1 must be gone, count = %d", r.Count())
	}
}

func TestStopDoesNotReportCancelledCycles(t *testing.T) {
	origin := hlstest.New(hlstest.Options{})
	origin.Publish(4)
	origin.SetFault(3, hlstest.Fault{Delay: 2 * time.Second})
	srv := httptest.NewServer(origin.Handler())
	defer srv.Close()

	col := &collector{seen: map[int64]int{}}
	r := New(col.sink, "test", nil)
	r.Sync([]probe.Target{{ID: 1, URL: srv.URL + "/v0/index.m3u8", Level: probe.LevelSegments, Revision: 1}})
	waitFor(t, "segment download to begin", func() bool { return origin.Requests("/v0/seg-3.ts") == 1 })

	done := make(chan struct{})
	go func() { r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop must interrupt a running download")
	}
	if n := col.count(1); n != 0 {
		t.Errorf("an interrupted cycle was reported %d times", n)
	}
}
