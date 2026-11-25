package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/overload"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
)

type collector struct {
	mu   sync.Mutex
	seen map[int64]int
}

func (c *collector) sink(_ context.Context, r *probe.Result) {
	c.mu.Lock()
	c.seen[r.TargetID.Int64()]++
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
	target := func(of, rev int64) probe.Target {
		return probe.Target{ID: id.StreamOf(of), URL: srv.URL + "/v0/index.m3u8", Level: level.Playlist(), Picture: picturemode.Check(), IntervalSec: 3600, Revision: rev}
	}
	r.Sync([]probe.Target{target(1, 1), target(2, 1)})
	waitFor(t, "first cycles", func() bool { return col.count(1) == 1 && col.count(2) == 1 })
	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2", r.Count())
	}

	if !r.CheckNow(id.StreamOf(1)) {
		t.Fatal("CheckNow must find a running stream")
	}
	waitFor(t, "triggered cycle", func() bool { return col.count(1) == 2 })
	if col.count(2) != 1 {
		t.Errorf("stream 2 ran %d cycles, want 1: the trigger was for stream 1 only", col.count(2))
	}
	if r.CheckNow(id.StreamOf(99)) {
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
	if r.Count() != 1 || r.CheckNow(id.StreamOf(1)) {
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
	r.Sync([]probe.Target{{ID: id.StreamOf(1), URL: srv.URL + "/v0/index.m3u8", Level: level.Segments(), Picture: picturemode.Check(), Revision: 1}})
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

// A stream that answers slowly makes its own checks late; that is its
// problem and is reported as such. The runner is behind only when the
// delay is its own: here, a result that takes long to be stored.
func TestBehindIsTheRunnersOwnDelay(t *testing.T) {
	origin := hlstest.New(hlstest.Options{})
	origin.Publish(4)
	origin.End()
	var delay atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(time.Duration(delay.Load()))
		origin.Handler().ServeHTTP(w, req)
	}))
	defer srv.Close()

	var store atomic.Int64
	var cycles atomic.Int64
	r := New(func(context.Context, *probe.Result) {
		time.Sleep(time.Duration(store.Load()))
		cycles.Add(1)
	}, "test", nil)
	defer r.Stop()
	r.Sync([]probe.Target{{ID: id.StreamOf(1), URL: srv.URL + "/v0/index.m3u8", Level: level.Playlist(), Picture: picturemode.Check(), IntervalSec: 0.2, Revision: 1}})

	waitFor(t, "the first cycle", func() bool { return cycles.Load() >= 1 })
	delay.Store(int64(1300 * time.Millisecond))
	from := cycles.Load()
	waitFor(t, "cycles against a slow stream", func() bool { return cycles.Load() >= from+3 })
	if st := r.Stats(); st.Behind != 0 || st.Streams != 1 || st.Cycles < 3 {
		t.Errorf("a slow stream must not count as the runner being behind: %+v", st)
	}

	delay.Store(0)
	store.Store(int64(1300 * time.Millisecond))
	from = cycles.Load()
	waitFor(t, "cycles with slow storing", func() bool { return cycles.Load() >= from+2 })
	if st := r.Stats(); st.Behind != 1 || st.LagMax < 1 {
		t.Errorf("results that wait to be stored make the runner late: %+v", st)
	}
}

// A fault in the checking code stops one cycle of one stream, and is
// counted; the runner goes on.
func TestCrashedCycleDoesNotStopTheRunner(t *testing.T) {
	origin := hlstest.New(hlstest.Options{})
	origin.Publish(4)
	origin.End()
	srv := httptest.NewServer(origin.Handler())
	defer srv.Close()

	col := &collector{seen: map[int64]int{}}
	r := New(col.sink, "test", nil)
	defer r.Stop()
	var crash atomic.Bool
	crash.Store(true)
	r.SetOverloaded(func(time.Time) overload.Reason {
		if crash.Load() {
			panic("a fault in the checking code")
		}
		return overload.Reason{}
	})
	r.Sync([]probe.Target{{ID: id.StreamOf(1), URL: srv.URL + "/v0/index.m3u8", Level: level.Playlist(), Picture: picturemode.Check(), IntervalSec: 3600, Revision: 1}})
	waitFor(t, "the crash to be counted", func() bool { return r.Stats().Crashed == 1 })
	if col.count(1) != 0 {
		t.Error("a crashed cycle must not be reported")
	}
	crash.Store(false)
	r.CheckNow(id.StreamOf(1))
	waitFor(t, "a cycle after the crash", func() bool { return col.count(1) == 1 })
}
