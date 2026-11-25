package probe

import (
	"testing"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

// A playlist of sound that falls behind the picture is told, by the numbers
// of the segments where the playlists are either side of a discontinuity.
func TestSoundFallsBehindThePicture(t *testing.T) {
	for _, lv := range []level.Level{level.Playlist(), level.Container()} {
		f := newFixture(t, hlstest.Options{SoundApart: true, Variants: 1}, "/master.m3u8", lv)
		if n, _ := watch(f, time.Minute, problemkind.VariantsOutOfSync()); n != 0 {
			t.Fatalf("level %v: a stream in step was told %d times", lv, n)
		}
		// The sound stands still for four segments of the picture.
		for behind := uint64(1); behind <= 4; behind++ {
			f.origin.SoundBehind(behind)
			f.step(1)
		}
		n, cycles := watch(f, time.Minute, problemkind.VariantsOutOfSync())
		if n < cycles-stepRuns {
			t.Fatalf("level %v: sound 4 segments behind was told in %d of %d checks", lv, n, cycles)
		}
		p := hasProblem(f.step(1), problemkind.VariantsOutOfSync())
		if p == nil || p.Variant == variantkey.Master() {
			t.Fatalf("level %v: the problem must be of the playlist that fell behind: %+v", lv, p)
		}
		f.origin.SoundBehind(0)
		f.step(1)
		if n, _ := watch(f, time.Minute, problemkind.VariantsOutOfSync()); n != 0 {
			t.Errorf("level %v: told %d times after the sound caught up", lv, n)
		}
	}
}

// With the timestamps of both read on one timeline, they say how far apart
// the playlists are.
func TestSoundBehindByTimestamps(t *testing.T) {
	f := newFixture(t, hlstest.Options{SoundApart: true, Variants: 1, NoDiscontinuity: true}, "/master.m3u8", level.Container())
	f.c.th.VariantEdgeSpread = 0.5
	if n, _ := watch(f, time.Minute, problemkind.VariantsOutOfSync()); n != 0 {
		t.Fatalf("a stream in step was told %d times", n)
	}
	f.origin.SoundBehind(1)
	f.step(1)
	p := hasProblem(f.step(1), problemkind.VariantsOutOfSync())
	if p == nil || !p.Details.Has(detail.KeyRenditionOffset()) {
		t.Fatalf("sound a segment behind with half a segment allowed: %+v", p)
	}
}

func TestAnchorPlace(t *testing.T) {
	seg := func(d float64, disc bool) *m3u8.MediaSegment {
		return &m3u8.MediaSegment{Duration: d, Discontinuity: disc}
	}
	segs := []plSegment{}
	for i := range 4 {
		segs = append(segs, plSegment{seq: uint64(10 + i), seg: seg(6, false)})
	}
	if end, ok := (anchor{ok: true, seq: 11, pts: 100}).place(segs); !ok || end != 118 {
		t.Errorf("from segment 11 at 100 s through three of 6 s: %v, %v; want 118", end, ok)
	}
	if _, ok := (anchor{ok: true, seq: 5, pts: 100}).place(segs); ok {
		t.Error("a segment that left the playlist says nothing of its end")
	}
	segs[3].seg = seg(6, true)
	if _, ok := (anchor{ok: true, seq: 11, pts: 100}).place(segs); ok {
		t.Error("a discontinuity after the segment starts another timeline")
	}
}

// A playlist that dates its first segment alone still tells how far behind
// real time its end is.
func TestLatencyFromTheFirstDate(t *testing.T) {
	f := newFixture(t, hlstest.Options{DateFirstOnly: true, NoDiscontinuity: true, Variants: 2}, "/master.m3u8", level.Playlist())
	f.step(1)
	r := f.step(1)
	n := 0
	for _, m := range r.Metrics {
		if m.Name == metricname.LiveLatencySeconds() {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the lag behind real time was told for %d playlists, want 2", n)
	}
}
