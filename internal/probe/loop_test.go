package probe

import (
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/trackkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

func TestClipGoingRound(t *testing.T) {
	// The test origin is itself a clip going round: three segments, over
	// and over. Nothing is decoded here; the container is enough.
	for _, how := range []struct {
		name string
		lv   level.Level
		fmp4 bool
	}{{"container", level.Container(), false}, {"header", level.Header(), false}, {"fmp4", level.Container(), true}} {
		opt := hlstest.Options{FMP4: how.fmp4}
		f := newFixture(t, opt, "/v0/index.m3u8", how.lv)
		var first, reported, cycles int
		for end := f.clock.Add(2 * time.Minute); f.clock.Before(end); cycles++ {
			if p := hasProblem(f.step(1), problemkind.PictureLoop()); p != nil {
				if reported++; first == 0 {
					first = cycles
					if p.Severity != severity.Crit() || !p.Details.Has(detail.KeyDistinctKeyframes()) || len(p.Details.Hashes(detail.KeyClipSignature())) == 0 {
						t.Errorf("%s: problem = %+v", how.name, p)
					}
				}
			}
		}
		// Three segments to have seen every picture, five more to be
		// sure, and half a minute of it at the least.
		if first == 0 || first > 20 || reported < cycles-first-1 {
			t.Errorf("%s: a clip going round was first told at cycle %d and in %d of %d cycles", how.name, first, reported, cycles)
		}
	}

	// The sound of the clip goes round with it, and is not told apart.
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level.Container())
	if n, _ := watch(f, 3*time.Minute, problemkind.AudioLoop()); n != 0 {
		t.Errorf("the sound of a clip going round was told by itself %d times", n)
	}

	// A stream said to show a still picture, or not to be looked at, is left alone.
	for _, setting := range []picturemode.Mode{picturemode.Static(), picturemode.Off()} {
		f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level.Container())
		f.c.t.Picture = setting
		if n, _ := watch(f, 2*time.Minute, problemkind.PictureLoop()); n != 0 {
			t.Errorf("picture %q: told as a clip going round %d times", setting, n)
		}
	}
}

func TestLivePicturesNeverComeRound(t *testing.T) {
	// A live encoder: every keyframe is new. Now and then one comes again
	// — a still caption — and that is not a clip.
	c := New(Target{ID: id.StreamOf(1), URL: "http://x/", Level: level.Container(), Picture: picturemode.Check()}, nil)
	st := &variantState{}
	v := Variant{Key: variantkey.OfURI("v")}
	at := time.Now()
	for i := 0; i < 400; i++ {
		at = at.Add(6 * time.Second)
		hashes := []uint64{uint64(1000 + 3*i), uint64(1001 + 3*i), uint64(1002 + 3*i)}
		if i%7 == 0 {
			hashes[1] = 42 // the same caption again
		}
		info := &media.Info{Tracks: []media.Track{{Kind: trackkind.Video(), KeyHashes: hashes}}}
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
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level.Container())
	for i := 0; i < 30; i++ {
		if p := hasProblem(f.step(1), problemkind.PictureLoop()); p != nil {
			return p.Details.Hashes(detail.KeyClipSignature())
		}
	}
	t.Fatal("the clip was never told to go round")
	return nil
}

func TestKnownClip(t *testing.T) {
	keys := clipSignature(t)

	// A clip marked as a slate is told at once, by its name.
	f := newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level.Container())
	f.c.Slates = func() []Slate { return []Slate{{ID: id.SlateOf(1), Name: "No signal", Keys: keys}} }
	p := hasProblem(f.step(1), problemkind.PictureSlate())
	if p == nil || p.Details.Text(detail.KeySlate()) != "No signal" || p.Severity != severity.Crit() {
		t.Fatalf("a known clip at the first segment: %+v", p)
	}
	if n, cycles := watch(f, 2*time.Minute, problemkind.PictureSlate()); n != cycles {
		t.Errorf("a known clip was told in %d of %d cycles", n, cycles)
	}
	if hasProblem(f.step(1), problemkind.PictureLoop()) != nil || hasProblem(f.step(1), problemkind.AudioLoop()) != nil {
		t.Error("a known clip is told as a clip going round as well")
	}

	// One known to be the channel's own is left alone, sound and all.
	f = newFixture(t, hlstest.Options{}, "/v0/index.m3u8", level.Container())
	f.c.Slates = func() []Slate { return []Slate{{ID: id.SlateOf(1), Name: "Their own", Keys: keys, Own: true}} }
	for end := f.clock.Add(3 * time.Minute); f.clock.Before(end); {
		r := f.step(1)
		for _, kind := range []problemkind.Kind{problemkind.PictureLoop(), problemkind.PictureSlate(), problemkind.AudioLoop()} {
			if hasProblem(r, kind) != nil {
				t.Fatalf("the channel's own clip was told as %s", kind)
			}
		}
		if r.OwnClip != "Their own" {
			t.Fatalf("the stream is said to play %q of its own", r.OwnClip)
		}
	}
}

func TestSoundGoingRound(t *testing.T) {
	c := New(Target{ID: id.StreamOf(1), URL: "http://x/", Level: level.Container(), Picture: picturemode.Static()}, nil)
	st := &variantState{}
	v := Variant{Key: variantkey.OfURI("a")}
	at := time.Now()
	segment := func(from uint64, silent bool) *media.Info {
		h := make([]uint64, 32)
		for i := range h {
			if h[i] = from + uint64(i); silent {
				h[i] = 7
			}
		}
		return &media.Info{Tracks: []media.Track{{Kind: trackkind.Audio(), SoundHashes: h}}}
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
	p, ok := c.loops[standKey{variant: variantkey.OfURI("a"), sound: true}]
	if !ok || p.kind != problemkind.AudioLoop() || p.severity != severity.Warn() {
		t.Fatalf("sound going round: %+v", c.loops)
	}
}
