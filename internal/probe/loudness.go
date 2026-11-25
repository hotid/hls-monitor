package probe

import (
	"context"
	"math"
	"time"

	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

// Loudness by EBU R 128 is a property of minutes of sound, and a look
// hears two seconds of it: a pause, a quiet line or a loud one. So it is
// measured on its own, every loudEvery: three segments some loudGap apart,
// two seconds of each, taken together as R 128 takes its blocks — their
// energy averaged, what is near silence or far below the rest left out.
const (
	loudEvery = 30 * time.Minute
	loudTimes = 3
	// loudGap is the least time between two of the segments, over the
	// segments of a few seconds that come in between.
	loudGap = 15 * time.Second
	// loudGiveUp is how long the three are waited for: a stream that
	// stopped meanwhile is measured by what was heard.
	loudGiveUp = 3 * time.Minute
	// loudFloor and loudRelative are the gates of EBU R 128.
	loudFloor    = -70.0
	loudRelative = 10.0
)

type loudState struct {
	// due is when the next measuring begins; zero before the first.
	due time.Time
	// While measuring: the playlist it is done on, the segments still to
	// take, when the next may be taken and when it began.
	key         variantkey.Key
	left        int
	next, begun time.Time
	heard       []float64
}

// wantsLoud tells whether the segment about to be fetched for a variant
// is to be measured for loudness. The sound is measured where it is: in
// the playlist of sound when the stream has one, with the picture when not.
func (c *Checker) wantsLoud(v Variant, encrypted bool, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := &c.loud
	if c.Looker == nil || c.PictureEvery <= 0 || c.t.Picture == picturemode.Off() || c.t.Level == level.Playlist() || encrypted {
		return false
	}
	if l.due.IsZero() {
		// Streams start together; their first measuring is spread out.
		l.due = now.Add(freshJitter(loudEvery) / 6)
	}
	if l.left == 0 {
		if now.Before(l.due) || !c.carriesSound(v) {
			return false
		}
		l.key, l.left, l.next, l.begun, l.heard = v.Key, loudTimes, now, now, nil
	}
	if v.Key != l.key || now.Before(l.next) {
		return false
	}
	l.left--
	l.next = now.Add(loudGap)
	return true
}

// carriesSound tells whether the sound of the stream is to be measured in
// the segments of v.
func (c *Checker) carriesSound(v Variant) bool {
	switch v.Kind {
	case variantkind.Audio():
		return true
	case variantkind.Video(), variantkind.Media():
		return !c.soundApart
	}
	return false
}

// measureLoud measures a segment taken by wantsLoud and, with the last of
// the three, tells the loudness.
func (c *Checker) measureLoud(ctx context.Context, cy *cycle, v Variant, data, init []byte, ok bool, now time.Time) {
	if ok {
		if len(init) > 0 {
			data = append(append(make([]byte, 0, len(init)+len(data)), init...), data...)
		}
		if f, err := c.Looker.Listen(ctx, data); err == nil && f.HasLoudness {
			c.mu.Lock()
			c.loud.heard = append(c.loud.heard, f.Loudness)
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loud.left == 0 {
		c.finishLoud(cy, v.Key, now)
	}
}

// finishLoud tells what the measuring heard and sets the next one.
func (c *Checker) finishLoud(cy *cycle, key variantkey.Key, now time.Time) {
	l := &c.loud
	if lufs, ok := gatedLoudness(l.heard); ok {
		cy.metric(metricname.AudioLoudnessLufs(), key, math.Round(lufs*10)/10)
	}
	l.left, l.heard = 0, nil
	l.due = now.Add(loudEvery)
}

// checkLoudGiveUp ends a measuring that has waited too long for its
// segments: the playlist went away or stopped.
func (c *Checker) checkLoudGiveUp(cy *cycle, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loud.left > 0 && now.Sub(c.loud.begun) > loudGiveUp {
		c.finishLoud(cy, c.loud.key, now)
	}
}

// gatedLoudness takes measures of loudness together the way EBU R 128
// takes its blocks: the mean of their energy, without those below the
// absolute gate and then those more than the relative gate below the rest.
func gatedLoudness(ms []float64) (float64, bool) {
	mean := func(above float64) (float64, bool) {
		sum, n := 0.0, 0
		for _, v := range ms {
			if v > above {
				sum += math.Pow(10, v/10)
				n++
			}
		}
		if n == 0 {
			return 0, false
		}
		return 10 * math.Log10(sum/float64(n)), true
	}
	ungated, ok := mean(loudFloor)
	if !ok {
		return 0, false
	}
	return mean(ungated - loudRelative)
}
