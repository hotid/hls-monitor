package probe

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/types/codecfamily"
)

// declaredVideo reads the profile and the level out of a CODECS entry of
// H.264 (avc1.PPCCLL, or the old avc1.P.L in decimal) or of HEVC
// (hvc1.P.C.LNN…). What it cannot read is left empty.
func declaredVideo(entry string) (profile, level string) {
	parts := strings.Split(strings.TrimSpace(entry), ".")
	switch codecfamily.Declared(entry) {
	case codecfamily.H264():
		var p, l uint64
		var err1, err2 error
		switch {
		case len(parts) == 2 && len(parts[1]) == 6:
			p, err1 = strconv.ParseUint(parts[1][:2], 16, 32)
			l, err2 = strconv.ParseUint(parts[1][4:], 16, 32)
		case len(parts) == 3:
			p, err1 = strconv.ParseUint(parts[1], 10, 32)
			l, err2 = strconv.ParseUint(parts[2], 10, 32)
		default:
			return "", ""
		}
		if err1 != nil || err2 != nil {
			return "", ""
		}
		return media.ProfileName(codecfamily.H264(), uint32(p)), fmt.Sprintf("%d.%d", l/10, l%10)
	case codecfamily.HEVC():
		if len(parts) < 4 {
			return "", ""
		}
		if p, err := strconv.ParseUint(strings.TrimLeft(parts[1], "ABC"), 10, 32); err == nil {
			profile = media.ProfileName(codecfamily.HEVC(), uint32(p))
		}
		if l, err := strconv.ParseUint(strings.TrimLeft(parts[3], "LH"), 10, 32); err == nil {
			level = fmt.Sprintf("%d.%d", l/30, (l%30)/3)
		}
	}
	return profile, level
}

// declaredDiffers tells in what the segments of a variant are not what the
// master playlist says of it. A player picks a variant by what is said:
// one that cannot play what it gets shows nothing, and one that could have
// played it never tries.
//
// The width is not compared: RESOLUTION is the size the picture is shown
// at, and a picture of 720 points across is shown 1024 wide. A level above
// the declared one is told, one below it harms nobody. A frame rate is told
// when it is neither the declared one nor twice or half of it, as fields
// are to frames, and only of a segment read whole.
func declaredDiffers(v Variant, vt, at *media.Track, partial bool) []string {
	var out []string
	var video, audio []string
	for _, e := range strings.Split(v.Codecs, ",") {
		switch f := codecfamily.Declared(e); {
		case f.IsZero():
		case f.Audio():
			audio = append(audio, strings.TrimSpace(e))
		default:
			video = append(video, strings.TrimSpace(e))
		}
	}
	if vt != nil {
		if real := vt.Codec.Family(); !real.IsZero() && len(video) > 0 {
			same := ""
			for _, e := range video {
				if codecfamily.Declared(e) == real {
					same = e
				}
			}
			if same == "" {
				out = append(out, fmt.Sprintf("the picture is %s, declared %s", vt.Codec, strings.Join(video, ",")))
			} else {
				profile, level := declaredVideo(same)
				if profile != "" && vt.Profile != "" && profile != vt.Profile {
					out = append(out, fmt.Sprintf("the profile is %s, declared %s (%s)", vt.Profile, profile, same))
				}
				dl, err1 := strconv.ParseFloat(level, 64)
				rl, err2 := strconv.ParseFloat(vt.Level, 64)
				if err1 == nil && err2 == nil && rl > dl {
					out = append(out, fmt.Sprintf("the level is %s, declared %s (%s)", vt.Level, level, same))
				}
			}
		}
		if _, h, ok := strings.Cut(v.Resolution, "x"); ok && vt.Height > 0 {
			if dh, err := strconv.Atoi(h); err == nil && absInt(dh-vt.Height) > 8 {
				out = append(out, fmt.Sprintf("the picture is %dx%d, declared %s", vt.Width, vt.Height, v.Resolution))
			}
		}
		if !partial && v.FrameRate > 0 && vt.FPS > 0 {
			r := vt.FPS / v.FrameRate
			if math.Abs(r-1) > 0.05 && math.Abs(r-2) > 0.1 && math.Abs(r-0.5) > 0.025 {
				out = append(out, fmt.Sprintf("the frame rate is %.3f, declared %.3f", vt.FPS, v.FrameRate))
			}
		}
	}
	if at != nil && len(audio) > 0 {
		if real := at.Codec.Family(); !real.IsZero() {
			found := false
			for _, e := range audio {
				found = found || codecfamily.Declared(e) == real
			}
			if !found {
				out = append(out, fmt.Sprintf("the sound is %s, declared %s", at.Codec, strings.Join(audio, ",")))
			}
		}
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
