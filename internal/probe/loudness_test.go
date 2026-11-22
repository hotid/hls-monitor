package probe

import (
	"math"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/picture"
)

func TestGatedLoudness(t *testing.T) {
	if v, ok := gatedLoudness([]float64{-22, -24, -55}); !ok || math.Abs(v+22.9) > 0.1 {
		t.Errorf("two at -23 and a pause: %.2f, %v", v, ok)
	}
	if _, ok := gatedLoudness([]float64{-90, -80}); ok {
		t.Error("silence has a loudness")
	}
}

// Every half hour three segments some seconds apart are measured, and the
// loudness told once, of the three together.
func TestLoudnessIsMeasuredOnThreeSegments(t *testing.T) {
	f := newFixture(t, hlstest.Options{Variants: 1}, "/master.m3u8", LevelHeader)
	s := &stubLooker{frame: liveFrame, heard: &picture.Frame{HasAudio: true, AudioDB: -20, Loudness: -23, HasLoudness: true}}
	f.c.Looker, f.c.PictureEvery = s, 5*time.Minute
	f.c.loud.due = time.Time{}
	told, at := 0, []time.Time{}
	for end := f.clock.Add(70 * time.Minute); f.clock.Before(end); {
		before := s.listens
		r := f.step(1)
		if s.listens > before {
			at = append(at, f.clock)
		}
		for _, m := range r.Metrics {
			if m.Name == "hls_audio_loudness_lufs" {
				told++
				if m.Value != -23 {
					t.Errorf("told %v", m.Value)
				}
			}
		}
	}
	if told < 2 || told > 3 || len(at) != 3*told {
		t.Fatalf("told %d times from %d segments", told, len(at))
	}
	for i := 1; i < 3; i++ {
		if gap := at[i].Sub(at[i-1]); gap < loudGap || gap > 2*loudGap {
			t.Errorf("segments %d and %d of a measuring are %s apart", i, i+1, gap)
		}
	}
	if gap := at[3].Sub(at[2]); gap < loudEvery-time.Minute {
		t.Errorf("the next measuring began %s after the last", gap)
	}
}
