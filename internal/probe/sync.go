package probe

import (
	"math"
	"time"
)

// anchor is where one segment of a playlist starts on the timeline of its
// media: what tells where the playlist itself has got to.
type anchor struct {
	ok   bool
	seq  uint64
	pts  float64
	wrap float64
}

// inStep remembers how a playlist stood against the picture.
type inStep struct {
	// once tells that the timestamps were once together: they are on one
	// timeline, and a difference is a difference.
	once bool
	// runs counts the checks in a row that found the playlist apart.
	runs int

	// By numbers: where the playlist's last segment stood against the
	// picture's when first seen, of which count of each, and how far it
	// has moved from there and since when.
	haveBase bool
	base     int64
	epochs   [2]int
	moved    int64
	movedAt  time.Time
}

const (
	// pcrGapLimit is how long MPEG-TS may go without a program clock
	// reference: TR 101 290 asks for 40 ms and takes 100 ms for an error.
	pcrGapLimit = 0.1
	// stepRuns checks in a row must find a playlist apart: the playlists of
	// one check are not read at one moment.
	stepRuns = 2
	// ownTimeline is how far apart timestamps are taken to be of timelines
	// that were never one, when they were never seen together.
	ownTimeline = 300.0
	// stepSegments is how many segments a playlist may move against the
	// picture by their numbers.
	stepSegments = 3
	// stepSettled is how long a playlist must keep its new place by the
	// numbers for that place to be taken as where it belongs.
	stepSettled = time.Hour
)

// place tells where the end of a playlist is on the timeline of its media:
// from the last segment whose timestamps were read on through the lengths
// the playlist gives for those after it. A discontinuity after that segment
// starts a timeline nothing was read of.
func (a anchor) place(segs []plSegment) (float64, bool) {
	if !a.ok {
		return 0, false
	}
	at := -1
	for i, s := range segs {
		if s.seq == a.seq {
			at = i
		}
	}
	if at < 0 {
		return 0, false
	}
	end := a.pts
	for i := at; i < len(segs); i++ {
		if i > at && segs[i].seg.Discontinuity {
			return 0, false
		}
		end += segs[i].seg.Duration
	}
	return end, true
}

// renditionsInStep compares where every playlist of picture and of sound
// has got to with the first playlist of picture. A player takes them
// together, and one that fell behind has nothing to play next to the other.
//
// The timestamps in the segments say where a playlist is. Where they were
// not read, or the playlists are either side of a discontinuity, the
// numbers of the segments are left: they need not be one count (RFC 8216,
// section 6.3.2), but the distance between two counts does not change while
// both go on.
func (c *Checker) renditionsInStep(cy *cycle, edges []edge) {
	var ref *edge
	for i := range edges {
		if e := &edges[i]; e.ok && e.live && e.kind == KindVideo {
			ref = e
			break
		}
	}
	seen := map[string]bool{}
	now := c.now()
	for i := range edges {
		e := &edges[i]
		if ref == nil || e == ref || !e.ok || !e.live || e.kind == KindSubtitles {
			continue
		}
		seen[e.key] = true
		if c.steps == nil {
			c.steps = map[string]*inStep{}
		}
		s := c.steps[e.key]
		if s == nil {
			s = &inStep{}
			c.steps[e.key] = s
		}
		limit := c.th.VariantEdgeSpread * max(ref.targetDuration, e.targetDuration)
		apart := false
		var report func()
		if ref.hasMedia && e.hasMedia && ref.disc == e.disc {
			d := wrapDiff(e.media-ref.media, max(ref.wrap, e.wrap))
			if math.Abs(d) <= limit {
				s.once = true
			}
			if s.once || math.Abs(d) < ownTimeline {
				cy.metric("hls_variant_offset_seconds", e.key, d)
				if math.Abs(d) > limit {
					apart = true
					report = func() {
						how := "behind"
						if d > 0 {
							how = "ahead of"
						}
						cy.problem(ProbVariantsOutOfSync, SevWarn, e.key, "the playlist is %.1fs %s the picture (%s) by the timestamps in the segments", math.Abs(d), how, ref.key)
						cy.detail(ProbVariantsOutOfSync, e.key, "rendition_offset", d, "rendition_offset_limit", limit, "compared_with", ref.key)
					}
				}
			}
		} else {
			d := int64(e.seq) - int64(ref.seq)
			epochs := [2]int{ref.epoch, e.epoch}
			moved := d - s.base
			switch {
			case !s.haveBase || s.epochs != epochs:
				s.haveBase, s.base, s.epochs, s.moved = true, d, epochs, 0
			case moved > -stepSegments && moved < stepSegments:
				s.moved = 0
			case moved == s.moved && now.Sub(s.movedAt) >= stepSettled:
				s.base, s.moved = d, 0
			default:
				if moved != s.moved {
					s.moved, s.movedAt = moved, now
				}
				apart = true
				report = func() {
					how := "fell behind"
					if moved > 0 {
						how = "ran ahead of"
					}
					cy.problem(ProbVariantsOutOfSync, SevWarn, e.key, "the playlist %s the picture (%s) by %d segments, counting by their numbers", how, ref.key, abs64(moved))
					cy.detail(ProbVariantsOutOfSync, e.key, "segments_moved", moved, "segments_moved_limit", stepSegments, "compared_with", ref.key)
				}
			}
		}
		if !apart {
			s.runs = 0
			continue
		}
		if s.runs++; s.runs >= stepRuns {
			report()
		}
	}
	for k := range c.steps {
		if !seen[k] {
			delete(c.steps, k)
		}
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
