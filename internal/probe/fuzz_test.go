package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A playlist is text from a server the monitor does not control. Whatever
// it holds, a check cycle must end with a result.
func FuzzPlaylist(f *testing.F) {
	f.Add("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:2.0,\nseg-7.ts\n#EXTINF:2.0,\nseg-8.ts\n")
	f.Add("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\nv0/index.m3u8\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"x\",URI=\"a.m3u8\"\n")
	f.Add("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:18446744073709551615\n#EXT-X-PROGRAM-DATE-TIME:2026-11-17T00:00:00Z\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXT-X-MAP:URI=\"i.mp4\",BYTERANGE=\"10@0\"\n#EXT-X-BYTERANGE:5@99999999999\n#EXTINF:-1,\ns.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:1e309,\n://bad\n#EXT-X-ENDLIST\n")
	f.Add("")
	f.Add("<html>502</html>")

	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	f.Cleanup(srv.Close)

	f.Fuzz(func(t *testing.T, text string) {
		body = text
		c := New(Target{ID: 1, URL: srv.URL + "/p.m3u8", Level: LevelSegments, TimeoutSec: 2}, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// Twice: most of what a checker concludes is a difference between
		// two cycles.
		for range 2 {
			r := c.Run(ctx)
			if r == nil || r.Status == "" {
				t.Fatalf("no result for playlist %q", text)
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("checking playlist %q did not end in time", text)
		}
	})
}
