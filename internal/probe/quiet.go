package probe

import (
	"math"
	"time"

	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/types/codecfamily"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
)

// A picture that does not change costs an encoder next to nothing: the
// frames between keyframes say "the same again" and are a few dozen bytes
// each. Digital silence is likewise a frame of a dozen bytes. Neither takes
// decoding to see — the sizes of the coded frames are in the container —
// and every segment is seen, not one in a few minutes.
//
// It is a sign and no more. A camera pointed at a wall has noise in it and
// codes into real frames; an encoder that pads its sound to a constant
// rate hides silence. So what the sizes tell is told as a warning where
// there is no decoder, and where there is one it is asked to look at once.

const (
	// stillBits is how many bits a point of the picture takes in a frame
	// that is not a keyframe, filler aside, below which the picture is
	// not changing. Of 186 segments of live television the least was
	// 0.028; a black screen and a still picture take 0.0004 to 0.0014.
	stillBits = 0.003
	// stillFrames is how many such frames a segment must have to be judged.
	stillFrames = 20
	// muteBytes is the size of a frame of AAC below which it is silence:
	// 13 bytes against 340 for the quietest live sound seen.
	muteBytes = 40
)

// noteSizes looks at how large the coded frames of a segment are.
func (c *Checker) noteSizes(v Variant, st *variantState, info *media.Info, at time.Time) {
	if info == nil || c.t.Picture == picturemode.Off() {
		return
	}
	var bits, bytes float64
	still, mute := false, false
	if video := info.Video(); video != nil && c.t.Picture == picturemode.Check() && video.PlainFrames >= stillFrames && video.Width > 0 && video.Height > 0 {
		bits = float64(video.PlainBytes) * 8 / float64(video.PlainFrames) / float64(video.Width*video.Height)
		still = bits < stillBits
	}
	if audio := info.Audio(); audio != nil && audio.Codec.Family() == codecfamily.AAC() && audio.Frames >= stillFrames {
		bytes = float64(audio.Bytes) / float64(audio.Frames)
		mute = bytes < muteBytes
	}
	st.still.note(still, at)
	st.mute.note(mute, at)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.signs == nil {
		c.signs = map[standKey]standingProblem{}
	}
	stand := func(key standKey, r run, kind problemkind.Kind, message string, details ...detail.Field) {
		if r.n < loopSegments || at.Sub(r.since) < loopSpan {
			delete(c.signs, key)
			return
		}
		// A decoder tells for certain: it is asked now rather than at its
		// next turn, once for every time the sign appears.
		if r.n == loopSegments && c.PictureEvery > 0 {
			c.looks.at = at.Add(-c.PictureEvery)
		}
		c.signs[key] = standingProblem{at: at, kind: kind, severity: severity.Warn(), variant: v.Key, message: message,
			details: append(details, detail.JudgedBy("the size of coded frames"), detail.SegmentsInARow(r.n), detail.Since(r.since))}
	}
	stand(standKey{variant: v.Key}, st.still, problemkind.PictureFrozen(), "the picture does not seem to change", detail.BitsPerPixel(math.Round(bits*1e5)/1e5))
	stand(standKey{variant: v.Key, sound: true}, st.mute, problemkind.AudioSilent(), "the sound seems to be silence", detail.AudioFrameBytes(math.Round(bytes)))
}
