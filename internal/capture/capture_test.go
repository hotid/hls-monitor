package capture

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/hlstest"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/capturestate"
	"github.com/hotid/hls-monitor/internal/types/filerole"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

// stream is a fake stream and the means to ask for a capture of it.
type stream struct {
	t      *testing.T
	origin *hlstest.Origin
	url    string
	hc     *httpx.Client
}

func newStream(t *testing.T) *stream {
	t.Helper()
	o := hlstest.New(hlstest.Options{Window: 8})
	srv := httptest.NewServer(o.Handler())
	t.Cleanup(srv.Close)
	o.Publish(9)
	return &stream{t: t, origin: o, url: srv.URL + "/v0/index.m3u8", hc: httpx.New(httpx.Options{})}
}

func (s *stream) get(ctx context.Context, url string, offset, limit int64) (*httpx.Response, error) {
	return s.hc.Get(ctx, httpx.Request{URL: url, RangeOffset: offset, RangeLength: limit})
}

// request is what a checker would hand over for the segment before the
// last one of the playlist, having read the beginning of it.
func (s *stream) request(of int64, kinds ...problemkind.Kind) probe.CaptureRequest {
	s.t.Helper()
	pl, err := s.get(context.Background(), s.url, 0, 0)
	if err != nil || !pl.OK() {
		s.t.Fatalf("playlist: %v", err)
	}
	w := window(s.url, pl.Body)
	if len(w) < 3 {
		s.t.Fatalf("the playlist has %d segments", len(w))
	}
	seg := w[len(w)-2]
	head, err := s.get(context.Background(), seg.URL, 0, 1880)
	if err != nil || !head.OK() {
		s.t.Fatalf("segment: %v", err)
	}
	return probe.CaptureRequest{StreamID: id.StreamOf(of), Variant: variantkey.OfURI("v0"), Kinds: kinds, At: time.Now(), PlaylistURL: s.url, Playlist: pl.Body,
		TargetDuration: 1, Window: w, Seq: seg.Seq, Evidence: head.Body, Status: head.Status, Server: netip.MustParseAddr("192.0.2.1"), Get: s.get}
}

// publish lets the stream go on while a capture waits for what follows.
func (s *stream) publish(n int) {
	go func() {
		for range n {
			time.Sleep(300 * time.Millisecond)
			s.origin.Publish(1)
		}
	}()
}

func open(t *testing.T, max int64) *Store {
	t.Helper()
	o := Options{Dir: t.TempDir(), MaxBytes: max}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		o.FFprobe = p
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		o.FFmpeg = p
	}
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCapture(t *testing.T) {
	st := newStream(t)
	s := open(t, 0)
	rq := st.request(7, problemkind.PictureBlack())
	st.publish(6)
	s.Capture(rq)
	if l := s.List(id.StreamOf(7)); len(l) != 1 || l[0].State != capturestate.Pending() || l[0].Done {
		t.Fatalf("a capture that was just asked for: %+v", l)
	}
	// One of a stream is enough for a while.
	s.Capture(st.request(7, problemkind.PictureBlack()))
	s.Wait()

	l := s.List(id.StreamOf(7))
	if len(l) != 1 || !l[0].Done || !l[0].Playable || l[0].Bytes == 0 {
		t.Fatalf("captures: %+v", l)
	}
	c := l[0]
	byPosition := map[int]File{}
	var evidence *File
	for i, f := range c.Files {
		switch f.Role {
		case filerole.Evidence():
			evidence = &c.Files[i]
		case filerole.Segment():
			byPosition[f.Position] = f
		}
	}
	if evidence == nil || evidence.Whole || evidence.AfterDetection || evidence.Bytes != 1880 || evidence.Server.String() != "192.0.2.1" {
		t.Fatalf("the bytes the check read: %+v", evidence)
	}
	// The segment itself, fetched whole after the finding and said to be so.
	if f := byPosition[0]; !f.Whole || !f.AfterDetection || f.Seq != rq.Seq || f.Bytes <= 1880 || f.SHA256 == "" {
		t.Fatalf("the segment in question: %+v", f)
	}
	for _, p := range []int{-4, -3, -2, -1, 1, 2, 3, 4} {
		if f := byPosition[p]; !f.Whole || f.Seq != rq.Seq+uint64(int64(p)) {
			t.Errorf("the segment at %d: %+v", p, f)
		}
	}
	if s.o.FFprobe != "" && byPosition[0].Probe == "" {
		t.Error("ffprobe said nothing of the segment in question")
	}
	// It plays with no stream to ask: the playlist names the files saved.
	index, err := os.ReadFile(filepath.Join(s.o.Dir, c.ID.String(), "index.m3u8"))
	if err != nil || strings.Count(string(index), "segments/") != 9 || !strings.Contains(string(index), "#EXT-X-ENDLIST") || strings.Contains(string(index), "http") {
		t.Fatalf("index.m3u8: %v\n%s", err, index)
	}
	if _, err := os.Stat(filepath.Join(s.o.Dir, c.ID.String(), "source", "playlist.m3u8")); err != nil {
		t.Errorf("the playlist as it was: %v", err)
	}

	// Reported: kept. And it is all in the archive.
	s.Confirm(id.StreamOf(7), []problemkind.Kind{problemkind.AudioSilent()})
	if s.List(id.StreamOf(7))[0].State != capturestate.Pending() {
		t.Fatal("kept for a problem it was not made for")
	}
	s.Confirm(id.StreamOf(7), []problemkind.Kind{problemkind.PictureBlack()})
	if c = s.List(id.StreamOf(7))[0]; c.State != capturestate.Kept() || len(c.Confirmed) != 1 {
		t.Fatalf("after the problem was reported: %+v", c)
	}
	var buf bytes.Buffer
	if err := s.WriteZip(&buf, c.ID); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[strings.TrimPrefix(f.Name, c.ID.String()+"/")] = true
	}
	for _, want := range []string{"capture.json", "index.m3u8", "source/playlist.m3u8", byPosition[0].Name, evidence.Name} {
		if !names[want] {
			t.Errorf("%s is not in the archive: %v", want, names)
		}
	}

	// It is there after a restart, and gone when it is old.
	again, err := Open(s.o)
	if err != nil || len(again.List(id.StreamOf(0))) != 1 {
		t.Fatalf("after a restart: %v, %+v", err, again.List(id.StreamOf(0)))
	}
	again.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	again.Sweep()
	if len(again.List(id.StreamOf(0))) != 0 {
		t.Error("a capture eight days old is still kept")
	}
}

func TestUnconfirmedCaptureIsDropped(t *testing.T) {
	st := newStream(t)
	s := open(t, 0)
	rq := st.request(7, problemkind.PictureBlack())
	rq.Window = rq.Window[len(rq.Window)-2:] // nothing follows, little precedes: quick
	rq.Playlist = nil
	s.Capture(rq)
	s.Wait()
	if len(s.List(id.StreamOf(7))) != 1 {
		t.Fatal("no capture")
	}
	s.Sweep()
	if len(s.List(id.StreamOf(7))) != 1 {
		t.Fatal("dropped before it could be confirmed")
	}
	s.now = func() time.Time { return time.Now().Add(pendingFor + time.Second) }
	s.Sweep()
	// A segment a decoder found fault with would be kept; these are sound.
	if l := s.List(id.StreamOf(7)); len(l) != 0 {
		t.Fatalf("not confirmed and still kept: %+v", l)
	}

	// Somebody asked for one: kept without any problem reported.
	s.now = time.Now
	rq = st.request(8)
	rq.Manual, rq.Window, rq.Playlist = true, rq.Window[len(rq.Window)-2:], nil
	s.Capture(rq)
	s.Wait()
	s.now = func() time.Time { return time.Now().Add(pendingFor + time.Second) }
	s.Sweep()
	if l := s.List(id.StreamOf(8)); len(l) != 1 || l[0].State != capturestate.Kept() || !l[0].Manual {
		t.Fatalf("a capture that was asked for: %+v", l)
	}
	if !s.Delete(l8(s)) || len(s.List(id.StreamOf(8))) != 0 {
		t.Error("it could not be deleted")
	}
}

func l8(s *Store) ID { return s.List(id.StreamOf(8))[0].ID }

func TestCaptureLimits(t *testing.T) {
	st := newStream(t)
	// No room for a capture at all: none is made.
	s := open(t, 1<<20)
	s.Capture(st.request(7, problemkind.PictureBlack()))
	s.Wait()
	if u := s.Usage(); len(s.List(id.StreamOf(0))) != 0 || u.Refused != 1 || u.Made != 0 {
		t.Fatalf("with no room: %+v, %d captures", u, len(s.List(id.StreamOf(0))))
	}

	// One at a time from one server; another server is another matter.
	s = open(t, 0)
	block := make(chan struct{})
	slow := st.request(1, problemkind.PictureBlack())
	slow.Playlist = nil
	get := slow.Get
	slow.Get = func(ctx context.Context, url string, offset, limit int64) (*httpx.Response, error) {
		<-block
		return get(ctx, url, offset, limit)
	}
	s.Capture(slow)
	same := st.request(2, problemkind.PictureBlack())
	same.Playlist = nil
	s.Capture(same)
	other := st.request(3, problemkind.PictureBlack())
	other.Playlist, other.Server = nil, netip.MustParseAddr("192.0.2.2")
	s.Capture(other)
	third := st.request(4, problemkind.PictureBlack())
	third.Playlist, third.Server = nil, netip.MustParseAddr("192.0.2.3")
	s.Capture(third)
	// What somebody asked for is made all the same.
	asked := st.request(5)
	asked.Playlist, asked.Manual = nil, true
	s.Capture(asked)
	if len(s.List(id.StreamOf(5))) != 1 {
		t.Fatal("a capture that was asked for was turned away for those being made")
	}
	close(block)
	s.Wait()
	if len(s.List(id.StreamOf(1))) != 1 || len(s.List(id.StreamOf(2))) != 0 || len(s.List(id.StreamOf(3))) != 1 || len(s.List(id.StreamOf(4))) != 0 {
		t.Fatalf("captures of streams 1 to 4: %d %d %d %d", len(s.List(id.StreamOf(1))), len(s.List(id.StreamOf(2))), len(s.List(id.StreamOf(3))), len(s.List(id.StreamOf(4))))
	}
	if u := s.Usage(); u.Refused != 2 || u.Made != 3 || u.Bytes == 0 {
		t.Errorf("usage: %+v", u)
	}

	// When there is too much, what nobody confirmed goes first.
	s.Delete(s.List(id.StreamOf(5))[0].ID)
	s.Confirm(id.StreamOf(3), []problemkind.Kind{problemkind.PictureBlack()})
	s.mu.Lock()
	s.o.MaxBytes = s.captures[s.listID(id.StreamOf(3))].Bytes + 1
	s.mu.Unlock()
	s.Sweep()
	if len(s.List(id.StreamOf(1))) != 0 || len(s.List(id.StreamOf(3))) != 1 {
		t.Errorf("after making room: %d unconfirmed, %d confirmed", len(s.List(id.StreamOf(1))), len(s.List(id.StreamOf(3))))
	}
}

// listID is the id of the capture of a stream; the lock is held.
func (s *Store) listID(streamID id.Stream) ID {
	for id, c := range s.captures {
		if c.StreamID == streamID {
			return id
		}
	}
	return ID{}
}

func TestTheSameProblemIsCapturedOnce(t *testing.T) {
	st := newStream(t)
	s := open(t, 0)
	short := func(id int64, kinds ...problemkind.Kind) probe.CaptureRequest {
		rq := st.request(id, kinds...)
		rq.Window, rq.Playlist, rq.At = rq.Window[len(rq.Window)-2:], nil, s.now()
		return rq
	}
	s.Capture(short(7, problemkind.PTSDiscontinuity()))
	s.Wait()
	s.Confirm(id.StreamOf(7), []problemkind.Kind{problemkind.PTSDiscontinuity()})
	// An hour later the stream has the same thing wrong: no second capture.
	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	s.Capture(short(7, problemkind.PTSDiscontinuity()))
	s.Wait()
	if len(s.List(id.StreamOf(7))) != 1 {
		t.Fatalf("the same problem of the same stream was captured %d times", len(s.List(id.StreamOf(7))))
	}
	// Something else wrong with it is another matter, and so is another stream.
	s.Capture(short(7, problemkind.PictureBlack()))
	s.Wait()
	s.Capture(short(8, problemkind.PTSDiscontinuity()))
	s.Wait()
	if len(s.List(id.StreamOf(7))) != 2 || len(s.List(id.StreamOf(8))) != 1 {
		t.Fatalf("captures: %d of stream 7, %d of stream 8", len(s.List(id.StreamOf(7))), len(s.List(id.StreamOf(8))))
	}
	// A day later it is captured again.
	s.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	s.Capture(short(7, problemkind.PTSDiscontinuity()))
	s.Wait()
	if len(s.List(id.StreamOf(7))) != 3 {
		t.Errorf("a day later: %d captures", len(s.List(id.StreamOf(7))))
	}

	// With too much kept, the common goes before the rare: of three
	// captures of breaks in the timestamps and one of a black picture,
	// room is made by dropping the oldest of the three.
	s.Confirm(id.StreamOf(7), []problemkind.Kind{problemkind.PTSDiscontinuity(), problemkind.PictureBlack()})
	s.Confirm(id.StreamOf(8), []problemkind.Kind{problemkind.PTSDiscontinuity()})
	var total int64
	for _, c := range s.List(id.StreamOf(0)) {
		if c.State != capturestate.Kept() {
			t.Fatalf("not kept: %+v", c)
		}
		total += c.Bytes
	}
	s.mu.Lock()
	s.o.MaxBytes = total - 1
	s.mu.Unlock()
	s.now = time.Now
	s.Sweep()
	left := map[problemkind.Kind]int{}
	for _, c := range s.List(id.StreamOf(0)) {
		left[c.Kinds[0]]++
	}
	if left[problemkind.PictureBlack()] != 1 || left[problemkind.PTSDiscontinuity()] != 2 {
		t.Errorf("after making room: %v", left)
	}
}
