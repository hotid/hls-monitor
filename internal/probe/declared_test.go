package probe

import (
	"strings"
	"testing"

	"github.com/hotid/hls-monitor/internal/media"
)

func TestDeclaredAgainstTheSegments(t *testing.T) {
	high := &media.Track{Kind: media.KindVideo, Codec: "h264", Profile: "High", Level: "4.0", Width: 720, Height: 576, FPS: 25}
	aac := &media.Track{Kind: media.KindAudio, Codec: "aac"}
	for name, c := range map[string]struct {
		v       Variant
		vt, at  *media.Track
		partial bool
		want    string
	}{
		"as declared, shown wider than coded": {v: Variant{Codecs: "avc1.640028,mp4a.40.2", Resolution: "1048x576", FrameRate: 25}, vt: high, at: aac},
		"fields for frames":                   {v: Variant{Codecs: "avc1.640028", FrameRate: 50}, vt: high},
		"a level below the declared":          {v: Variant{Codecs: "avc1.64002a"}, vt: high},
		"sound in a playlist of its own":      {v: Variant{Codecs: "avc1.640028,mp4a.40.2"}, vt: high},
		"nothing declared":                    {v: Variant{}, vt: high, at: aac},
		"old way of writing":                  {v: Variant{Codecs: "avc1.100.40"}, vt: high},
		"rate of a beginning is not judged":   {v: Variant{FrameRate: 30}, vt: high, partial: true},
		"another profile":                     {v: Variant{Codecs: "avc1.4d4028"}, vt: high, want: "profile is High, declared Main"},
		"a level above the declared":          {v: Variant{Codecs: "avc1.64001e"}, vt: high, want: "level is 4.0, declared 3.0"},
		"another coding":                      {v: Variant{Codecs: "hvc1.1.6.L93.B0"}, vt: high, want: "picture is h264, declared hvc1"},
		"another height":                      {v: Variant{Resolution: "1920x1080"}, vt: high, want: "picture is 720x576, declared 1920x1080"},
		"another rate":                        {v: Variant{FrameRate: 30}, vt: high, want: "frame rate is 25.000, declared 30.000"},
		"another sound":                       {v: Variant{Codecs: "avc1.640028,ac-3"}, vt: high, at: aac, want: "sound is aac, declared ac-3"},
	} {
		got := strings.Join(declaredDiffers(c.v, c.vt, c.at, c.partial), "; ")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	if p, l := declaredVideo("hvc1.2.4.L153.B0"); p != "Main 10" || l != "5.1" {
		t.Errorf("hvc1.2.4.L153.B0 read as %q, %q", p, l)
	}
}
