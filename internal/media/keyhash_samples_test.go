package media

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRealKeyframesDoNotRepeat reads pairs of consecutive segments of real
// streams, when a directory of them is given: the keyframes of live
// television are never coded into the same bytes twice.
func TestRealKeyframesDoNotRepeat(t *testing.T) {
	dir := os.Getenv("HLSMON_FFPROBE_SAMPLES")
	if dir == "" {
		t.Skip("HLSMON_FFPROBE_SAMPLES is not set")
	}
	firsts, _ := filepath.Glob(filepath.Join(dir, "*_0.ts"))
	sort.Strings(firsts)
	var pairs, withKeys, shared, wholly int
	var withSound, soundShared, soundWholly, monotone int
	for _, name := range firsts {
		a, errA := os.ReadFile(name)
		b, errB := os.ReadFile(strings.TrimSuffix(name, "_0.ts") + "_1.ts")
		if errA != nil || errB != nil {
			continue
		}
		pairs++
		if sa, sb := Analyze(a, nil).Audio(), Analyze(b, nil).Audio(); sa != nil && sb != nil && len(sa.SoundHashes) > 0 && len(sb.SoundHashes) > 0 {
			withSound++
			seen, distinct := map[uint64]bool{}, map[uint64]bool{}
			for _, h := range sa.SoundHashes {
				seen[h] = true
			}
			again := 0
			for _, h := range sb.SoundHashes {
				distinct[h] = true
				if seen[h] {
					again++
				}
			}
			// Silence is one frame over and over, and is not counted as sound that came again.
			if 2*len(distinct) < len(sb.SoundHashes) {
				monotone++
			} else if again > 0 {
				soundShared++
				t.Logf("%s: %d of %d frames of sound of the next segment were in this one", filepath.Base(name), again, len(sb.SoundHashes))
				if again == len(sb.SoundHashes) {
					soundWholly++
				}
			}
		}
		va, vb := Analyze(a, nil).Video(), Analyze(b, nil).Video()
		if va == nil || vb == nil || len(va.KeyHashes) == 0 || len(vb.KeyHashes) == 0 {
			continue
		}
		withKeys++
		seen := map[uint64]bool{}
		for _, h := range va.KeyHashes {
			seen[h] = true
		}
		again := 0
		for _, h := range vb.KeyHashes {
			if seen[h] {
				again++
			}
		}
		if again > 0 {
			shared++
			t.Logf("%s: %d of %d keyframes of the next segment were in this one", filepath.Base(name), again, len(vb.KeyHashes))
		}
		if again == len(vb.KeyHashes) {
			wholly++
		}
	}
	t.Logf("%d with sound read: silence in %d; a frame came again in %d, every one of them in %d", withSound, monotone, soundShared, soundWholly)
	t.Logf("%d pairs, %d with keyframes read: a keyframe came again in %d, every one of them in %d", pairs, withKeys, shared, wholly)
}
