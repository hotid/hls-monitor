// Package hlstest is a small fake HLS origin: a live sliding-window stream
// built from a few embedded sample segments, with switches to break it in
// the ways a monitor is expected to notice. It backs the tests and the
// development tool cmd/hls-fake-origin.
package hlstest

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Assets holds the sample media: assets/ts/seg{0,1,2}.ts and
// assets/fmp4/{init.mp4,seg{0,1,2}.m4s}. Every segment is 2 seconds of
// 160x90 H.264 at 25 fps with stereo AAC; the three are consecutive.
//
//go:embed assets
var Assets embed.FS

const (
	samples    = 3
	SegmentDur = 2.0
)

// Fault changes how one segment is served.
type Fault struct {
	Status   int           // respond with this status instead of the segment
	Delay    time.Duration // wait before answering
	Truncate int           // serve only this many bytes, with the full Content-Length
	Corrupt  func([]byte) []byte
}

// Options configures an Origin.
type Options struct {
	FMP4     bool // serve fragmented MP4 instead of MPEG-TS
	Window   int  // segments kept in the playlist, default 5
	Variants int  // variants in the master playlist, default 2
	// NoDiscontinuity omits the EXT-X-DISCONTINUITY tag where the sample
	// loop restarts, which makes timestamps jump unannounced.
	NoDiscontinuity bool
	// NoProgramDateTime omits EXT-X-PROGRAM-DATE-TIME.
	NoProgramDateTime bool
	Start             time.Time
}

// Origin is the fake stream. All methods are safe for concurrent use.
type Origin struct {
	opt Options

	mu       sync.Mutex
	next     uint64 // sequence number of the next segment to publish
	first    uint64 // sequence number of the oldest segment in the window
	ended    bool
	down     int // when non-zero, playlists answer with this status
	garbage  bool
	faults   map[uint64]Fault
	variants int
	requests map[string]int
	rangeReq map[string]string
	noRange  bool
	backend  string
}

// New creates an origin with no segments published yet.
func New(opt Options) *Origin {
	if opt.Window <= 0 {
		opt.Window = 5
	}
	if opt.Variants <= 0 {
		opt.Variants = 2
	}
	if opt.Start.IsZero() {
		opt.Start = time.Now().Add(-time.Hour).Truncate(time.Second)
	}
	return &Origin{opt: opt, faults: map[uint64]Fault{}, variants: opt.Variants, requests: map[string]int{}, rangeReq: map[string]string{}}
}

// Publish appends n segments to the stream, sliding the window.
func (o *Origin) Publish(n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next += uint64(n)
	if o.next-o.first > uint64(o.opt.Window) {
		o.first = o.next - uint64(o.opt.Window)
	}
}

// Rewind moves the media sequence back by n, as a restarted packager would.
func (o *Origin) Rewind(n uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next -= min(n, o.next)
	o.first = 0
	if o.next > uint64(o.opt.Window) {
		o.first = o.next - uint64(o.opt.Window)
	}
}

// End closes the stream with EXT-X-ENDLIST.
func (o *Origin) End() { o.set(func() { o.ended = true }) }

// SetDown makes playlists answer with the given status; zero restores them.
func (o *Origin) SetDown(status int) { o.set(func() { o.down = status }) }

// SetGarbage makes playlists return an HTML page instead of a playlist.
func (o *Origin) SetGarbage(on bool) { o.set(func() { o.garbage = on }) }

// SetVariants changes how many variants the master playlist lists.
func (o *Origin) SetVariants(n int) { o.set(func() { o.variants = n }) }

// SetNoRange makes the origin ignore Range headers and always answer with
// the whole segment, as some servers do.
func (o *Origin) SetNoRange(on bool) { o.set(func() { o.noRange = on }) }

// RangeRequested returns the Range header of the last request for a path.
func (o *Origin) RangeRequested(path string) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.rangeReq[path]
}

// SetBackend sets where the balancer path /lb/... redirects: requests for
// /lb/x answer 302 to base + "/x". It models a CDN entry point that sends
// clients to one of several servers.
func (o *Origin) SetBackend(base string) { o.set(func() { o.backend = base }) }

// SetFault installs a fault for one sequence number in every variant.
func (o *Origin) SetFault(seq uint64, f Fault) { o.set(func() { o.faults[seq] = f }) }

// ClearFaults removes all segment faults.
func (o *Origin) ClearFaults() { o.set(func() { o.faults = map[uint64]Fault{} }) }

// Last returns the sequence number of the newest published segment.
func (o *Origin) Last() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.next - 1
}

// Requests reports how many times a path was requested.
func (o *Origin) Requests(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests[path]
}

func (o *Origin) set(f func()) {
	o.mu.Lock()
	f()
	o.mu.Unlock()
}

// Run publishes one segment every SegmentDur until ctx is done.
func (o *Origin) Run(ctx context.Context) {
	t := time.NewTicker(time.Duration(SegmentDur * float64(time.Second)))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.Publish(1)
		}
	}
}

func (o *Origin) ext() string {
	if o.opt.FMP4 {
		return "m4s"
	}
	return "ts"
}

// Handler serves /master.m3u8, /v{n}/index.m3u8, /v{n}/seg-{seq}.{ts|m4s}
// and /v{n}/init.mp4, plus /lb/... which redirects to the backend set with
// SetBackend.
func (o *Origin) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.requests[r.URL.Path]++
		down, garbage := o.down, o.garbage
		o.mu.Unlock()

		if rest, ok := strings.CutPrefix(r.URL.Path, "/lb/"); ok {
			o.mu.Lock()
			backend := o.backend
			o.mu.Unlock()
			http.Redirect(w, r, backend+"/"+rest, http.StatusFound)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		isPlaylist := strings.HasSuffix(path, ".m3u8")
		if isPlaylist && down != 0 {
			http.Error(w, http.StatusText(down), down)
			return
		}
		if isPlaylist && garbage {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>Service temporarily unavailable</body></html>")
			return
		}
		if path == "master.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, o.master())
			return
		}
		dir, file, ok := strings.Cut(path, "/")
		if !ok || !strings.HasPrefix(dir, "v") {
			http.NotFound(w, r)
			return
		}
		switch {
		case file == "index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, o.media())
		case file == "init.mp4" && o.opt.FMP4:
			o.serveAsset(w, "assets/fmp4/init.mp4", Fault{})
		case strings.HasPrefix(file, "seg-"):
			num := strings.TrimSuffix(strings.TrimPrefix(file, "seg-"), "."+o.ext())
			seq, err := strconv.ParseUint(num, 10, 64)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			o.mu.Lock()
			f := o.faults[seq]
			o.mu.Unlock()
			name := fmt.Sprintf("assets/ts/seg%d.ts", seq%samples)
			if o.opt.FMP4 {
				name = fmt.Sprintf("assets/fmp4/seg%d.m4s", seq%samples)
			}
			o.mu.Lock()
			o.rangeReq[r.URL.Path] = r.Header.Get("Range")
			noRange := o.noRange
			o.mu.Unlock()
			if rng := r.Header.Get("Range"); rng != "" && !noRange && f.Status == 0 && f.Delay == 0 && f.Truncate == 0 && f.Corrupt == nil {
				o.serveRange(w, name, rng)
				return
			}
			o.serveAsset(w, name, f)
		default:
			http.NotFound(w, r)
		}
	})
}

// serveRange answers a "bytes=a-b" request with 206 and Content-Range.
func (o *Origin) serveRange(w http.ResponseWriter, name, rng string) {
	data, err := Assets.ReadFile(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var a, b int
	if n, _ := fmt.Sscanf(rng, "bytes=%d-%d", &a, &b); n != 2 || a < 0 || a > b || a >= len(data) {
		http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	b = min(b, len(data)-1)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(data)))
	w.Header().Set("Content-Length", strconv.Itoa(b-a+1))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(data[a : b+1])
}

func (o *Origin) serveAsset(w http.ResponseWriter, name string, f Fault) {
	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}
	if f.Status != 0 {
		http.Error(w, http.StatusText(f.Status), f.Status)
		return
	}
	data, err := Assets.ReadFile(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if f.Corrupt != nil {
		data = f.Corrupt(append([]byte(nil), data...))
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if f.Truncate > 0 && f.Truncate < len(data) {
		data = data[:f.Truncate]
	}
	_, _ = w.Write(data)
}

func (o *Origin) master() string {
	o.mu.Lock()
	n := o.variants
	o.mu.Unlock()
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:6\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for i := range n {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=160x90,FRAME-RATE=25.000,CODECS=\"avc1.64000b,mp4a.40.2\"\n",
			200000*(i+1), 150000*(i+1))
		fmt.Fprintf(&b, "v%d/index.m3u8\n", i)
	}
	return b.String()
}

func (o *Origin) media() string {
	o.mu.Lock()
	first, next, ended := o.first, o.next, o.ended
	o.mu.Unlock()

	var b strings.Builder
	version := 3
	if o.opt.FMP4 {
		version = 7
	}
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n", version, int(SegmentDur), first)
	// Each restart of the sample loop is a discontinuity.
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", (first+samples-1)/samples)
	if o.opt.FMP4 {
		b.WriteString("#EXT-X-MAP:URI=\"init.mp4\"\n")
	}
	for seq := first; seq < next; seq++ {
		if seq%samples == 0 && seq > 0 && !o.opt.NoDiscontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if !o.opt.NoProgramDateTime {
			t := o.opt.Start.Add(time.Duration(float64(seq) * SegmentDur * float64(time.Second)))
			fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n", t.UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\nseg-%d.%s\n", SegmentDur, seq, o.ext())
	}
	if ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}
