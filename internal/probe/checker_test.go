package probe

import (
	"context"
	"fmt"
	"github.com/Eyevinn/hls-m3u8/m3u8"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/httpx"
)

// fixture is a fake origin plus a checker with a controllable clock.
type fixture struct {
	t      *testing.T
	origin *hlstest.Origin
	srv    *httptest.Server
	c      *Checker
	clock  time.Time
}

func newFixture(t *testing.T, opt hlstest.Options, path string, level Level) *fixture {
	t.Helper()
	f := &fixture{t: t, origin: hlstest.New(opt), clock: time.Now()}
	f.srv = httptest.NewServer(f.origin.Handler())
	t.Cleanup(f.srv.Close)
	f.c = New(Target{ID: 1, URL: f.srv.URL + path, Level: level, TimeoutSec: 5}, nil)
	f.c.now = func() time.Time { return f.clock }
	return f
}

// step publishes n segments, moves the clock as a real stream would and runs a cycle.
func (f *fixture) step(n int) *Result {
	f.t.Helper()
	f.origin.Publish(n)
	f.clock = f.clock.Add(time.Duration(float64(n) * hlstest.SegmentDur * float64(time.Second)))
	return f.c.Run(context.Background())
}

func (f *fixture) run() *Result { return f.c.Run(context.Background()) }

func hasProblem(r *Result, kind string) *Problem {
	for i := range r.Problems {
		if r.Problems[i].Kind == kind {
			return &r.Problems[i]
		}
	}
	return nil
}

func metric(r *Result, name, variant string) (float64, bool) {
	for _, m := range r.Metrics {
		if m.Name == name && m.Variant == variant {
			return m.Value, true
		}
	}
	return 0, false
}

func wantClean(t *testing.T, r *Result) {
	t.Helper()
	if r.Status != StatusOK || len(r.Problems) != 0 {
		t.Fatalf("status = %s, problems = %+v; want a clean cycle", r.Status, r.Problems)
	}
}

func wantProblem(t *testing.T, r *Result, kind, severity string) *Problem {
	t.Helper()
	p := hasProblem(r, kind)
	if p == nil {
		t.Fatalf("problem %q not reported; got %+v", kind, r.Problems)
	}
	if p.Severity != severity {
		t.Fatalf("problem %q severity = %s, want %s", kind, p.Severity, severity)
	}
	return p
}

func TestHealthyLiveStream(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)

	r := f.step(4)
	wantClean(t, r)
	if len(r.Segments) != 1 || r.Segments[0].Seq != 3 {
		t.Fatalf("first cycle must check only the newest segment, got %+v", r.Segments)
	}
	if len(r.Variants) != 1 || r.Variants[0].Key != VariantMedia {
		t.Fatalf("variants = %+v", r.Variants)
	}

	// Walk across the point where the sample loop restarts: the playlist
	// announces the timestamp jump, so it is not a problem.
	for i := range 6 {
		r = f.step(1)
		wantClean(t, r)
		if len(r.Segments) != 1 || r.Segments[0].Seq != uint64(4+i) {
			t.Fatalf("cycle %d: want exactly the new segment %d, got %+v", i, 4+i, r.Segments)
		}
	}

	seg := r.Segments[0]
	if seg.Status != 200 || seg.Size == 0 || seg.Media == nil || seg.Media.Video() == nil {
		t.Fatalf("segment record incomplete: %+v", seg)
	}
	if !strings.HasPrefix(seg.URL, f.srv.URL+"/v0/seg-") {
		t.Errorf("segment URL must be resolved against the media playlist, got %s", seg.URL)
	}
	for _, name := range []string{
		"hls_playlist_up", "hls_playlist_total_seconds", "hls_media_sequence", "hls_live_latency_seconds",
		"hls_segment_total_seconds", "hls_segment_bitrate_bps", "hls_media_fps", "hls_media_width",
		"hls_media_pts_gap_seconds", "hls_buffer_seconds", "hls_startup_seconds", "hls_status",
	} {
		if _, ok := metric(r, name, VariantMedia); !ok {
			t.Errorf("metric %s missing", name)
		}
	}
	if v, _ := metric(r, "hls_media_width", VariantMedia); v != 160 {
		t.Errorf("hls_media_width = %v, want 160", v)
	}
	if v, _ := metric(r, "hls_playlist_new_segments", VariantMedia); v != 1 {
		t.Errorf("hls_playlist_new_segments = %v, want 1", v)
	}
	if got := f.c.Interval(); got != time.Second {
		t.Errorf("interval = %s, want half of the 2s target duration", got)
	}
}

func TestSegmentsAreNotDownloadedTwice(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	f.step(1)
	// Nothing new: the playlist is re-read but no segment is fetched.
	f.clock = f.clock.Add(time.Second)
	r := f.run()
	wantClean(t, r)
	if len(r.Segments) != 0 {
		t.Fatalf("no new segments, yet %d were downloaded", len(r.Segments))
	}
	for seq := uint64(0); seq <= f.origin.Last(); seq++ {
		if n := f.origin.Requests("/v0/seg-" + itoa(seq) + ".ts"); n > 1 {
			t.Errorf("segment %d requested %d times", seq, n)
		}
	}
}

func TestPlaylistLevelDownloadsNothing(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelPlaylist)
	wantClean(t, f.step(4))
	r := f.step(1)
	wantClean(t, r)
	if len(r.Segments) != 0 {
		t.Fatalf("playlist level must not download segments, got %d", len(r.Segments))
	}
	if _, ok := metric(r, "hls_buffer_seconds", VariantMedia); ok {
		t.Error("player model must not run without segment downloads")
	}
}

func TestSequenceJump(t *testing.T) {
	f := newFixture(t, hlstest.Options{Window: 3}, "/v0/index.m3u8", LevelSegments)
	f.step(3)
	// Six segments appear at once in a three-segment window: three were never seen.
	f.origin.Publish(6)
	f.clock = f.clock.Add(2 * time.Second)
	r := f.run()
	p := wantProblem(t, r, ProbSeqJump, SevWarn)
	if !strings.Contains(p.Message, "3 segments") {
		t.Errorf("message = %q, want it to count 3 lost segments", p.Message)
	}
	if v, _ := metric(r, "hls_segments_missed", VariantMedia); v != 3 {
		t.Errorf("hls_segments_missed = %v, want 3", v)
	}
	if len(r.Snapshots) != 1 || !strings.Contains(r.Snapshots[0].Body, "#EXTM3U") {
		t.Fatalf("a playlist snapshot must accompany the problem, got %+v", r.Snapshots)
	}
	// What the playlist said one check earlier is kept with it.
	sn := r.Snapshots[0]
	if !strings.Contains(sn.Previous, "#EXTM3U") || sn.Previous == sn.Body || sn.PreviousTime == nil || !sn.PreviousTime.Equal(f.clock.Add(-2*time.Second)) {
		t.Errorf("snapshot must carry the previous playlist and its time, got previous at %v:\n%s", sn.PreviousTime, sn.Previous)
	}
}

// Segments that left the playlist while it could not be read are a
// consequence of the outage: recorded, with the outage named, not a warning.
func TestSequenceJumpAfterOutage(t *testing.T) {
	f := newFixture(t, hlstest.Options{Window: 3}, "/v0/index.m3u8", LevelSegments)
	f.step(3)
	f.origin.SetDown(404)
	for range 2 {
		f.clock = f.clock.Add(2 * time.Second)
		wantProblem(t, f.run(), ProbPlaylistHTTP, SevCrit)
	}
	f.origin.SetDown(0)
	f.origin.Publish(6)
	f.clock = f.clock.Add(2 * time.Second)
	r := f.run()
	p := wantProblem(t, r, ProbSeqJump, SevInfo)
	if p.Details["failed_checks_between"] != 2 || !strings.Contains(fmt.Sprint(p.Details["last_failure"]), "404") {
		t.Errorf("details must name the failed checks in between, got %v", p.Details)
	}
	if got := p.Details["since_previous_check"]; got != 6.0 {
		t.Errorf("since_previous_check = %v, want 6 s since the last check that read the playlist", got)
	}
}

func TestSequenceRegression(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(8)
	f.origin.Rewind(4)
	f.clock = f.clock.Add(2 * time.Second)
	wantProblem(t, f.run(), ProbSeqRegression, SevCrit)
	// The monitor re-bases on the new numbering and recovers.
	wantClean(t, f.step(1))
}

func TestStalePlaylist(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	f.clock = f.clock.Add(5 * time.Second)
	if p := hasProblem(f.run(), ProbPlaylistStale); p != nil {
		t.Fatalf("5s without change must be tolerated: %+v", p)
	}
	f.clock = f.clock.Add(10 * time.Second)
	r := f.run()
	stale := wantProblem(t, r, ProbPlaylistStale, SevCrit)
	if stale.Details["unchanged_for"] != 15.0 || stale.Details["playlist_url"] == nil || stale.Details["target_duration"] == nil {
		t.Errorf("stale playlist details = %v, want how long it stood still and what was asked", stale.Details)
	}
	stall := wantProblem(t, r, ProbPlaybackStall, SevCrit)
	// The calculation behind the stall: nothing arrived while 10 s played.
	if stall.Details["cycle_elapsed"] != 10.0 || stall.Details["cycle_media"] != 0.0 || stall.Details["buffer_shortfall"] == nil || stall.Details["media_rate"] == nil {
		t.Errorf("stall details = %v, want the figures the stall was concluded from", stall.Details)
	}
	if r.Status != StatusFail {
		t.Errorf("status = %s, want fail", r.Status)
	}
	if v, _ := metric(r, "hls_stalls", VariantMedia); v != 1 {
		t.Errorf("hls_stalls = %v, want 1 when the buffer runs dry", v)
	}

	// Still frozen: the player stays stalled, but it is the same stall.
	f.clock = f.clock.Add(2 * time.Second)
	r = f.run()
	if again := wantProblem(t, r, ProbPlaybackStall, SevCrit); again.Details["cycle_elapsed"] != 10.0 {
		t.Errorf("an ongoing stall must keep the figures it began with, got %v", again.Details)
	}
	if v, _ := metric(r, "hls_stalls", VariantMedia); v != 0 {
		t.Errorf("hls_stalls = %v, want 0: an ongoing stall is not a new one", v)
	}

	// The stream resumes: the player rejoins behind the live edge and plays on.
	for i := range 5 {
		r = f.step(1)
		if p := hasProblem(r, ProbPlaybackStall); p != nil {
			t.Fatalf("cycle %d after recovery still reports a stall", i)
		}
	}
	wantClean(t, r)
	if v, _ := metric(r, "hls_buffer_seconds", VariantMedia); v < 5 {
		t.Errorf("hls_buffer_seconds = %v after recovery, want about three target durations", v)
	}
}

func TestSegmentFailures(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
	f.step(4)

	f.origin.SetFault(4, hlstest.Fault{Status: http.StatusNotFound})
	r := f.step(1)
	wantProblem(t, r, ProbSegmentHTTP, SevCrit)
	if r.Segments[0].Status != 404 || len(r.Segments[0].Problems) == 0 {
		t.Errorf("segment record must carry the failure: %+v", r.Segments[0])
	}
	if v, _ := metric(r, "hls_segment_errors", VariantMedia); v != 1 {
		t.Errorf("hls_segment_errors = %v, want 1", v)
	}

	f.origin.SetFault(5, hlstest.Fault{Truncate: 1000})
	wantProblem(t, f.step(1), ProbSegmentTruncated, SevCrit)

	// Recovery: the segment after a failed one has nothing to be compared with.
	wantClean(t, f.step(1))
}

func TestCorruptTransportStream(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
	f.step(4)

	f.origin.SetFault(4, hlstest.Fault{Corrupt: func(b []byte) []byte {
		return append(b[:188*40], b[188*43:]...) // lose three packets
	}})
	wantProblem(t, f.step(1), ProbTSContinuity, SevWarn)

	f.origin.SetFault(5, hlstest.Fault{Corrupt: func(b []byte) []byte {
		return append(b[:188*40+20], b[188*40+30:]...) // lose framing
	}})
	wantProblem(t, f.step(1), ProbTSSync, SevCrit)

	f.origin.SetFault(6, hlstest.Fault{Corrupt: func([]byte) []byte { return []byte("<html>error</html>") }})
	wantProblem(t, f.step(1), ProbMediaParse, SevWarn)
}

func TestUnannouncedTimestampJump(t *testing.T) {
	f := newFixture(t, hlstest.Options{NoDiscontinuity: true}, "/v0/index.m3u8", LevelContainer)
	f.step(2) // checks segment 1
	wantClean(t, f.step(1))
	// Segment 3 restarts the sample loop: timestamps go back 6 seconds.
	r := f.step(1)
	p := wantProblem(t, r, ProbPTSDiscontinuity, SevWarn)
	if !strings.Contains(p.Message, "-6.000s") {
		t.Errorf("message = %q, want the -6.000s jump", p.Message)
	}
	if v, _ := metric(r, "hls_media_pts_gap_seconds", VariantMedia); v > -5.9 || v < -6.1 {
		t.Errorf("hls_media_pts_gap_seconds = %v, want about -6", v)
	}
}

func TestFragmentedMP4(t *testing.T) {
	f := newFixture(t, hlstest.Options{FMP4: true}, "/v0/index.m3u8", LevelContainer)
	wantClean(t, f.step(4))
	var r *Result
	for range 4 {
		r = f.step(1)
		wantClean(t, r)
	}
	m := r.Segments[0].Media
	if m == nil || m.Container != "fmp4" || m.Video() == nil || !m.Video().HasTiming {
		t.Fatalf("fmp4 segment not analysed with its init section: %+v", m)
	}
	if n := f.origin.Requests("/v0/init.mp4"); n != 1 {
		t.Errorf("init section requested %d times, want once", n)
	}
}

func TestMasterPlaylist(t *testing.T) {
	f := newFixture(t, hlstest.Options{Variants: 2}, "/master.m3u8", LevelContainer)
	r := f.step(4)
	wantClean(t, r)
	if len(r.Variants) != 3 {
		t.Fatalf("want master + 2 variants, got %+v", r.Variants)
	}
	if r.Variants[0].Kind != KindMaster || r.Variants[1].Bandwidth != 200000 || r.Variants[2].Resolution != "160x90" {
		t.Errorf("variant attributes not carried over: %+v", r.Variants)
	}
	if len(r.Segments) != 2 {
		t.Fatalf("each variant must have its newest segment checked, got %d", len(r.Segments))
	}
	if v, _ := metric(r, "hls_variants", VariantMaster); v != 2 {
		t.Errorf("hls_variants = %v, want 2", v)
	}
	if _, ok := metric(r, "hls_segment_bitrate_ratio", "v1/index.m3u8"); !ok {
		t.Error("declared bandwidth must give a bitrate ratio per variant")
	}
	wantClean(t, f.step(1))

	f.origin.SetVariants(1)
	r = f.step(1)
	if p := wantProblem(t, r, ProbVariantsChanged, SevWarn); p.Message != "set of variants changed: removed v1/index.m3u8" {
		t.Errorf("message = %q, want it to name the removed variant", p.Message)
	}
	if len(r.Variants) != 2 {
		t.Errorf("variants after the change = %+v", r.Variants)
	}
	wantClean(t, f.step(1))
}

func TestEntryPlaylistFailures(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/master.m3u8", LevelSegments)
	wantClean(t, f.step(4))

	f.origin.SetDown(http.StatusBadGateway)
	r := f.step(1)
	p := wantProblem(t, r, ProbPlaylistHTTP, SevCrit)
	if p.Variant != VariantMaster {
		t.Errorf("failure must be attributed to the master playlist, got %q", p.Variant)
	}
	if r.Variants != nil {
		t.Errorf("variants must be unknown when the entry playlist failed: %+v", r.Variants)
	}
	if v, _ := metric(r, "hls_up", VariantMaster); v != 0 {
		t.Errorf("hls_up = %v, want 0", v)
	}

	f.origin.SetDown(0)
	f.origin.SetGarbage(true)
	wantProblem(t, f.step(1), ProbPlaylistParse, SevCrit)

	f.origin.SetGarbage(false)
	f.srv.Close()
	wantProblem(t, f.step(1), ProbPlaylistUnreachable, SevCrit)
}

func TestStreamEnd(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	f.origin.End()
	wantProblem(t, f.step(0), ProbStreamEnded, SevWarn)
	// A finished stream is no longer expected to change.
	f.clock = f.clock.Add(time.Minute)
	r := f.run()
	wantClean(t, r)
	if f.c.Interval() != vodInterval {
		t.Errorf("interval = %s, want %s for a finished stream", f.c.Interval(), vodInterval)
	}
}

func TestSlowSegment(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	f.origin.SetFault(4, hlstest.Fault{Delay: 2100 * time.Millisecond})
	r := f.step(1)
	wantProblem(t, r, ProbSegmentSlow, SevWarn)
	if v, _ := metric(r, "hls_segment_download_ratio", VariantMedia); v <= 1 {
		t.Errorf("hls_segment_download_ratio = %v, want > 1", v)
	}
}

func itoa(n uint64) string {
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

// A CDN entry point that redirects to one of several servers: relative
// URIs must resolve against where the playlist really came from, and it
// must be visible which server that was and when it changed.
func TestRedirectingBalancer(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/lb/v0/index.m3u8", LevelContainer)
	// Two servers with the same content; the fixture's own server is also the balancer.
	a := httptest.NewServer(f.origin.Handler())
	defer a.Close()
	b := httptest.NewServer(f.origin.Handler())
	defer b.Close()
	hostOf := func(s *httptest.Server) string { return strings.TrimPrefix(s.URL, "http://") }

	f.origin.SetBackend(a.URL)
	r := f.step(4)
	wantClean(t, r)
	if len(r.Sources) != 1 || r.Sources[0].Variant != VariantMedia {
		t.Fatalf("sources = %+v", r.Sources)
	}
	pl := r.Sources[0].Playlist
	if pl == nil || pl.URL != a.URL+"/v0/index.m3u8" || pl.Host != hostOf(a) || pl.IP != "127.0.0.1" {
		t.Fatalf("playlist endpoint = %+v, want server A", pl)
	}
	if len(pl.Redirects) != 1 || pl.Redirects[0].URL != f.srv.URL+"/lb/v0/index.m3u8" || pl.Redirects[0].Status != 302 {
		t.Errorf("redirect chain = %+v", pl.Redirects)
	}
	seg := r.Segments[0]
	if !strings.HasPrefix(seg.URL, a.URL+"/v0/seg-") {
		t.Errorf("segment URL %s must be resolved against the redirect target %s", seg.URL, a.URL)
	}
	if seg.Source == nil || seg.Source.Host != hostOf(a) || r.Sources[0].Segment == nil || r.Sources[0].Segment.Host != hostOf(a) {
		t.Errorf("segment endpoint = %+v", seg.Source)
	}
	if v, _ := metric(r, "hls_playlist_redirects", VariantMedia); v != 1 {
		t.Errorf("hls_playlist_redirects = %v, want 1", v)
	}

	// Same server again: nothing to report.
	wantClean(t, f.step(1))

	// The balancer starts sending to server B.
	f.origin.SetBackend(b.URL)
	r = f.step(1)
	if r.Status != StatusOK {
		t.Errorf("a change of server alone must not degrade the status, got %s: %+v", r.Status, r.Problems)
	}
	p := wantProblem(t, r, ProbPlaylistSource, SevInfo)
	if !strings.Contains(p.Message, hostOf(b)) || !strings.Contains(p.Message, hostOf(a)) {
		t.Errorf("message %q must name both servers", p.Message)
	}
	wantProblem(t, r, ProbSegmentSource, SevInfo)
	if v, _ := metric(r, "hls_playlist_source_changed", VariantMedia); v != 1 {
		t.Errorf("hls_playlist_source_changed = %v, want 1", v)
	}
	if v, _ := metric(r, "hls_status", VariantMedia); v != 0 {
		t.Errorf("hls_status = %v, want 0", v)
	}
	if got := r.Sources[0].Playlist.Host; got != hostOf(b) {
		t.Errorf("playlist now from %s, want server B", got)
	}
	wantClean(t, f.step(1))

	// A failure names the server that produced it.
	f.origin.SetDown(http.StatusBadGateway)
	p = wantProblem(t, f.step(1), ProbPlaylistHTTP, SevCrit)
	if !strings.Contains(p.Message, hostOf(b)) {
		t.Errorf("message %q must name the failing server", p.Message)
	}
}

func TestEndpointHostDropsDefaultPort(t *testing.T) {
	for in, want := range map[string]string{
		"http://edge.example.com:80/a.m3u8":   "edge.example.com",
		"https://edge.example.com:443/a.m3u8": "edge.example.com",
		"http://edge.example.com:8080/a.m3u8": "edge.example.com:8080",
		"https://edge.example.com:80/a.m3u8":  "edge.example.com:80",
		"http://edge.example.com/a.m3u8":      "edge.example.com",
	} {
		e := endpoint(in, &httpx.Response{FinalURL: in, RemoteAddr: "10.0.0.4:80"})
		if e.Host != want || e.IP != "10.0.0.4" {
			t.Errorf("%s: host %q ip %q, want host %q", in, e.Host, e.IP, want)
		}
	}
}

// A configured interval longer than the playlist window would lose
// segments between checks; the checker shortens it as far as needed.
func TestConfiguredIntervalIsCappedByWindow(t *testing.T) {
	f := newFixture(t, hlstest.Options{Window: 4}, "/v0/index.m3u8", LevelPlaylist)
	f.c.t.IntervalSec = 60
	if got := f.c.Interval(); got != time.Minute {
		t.Fatalf("interval before the first cycle = %s, want the configured one", got)
	}
	wantClean(t, f.step(4))
	// Four 2-second segments: one may leave at any moment, three stay for
	// at least 6 seconds; polling twice in that time gives 3 seconds.
	if got := f.c.Interval(); got != 3*time.Second {
		t.Errorf("interval = %s, want 3s", got)
	}

	// A window long enough for the configured interval leaves it alone.
	g := newFixture(t, hlstest.Options{Window: 5}, "/v0/index.m3u8", LevelPlaylist)
	g.c.t.IntervalSec = 2
	wantClean(t, g.step(5))
	if got := g.c.Interval(); got != 2*time.Second {
		t.Errorf("interval = %s, want the configured 2s", got)
	}
}

// At header level only the beginning of each segment is fetched, yet the
// segment's existence, size, server and first timestamps are checked.
func TestHeaderLevel(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelHeader)
	r := f.step(4)
	wantClean(t, r)
	seg := r.Segments[0]
	if got := f.origin.RangeRequested("/v0/seg-3.ts"); got != "bytes=0-131599" {
		t.Fatalf("Range sent = %q, want the first 131600 bytes", got)
	}
	// Sample segments are 27-33 KB, smaller than the header window, so the
	// whole of them arrives; what matters is that the size is the full one.
	if !seg.Partial || seg.Status != 206 || seg.Size != seg.Fetched || seg.Size < 20000 {
		t.Fatalf("segment record = %+v", seg)
	}
	if seg.Media == nil || seg.Media.Video() == nil || seg.Media.Video().Width != 160 {
		t.Fatalf("the beginning of the segment was not analysed: %+v", seg.Media)
	}
	for i := range 6 {
		r = f.step(1)
		wantClean(t, r) // includes the announced discontinuity where the sample loop restarts
		if len(r.Segments) != 1 {
			t.Fatalf("cycle %d: %d segments", i, len(r.Segments))
		}
	}
	for _, name := range []string{"hls_segment_bitrate_bps", "hls_segment_ttfb_seconds", "hls_media_width", "hls_media_pts_gap_seconds", "hls_buffer_seconds"} {
		if _, ok := metric(r, name, VariantMedia); !ok {
			t.Errorf("metric %s missing at header level", name)
		}
	}
	for _, name := range []string{"hls_segment_total_seconds", "hls_segment_download_ratio", "hls_media_fps", "hls_media_gop_seconds", "hls_media_duration_diff_seconds", "hls_startup_seconds"} {
		if _, ok := metric(r, name, VariantMedia); ok {
			t.Errorf("metric %s needs the whole segment and must not be reported at header level", name)
		}
	}

	// A missing segment is noticed without downloading anything.
	f.origin.SetFault(f.origin.Last()+1, hlstest.Fault{Status: http.StatusNotFound})
	wantProblem(t, f.step(1), ProbSegmentHTTP, SevCrit)
	wantClean(t, f.step(1))
}

func TestHeaderLevelDetectsTimestampJump(t *testing.T) {
	f := newFixture(t, hlstest.Options{NoDiscontinuity: true}, "/v0/index.m3u8", LevelHeader)
	f.step(2)
	wantClean(t, f.step(1))
	// Segment 3 restarts the sample loop: the first timestamp is 6 s earlier
	// than the previous start plus its duration.
	p := wantProblem(t, f.step(1), ProbPTSDiscontinuity, SevWarn)
	if !strings.Contains(p.Message, "-6.000s") {
		t.Errorf("message = %q, want the -6.000s jump", p.Message)
	}
}

// A server that ignores Range sends the whole segment; the download is cut
// at the header window and the size comes from Content-Length.
func TestHeaderLevelWithoutRangeSupport(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelHeader)
	f.origin.SetNoRange(true)
	r := f.step(4)
	wantClean(t, r)
	seg := r.Segments[0]
	if seg.Status != 200 || seg.Size < 20000 || seg.Media == nil || seg.Media.Video() == nil {
		t.Fatalf("segment record = %+v", seg)
	}
	wantClean(t, f.step(1))
}

func TestTargetDurationChangeIsInformational(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelPlaylist)
	wantClean(t, f.step(4))
	f.c.vars[VariantMedia].targetDuration = 5 // as if the previous playlist had said 5
	r := f.step(1)
	wantProblem(t, r, ProbTargetDurChanged, SevInfo)
	if r.Status != StatusOK {
		t.Errorf("status = %s, want ok", r.Status)
	}
}

// Variants are compared by where their playlists end in time, never by
// media sequence numbers, which they may count independently.
func TestVariantEdgesApart(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	video := func(pdt time.Time) edge { return edge{ok: true, kind: KindVideo, pdt: pdt, targetDuration: 6} }

	apart, limit, ok := edgesApart([]edge{video(at), video(at.Add(4 * time.Second)), {ok: true, kind: KindAudio, pdt: at.Add(time.Hour), targetDuration: 6}}, 2)
	if !ok || apart != 4 || limit != 12 {
		t.Errorf("two video variants 4 s apart: got %v s, limit %v, ok %v; want 4, 12, true (audio is not compared)", apart, limit, ok)
	}
	if apart, limit, _ := edgesApart([]edge{video(at), video(at.Add(30 * time.Second))}, 2); apart <= limit {
		t.Errorf("30 s apart with 6 s segments must exceed the limit, got %v <= %v", apart, limit)
	}
	// Nothing to judge by: a variant without date-times, a failed one, a single one.
	for name, edges := range map[string][]edge{
		"no date-time": {video(at), video(time.Time{})},
		"one failed":   {video(at), {kind: KindVideo}},
		"single":       {video(at)},
	} {
		if _, _, ok := edgesApart(edges, 2); ok {
			t.Errorf("%s: must not be compared", name)
		}
	}
}

// Continuity counters must run on from one segment into the next.
func TestContinuityAcrossSegments(t *testing.T) {
	last := map[uint16]uint8{256: 15, 257: 4, 300: 9}
	if n, _, _, _ := ccBreaks(last, map[uint16]uint8{256: 0, 257: 5, 400: 7}); n != 0 {
		t.Errorf("counters that continue (with wrap) and a new PID: %d breaks, want 0", n)
	}
	n, pid, was, now := ccBreaks(last, map[uint16]uint8{256: 0, 257: 0, 300: 9})
	if n != 2 || pid != 257 || was != 4 || now != 0 {
		t.Errorf("breaks = %d, reported PID %d: %d after %d; want 2, PID 257: 0 after 4", n, pid, now, was)
	}
	if n, _, _, _ := ccBreaks(nil, map[uint16]uint8{256: 3}); n != 0 {
		t.Errorf("no previous segment: %d breaks, want 0", n)
	}
}

// The memory budget makes a download wait for room instead of piling up.
func TestBodyBudget(t *testing.T) {
	b := newBudget(100)
	ctx := context.Background()
	if got := b.acquire(ctx, 60); got != 60 {
		t.Fatalf("first reservation = %d, want 60", got)
	}
	done := make(chan int64)
	go func() { done <- b.acquire(ctx, 60) }()
	select {
	case <-done:
		t.Fatal("a reservation that does not fit must wait")
	case <-time.After(50 * time.Millisecond):
	}
	b.release(60)
	if got := <-done; got != 60 {
		t.Fatalf("after room was given back the reservation = %d, want 60", got)
	}
	b.release(60)

	// Larger than the whole budget: let through alone rather than never.
	if got := b.acquire(ctx, 500); got != 500 {
		t.Fatalf("an oversized reservation with nothing held = %d, want 500", got)
	}
	// Cancelled while waiting.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := b.acquire(cancelled, 10); got != 0 {
		t.Fatalf("a cancelled wait reserved %d, want 0", got)
	}
}

// Numbers that cannot be a continuation of the previous ones are a change
// of numbering, not thousands of lost segments.
func TestSequenceRenumbered(t *testing.T) {
	segs := []plSegment{{seq: 805737, seg: &m3u8.MediaSegment{Duration: 6}}, {seq: 805738, seg: &m3u8.MediaSegment{Duration: 6}}}
	for name, c := range map[string]struct {
		lastSeq, first, last uint64
		elapsed              float64
		otherServer, want    bool
	}{
		"far ahead of what 6 s could publish":   {20002, 805737, 805740, 6, false, true},
		"back, from another server":             {805737, 20000, 20002, 47, true, true},
		"far back on the same server":           {805737, 20000, 20002, 6, false, true},
		"a little back on the same server":      {805737, 805730, 805734, 6, false, false},
		"two segments lost":                     {805734, 805737, 805740, 6, false, false},
		"a long outage explains a long gap":     {805000, 805737, 805740, 4200, false, false},
		"the next segment, from another server": {805736, 805737, 805740, 6, true, false},
	} {
		if got := renumbered(c.lastSeq, c.first, c.last, segs, c.elapsed, c.otherServer); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}

	f := newFixture(t, hlstest.Options{Window: 3}, "/v0/index.m3u8", LevelSegments)
	f.step(3)
	f.origin.Publish(100000)
	f.clock = f.clock.Add(2 * time.Second)
	r := f.run()
	p := wantProblem(t, r, ProbSeqRenumbered, SevWarn)
	if !strings.Contains(p.Message, "numbering changed") {
		t.Errorf("message = %q", p.Message)
	}
	if _, ok := metric(r, "hls_segments_missed", VariantMedia); ok {
		t.Error("a change of numbering must not count lost segments")
	}
	for _, q := range r.Problems {
		if q.Kind == ProbSeqJump {
			t.Errorf("a change of numbering reported as a loss too: %s", q.Message)
		}
	}
}

// A monitor that cannot keep up measures its own delay. What it concludes
// from timing alone is left out; what it saw with its own eyes stays.
func TestOverloadedCheckerDoesNotBlameTheStream(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	overloaded := ""
	f.c.Overloaded = func(time.Time) string { return overloaded }
	f.step(4)

	overloaded = "processor"
	f.origin.SetFault(4, hlstest.Fault{Delay: 2100 * time.Millisecond})
	f.origin.SetFault(5, hlstest.Fault{Status: 404})
	r := f.step(2)
	if r.Overloaded != "processor" {
		t.Errorf("Overloaded = %q", r.Overloaded)
	}
	if p := hasProblem(r, ProbSegmentSlow); p != nil {
		t.Errorf("a slow download measured by an overloaded checker was reported: %+v", p)
	}
	wantProblem(t, r, ProbSegmentHTTP, SevCrit)
	if v, ok := metric(r, "hls_check_overloaded", ""); !ok || v != 1 {
		t.Errorf("hls_check_overloaded = %v, %v", v, ok)
	}

	// The same without overload is reported as before.
	overloaded = ""
	f.origin.SetFault(6, hlstest.Fault{Delay: 2100 * time.Millisecond})
	r = f.step(1)
	wantProblem(t, r, ProbSegmentSlow, SevWarn)
	if r.Overloaded != "" {
		t.Errorf("Overloaded = %q", r.Overloaded)
	}
}

// The policy of the process decides whether checks may go inside the
// monitor's own network; the test origin is on the loopback.
func TestPrivateNetworksPolicy(t *testing.T) {
	t.Cleanup(func() { _ = SetPrivateNetworks("same") })
	if err := SetPrivateNetworks("anything"); err == nil {
		t.Error("an unknown policy must be refused")
	}

	// By default a stream that was pointed inside is followed inside.
	f := newFixture(t, hlstest.Options{}, "/master.m3u8", LevelSegments)
	wantClean(t, f.step(4))

	// Under "deny" nothing inside is requested at all.
	_ = SetPrivateNetworks("deny")
	f = newFixture(t, hlstest.Options{}, "/master.m3u8", LevelSegments)
	r := f.step(4)
	p := wantProblem(t, r, ProbPlaylistUnreachable, SevCrit)
	if !strings.Contains(p.Message, "not a public one") {
		t.Errorf("the finding must say why: %q", p.Message)
	}
	if n := f.origin.Requests("/master.m3u8"); n != 0 {
		t.Errorf("the origin was requested %d times", n)
	}

	_ = SetPrivateNetworks("allow")
	f = newFixture(t, hlstest.Options{}, "/master.m3u8", LevelSegments)
	wantClean(t, f.step(4))
}

// A cycle that starts late looks only at the latest segment of each
// playlist: that is how a checker that fell behind catches up.
func TestCatchUpCycleIsShort(t *testing.T) {
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	f.c.CatchUp = true
	r := f.step(3)
	wantClean(t, r)
	if len(r.Segments) != 1 || r.Segments[0].Seq != 6 {
		t.Fatalf("segments checked = %+v, want the latest one only", r.Segments)
	}
	if v, _ := metric(r, "hls_segments_skipped", VariantMedia); v != 2 {
		t.Errorf("hls_segments_skipped = %v, want 2", v)
	}
	// Back on time, the next cycle is a full one and finds nothing wrong
	// with having skipped.
	f.c.CatchUp = false
	r = f.step(2)
	wantClean(t, r)
	if len(r.Segments) != 2 {
		t.Errorf("segments checked = %d, want 2", len(r.Segments))
	}
}

// Found by fuzzing: a tag without its attributes made the playlist parser
// panic, and the process with it.
func TestPlaylistThatCrashesTheParser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-MEDIA:0\n"))
	}))
	defer srv.Close()
	r := New(Target{ID: 1, URL: srv.URL + "/p.m3u8", Level: LevelSegments}, nil).Run(context.Background())
	p := wantProblem(t, r, ProbPlaylistParse, SevCrit)
	if !strings.Contains(p.Message, "parser failed") {
		t.Errorf("message = %q", p.Message)
	}
}

// Seen in production after every restart of the monitor: a balancer sends
// consecutive requests to two servers that number the same segments one
// apart. That is a change of server, not segments that changed.
func TestServersThatNumberSegmentsDifferently(t *testing.T) {
	playlist := func(firstSeq, firstSeg int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:%d\n", firstSeq)
		for i := range 4 {
			fmt.Fprintf(&b, "#EXTINF:6.0,\nseg-%d.ts\n", firstSeg+i)
		}
		return b.String()
	}
	var calls atomic.Int64
	var port string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.m3u8":
			// The balancer: the first server twice, then the second.
			host := "127.0.0.1"
			if calls.Add(1) > 2 {
				host = "localhost"
			}
			http.Redirect(w, r, "http://"+host+":"+port+"/node.m3u8?n="+strconv.FormatInt(calls.Load(), 10), http.StatusFound)
		case "/node.m3u8":
			n, _ := strconv.Atoi(r.URL.Query().Get("n"))
			// Both serve the same segments, one more each time; the
			// second server's numbers are one behind.
			seq := 100 + n
			if strings.HasPrefix(r.Host, "localhost") {
				seq--
			}
			_, _ = w.Write([]byte(playlist(seq, 500+n)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, port, _ = net.SplitHostPort(srv.Listener.Addr().String())

	c := New(Target{ID: 1, URL: srv.URL + "/live.m3u8", Level: LevelPlaylist}, nil)
	clock := time.Now()
	c.now = func() time.Time { return clock }
	var last *Result
	for range 4 {
		clock = clock.Add(6 * time.Second)
		last = c.Run(context.Background())
		if p := hasProblem(last, ProbSegmentChanged); p != nil {
			t.Fatalf("segments of another server were compared with the previous one's: %s", p.Message)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("balancer was asked %d times", calls.Load())
	}
	// The same server keeps being compared with itself.
	if hasProblem(last, ProbPlaylistSource) != nil {
		t.Error("the fourth check came from the same server as the third")
	}
}

func TestRedirectLinesAreReadable(t *testing.T) {
	hop := func(u, server string) httpx.Hop { return httpx.Hop{URL: u, Status: 302, Server: server} }
	got := hopLines([]httpx.Hop{
		hop("http://cdn.example/a.m3u8", ""),
		hop("http://n6.example/a.m3u8", "Streamer 26.10"),
		hop("http://n4.example:80/a.m3u8", "Streamer 26.09"),
		hop("http://n4.example:80/a.m3u8", "Streamer 26.09"),
		hop("http://n4.example:80/a.m3u8", "Streamer 26.09"),
	})
	want := []string{
		"HTTP 302 http://cdn.example/a.m3u8 → http://n6.example/a.m3u8",
		"HTTP 302 http://n6.example/a.m3u8 (Streamer 26.10) → http://n4.example:80/a.m3u8",
		"HTTP 302 http://n4.example:80/a.m3u8 (Streamer 26.09) → http://n4.example:80/a.m3u8 ×3",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if one := hopLines([]httpx.Hop{hop("http://a.example/x", "")}); len(one) != 1 || one[0] != "HTTP 302 http://a.example/x" {
		t.Errorf("a single redirect: %v", one)
	}
}

// What a check downloads is reported, so that the load the monitor puts
// on the network can be seen: little at the playlist level, the whole
// segments at the segment level.
func TestCheckTrafficIsReported(t *testing.T) {
	bytesOf := func(level Level) float64 {
		f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level)
		f.step(4)
		r := f.step(1)
		v, ok := metric(r, "hls_check_bytes", "")
		if !ok {
			t.Fatalf("%s: no hls_check_bytes", level)
		}
		return v
	}
	playlist, header, whole := bytesOf(LevelPlaylist), bytesOf(LevelHeader), bytesOf(LevelSegments)
	if playlist <= 0 || playlist > 2000 {
		t.Errorf("playlist level downloaded %v bytes: a playlist is a few hundred", playlist)
	}
	if whole < 10000 || whole <= playlist {
		t.Errorf("segment level downloaded %v bytes, playlist level %v", whole, playlist)
	}
	if header < playlist || header > whole {
		t.Errorf("header level downloaded %v bytes: between %v and %v expected", header, playlist, whole)
	}
}

// Regular checks record what the kernel knows of the connections they use.
func TestConnectionFiguresAreRecorded(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read on Linux only")
	}
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelSegments)
	f.step(4)
	r := f.step(1)
	rtt, ok := metric(r, "hls_tcp_rtt_seconds", VariantMedia)
	if !ok || rtt <= 0 || rtt > 1 {
		t.Errorf("hls_tcp_rtt_seconds = %v, %v", rtt, ok)
	}
	for _, name := range []string{"hls_tcp_out_of_order", "hls_tcp_retransmits"} {
		if v, ok := metric(r, name, VariantMedia); !ok || v < 0 {
			t.Errorf("%s = %v, %v", name, v, ok)
		}
	}
}
