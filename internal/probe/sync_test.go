package probe

import (
	"testing"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/hlstest"
)

// A playlist of sound that falls behind the picture is told, by the numbers
// of the segments where the playlists are either side of a discontinuity.
func TestSoundFallsBehindThePicture(t *testing.T) {
	for _, level := range []Level{LevelPlaylist, LevelContainer} {
		f := newFixture(t, hlstest.Options{SoundApart: true, Variants: 1}, "/master.m3u8", level)
		if n, _ := watch(f, time.Minute, ProbVariantsOutOfSync); n != 0 {
			t.Fatalf("level %v: a stream in step was told %d times", level, n)
		}
		// The sound stands still for four segments of the picture.
		for behind := uint64(1); behind <= 4; behind++ {
			f.origin.SoundBehind(behind)
			f.step(1)
		}
		n, cycles := watch(f, time.Minute, ProbVariantsOutOfSync)
		if n < cycles-stepRuns {
			t.Fatalf("level %v: sound 4 segments behind was told in %d of %d checks", level, n, cycles)
		}
		p := hasProblem(f.step(1), ProbVariantsOutOfSync)
		if p == nil || p.Variant == VariantMaster {
			t.Fatalf("level %v: the problem must be of the playlist that fell behind: %+v", level, p)
		}
		f.origin.SoundBehind(0)
		f.step(1)
		if n, _ := watch(f, time.Minute, ProbVariantsOutOfSync); n != 0 {
			t.Errorf("level %v: told %d times after the sound caught up", level, n)
		}
	}
}

// With the timestamps of both read on one timeline, they say how far apart
// the playlists are.
func TestSoundBehindByTimestamps(t *testing.T) {
	f := newFixture(t, hlstest.Options{SoundApart: true, Variants: 1, NoDiscontinuity: true}, "/master.m3u8", LevelContainer)
	f.c.th.VariantEdgeSpread = 0.5
	if n, _ := watch(f, time.Minute, ProbVariantsOutOfSync); n != 0 {
		t.Fatalf("a stream in step was told %d times", n)
	}
	f.origin.SoundBehind(1)
	f.step(1)
	p := hasProblem(f.step(1), ProbVariantsOutOfSync)
	if p == nil || p.Details["rendition_offset"] == nil {
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
