package probe

import (
	"time"

	"github.com/hotid/hls-monitor/internal/media"
)

// What an operator puts on air in place of a channel is most often a short
// clip going round: "the broadcast is interrupted for technical reasons",
// with something moving on it. It is not black and it does not stand
// still. But it is the same file over and over, so the coded bytes of its
// keyframes come round again exactly, and those of a live encoder never
// do: no two pictures are coded into the same bytes. That takes no
// decoding to see, only the container.

const (
	// loopSegments segments in a row whose every keyframe was seen before,
	// over at least loopSpan, are a clip going round.
	loopSegments = 5
	loopSpan     = 30 * time.Second
	// keyMemory is for how long, and keyMemoryMax for how many, keyframes
	// are remembered.
	keyMemory    = 10 * time.Minute
	keyMemoryMax = 512
)

// keyframes is what a playlist's keyframes are remembered by.
type keyframes struct {
	seen     map[uint64]time.Time
	repeated run
}

// noteKeyframes looks at the keyframes of a segment that was read, and
// tells whether the playlist shows the same pictures over and over.
func (c *Checker) noteKeyframes(v Variant, st *variantState, info *media.Info, at time.Time) {
	if c.t.Picture != PictureCheck || info == nil {
		return
	}
	video := info.Video()
	if video == nil || len(video.KeyHashes) == 0 {
		return
	}
	k := &st.keys
	if k.seen == nil {
		k.seen = map[uint64]time.Time{}
	}
	again := true
	for _, h := range video.KeyHashes {
		if _, ok := k.seen[h]; !ok {
			again = false
		}
	}
	for _, h := range video.KeyHashes {
		k.seen[h] = at
	}
	if len(k.seen) > keyMemoryMax {
		for h, t := range k.seen {
			if at.Sub(t) > keyMemory || len(k.seen) > 2*keyMemoryMax {
				delete(k.seen, h)
			}
		}
	}
	k.repeated.note(again, at)

	c.mu.Lock()
	defer c.mu.Unlock()
	if k.repeated.n >= loopSegments && at.Sub(k.repeated.since) >= loopSpan {
		if c.loops == nil {
			c.loops = map[string]standingProblem{}
		}
		// How many pictures there are to the clip: those seen since it began.
		pictures := 0
		for _, t := range k.seen {
			if !t.Before(k.repeated.since) {
				pictures++
			}
		}
		c.loops[v.Key] = standingProblem{at: at, kind: ProbPictureLoop, severity: SevCrit, variant: v.Key,
			message: "the same pictures go round and round",
			details: []any{"segments_in_a_row", k.repeated.n, "distinct_keyframes", pictures, "since", k.repeated.since.UTC().Format(time.RFC3339)}}
	} else {
		delete(c.loops, v.Key)
	}
}
