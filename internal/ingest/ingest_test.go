package ingest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/overload"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/status"
	"github.com/hotid/hls-monitor/internal/types/summarykey"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
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
	e.stream, err = st.CreateStream(ctx, store.StreamInput{Name: "s", URL: "http://x/master.m3u8", Enabled: true, Level: level.Segments(), Picture: picturemode.Check(),
		AgentIDs: []id.Agent{store.LocalAgentID()}}, e.clock)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var twoVariants = []probe.Variant{
	{Key: variantkey.Master(), Kind: variantkind.Master()},
	{Key: variantkey.OfURI("hi.m3u8"), Kind: variantkind.Video(), Bandwidth: 2000000, Resolution: "1280x720"},
	{Key: variantkey.OfURI("lo.m3u8"), Kind: variantkind.Video(), Bandwidth: 500000, Resolution: "640x360"},
}

// result builds a cycle result in which every listed variant was reached.
func (e *env) result(variants []probe.Variant, problems ...probe.Problem) *probe.Result {
	e.clock = e.clock.Add(10 * time.Second)
	r := &probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: status.OK(), Variants: variants, Problems: problems}
	for _, v := range variants {
		r.Metrics = append(r.Metrics, probe.Metric{Name: metricname.Up(), Variant: v.Key, Value: 1}, probe.Metric{Name: metricname.Status(), Variant: v.Key, Value: 0})
	}
	r.Metrics = append(r.Metrics, probe.Metric{Name: metricname.StreamStatus(), Value: 0})
	for _, p := range problems {
		if p.Severity == severity.Crit() {
			r.Status = status.Fail()
		} else if r.Status == status.OK() {
			r.Status = status.Warn()
		}
	}
	return r
}

func (e *env) apply(r *probe.Result) {
	e.t.Helper()
	if err := e.in.Apply(ctx, store.LocalAgentID(), r); err != nil {
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
	r := e.result(twoVariants, probe.Problem{Kind: problemkind.SeqJump(), Severity: severity.Warn(), Variant: variantkey.OfURI("hi.m3u8"), Message: "2 segments lost", Count: 1})
	r.Metrics = append(r.Metrics,
		probe.Metric{Name: metricname.SegmentBitrateBps(), Variant: variantkey.OfURI("hi.m3u8"), Value: 1.9e6},
		probe.Metric{Name: metricname.SegmentBitrateBps(), Variant: variantkey.OfURI("lo.m3u8"), Value: 0.4e6},
	)
	r.Segments = []probe.Segment{{Variant: variantkey.OfURI("hi.m3u8"), Seq: 41, URL: "http://x/hi/41.ts", Time: e.clock, Duration: 6, Status: 200, Size: 1000, TotalMs: 120,
		Problems: []problemkind.Kind{problemkind.SegmentSlow()}}}
	r.Snapshots = []probe.Snapshot{{Variant: variantkey.OfURI("hi.m3u8"), URL: "http://x/hi.m3u8", Body: "#EXTM3U\n"}}
	e.apply(r)

	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	if len(variants) != 3 || variants[0].Kind != variantkind.Master() || variants[1].Bandwidth != 2000000 {
		t.Fatalf("variants = %+v", variants)
	}
	hi := variants[1].ID

	states, _ := e.st.StreamStates(ctx, e.stream.ID)
	if len(states) != 1 || states[0].Status != status.Warn() || states[0].Summary[summarykey.BitrateBps()] != 1.9e6 || states[0].Summary[summarykey.Variants()] != 2 || states[0].Summary[summarykey.Subtitles()] != 0 {
		t.Errorf("stream state = %+v", states)
	}
	vstates, _ := e.st.VariantStates(ctx, e.stream.ID)
	if len(vstates) != 3 {
		t.Errorf("variant states = %d, want 3", len(vstates))
	}

	segs, _ := e.st.ListSegments(ctx, store.SegmentFilter{StreamID: e.stream.ID, OnlyProblems: true})
	if len(segs) != 1 || segs[0].Seq != 41 || segs[0].VariantID != hi || segs[0].Problems[0] != problemkind.SegmentSlow() {
		t.Errorf("segments = %+v", segs)
	}
	snaps, _ := e.st.ListSnapshots(ctx, e.stream.ID, 10)
	if len(snaps) != 1 || snaps[0].Problems[0] != problemkind.SeqJump() || snaps[0].Body != "" {
		t.Fatalf("snapshots = %+v", snaps)
	}
	if full, _ := e.st.GetSnapshot(ctx, snaps[0].ID); full.Body != "#EXTM3U\n" {
		t.Errorf("snapshot body = %q", full.Body)
	}

	if err := e.mx.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	sent := strings.Join(e.sent, "")
	wantLine := `hls_segment_bitrate_bps{stream="1",agent="1",variant="` + itoa(hi.Int64()) + `"} 1.9e+06 `
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
	gone.TargetID = id.StreamOf(999)
	if err := e.in.Apply(ctx, store.LocalAgentID(), &gone); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a result for a missing stream must be rejected with ErrNotFound, got %v", err)
	}
}

func TestIncidentLifecycle(t *testing.T) {
	e := newEnv(t)
	stale := probe.Problem{Kind: problemkind.PlaylistStale(), Severity: severity.Crit(), Variant: variantkey.OfURI("hi.m3u8"), Message: "stale for 20s", Count: 1}

	snap := []probe.Snapshot{{Variant: variantkey.OfURI("hi.m3u8"), URL: "http://x/hi.m3u8", Body: "#EXTM3U\n"}}
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
	if inc.Count != 2 || inc.Message != "stale for 30s" || inc.VariantKey != variantkey.OfURI("hi.m3u8") || inc.Severity != severity.Crit() {
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
	e.apply(e.result(twoVariants, probe.Problem{Kind: problemkind.SegmentHTTP(), Severity: severity.Crit(), Variant: variantkey.OfURI("lo.m3u8"), Message: "404", Count: 1}))

	// The master playlist fails: variants are not reached at all.
	for range 5 {
		e.clock = e.clock.Add(10 * time.Second)
		e.apply(&probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: status.Fail(),
			Metrics:  []probe.Metric{{Name: metricname.Up(), Variant: variantkey.Master(), Value: 0}},
			Problems: []probe.Problem{{Kind: problemkind.PlaylistHTTP(), Severity: severity.Crit(), Variant: variantkey.Master(), Message: "502", Count: 1}}})
	}
	open := e.incidents(true)
	if len(open) != 2 {
		t.Fatalf("the variant incident must survive while the variant is unreachable: %+v", open)
	}

	// The variant disappears from the master playlist: its incident ends at once.
	e.apply(e.result(twoVariants[:2]))
	open = e.incidents(true)
	if len(open) != 1 || open[0].Kind != problemkind.PlaylistHTTP() {
		t.Fatalf("open after variant removal = %+v", open)
	}
	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	if last := variants[len(variants)-1]; last.Key != variantkey.OfURI("lo.m3u8") || last.Active {
		t.Errorf("removed variant must be kept as inactive: %+v", last)
	}
}

func TestDisablingStreamClosesIncidents(t *testing.T) {
	e := newEnv(t)
	e.apply(e.result(twoVariants, probe.Problem{Kind: problemkind.PlaylistStale(), Severity: severity.Crit(), Variant: variantkey.OfURI("hi.m3u8"), Message: "x", Count: 1}))
	if err := e.st.SetStreamEnabled(ctx, e.stream.ID, false, e.clock); err != nil {
		t.Fatal(err)
	}
	if len(e.incidents(true)) != 0 {
		t.Error("incidents of a disabled stream must be closed")
	}
	if err := e.st.DeleteStream(ctx, e.stream.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.in.Apply(ctx, store.LocalAgentID(), e.result(twoVariants)); !errors.Is(err, store.ErrNotFound) {
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
	r.Metrics = append(r.Metrics, probe.Metric{Name: metricname.SegmentBitrateBps(), Variant: variantkey.OfURI("hi.m3u8"), Value: 1.9e6})
	e.apply(r)

	// A cycle without a new segment has no bitrate; the state still shows the last one.
	e.apply(e.result(twoVariants))
	states, _ := e.st.StreamStates(ctx, e.stream.ID)
	if states[0].Summary[summarykey.BitrateBps()] != 1.9e6 {
		t.Errorf("summary lost the last known bitrate: %+v", states[0].Summary)
	}

	// Once the variant cannot be reached, old figures are not presented as current.
	e.clock = e.clock.Add(10 * time.Second)
	e.apply(&probe.Result{TargetID: e.stream.ID, Time: e.clock, Status: status.Fail(), Variants: twoVariants,
		Metrics: []probe.Metric{{Name: metricname.Up(), Variant: variantkey.OfURI("hi.m3u8"), Value: 0}, {Name: metricname.Status(), Variant: variantkey.OfURI("hi.m3u8"), Value: 2}}})
	states, _ = e.st.StreamStates(ctx, e.stream.ID)
	if _, ok := states[0].Summary[summarykey.BitrateBps()]; ok {
		t.Errorf("an unreachable variant still reports a bitrate: %+v", states[0].Summary)
	}
}

func TestSourcesAndInformationalIncidents(t *testing.T) {
	e := newEnv(t)
	epA := &probe.Endpoint{URL: "http://edge-a/x.m3u8", Host: "edge-a", IP: netip.MustParseAddr("10.0.0.6"), Headers: map[string]string{"Server": "nginx/1.24"}}
	segA := &probe.Endpoint{URL: "http://edge-a/1.ts", Host: "edge-a", IP: netip.MustParseAddr("10.0.0.6")}
	r := e.result(twoVariants)
	r.Sources = []probe.Source{{Variant: variantkey.OfURI("hi.m3u8"), Playlist: epA, Segment: segA}}
	e.apply(r)

	// The next cycle downloads no segment and is answered by another server.
	epB := &probe.Endpoint{URL: "http://edge-b/x.m3u8", Host: "edge-b", IP: netip.MustParseAddr("10.0.0.7")}
	r = e.result(twoVariants, probe.Problem{Kind: problemkind.PlaylistSource(), Severity: severity.Info(), Variant: variantkey.OfURI("hi.m3u8"), Message: "moved", Count: 1})
	r.Status = status.OK()
	r.Sources = []probe.Source{{Variant: variantkey.OfURI("hi.m3u8"), Playlist: epB}}
	e.apply(r)

	var hi *store.VariantState
	states, _ := e.st.VariantStates(ctx, e.stream.ID)
	variants, _ := e.st.ListVariants(ctx, e.stream.ID)
	for _, vs := range states {
		for _, v := range variants {
			if v.ID == vs.VariantID && v.Key == variantkey.OfURI("hi.m3u8") {
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
		got[src.What.String()+" "+src.Host+" "+src.Software] = src.Hits
	}
	if len(seen) != 3 || got["playlist edge-a nginx/1.24"] != 1 || got["playlist edge-b "] != 1 || got["segment edge-a "] != 1 {
		t.Errorf("source history = %v", got)
	}
	if recent, _ := e.st.ListSources(ctx, id.StreamOf(0), e.clock); len(recent) != 1 || recent[0].Host != "edge-b" {
		t.Errorf("sources seen in the last cycle = %+v", recent)
	}

	ss, _ := e.st.StreamStates(ctx, e.stream.ID)
	if ss[0].Status != status.OK() || ss[0].Summary[summarykey.Problems()] != 0 {
		t.Errorf("an informational event changed the state: %+v", ss[0])
	}
	all, _ := e.st.ListIncidents(ctx, store.IncidentFilter{StreamID: e.stream.ID})
	problems, _ := e.st.ListIncidents(ctx, store.IncidentFilter{StreamID: e.stream.ID, Problems: true})
	if len(all) != 1 || all[0].Severity != severity.Info() || len(problems) != 0 {
		t.Errorf("incidents: all = %+v, problems only = %+v", all, problems)
	}
}

// Something a source does every minute is one event that keeps recurring,
// not a new incident each time; and the segment journal is written.
func TestRecurringEventIsOneIncident(t *testing.T) {
	e := newEnv(t)
	flap := probe.Problem{Kind: problemkind.TargetDurChanged(), Severity: severity.Info(), Variant: variantkey.OfURI("hi.m3u8"), Message: "target duration changed from 3s to 2s", Count: 1}
	for range 6 {
		r := e.result(twoVariants, flap)
		r.Status = status.OK()
		r.Segments = []probe.Segment{{Variant: variantkey.OfURI("hi.m3u8"), Seq: 1, URL: "http://x/1.ts", Time: e.clock, Duration: 5, Status: 200}}
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
	stale := probe.Problem{Kind: problemkind.PlaylistStale(), Severity: severity.Crit(), Variant: variantkey.OfURI("hi.m3u8"), Message: "stale for 20s", Count: 1}
	first := e.result(twoVariants, stale)
	e.apply(first)
	second := e.result(twoVariants)
	e.apply(second)
	_ = e.mx.Flush(ctx)
	sent := len(e.sent)

	for name, r := range map[string]*probe.Result{"the same result again": second, "an older result": first} {
		if err := e.in.Apply(ctx, store.LocalAgentID(), r); !errors.Is(err, ErrStale) {
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
	if len(states) != 1 || !states[0].CheckedAt.Equal(second.Time) || states[0].Status != status.OK() {
		t.Errorf("the state must stay as the newest result left it, got %+v", states[0])
	}
}

// A result made by an overloaded checker lacks what rests on timing; that
// is no evidence that the problem is over.
func TestOverloadedResultClosesNothing(t *testing.T) {
	e := newEnv(t)
	e.apply(e.result(twoVariants, probe.Problem{Kind: problemkind.SegmentSlow(), Severity: severity.Warn(), Variant: variantkey.OfURI("lo.m3u8"), Message: "slow", Count: 1}))
	for range 6 {
		e.clock = e.clock.Add(20 * time.Second)
		r := e.result(twoVariants)
		r.Overloaded = overload.Processor()
		e.apply(r)
	}
	if open := e.incidents(true); len(open) != 1 {
		t.Fatalf("the incident must stay open while the checker is overloaded: %+v", open)
	}
	st, _ := e.st.StreamStates(ctx, e.stream.ID)
	if len(st) != 1 || st[0].Summary[summarykey.Overloaded()] != 1 {
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

// The state of a playlist is what the result says it is: never what a
// figure that is not there would read as.
func TestVariantStatusIsTold(t *testing.T) {
	e := newEnv(t)
	hi := twoVariants[1].Key
	state := func() map[variantkey.Key]status.Status {
		t.Helper()
		variants, err := e.st.ListVariants(context.Background(), e.stream.ID)
		if err != nil {
			t.Fatal(err)
		}
		keys := map[id.Variant]variantkey.Key{}
		for _, v := range variants {
			keys[v.ID] = v.Key
		}
		states, err := e.st.VariantStates(context.Background(), e.stream.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[variantkey.Key]status.Status{}
		for _, s := range states {
			out[keys[s.VariantID]] = s.Status
		}
		return out
	}
	// Told as a value, it is taken as told, whatever the figure says.
	r := e.result(twoVariants)
	r.Statuses = []probe.VariantStatus{{Variant: hi, Status: status.Fail()}}
	e.apply(r)
	if got := state()[hi]; got != status.Fail() {
		t.Fatalf("a status told as fail was stored as %v", got)
	}
	// An older agent tells it as a figure only, and it is read from there.
	e.apply(e.result(twoVariants))
	if got := state()[hi]; got != status.OK() {
		t.Fatalf("a status told by a figure was stored as %v", got)
	}
	// Told in no way, it is not made up: what was known stays.
	r = e.result(twoVariants)
	r.Statuses = []probe.VariantStatus{{Variant: hi, Status: status.Warn()}}
	e.apply(r)
	r = e.result(twoVariants)
	kept := r.Metrics[:0]
	for _, m := range r.Metrics {
		if m.Name != metricname.Status() {
			kept = append(kept, m)
		}
	}
	r.Metrics = kept
	e.apply(r)
	if got := state()[hi]; got != status.Warn() {
		t.Fatalf("with no status told the stored one became %v", got)
	}
}
