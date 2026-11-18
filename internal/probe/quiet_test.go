package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/media"
)

// encoded has ffmpeg make six seconds of MPEG-TS from a picture source
// and a sound source.
func encoded(t *testing.T, video, audio string, more ...string) *media.Info {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes video")
	}
	out := filepath.Join(t.TempDir(), "clip.ts")
	args := []string{"-v", "error", "-f", "lavfi", "-i", video, "-f", "lavfi", "-i", audio, "-t", "6",
		"-c:v", "libx264", "-g", "50", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k"}
	args = append(append(args, more...), "-f", "mpegts", out)
	if msg, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode this: %s", strings.TrimSpace(string(msg)))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return media.Analyze(data, nil)
}

// signsOf feeds the same segment to a checker for a minute and returns
// what the sizes of its frames were told to be signs of.
func signsOf(info *media.Info, setting string) map[string]bool {
	c := New(Target{ID: 1, URL: "http://x/", Level: LevelContainer, Picture: setting}, nil)
	st := &variantState{}
	at := time.Now()
	for i := 0; i < 10; i++ {
		at = at.Add(6 * time.Second)
		c.noteSizes(Variant{Key: "v"}, st, info, at)
	}
	out := map[string]bool{}
	for _, p := range c.signs {
		out[p.kind] = p.severity == SevWarn && p.details != nil
	}
	return out
}

func TestSignsInTheSizesOfFrames(t *testing.T) {
	const size = "size=1280x720:rate=25"
	cbr := []string{"-b:v", "2M", "-minrate", "2M", "-maxrate", "2M", "-bufsize", "2M", "-x264-params", "nal-hrd=cbr"}

	// A black screen in silence, padded to a constant rate or not.
	for _, more := range [][]string{nil, cbr} {
		got := signsOf(encoded(t, "color=c=black:"+size, "anullsrc=sample_rate=48000", more...), PictureCheck)
		if !got[ProbPictureFrozen] || !got[ProbAudioSilent] {
			t.Errorf("black and silent (%v): %v", more, got)
		}
	}
	// A still picture with a tone: the picture alone.
	still := encoded(t, "smptebars="+size, "sine=frequency=440:sample_rate=48000")
	if got := signsOf(still, PictureCheck); !got[ProbPictureFrozen] || got[ProbAudioSilent] {
		t.Errorf("still with a tone: %v", got)
	}
	// Where a still picture is what the stream shows, it is not a sign.
	if got := signsOf(still, PictureStatic); len(got) != 0 {
		t.Errorf("a stream of still pictures: %v", got)
	}
	// Something moving, with sound.
	if got := signsOf(encoded(t, "testsrc2="+size, "sine=frequency=440:sample_rate=48000"), PictureCheck); len(got) != 0 {
		t.Errorf("a moving picture: %v", got)
	}
}

// TestRealSegmentsShowNoSigns reads real segments, when a directory of
// them is given: live television is never as small as a still picture.
func TestRealSegmentsShowNoSigns(t *testing.T) {
	dir := os.Getenv("HLSMON_FFPROBE_SAMPLES")
	if dir == "" {
		t.Skip("HLSMON_FFPROBE_SAMPLES is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ts"))
	least, judged := 1.0, 0
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		info := media.Analyze(data, nil)
		if got := signsOf(info, PictureCheck); len(got) != 0 {
			t.Errorf("%s: %v", filepath.Base(name), got)
		}
		if v := info.Video(); v != nil && v.PlainFrames >= stillFrames && v.Width > 0 {
			judged++
			least = min(least, float64(v.PlainBytes)*8/float64(v.PlainFrames)/float64(v.Width*v.Height))
		}
	}
	t.Logf("%d segments, %d judged; the least bits a point: %.4f against the threshold of %v", len(files), judged, least, stillBits)
}
