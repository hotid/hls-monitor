package probe

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/hotid/hls-monitor/internal/media"
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
	return kind == ProbPictureBlack || kind == ProbPictureFrozen || kind == ProbAudioSilent || kind == ProbPictureSlate || kind == ProbPictureLoop || kind == ProbAudioLoop
}

// Slate is a picture put on air in place of a channel — "the broadcast is
// interrupted for technical reasons" — as somebody marked it. One that
// moves has several frames.
type Slate struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Frames [][]byte `json:"frames"`
	// Keys are the coded frames of a clip, sorted: what it is known by
	// without decoding anything.
	Keys []uint64 `json:"keys,omitempty"`
	// Own tells that it is no slate but something a channel itself plays
	// over and over: known in order to be left alone.
	Own bool `json:"own,omitempty"`
}

// slateMoves is how much a picture may differ from a slate's frame, where
// it differs most, and still be that slate: more than the noise of coding
// alone, for every channel codes it anew, at its own size and rate.
const slateMoves = 14

// A slate with something to it — a test card, a notice in large letters —
// is also known when a small part of it differs as it likes: a clock on
// it, a sign that blinks. Apart from that part it may differ by
// slateMostlyMoves. A flat slate, dark all over, is not given that
// leave: any dark scene is flat and dark apart from a small part.
const (
	slateMostlyMoves = 8
	slateContrast    = 10.0
)

// slateOf returns the slate a picture is, if it is one.
func slateOf(slates []Slate, cells []byte) *Slate {
	for i := range slates {
		if slates[i].Own {
			continue
		}
		for _, frame := range slates[i].Frames {
			if picture.Moved(cells, frame) <= slateMoves ||
				(picture.Contrast(frame) >= slateContrast && picture.MostlyMoved(cells, frame) <= slateMostlyMoves) {
				return &slates[i]
			}
		}
	}
	return nil
}

// ValidPicture reports whether p is one of the settings above.
func ValidPicture(p string) bool {
	return p == PictureCheck || p == PictureStatic || p == PictureOff
}

const (
	// lookBytes is how much of a segment is fetched for a look where the
	// check reads only the beginning of segments: enough for the first
	// keyframe and a moment of sound. A whole number of MPEG-TS packets,
	// as headerBytes is: a packet cut in two reads as a loss of
	// synchronisation.
	lookBytes = 188 * 2800
	// lookAgain is how soon a look is repeated after one that found
	// something: a dark scene and a pause are over in a moment, a fault
	// is not. lookRuns such looks in a row, over at least lookSpan, make
	// a finding.
	lookAgain = 20 * time.Second
	lookRuns  = 3
	lookSpan  = time.Minute
	// lookStanding is how often a stream with a finding is looked at:
	// often enough to tell soon that it is over, and no oftener.
	lookStanding = time.Minute

	// A picture is black when it is this dark and has this share of
	// points that are all but black; sound is silence below this level.
	blackLuma   = 20.0
	blackShare  = 0.98
	silentBelow = -70.0
	// A picture stands still when no part of it changed by more than
	// this between two looks: the noise of coding and no more. Live
	// television that looks the same from one look to the next — a studio,
	// someone talking — moves by tens where the face is.
	stillMoves = 6
)

// Picture is what a look at a stream found: carried in the result to be
// shown, and kept with an incident as its evidence.
type Picture struct {
	Variant string    `json:"variant"`
	Time    time.Time `json:"time"`
	// JPEG is the picture, small; absent when the stream has none. Cells
	// is the same shrunk further, to compare pictures by.
	JPEG  []byte `json:"jpeg,omitempty"`
	Cells []byte `json:"cells,omitempty"`
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

	cells       []byte // the picture of the look before, shrunk
	black, same run
	silent      run
	// nothing counts looks that got neither a picture nor sound out of
	// the segment.
	nothing run
	// problems stand from one look to the next: a look is rare, and an
	// incident ends when its problem is not reported.
	problems []standingProblem
}

type standingProblem struct {
	// at is when it was last found to stand, for what is found by other
	// means than looks.
	at                               time.Time
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
// info is what reading the container of the same segment found, when it
// was read.
func (c *Checker) look(ctx context.Context, cy *cycle, v Variant, data, init []byte, info *media.Info, at time.Time) {
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
		// A segment nothing can be made of, look after look, is a stream
		// no player shows: scrambled, or garbage in a valid container.
		// One such look is a segment that happened to be cut short.
		if errors.Is(err, picture.ErrNothing) && ctx.Err() == nil {
			l.nothing.note(true, at)
			l.at = at.Add(min(lookAgain, c.PictureEvery) - c.PictureEvery)
			if l.nothing.standing(at) {
				l.at = at.Add(min(lookStanding, c.PictureEvery) - c.PictureEvery)
				details := []any{"decoder_says", err.Error(), "looks_in_a_row", l.nothing.n,
					"since", l.nothing.since.UTC().Format(time.RFC3339), "looked_at", at.UTC().Format(time.RFC3339)}
				// What the container says of the video narrows it down: a
				// track with packets in it and next to no pictures among
				// them is not video that failed to decode, it is not video.
				if video := videoOf(info); video != nil && video.Bytes > 0 {
					details = append(details, "video_pictures", video.Frames, "video_packets", video.Packets)
					if d := video.Duration(); d > 0 {
						details = append(details, "video_kbit_s", math.Round(float64(video.Bytes)*8/d/1000))
					}
				}
				l.problems = []standingProblem{{kind: ProbMediaUndecodable, severity: SevCrit, variant: v.Key,
					message: "neither the picture nor the sound can be decoded", details: details}}
			}
		}
		return
	}
	l.at = at
	l.nothing = run{}
	cy.picture(&Picture{Variant: v.Key, Time: at, JPEG: f.JPEG, Cells: f.Cells, HasPicture: f.HasPicture, Luma: round1(f.Luma), Dark: round3(f.Dark),
		HasAudio: f.HasAudio, AudioDB: round1(f.AudioDB)})

	l.problems = nil
	stand := func(kind, message string, details ...any) {
		l.problems = append(l.problems, standingProblem{kind: kind, severity: SevCrit, variant: v.Key, message: message,
			details: append(details, "looked_at", at.UTC().Format(time.RFC3339))})
	}
	suspect := false
	if f.HasPicture {
		black := f.Luma < blackLuma && f.Dark >= blackShare
		same := picture.Moved(l.cells, f.Cells) <= stillMoves
		l.black.note(black, at)
		l.same.note(same && c.t.Picture != PictureStatic, at)
		l.cells = f.Cells
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
		// A picture somebody marked as a slate is known at the first
		// look, and is not told a second time as one that does not change.
		var slate *Slate
		if c.Slates != nil {
			slate = slateOf(c.Slates(), f.Cells)
		}
		if slate != nil {
			stand(ProbPictureSlate, "the stream shows the slate \""+slate.Name+"\"", "slate", slate.Name)
		}
		// A black screen does not change either; it is told as black. A
		// dark notice with something blinking on it is black one look and
		// not the next, and is told as what it is: a picture that stays.
		if slate == nil && l.same.standing(at) && !l.black.standing(at) {
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
	switch {
	case len(l.problems) > 0:
		l.at = at.Add(min(lookStanding, c.PictureEvery) - c.PictureEvery)
	case suspect:
		// Look again soon: what it is shows in a minute, not in five.
		l.at = at.Add(min(lookAgain, c.PictureEvery) - c.PictureEvery)
	}
}

func videoOf(info *media.Info) *media.Track {
	if info == nil {
		return nil
	}
	return info.Video()
}

// reportLooks tells, at the end of every cycle, what the latest look found
// and still stands.
func (c *Checker) reportLooks(cy *cycle) {
	c.mu.Lock()
	standing := append([]standingProblem(nil), c.looks.problems...)
	slate := false
	for _, p := range standing {
		slate = slate || p.kind == ProbPictureSlate
	}
	// What the coded frames tell. A playlist whose segments are no
	// longer read says nothing.
	var clips, sounds []standingProblem
	for key, p := range c.loops {
		switch {
		case c.now().Sub(p.at) > knownFor:
			delete(c.loops, key)
		case p.kind == ProbAudioLoop:
			sounds = append(sounds, p)
		default:
			clips = append(clips, p)
		}
	}
	// What the sizes of coded frames tell is a sign; where a decoder looks
	// at the stream it is the decoder that tells, and for certain.
	var signs []standingProblem
	for key, p := range c.signs {
		switch {
		case c.now().Sub(p.at) > knownFor:
			delete(c.signs, key)
		case c.Looker == nil || c.PictureEvery <= 0:
			signs = append(signs, p)
		}
	}
	if !slate && len(clips) == 0 {
		standing = append(standing, signs...)
	}
	// A clip going round is told once: by the name of the slate when a
	// look knew it, and otherwise as what it is — not as a picture that
	// does not change as well, nor as sound that goes round with it.
	if slate || len(clips) > 0 {
		c.clipAt = c.now()
	}
	switch {
	case slate:
	case len(clips) > 0:
		kept := standing[:0]
		for _, p := range standing {
			if p.kind != ProbPictureFrozen {
				kept = append(kept, p)
			}
		}
		standing = append(kept, clips...)
	case c.now().Sub(c.ownAt) >= knownFor && c.now().Sub(c.clipAt) >= knownFor:
		// Not while the pictures went round a moment ago either: a clip
		// with a keyframe that was not seen before drops out for a
		// segment, and its sound does not.
		standing = append(standing, sounds...)
	}
	c.mu.Unlock()
	for _, p := range standing {
		cy.problem(p.kind, p.severity, p.variant, "%s", p.message)
		cy.detail(p.kind, p.variant, p.details...)
	}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
