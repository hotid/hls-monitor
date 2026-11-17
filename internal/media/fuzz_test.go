package media

import (
	"io/fs"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
)

// Segments come from servers the monitor does not control, and a broken
// encoder or a wrong URL can put anything in them. Whatever the bytes, the
// analysis must return: no crash, no endless loop, no figures that are not
// numbers.
func FuzzAnalyze(f *testing.F) {
	var initData []byte
	err := fs.WalkDir(hlstest.Assets, "assets", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := hlstest.Assets.ReadFile(name)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, "init.mp4") {
			initData = data
		}
		// Whole segments are too large to mutate well; their beginnings
		// hold the headers and the first frames.
		f.Add(data[:min(len(data), 16<<10)])
		return nil
	})
	if err != nil || initData == nil {
		f.Fatalf("no samples: %v", err)
	}
	f.Add([]byte("<html>404 not found</html>"))
	f.Add([]byte{0x47, 0x40, 0x00, 0x10, 0x00})
	init, _ := ParseInit(initData)

	f.Fuzz(func(t *testing.T, data []byte) {
		for _, in := range []*Init{nil, init} {
			start := time.Now()
			info := Analyze(data, in)
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("analysis of %d bytes took %s", len(data), took)
			}
			if strings.HasPrefix(info.Error, "parser panic") {
				t.Fatalf("the parser panicked: %s", info.Error)
			}
			for _, tr := range info.Tracks {
				for name, v := range map[string]float64{"fps": tr.FPS, "duration": tr.Duration(), "max GOP": tr.MaxGOP, "bitrate": tr.Bitrate()} {
					if math.IsNaN(v) || math.IsInf(v, 0) {
						t.Fatalf("track %d: %s is %v", tr.ID, name, v)
					}
				}
				if tr.Frames < 0 || tr.Keyframes < 0 || tr.FPS < 0 || tr.MaxGOP < 0 {
					t.Fatalf("track %d has a negative figure: %+v", tr.ID, tr)
				}
			}
		}
		if in, err := ParseInit(data); err != nil && strings.HasPrefix(err.Error(), "parser panic") {
			t.Fatalf("the parser of the initialization section panicked: %v (%v)", err, in)
		}
	})
}
