package probe

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/media"
)

// What an operator puts on air in place of a channel is most often a short
// clip going round: "the broadcast is interrupted for technical reasons",
// with something moving on it. It is not black and it does not stand
// still. But it is the same file over and over, so the coded bytes of its
// keyframes come round again exactly, and those of a live encoder never
// do: no two pictures are coded into the same bytes. That takes no
// decoding to see, only the container. The same holds for sound, frame by
// frame — apart from silence, which is coded the same every time.
//
// The frames a clip is made of are also what it is known by afterwards:
// somebody who saw it says what it is — a slate, or something the channel
// itself shows over and over, which is its own business — and a stream
// that plays it again is told at the first segment, or left alone.

const (
	// loopSegments segments in a row whose every frame was seen before,
	// over at least loopSpan, are a clip going round.
	loopSegments = 5
	loopSpan     = 30 * time.Second
	// keyMemory is for how long, and keyMemoryMax and soundMemoryMax for
	// how many, keyframes and frames of sound are remembered.
	keyMemory      = 10 * time.Minute
	keyMemoryMax   = 512
	soundMemoryMax = 4096
	// SignatureMax is how many frames a clip is known by at the most.
	SignatureMax = 256
	// knownFor is for how long a playlist that played a known clip is
	// taken to play it still: a long clip is known by a part of its frames.
	knownFor = 2 * time.Minute
	// soundKey is added to the key of a playlist for what its sound does.
	soundKey = "/sound"
)

// coded is what the frames of a playlist are remembered by.
type coded struct {
	seen     map[uint64]time.Time
	repeated run
	// known is the clip the playlist was last found to play, and when.
	known   string
	own     bool
	knownAt time.Time
}

// clipOf returns the known clip a segment's frames belong to: the one
// that has at least half of them.
func clipOf(slates []Slate, hashes []uint64) *Slate {
	for i := range slates {
		if len(slates[i].Keys) == 0 {
			continue
		}
		n := 0
		for _, h := range hashes {
			if _, ok := slices.BinarySearch(slates[i].Keys, h); ok {
				n++
			}
		}
		if 2*n >= len(hashes) {
			return &slates[i]
		}
	}
	return nil
}

// Signature writes the frames a clip is known by the way they are carried
// in the details of an incident; ParseSignature reads them back, sorted.
func Signature(hashes []uint64) string {
	parts := make([]string, len(hashes))
	for i, h := range hashes {
		parts[i] = strconv.FormatUint(h, 16)
	}
	return strings.Join(parts, ",")
}

func ParseSignature(s string) []uint64 {
	var out []uint64
	for _, p := range strings.Split(s, ",") {
		if h, err := strconv.ParseUint(p, 16, 64); err == nil && len(out) < SignatureMax {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// noteCoded looks at the coded frames of a segment that was read, and
// tells whether the playlist plays the same thing over and over.
func (c *Checker) noteCoded(v Variant, st *variantState, info *media.Info, at time.Time) {
	if info == nil || c.t.Picture == PictureOff {
		return
	}
	var slates []Slate
	if c.Slates != nil {
		slates = c.Slates()
	}
	// A stream said to show a still picture is not asked what it shows;
	// what it sounds like is another matter.
	if video := info.Video(); video != nil && c.t.Picture == PictureCheck {
		c.noteLoop(v.Key, v.Key, &st.keys, video.KeyHashes, false, false, slates, at)
	}
	if audio := info.Audio(); audio != nil {
		// Sound that goes round with its picture is the picture's clip,
		// and is told as that when the pictures have come round enough.
		c.noteLoop(v.Key+soundKey, v.Key, &st.sound, audio.SoundHashes, true, st.keys.repeated.n > 0, slates, at)
	}
}

// noteLoop takes the frames of one track of a segment. With quiet set a
// clip found going round is not told: something else tells it.
func (c *Checker) noteLoop(key, variant string, k *coded, hashes []uint64, sound, quiet bool, slates []Slate, at time.Time) {
	if len(hashes) == 0 {
		return
	}
	if sound {
		// Silence is the same frame over and over, and no clip: whether
		// a stream is silent is told by listening to it.
		distinct := map[uint64]struct{}{}
		for _, h := range hashes {
			distinct[h] = struct{}{}
		}
		if 2*len(distinct) < len(hashes) {
			k.repeated = run{}
			c.mu.Lock()
			delete(c.loops, key)
			c.mu.Unlock()
			return
		}
	}
	if k.seen == nil {
		k.seen = map[uint64]time.Time{}
	}
	again := true
	for _, h := range hashes {
		if _, ok := k.seen[h]; !ok {
			again = false
		}
	}
	for _, h := range hashes {
		k.seen[h] = at
	}
	most := keyMemoryMax
	if sound {
		most = soundMemoryMax
	}
	if len(k.seen) > most {
		for h, t := range k.seen {
			if at.Sub(t) > keyMemory || len(k.seen) > 2*most {
				delete(k.seen, h)
			}
		}
	}
	k.repeated.note(again, at)
	if clip := clipOf(slates, hashes); clip != nil {
		k.known, k.own, k.knownAt = clip.Name, clip.Own, at
	}
	known := k.known != "" && at.Sub(k.knownAt) < knownFor

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loops == nil {
		c.loops = map[string]standingProblem{}
	}
	// A clip is found by its coded frames, between two looks: the stream
	// is looked at now, once, for the incident to have a picture to show —
	// not every time a clip that drops out for a segment comes back.
	if _, told := c.loops[key]; !told && c.PictureEvery > 0 && at.Sub(c.clipAt) >= knownFor && ((known && !k.own) || (!quiet && k.repeated.n >= loopSegments && at.Sub(k.repeated.since) >= loopSpan)) {
		c.looks.at = at.Add(-c.PictureEvery)
	}
	switch {
	case known && k.own:
		// What the channel itself plays over and over is not a fault.
		c.ownAt, c.ownName = at, k.known
		delete(c.loops, key)
	case known:
		message := "the stream shows the slate \"" + k.known + "\""
		if sound {
			message = "the stream plays the clip \"" + k.known + "\""
		}
		c.loops[key] = standingProblem{at: at, kind: ProbPictureSlate, severity: SevCrit, variant: variant, message: message,
			details: []any{"slate", k.known, "known_by", "coded frames"}}
	case !quiet && k.repeated.n >= loopSegments && at.Sub(k.repeated.since) >= loopSpan:
		// The clip is the frames seen since it began.
		var clip []uint64
		for h, t := range k.seen {
			if !t.Before(k.repeated.since) {
				clip = append(clip, h)
			}
		}
		slices.Sort(clip)
		p := standingProblem{at: at, kind: ProbPictureLoop, severity: SevCrit, variant: variant,
			message: "the same pictures go round and round",
			details: []any{"segments_in_a_row", k.repeated.n, "distinct_keyframes", len(clip), "since", k.repeated.since.UTC().Format(time.RFC3339),
				"clip_signature", Signature(clip[:min(len(clip), SignatureMax)])}}
		if sound {
			p.kind, p.severity, p.message = ProbAudioLoop, SevWarn, "the same sound goes round and round"
			p.details[2] = "distinct_frames"
		}
		c.loops[key] = p
	default:
		delete(c.loops, key)
	}
}
