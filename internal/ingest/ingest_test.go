package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

var ctx = context.Background()

type env struct {
	t      *testing.T
	st     *store.Store
	mx     *metrics.Client
	in     *Ingester
	stream *store.Stream
	clock  time.Time

	mu   sync.Mutex
	sent []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, clock: time.Now().Truncate(time.Second)}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.sent = append(e.sent, string(b))
		e.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(vm.Close)

	e.st = st
	e.mx = metrics.New(vm.URL, nil)
	e.in = New(st, e.mx, nil)
	e.in.now = func() time.Time { return e.clock }
	e.stream, err = st.CreateStream(ctx, store.StreamInput{Name: "s", URL: "http://x/master.m3u8", Enabled: true, Level: "segments",
		AgentIDs: []int64{store.LocalAgentID}}, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var twoVariants = []probe.Variant{
	{Key: probe.VariantMaster, Kind: probe.KindMaster},
	{Key: "hi.m3u8", Kind: probe.KindVideo, Bandwidth: 2000000, Resolution: "1280x720"},
	{Key: "lo.m3u8", Kind: probe.KindVideo, Bandwidth: 500000, Resolution: "640x360"},
}

// result builds a cycle result in which every listed variant was reached.
func (e *env) result(variants []probe.Variant, problems ...probe.Problem) *probe.Result {
	e.clock = e.clock.Add(10 * time.Second)
	r := &probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: probe.StatusOK, Variants: variants, Problems: problems}
	for _, v := range variants {
		r.Metrics = append(r.Metrics, probe.Metric{Name: "hls_up", Variant: v.Key, Value: 1}, probe.Metric{Name: "hls_status", Variant: v.Key, Value: 0})
	}
	r.Metrics = append(r.Metrics, probe.Metric{Name: "hls_stream_status", Value: 0})
	for _, p := range problems {
		if p.Severity == probe.SevCrit {
			r.Status = probe.StatusFail
		} else if r.Status == probe.StatusOK {
			r.Status = probe.StatusWarn
		}
	}
	return r
}

func (e *env) apply(r *probe.Result) {
	e.t.Helper()
	if err := e.in.Apply(ctx, store.LocalAgentID, r); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) incidents(open bool) []*store.Incident {
	e.t.Helper()
	list, err := e.st.ListIncidents(ctx, store.IncidentFilter{StreamID: e.stream.ID, Open: &open})
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

func TestApplyStoresEverything(t *testing.T) {
	e := newEnv(t)
	r := e.result(twoVariants, probe.Problem{Kind: probe.ProbSeqJump, Severity: probe.SevWarn, Variant: "hi.m3u8", Message: "2 segments lost", Count: 1})
	r.Metrics = append(r.Metrics,
		probe.Metric{Name: "hls_segment_bitrate_bps", Variant: "hi.m3u8", Value: 1.9e6},
		probe.Metric{Name: "hls_segment_bitrate_bps", Variant: "lo.m3u8", Value: 0.4e6},
		probe.Metric{Name: "Bad-Name", Variant: "lo.m3u8", Value: 1},
	)
	r.Segments = []probe.Segment{{Variant: "hi.m3u8", Seq: 41, URL: "http://x/hi/41.ts", Time: e.clock, Duration: 6, Status: 200, Size: 1000, TotalMs: 120,
		Problems: []string{probe.ProbSegmentSlow}}}
	r.Snapshots = []probe.Snapshot{{Variant: "hi.m3u8", URL: "http://x/hi.m3u8", Body: "#EXTM3U\n"}}
	e.apply(r)

	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	if len(variants) != 3 || variants[0].Kind != probe.KindMaster || variants[1].Bandwidth != 2000000 {
		t.Fatalf("variants = %+v", variants)
	}
	hi := variants[1].ID

	states, _ := e.st.StreamStates(ctx, e.stream.ID)
	if len(states) != 1 || states[0].Status != probe.StatusWarn || states[0].Summary["bitrate_bps"] != 1.9e6 || states[0].Summary["variants"] != 2 || states[0].Summary["subtitles"] != 0 {
		t.Errorf("stream state = %+v", states)
	}
	vstates, _ := e.st.VariantStates(ctx, e.stream.ID)
	if len(vstates) != 3 {
		t.Errorf("variant states = %d, want 3", len(vstates))
	}

	segs, _ := e.st.ListSegments(ctx, store.SegmentFilter{StreamID: e.stream.ID, OnlyProblems: true})
	if len(segs) != 1 || segs[0].Seq != 41 || segs[0].VariantID != hi || segs[0].Problems[0] != probe.ProbSegmentSlow {
		t.Errorf("segments = %+v", segs)
	}
	snaps, _ := e.st.ListSnapshots(ctx, e.stream.ID, 10)
	if len(snaps) != 1 || snaps[0].Problems[0] != probe.ProbSeqJump || snaps[0].Body != "" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if full, _ := e.st.GetSnapshot(ctx, snaps[0].ID); full.Body != "#EXTM3U\n" {
		t.Errorf("snapshot body = %q", full.Body)
	}

	if err := e.mx.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(e.sent, "")
	wantLine := `hls_segment_bitrate_bps{stream="1",agent="1",variant="` + itoa(hi) + `"} 1.9e+06 `
	if !strings.Contains(sent, wantLine) {
		t.Errorf("series line %q not sent; got:\n%s", wantLine, sent)
	}
	if !strings.Contains(sent, `hls_stream_status{stream="1",agent="1"} 0 `) {
		t.Errorf("stream-level series must have no variant label:\n%s", sent)
	}
	if strings.Contains(sent, "Bad-Name") {
		t.Error("invalid metric names must be dropped")
	}

	gone := *r
	gone.TargetID = 999
	if err := e.in.Apply(ctx, store.LocalAgentID, &gone); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a result for a missing stream must be rejected with ErrNotFound, got %v", err)
	}
}

func TestIncidentLifecycle(t *testing.T) {
	e := newEnv(t)
	stale := probe.Problem{Kind: probe.ProbPlaylistStale, Severity: probe.SevCrit, Variant: "hi.m3u8", Message: "stale for 20s", Count: 1}

	snap := []probe.Snapshot{{Variant: "hi.m3u8", URL: "http://x/hi.m3u8", Body: "#EXTM3U\n"}}
	r := e.result(twoVariants, stale)
	r.Snapshots = snap
	e.apply(r)
	stale.Message = "stale for 30s"
	r = e.result(twoVariants, stale)
	r.Snapshots = snap
	e.apply(r)
	if snaps, _ := e.st.ListSnapshots(ctx, e.stream.ID, 10); len(snaps) != 1 {
		t.Errorf("a persisting problem stored %d snapshots, want only the one from its first cycle", len(snaps))
	}
	open := e.incidents(true)
	if len(open) != 1 {
		t.Fatalf("one incident must stay open across repeats, got %d", len(open))
	}
	inc := open[0]
	if inc.Count != 2 || inc.Message != "stale for 30s" || inc.VariantKey != "hi.m3u8" || inc.Severity != probe.SevCrit {
		t.Errorf("incident = %+v", inc)
	}
	if !inc.LastSeen.After(inc.StartedAt) {
		t.Errorf("last seen %v must move past start %v", inc.LastSeen, inc.StartedAt)
	}

	// Two clean cycles are not enough.
	e.apply(e.result(twoVariants))
	e.apply(e.result(twoVariants))
	if len(e.incidents(true)) != 1 {
		t.Fatal("incident closed too early")
	}
	// A relapse resets the count of clean cycles.
	e.apply(e.result(twoVariants, stale))
	e.apply(e.result(twoVariants))
	e.apply(e.result(twoVariants))
	if len(e.incidents(true)) != 1 {
		t.Fatal("incident closed although the problem came back in between")
	}
	e.apply(e.result(twoVariants))
	if len(e.incidents(true)) != 0 {
		t.Fatal("incident must close after three clean cycles")
	}
	closed := e.incidents(false)
	if len(closed) != 1 || closed[0].EndedAt == nil || closed[0].Count != 3 {
		t.Fatalf("closed incidents = %+v", closed)
	}

	// The same problem soon after is the same incident going on: it is
	// opened again, and tells how many times it began and how long it
	// was away.
	e.apply(e.result(twoVariants, stale))
	open = e.incidents(true)
	if len(open) != 1 || open[0].ID != closed[0].ID {
		t.Fatalf("a problem that came back at once must open the same incident again: %+v", open)
	}
	if again := open[0]; again.Episodes != 2 || again.IdleSeconds != 10 || again.Count != 4 || !again.Active {
		t.Errorf("opened again: episodes %d, idle %v s, count %d, active %v; want 2, 10, 4, true", again.Episodes, again.IdleSeconds, again.Count, again.Active)
	}
	for range 3 {
		e.apply(e.result(twoVariants))
	}
	if len(e.incidents(true)) != 0 {
		t.Fatal("the incident opened again must close like any other")
	}

	// Much later it is a new one.
	e.clock = e.clock.Add(2 * ReopenWithin)
	e.apply(e.result(twoVariants, stale))
	if open := e.incidents(true); len(open) != 1 || open[0].ID == closed[0].ID || open[0].Episodes != 1 {
		t.Fatalf("a recurrence long after must open a new incident: %+v", open)
	}
}

func TestIncidentNeedsEvidenceToClose(t *testing.T) {
	e := newEnv(t)
	e.apply(e.result(twoVariants, probe.Problem{Kind: probe.ProbSegmentHTTP, Severity: probe.SevCrit, Variant: "lo.m3u8", Message: "404", Count: 1}))

	// The master playlist fails: variants are not reached at all.
	for range 5 {
		e.clock = e.clock.Add(10 * time.Second)
		e.apply(&probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: probe.StatusFail,
			Metrics:  []probe.Metric{{Name: "hls_up", Variant: probe.VariantMaster, Value: 0}},
			Problems: []probe.Problem{{Kind: probe.ProbPlaylistHTTP, Severity: probe.SevCrit, Variant: probe.VariantMaster, Message: "502", Count: 1}}})
	}
	open := e.incidents(true)
	if len(open) != 2 {
		t.Fatalf("the variant incident must survive while the variant is unreachable: %+v", open)
	}

	// The variant disappears from the master playlist: its incident ends at once.
	e.apply(e.result(twoVariants[:2]))
	open = e.incidents(true)
	if len(open) != 1 || open[0].Kind != probe.ProbPlaylistHTTP {
		t.Fatalf("open after variant removal = %+v", open)
	}
	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	if last := variants[len(variants)-1]; last.Key != "lo.m3u8" || last.Active {
		t.Errorf("removed variant must be kept as inactive: %+v", last)
	}
}

func TestDisablingStreamClosesIncidents(t *testing.T) {
	e := newEnv(t)
	e.apply(e.result(twoVariants, probe.Problem{Kind: probe.ProbPlaylistStale, Severity: probe.SevCrit, Variant: "hi.m3u8", Message: "x", Count: 1}))
	if err := e.st.SetStreamEnabled(ctx, e.stream.ID, false, e.clock); err != nil {
		t.Fatal(err)
	}
	if len(e.incidents(true)) != 0 {
		t.Error("incidents of a disabled stream must be closed")
	}
	if err := e.st.DeleteStream(ctx, e.stream.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Apply(ctx, store.LocalAgentID, e.result(twoVariants)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("result for a deleted stream: %v", err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestLatestStateKeepsLastKnownValues(t *testing.T) {
	e := newEnv(t)
	r := e.result(twoVariants)
	r.Metrics = append(r.Metrics, probe.Metric{Name: "hls_segment_bitrate_bps", Variant: "hi.m3u8", Value: 1.9e6})
	e.apply(r)

	// A cycle without a new segment has no bitrate; the state still shows the last one.
	e.apply(e.result(twoVariants))
	states, _ := e.st.StreamStates(ctx, e.stream.ID)
	if states[0].Summary["bitrate_bps"] != 1.9e6 {
		t.Errorf("summary lost the last known bitrate: %+v", states[0].Summary)
	}

	// Once the variant cannot be reached, old figures are not presented as current.
	e.clock = e.clock.Add(10 * time.Second)
	e.apply(&probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: probe.StatusFail, Variants: twoVariants,
		Metrics: []probe.Metric{{Name: "hls_up", Variant: "hi.m3u8", Value: 0}, {Name: "hls_status", Variant: "hi.m3u8", Value: 2}}})
	states, _ = e.st.StreamStates(ctx, e.stream.ID)
	if _, ok := states[0].Summary["bitrate_bps"]; ok {
		t.Errorf("an unreachable variant still reports a bitrate: %+v", states[0].Summary)
	}
}

func TestSourcesAndInformationalIncidents(t *testing.T) {
	e := newEnv(t)
	epA := &probe.Endpoint{URL: "http://edge-a/x.m3u8", Host: "edge-a", IP: "10.0.0.6", Headers: map[string]string{"Server": "nginx/1.24"}}
	segA := &probe.Endpoint{URL: "http://edge-a/1.ts", Host: "edge-a", IP: "10.0.0.6"}
	r := e.result(twoVariants)
	r.Sources = []probe.Source{{Variant: "hi.m3u8", Playlist: epA, Segment: segA}}
	e.apply(r)

	// The next cycle downloads no segment and is answered by another server.
	epB := &probe.Endpoint{URL: "http://edge-b/x.m3u8", Host: "edge-b", IP: "10.0.0.7"}
	r = e.result(twoVariants, probe.Problem{Kind: probe.ProbPlaylistSource, Severity: probe.SevInfo, Variant: "hi.m3u8", Message: "moved", Count: 1})
	r.Status = probe.StatusOK
	r.Sources = []probe.Source{{Variant: "hi.m3u8", Playlist: epB}}
	e.apply(r)

	var hi *store.VariantState
	states, _ := e.st.VariantStates(ctx, e.stream.ID)
	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	for _, vs := range states {
		for _, v := range variants {
			if v.ID == vs.VariantID && v.Key == "hi.m3u8" {
				hi = vs
			}
		}
	}
	if hi == nil || hi.PlaylistSource == nil || hi.PlaylistSource.Host != "edge-b" {
		t.Fatalf("playlist source = %+v, want the latest one", hi)
	}
	if hi.SegmentSource == nil || hi.SegmentSource.Host != "edge-a" {
		t.Errorf("segment source = %+v, want the last known one kept", hi.SegmentSource)
	}

	// Both servers are remembered, with how often each answered.
	seen, _ := e.st.ListSources(ctx, e.stream.ID, e.clock.Add(-time.Hour))
	got := map[string]int64{}
	for _, src := range seen {
		got[src.What+" "+src.Host+" "+src.Software] = src.Hits
	}
	if len(seen) != 3 || got["playlist edge-a nginx/1.24"] != 1 || got["playlist edge-b "] != 1 || got["segment edge-a "] != 1 {
		t.Errorf("source history = %v", got)
	}
	if recent, _ := e.st.ListSources(ctx, 0, e.clock); len(recent) != 1 || recent[0].Host != "edge-b" {
		t.Errorf("sources seen in the last cycle = %+v", recent)
	}

	ss, _ := e.st.StreamStates(ctx, e.stream.ID)
	if ss[0].Status != probe.StatusOK || ss[0].Summary["problems"] != 0 {
		t.Errorf("an informational event changed the state: %+v", ss[0])
	}
	all, _ := e.st.ListIncidents(ctx, store.IncidentFilter{StreamID: e.stream.ID})
	problems, _ := e.st.ListIncidents(ctx, store.IncidentFilter{StreamID: e.stream.ID, Problems: true})
	if len(all) != 1 || all[0].Severity != probe.SevInfo || len(problems) != 0 {
		t.Errorf("incidents: all = %+v, problems only = %+v", all, problems)
	}
}

// Something a source does every minute is one event that keeps recurring,
// not a new incident each time; and the segment journal is written.
func TestRecurringEventIsOneIncident(t *testing.T) {
	e := newEnv(t)
	flap := probe.Problem{Kind: probe.ProbTargetDurChanged, Severity: probe.SevInfo, Variant: "hi.m3u8", Message: "target duration changed from 3s to 2s", Count: 1}
	for range 6 {
		r := e.result(twoVariants, flap)
		r.Status = probe.StatusOK
		r.Segments = []probe.Segment{{Variant: "hi.m3u8", Seq: 1, URL: "http://x/1.ts", Time: e.clock, Duration: 5, Status: 200}}
		e.apply(r)
		// A minute of clean cycles: enough to close a real problem.
		for range 6 {
			e.apply(e.result(twoVariants))
		}
	}
	open := e.incidents(true)
	if len(open) != 1 || open[0].Count != 6 || len(e.incidents(false)) != 0 {
		t.Fatalf("a flapping value gave %d open and %d closed incidents, want a single open one counting 6", len(open), len(e.incidents(false)))
	}
	// Quiet for long enough, it ends.
	e.clock = e.clock.Add(e.in.CloseEventAfter)
	e.apply(e.result(twoVariants))
	if len(e.incidents(true)) != 0 || len(e.incidents(false)) != 1 {
		t.Errorf("the event must close once it has not recurred for %s", e.in.CloseEventAfter)
	}
	if segs, _ := e.st.ListSegments(ctx, store.SegmentFilter{StreamID: e.stream.ID}); len(segs) != 6 {
		t.Errorf("journal rows = %d, want 6", len(segs))
	}
}

// A result delivered twice, or one older than what is stored, changes
// nothing: no second count on the incident, no step back in the state.
func TestRepeatedAndLateResults(t *testing.T) {
	e := newEnv(t)
	stale := probe.Problem{Kind: probe.ProbPlaylistStale, Severity: probe.SevCrit, Variant: "hi.m3u8", Message: "stale for 20s", Count: 1}
	first := e.result(twoVariants, stale)
	e.apply(first)
	second := e.result(twoVariants)
	e.apply(second)
	_ = e.mx.Flush(ctx)
	sent := len(e.sent)

	for name, r := range map[string]*probe.Result{"the same result again": second, "an older result": first} {
		if err := e.in.Apply(ctx, store.LocalAgentID, r); !errors.Is(err, ErrStale) {
			t.Fatalf("%s: err = %v, want ErrStale", name, err)
		}
	}
	_ = e.mx.Flush(ctx)
	if len(e.sent) != sent {
		t.Errorf("repeated results sent metrics again")
	}
	inc := append(e.incidents(true), e.incidents(false)...)
	if len(inc) != 1 || inc[0].Count != 1 {
		t.Fatalf("incidents after repeats: %d, count %d; want one counted once", len(inc), inc[0].Count)
	}
	states, _ := e.st.StreamStates(ctx, e.stream.ID)
	if len(states) != 1 || !states[0].CheckedAt.Equal(second.Time) || states[0].Status != probe.StatusOK {
		t.Errorf("the state must stay as the newest result left it, got %+v", states[0])
	}
}

// A result made by an overloaded checker lacks what rests on timing; that
// is no evidence that the problem is over.
func TestOverloadedResultClosesNothing(t *testing.T) {
	e := newEnv(t)
	e.apply(e.result(twoVariants, probe.Problem{Kind: probe.ProbSegmentSlow, Severity: probe.SevWarn, Variant: "lo.m3u8", Message: "slow", Count: 1}))
	for range 6 {
		e.clock = e.clock.Add(20 * time.Second)
		r := e.result(twoVariants)
		r.Overloaded = "processor"
		e.apply(r)
	}
	if open := e.incidents(true); len(open) != 1 {
		t.Fatalf("the incident must stay open while the checker is overloaded: %+v", open)
	}
	st, _ := e.st.StreamStates(ctx, e.stream.ID)
	if len(st) != 1 || st[0].Summary["overloaded"] != 1 {
		t.Errorf("the state must say the check was made under overload: %+v", st)
	}
	for range 4 {
		e.clock = e.clock.Add(20 * time.Second)
		e.apply(e.result(twoVariants))
	}
	if open := e.incidents(true); len(open) != 0 {
		t.Errorf("clean checks without overload close it: %+v", open)
	}
	if s := e.in.Stats(); s.Stored != 11 || s.Failed != 0 {
		t.Errorf("stats = %+v", s)
	}
}
