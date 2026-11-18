package probe

import (
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/media"
)

func TestClipGoingRound(t *testing.T) {
	// The test origin is itself a clip going round: three segments, over
	// and over. Nothing is decoded here; the container is enough.
	for _, level := range []Level{LevelContainer, LevelHeader} {
		f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level)
		var first, reported, cycles int
		for end := f.clock.Add(2 * time.Minute); f.clock.Before(end); cycles++ {
			if p := hasProblem(f.step(1), ProbPictureLoop); p != nil {
				if reported++; first == 0 {
					first = cycles
					if p.Severity != SevCrit || p.Details["distinct_keyframes"] == nil {
						t.Errorf("%s: problem = %+v", level, p)
					}
				}
			}
		}
		// Three segments to have seen every picture, five more to be
		// sure, and half a minute of it at the least.
		if first == 0 || first > 20 || reported < cycles-first-1 {
			t.Errorf("%s: a clip going round was first told at cycle %d and in %d of %d cycles", level, first, reported, cycles)
		}
	}

	// A stream said to show a still picture, or not to be looked at, is left alone.
	for _, setting := range []string{PictureStatic, PictureOff} {
		f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
		f.c.t.Picture = setting
		if n, _ := watch(f, 2*time.Minute, ProbPictureLoop); n != 0 {
			t.Errorf("picture %q: told as a clip going round %d times", setting, n)
		}
	}
}

func TestLivePicturesNeverComeRound(t *testing.T) {
	// A live encoder: every keyframe is new. Now and then one comes again
	// — a still caption — and that is not a clip.
	c := New(Target{ID: 1, URL: "http://x/", Level: LevelContainer}, nil)
	st := &variantState{}
	v := Variant{Key: "v"}
	at := time.Now()
	for i := 0; i < 400; i++ {
		at = at.Add(6 * time.Second)
		hashes := []uint64{uint64(1000 + 3*i), uint64(1001 + 3*i), uint64(1002 + 3*i)}
		if i%7 == 0 {
			hashes[1] = 42 // the same caption again
		}
		info := &media.Info{Tracks: []media.Track{{Kind: media.KindVideo, KeyHashes: hashes}}}
		c.noteKeyframes(v, st, info, at)
		if len(c.loops) != 0 {
			t.Fatalf("segment %d: live pictures were told to go round", i)
		}
	}
	if len(st.keys.seen) > 2*keyMemoryMax {
		t.Errorf("%d keyframes are remembered", len(st.keys.seen))
	}
}
