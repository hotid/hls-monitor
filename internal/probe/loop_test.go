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
	for _, level := range []Level{LevelContainer, LevelHeader, "fmp4"} {
		opt := hlstest.Options{}
		if level == "fmp4" {
			opt.FMP4, level = true, LevelContainer
		}
		f := newFixture(t, opt, "/v0/index.m3u8", level)
		var first, reported, cycles int
		for end := f.clock.Add(2 * time.Minute); f.clock.Before(end); cycles++ {
			if p := hasProblem(f.step(1), ProbPictureLoop); p != nil {
				if reported++; first == 0 {
					first = cycles
					if p.Severity != SevCrit || p.Details["distinct_keyframes"] == nil || len(ParseSignature(p.Details["clip_signature"].(string))) == 0 {
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
		c.noteCoded(v, st, info, at)
		if len(c.loops) != 0 {
			t.Fatalf("segment %d: live pictures were told to go round", i)
		}
	}
	if len(st.keys.seen) > 2*keyMemoryMax {
		t.Errorf("%d keyframes are remembered", len(st.keys.seen))
	}
}

// clipSignature runs the test origin's clip until it is told to go round,
// and returns what it is known by.
func clipSignature(t *testing.T) []uint64 {
	t.Helper()
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
	for i := 0; i < 30; i++ {
		if p := hasProblem(f.step(1), ProbPictureLoop); p != nil {
			return ParseSignature(p.Details["clip_signature"].(string))
		}
	}
	t.Fatal("the clip was never told to go round")
	return nil
}

func TestKnownClip(t *testing.T) {
	keys := clipSignature(t)

	// A clip marked as a slate is told at once, by its name.
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
	f.c.Slates = func() []Slate { return []Slate{{ID: 1, Name: "No signal", Keys: keys}} }
	p := hasProblem(f.step(1), ProbPictureSlate)
	if p == nil || p.Details["slate"] != "No signal" || p.Severity != SevCrit {
		t.Fatalf("a known clip at the first segment: %+v", p)
	}
	if n, cycles := watch(f, 2*time.Minute, ProbPictureSlate); n != cycles {
		t.Errorf("a known clip was told in %d of %d cycles", n, cycles)
	}
	if hasProblem(f.step(1), ProbPictureLoop) != nil || hasProblem(f.step(1), ProbAudioLoop) != nil {
		t.Error("a known clip is told as a clip going round as well")
	}

	// One known to be the channel's own is left alone, sound and all.
	f = newFixture(t, hlstest.Options{}, "/v0/index.m3u8", LevelContainer)
	f.c.Slates = func() []Slate { return []Slate{{ID: 1, Name: "Their own", Keys: keys, Own: true}} }
	for end := f.clock.Add(3 * time.Minute); f.clock.Before(end); {
		r := f.step(1)
		for _, kind := range []string{ProbPictureLoop, ProbPictureSlate, ProbAudioLoop} {
			if hasProblem(r, kind) != nil {
				t.Fatalf("the channel's own clip was told as %s", kind)
			}
		}
	}
}

func TestSoundGoingRound(t *testing.T) {
	c := New(Target{ID: 1, URL: "http://x/", Level: LevelContainer, Picture: PictureStatic}, nil)
	st := &variantState{}
	v := Variant{Key: "a"}
	at := time.Now()
	segment := func(from uint64, silent bool) *media.Info {
		h := make([]uint64, 32)
		for i := range h {
			if h[i] = from + uint64(i); silent {
				h[i] = 7
			}
		}
		return &media.Info{Tracks: []media.Track{{Kind: media.KindAudio, SoundHashes: h}}}
	}
	// Live sound, then silence: neither goes round.
	for i := 0; i < 60; i++ {
		at = at.Add(6 * time.Second)
		c.noteCoded(v, st, segment(uint64(1000*i), i > 30), at)
		if len(c.loops) != 0 {
			t.Fatalf("segment %d: told to go round", i)
		}
	}
	// A jingle of four segments, over and over.
	for i := 0; i < 16; i++ {
		at = at.Add(6 * time.Second)
		c.noteCoded(v, st, segment(uint64(1_000_000+1000*(i%4)), false), at)
	}
	p, ok := c.loops["a"+soundKey]
	if !ok || p.kind != ProbAudioLoop || p.severity != SevWarn {
		t.Fatalf("sound going round: %+v", c.loops)
	}
}
