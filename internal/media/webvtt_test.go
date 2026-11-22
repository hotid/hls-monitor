package media

import "testing"

func TestWebVTT(t *testing.T) {
	s, err := ParseWebVTT([]byte("\xef\xbb\xbfWEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000\n\nNOTE a remark\n\n1\n00:00:01.000 --> 00:00:03.500 line:80%\nHello\n\n01:02.000 --> 01:04.000\nAgain\n"))
	if err != nil || s.Cues != 2 || !s.Mapped || s.Start != 11 || s.End != 74 {
		t.Fatalf("read %+v, %v; want 2 cues from 11 to 74 s of the stream", s, err)
	}
	if s, err := ParseWebVTT([]byte("WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n")); err != nil || s.Cues != 0 {
		t.Errorf("a segment with nothing said: %+v, %v", s, err)
	}
	for name, body := range map[string]string{
		"not WebVTT":         "<html>404</html>",
		"WEBVTTX":            "WEBVTTX\n\n",
		"a cue backwards":    "WEBVTT\n\n00:00:05.000 --> 00:00:01.000\nx\n",
		"times not readable": "WEBVTT\n\n00:00:05 --> 00:00:06\nx\n",
		"half a mapping":     "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000\n\n",
	} {
		if s, err := ParseWebVTT([]byte(body)); err == nil {
			t.Errorf("%s: read as %+v", name, s)
		}
	}
}
