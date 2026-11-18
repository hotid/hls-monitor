package probe

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/hotid/hls-monitor/internal/picture"
)

// A stream whose playlists and segments are all in order may still show
// nothing: a black screen, a picture that stopped, silence. Every so often
// a checker has one picture of a segment decoded, and a little of its
// sound, and judges by what a few such looks in a row found.

// Looker decodes a picture and some sound out of a segment.
type Looker interface {
	Sample(ctx context.Context, data []byte) (*picture.Frame, error)
	// Busy tells that a look asked for now would have to wait.
	Busy() bool
}

// What a stream's picture is checked for (Target.Picture).
const (
	PictureCheck  = ""       // everything
	PictureStatic = "static" // a picture that does not change is what it shows: radio, a test card
	PictureOff    = "off"    // not looked at
)

// PictureProblem reports whether a problem is found by looking at the
// picture or listening to the sound.
func PictureProblem(kind string) bool {
	return kind == ProbPictureBlack || kind == ProbPictureFrozen || kind == ProbAudioSilent
}

// ValidPicture reports whether p is one of the settings above.
func ValidPicture(p string) bool {
	return p == PictureCheck || p == PictureStatic || p == PictureOff
}

const (
	// lookBytes is how much of a segment is fetched for a look where the
	// check reads only the beginning of segments: enough for the first
	// keyframe and a moment of sound.
	lookBytes = 512 << 10
	// lookAgain is how soon a look is repeated after one that found
	// something: a dark scene and a pause are over in a moment, a fault
	// is not. lookRuns such looks in a row, over at least lookSpan, make
	// a finding.
	lookAgain = 20 * time.Second
	lookRuns  = 3
	lookSpan  = time.Minute

	// A picture is black when it is this dark and has this share of
	// points that are all but black; sound is silence below this level.
	blackLuma   = 20.0
	blackShare  = 0.98
	silentBelow = -70.0
	// Two pictures are the same when their hashes differ in this many
	// bits at most and their brightness by this much.
	sameBits = 2
	sameLuma = 2.0
)

// Picture is what a look at a stream found: carried in the result to be
// shown, and kept with an incident as its evidence.
type Picture struct {
	Variant string    `json:"variant"`
	Time    time.Time `json:"time"`
	// JPEG is the picture, small; absent when the stream has none.
	JPEG []byte `json:"jpeg,omitempty"`
	// Luma is the mean brightness, 0 to 255; Dark the share of points
	// that are all but black.
	HasPicture bool    `json:"has_picture"`
	Luma       float64 `json:"luma"`
	Dark       float64 `json:"dark"`
	// AudioDB is the level of the sound in decibels below full scale.
	HasAudio bool    `json:"has_audio"`
	AudioDB  float64 `json:"audio_db"`
}

// run counts looks in a row that found the same thing.
type run struct {
	n     int
	since time.Time
}

func (r *run) note(found bool, at time.Time) {
	switch {
	case !found:
		*r = run{}
	case r.n == 0:
		*r = run{n: 1, since: at}
	default:
		r.n++
	}
}

// standing tells that the run is long enough to be a finding.
func (r *run) standing(at time.Time) bool {
	return r.n >= lookRuns && at.Sub(r.since) >= lookSpan
}

// looks is what a checker remembers between looks.
type looks struct {
	at time.Time // when the stream was last looked at; zero before the first turn
	// taken is set once a segment was picked for the look of this cycle.
	due, taken bool

	have        bool // a picture was seen before
	hash        uint64
	luma        float64
	black, same run
	silent      run
	// problems stand from one look to the next: a look is rare, and an
	// incident ends when its problem is not reported.
	problems []standingProblem
}

type standingProblem struct {
	kind, severity, variant, message string
	details                          []any
}

// beginLook decides at the start of a cycle whether this one looks.
func (c *Checker) beginLook(start time.Time) {
	l := &c.looks
	l.due, l.taken = false, false
	if c.Looker == nil || c.PictureEvery <= 0 || c.t.Picture == PictureOff || c.t.Level == LevelPlaylist {
		l.problems = nil
		return
	}
	if l.at.IsZero() {
		// Streams start together; their first looks are spread over the period.
		l.at = start.Add(freshJitter(c.PictureEvery) - c.PictureEvery)
	}
	if start.Sub(l.at) < c.PictureEvery {
		return
	}
	// Decoding costs processor time: a monitor that cannot keep up, or a
	// decoder with a queue, puts the look off rather than fall behind.
	if c.CatchUp || c.Looker.Busy() || (c.Overloaded != nil && c.Overloaded(start.Add(-c.PictureEvery)) != "") {
		return
	}
	l.due = true
}

// wantsLook tells whether the segment about to be fetched for a variant is
// the one to look at: the first one of a cycle that is due, of a playlist
// that carries video or is the stream itself.
func (c *Checker) wantsLook(v Variant) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := &c.looks
	if !l.due || l.taken || (v.Kind != KindVideo && v.Kind != KindMedia) {
		return false
	}
	l.taken = true
	return true
}

// look has a segment decoded and judges what it shows.
func (c *Checker) look(ctx context.Context, cy *cycle, v Variant, data, init []byte, at time.Time) {
	if len(init) > 0 {
		data = append(append(make([]byte, 0, len(init)+len(data)), init...), data...)
	}
	f, err := c.Looker.Sample(ctx, data)
	c.mu.Lock()
	defer c.mu.Unlock()
	l := &c.looks
	if err != nil {
		// Nothing to look at this time: too little of the segment, or
		// not decodable. The next turn comes at the usual time, and what
		// stood before stands.
		if errors.Is(err, picture.ErrNothing) || errors.Is(err, picture.ErrNoDecoder) || ctx.Err() != nil {
			l.at = at
		}
		return
	}
	l.at = at
	cy.picture(&Picture{Variant: v.Key, Time: at, JPEG: f.JPEG, HasPicture: f.HasPicture, Luma: round1(f.Luma), Dark: round3(f.Dark),
		HasAudio: f.HasAudio, AudioDB: round1(f.AudioDB)})

	l.problems = nil
	stand := func(kind, message string, details ...any) {
		l.problems = append(l.problems, standingProblem{kind: kind, severity: SevCrit, variant: v.Key, message: message,
			details: append(details, "looked_at", at.UTC().Format(time.RFC3339))})
	}
	suspect := false
	if f.HasPicture {
		black := f.Luma < blackLuma && f.Dark >= blackShare
		same := l.have && picture.Distance(l.hash, f.Hash) <= sameBits && math.Abs(l.luma-f.Luma) <= sameLuma
		l.black.note(black, at)
		// A black screen does not change either; it is told as black.
		l.same.note(same && !black && c.t.Picture != PictureStatic, at)
		l.have, l.hash, l.luma = true, f.Hash, f.Luma
		cy.metric("hls_picture_luma", v.Key, f.Luma)
		changed := 1.0
		if same {
			changed = 0
		}
		cy.metric("hls_picture_changed", v.Key, changed)
		suspect = suspect || l.black.n > 0 || l.same.n > 0
		if l.black.standing(at) {
			stand(ProbPictureBlack, "the picture is black", "picture_luma", round1(f.Luma), "picture_dark_share", round3(f.Dark),
				"looks_in_a_row", l.black.n, "since", l.black.since.UTC().Format(time.RFC3339))
		}
		if l.same.standing(at) {
			stand(ProbPictureFrozen, "the picture does not change", "looks_in_a_row", l.same.n+1,
				"since", l.same.since.UTC().Format(time.RFC3339))
		}
	}
	if f.HasAudio {
		l.silent.note(f.AudioDB < silentBelow, at)
		cy.metric("hls_audio_level_db", v.Key, f.AudioDB)
		suspect = suspect || l.silent.n > 0
		if l.silent.standing(at) {
			stand(ProbAudioSilent, "the sound is silence", "audio_level_db", round1(f.AudioDB),
				"looks_in_a_row", l.silent.n, "since", l.silent.since.UTC().Format(time.RFC3339))
		}
	}
	if suspect {
		// Look again soon: what it is shows in a minute, not in five.
		l.at = at.Add(lookAgain - c.PictureEvery)
	}
}

// reportLooks tells, at the end of every cycle, what the latest look found
// and still stands.
func (c *Checker) reportLooks(cy *cycle) {
	c.mu.Lock()
	standing := append([]standingProblem(nil), c.looks.problems...)
	c.mu.Unlock()
	for _, p := range standing {
		cy.problem(p.kind, p.severity, p.variant, "%s", p.message)
		cy.detail(p.kind, p.variant, p.details...)
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
