package media

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hotid/hls-monitor/internal/types/videorange"
)

// The dynamic range is read from the header of the picture, in MPEG-TS and
// in fragmented MP4, of H.264 and of HEVC.
func TestDynamicRange(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	dir := t.TempDir()
	for _, c := range []struct{ name, codec, trc, want string }{
		{"pq264.ts", "libx264", "smpte2084", "PQ"},
		{"hlg264.ts", "libx264", "arib-std-b67", "HLG"},
		{"sdr264.ts", "libx264", "bt709", ""},
		{"pq265.ts", "libx265", "smpte2084", "PQ"},
		{"hlg265.mp4", "libx265", "arib-std-b67", "HLG"},
		{"pq264.mp4", "libx264", "smpte2084", "PQ"},
	} {
		file := filepath.Join(dir, c.name)
		args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=25", "-t", "1", "-pix_fmt", "yuv420p",
			"-c:v", c.codec, "-color_trc", c.trc, "-color_primaries", "bt2020", "-colorspace", "bt2020nc"}
		// The encoder is told itself: ffmpeg's own options do not reach its header.
		if c.codec == "libx265" {
			args = append(args, "-x265-params", "log-level=error:transfer="+c.trc+":colorprim=bt2020:colormatrix=bt2020nc")
		} else {
			args = append(args, "-x264-params", "transfer="+c.trc+":colorprim=bt2020:colormatrix=bt2020nc")
		}
		if filepath.Ext(c.name) == ".mp4" {
			args = append(args, "-movflags", "frag_keyframe+empty_moov+default_base_moof")
		}
		if msg, err := exec.Command("ffmpeg", append(args, file)...).CombinedOutput(); err != nil {
			t.Skipf("ffmpeg cannot make %s: %v: %s", c.name, err, msg)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var r videorange.Range
		if filepath.Ext(c.name) == ".mp4" {
			init, err := ParseInit(data)
			if err != nil || len(init.order) == 0 {
				t.Fatalf("%s: %v", c.name, err)
			}
			r = init.tracks[init.order[0]].Range
		} else if v := Analyze(data, nil).Video(); v != nil {
			r = v.Range
		}
		got := ""
		if !r.IsZero() {
			got = r.String()
		}
		if got != c.want {
			t.Errorf("%s: range %q, want %q", c.name, got, c.want)
		}
	}
}
