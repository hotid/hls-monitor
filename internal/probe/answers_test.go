package probe

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/seconds"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

// changed is a fixture whose origin answers through change: what a
// server or a CDN in front of the packager does to its answers.
func changed(t *testing.T, opt hlstest.Options, path string, lv level.Level, change func(r *http.Request, rec *httptest.ResponseRecorder)) *fixture {
	t.Helper()
	f := newFixture(t, opt, path, lv)
	inner := f.origin.Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		inner.ServeHTTP(rec, r)
		change(r, rec)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	f.c = New(Target{ID: id.StreamOf(1), URL: srv.URL + path, Level: lv, Picture: picturemode.Check(), Timeout: seconds.Of(5)}, nil)
	f.c.now = func() time.Time { return f.clock }
	f.c.loud.due = f.clock.Add(1000 * time.Hour)
	return f
}

// A part of a segment that comes without the size of the whole is not
// taken for the whole segment: where it ends says nothing of where the
// segment does.
func TestHeaderLevelPartOfUnknownWhole(t *testing.T) {
	f := changed(t, hlstest.Options{PadTo: 200000}, "/v0/index.m3u8", level.Header(), func(r *http.Request, rec *httptest.ResponseRecorder) {
		var last int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=0-%d", &last); err != nil || !strings.HasSuffix(r.URL.Path, ".ts") {
			return
		}
		body := rec.Body.Bytes()
		rec.Body = bytes.NewBuffer(body[:min(last+1, len(body))])
		rec.Code = http.StatusPartialContent
		rec.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", last))
		rec.Header().Del("Content-Length")
	})
	f.origin.SetNoRange(true)
	r := f.step(4)
	wantClean(t, r)
	if seg := r.Segments[0]; seg.Status != 206 || seg.Fetched != headerBytes {
		t.Fatalf("segment record = %+v", seg)
	}
	for range 8 {
		wantClean(t, f.step(1))
	}
	// Its size is not known, and no bitrate is made of what was fetched.
	if b, _ := metric(f.step(1), metricname.SegmentBitrateBps(), variantkey.Media()); b != 0 {
		t.Errorf("bitrate of segments of unknown size = %.0f", b)
	}
}

// The clock of a packager that dates the first segment of its playlist
// alone jumps between two readings: the same segment is dated anew.
func TestProgramDateTimeMovesBetweenReadings(t *testing.T) {
	var shift atomic.Int64
	date := regexp.MustCompile(`(?m)^#EXT-X-PROGRAM-DATE-TIME:(.+)$`)
	f := changed(t, hlstest.Options{DateFirstOnly: true}, "/v0/index.m3u8", level.Playlist(), func(r *http.Request, rec *httptest.ResponseRecorder) {
		if !strings.HasSuffix(r.URL.Path, ".m3u8") || shift.Load() == 0 {
			return
		}
		body := date.ReplaceAllFunc(rec.Body.Bytes(), func(line []byte) []byte {
			at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(date.FindSubmatch(line)[1])))
			if err != nil {
				t.Errorf("date in the playlist: %v", err)
				return line
			}
			return []byte("#EXT-X-PROGRAM-DATE-TIME:" + at.Add(time.Duration(shift.Load())).Format(time.RFC3339Nano))
		})
		rec.Body = bytes.NewBuffer(body)
		rec.Header().Del("Content-Length")
	})
	wantClean(t, f.step(4))
	wantClean(t, f.step(1))
	shift.Store(int64(5 * time.Second))
	p := wantProblem(t, f.step(1), problemkind.PDTDiscontinuity(), severity.Warn())
	if !strings.Contains(p.Message, "+5.000s") || !strings.Contains(p.Message, "between two readings") {
		t.Errorf("message = %q", p.Message)
	}
	// The new dates stand: nothing more is said of them.
	wantClean(t, f.step(1))
}

// A segment under a token that is given anew with every playlist is the
// same segment.
func TestSegmentTokenIsNotAnotherSegment(t *testing.T) {
	var n atomic.Int64
	f := changed(t, hlstest.Options{}, "/v0/index.m3u8", level.Playlist(), func(r *http.Request, rec *httptest.ResponseRecorder) {
		if strings.HasSuffix(r.URL.Path, ".m3u8") {
			token := "?token=" + string(rune('a'+n.Add(1)))
			rec.Body = bytes.NewBuffer(regexp.MustCompile(`(?m)^(seg-\d+\.ts)$`).ReplaceAll(rec.Body.Bytes(), []byte("${1}"+token)))
			rec.Header().Del("Content-Length")
		}
	})
	wantClean(t, f.step(4))
	wantClean(t, f.step(1))
	wantClean(t, f.step(1))
}

// A segment that is a range of a larger file, from a server that takes no
// notice of Range: the bytes of the segment are found in the file, or
// nothing is said of what it holds.
func TestRangeOfAFileFromAServerWithoutRanges(t *testing.T) {
	file := []byte("0123456789abcdefghij")
	seg := func(off, limit int64, header bool, asked int64) *segCheck {
		k := &segCheck{segRun: &segRun{header: header}, asked: asked}
		k.s.seg = &m3u8.MediaSegment{Offset: off, Limit: limit}
		return k
	}
	resp := func(status int, body []byte) *httpx.Response { return &httpx.Response{Status: status, Body: body} }

	r := resp(200, file)
	if !seg(10, 5, false, 0).ownBytes(r) || string(r.Body) != "abcde" {
		t.Errorf("the range of a whole file = %q", r.Body)
	}
	r = resp(206, file[10:15])
	if !seg(10, 5, false, 0).ownBytes(r) || string(r.Body) != "abcde" {
		t.Errorf("a range the server did send = %q", r.Body)
	}
	// The beginning of the file is all that came, and the segment is further on.
	if r = resp(200, file[:8]); seg(10, 5, true, 8).ownBytes(r) {
		t.Errorf("the beginning of the file was taken for a segment at 10: %q", r.Body)
	}
	if r = resp(200, file[:8]); !seg(2, 10, true, 4).ownBytes(r) || string(r.Body) != "2345" {
		t.Errorf("the beginning of a segment within what came = %q", r.Body)
	}
}

// The level of whole segments reads of them what the level below does.
func TestWholeSegmentsLevelReadsTheirBeginning(t *testing.T) {
	f := newFixture(t, hlstest.Options{NoDiscontinuity: true}, "/v0/index.m3u8", level.Segments())
	r := f.step(2)
	wantClean(t, r)
	if seg := r.Segments[0]; seg.Media == nil || seg.Media.Video() == nil || seg.Media.Video().Width != 160 {
		t.Fatalf("the segment was not read: %+v", seg.Media)
	}
	if _, ok := metric(r, metricname.MediaWidth(), variantkey.Media()); !ok {
		t.Error("hls_media_width is missing at the level of whole segments")
	}
	wantClean(t, f.step(1))
	// Where the sample loop starts over without a word of the playlist.
	wantProblem(t, f.step(1), problemkind.PTSDiscontinuity(), severity.Warn())
}
