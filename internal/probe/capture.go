package probe

import (
	"context"
	"net/netip"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

// What a check finds is told in figures; what it was found in is gone a
// few seconds later, when the segment leaves the playlist. A capturer,
// when there is one, is handed the segment the moment something about it
// looks wrong — not when an incident is made of it, a minute and several
// looks later — and saves it with its neighbours, to be taken apart at
// leisure. It is optional: a checker without one captures nothing.

// CaptureSegment is one segment of the playlist at the moment of a capture.
type CaptureSegment struct {
	Seq      uint64
	URL      string
	Duration float64
	// Offset and Limit are the byte range of the segment, when it is one.
	Offset, Limit int64
	// InitURL is the initialization section it needs, for fragmented MP4.
	InitURL       string
	Discontinuity bool
	Encrypted     bool
}

// CaptureRequest is what a checker hands over.
type CaptureRequest struct {
	StreamID id.Stream
	Variant  variantkey.Key
	// Kinds are the problems the segment is suspected of; Manual tells
	// that somebody asked for the capture instead.
	Kinds  []problemkind.Kind
	Manual bool
	At     time.Time

	PlaylistURL string
	Playlist    []byte
	MasterURL   string
	Master      []byte
	// TargetDuration is that of the playlist, in seconds.
	TargetDuration float64
	// Window is the playlist's segments, oldest first, and Seq the one
	// the finding is about.
	Window []CaptureSegment
	Seq    uint64

	// Evidence is the very bytes the check read of that segment: the
	// beginning of it, or with EvidenceWhole all of it. Status, Server
	// and Software describe the answer they came in.
	Evidence      []byte
	EvidenceWhole bool
	Status        int
	Server        netip.Addr
	Software      string

	// Get fetches an address the way the checker does: with the stream's
	// headers, where they belong, and its rules about private networks.
	Get func(ctx context.Context, url string, offset, limit int64) (*httpx.Response, error)
}

// Capturer saves segments around a finding. Both methods return at once.
type Capturer interface {
	// Capture asks for the segments around one to be saved. It may be
	// refused: there is a limit to how much is captured at a time.
	Capture(CaptureRequest)
	// Confirm tells that problems of these kinds were reported for a
	// stream: what was captured on suspicion of them is to be kept.
	Confirm(streamID id.Stream, kinds []problemkind.Kind)
}

// Capturable reports whether a problem is one whose segments are worth
// keeping: something in the contents, the container or the timestamps. For
// a playlist that cannot be reached there is nothing to keep but what the
// incident already has.
func Capturable(kind problemkind.Kind) bool {
	switch kind {
	case problemkind.MediaUndecodable(), problemkind.MediaParse(), problemkind.TSSync(), problemkind.TSContinuity(), problemkind.TSTransport(), problemkind.TSNoTables(),
		problemkind.NoKeyframeStart(), problemkind.DurationMismatch(), problemkind.PTSDiscontinuity(), problemkind.AVDesync(), problemkind.MediaParamsChanged(),
		problemkind.SegmentTruncated():
		return true
	}
	// The stream having no sound at all is a matter of record, not of segments.
	return PictureProblem(kind) && kind != problemkind.AudioAbsent()
}

// captureMaxBody bounds one segment fetched for a capture.
const captureMaxBody = 64 << 20

// CaptureNow asks for the segments of the stream to be captured at the
// next cycle, whatever the checks find. It is safe for concurrent use.
func (c *Checker) CaptureNow() { c.captureNow.Store(true) }

// suspicions names what the look or the listen just made began to suspect.
func (c *Checker) suspicions() []problemkind.Kind {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := &c.looks
	var out []problemkind.Kind
	for _, s := range []struct {
		run  run
		kind problemkind.Kind
	}{
		{l.black, problemkind.PictureBlack()}, {l.same, problemkind.PictureFrozen()}, {l.silent, problemkind.AudioSilent()},
		{l.oneSided, problemkind.AudioChannelSilent()}, {l.deaf, problemkind.AudioUndecodable()}, {l.nothing, problemkind.MediaUndecodable()},
	} {
		if s.run.n == 1 {
			out = append(out, s.kind)
		}
	}
	return out
}

// capture hands a segment and its playlist to the capturer.
func (c *Checker) capture(v Variant, plURL string, plResp, entry *httpx.Response, st *variantState, s plSegment, segURL string, resp *httpx.Response, kinds []problemkind.Kind, manual, whole bool) {
	rq := CaptureRequest{
		StreamID: c.t.ID, Variant: v.Key, Kinds: kinds, Manual: manual, At: c.now(),
		PlaylistURL: plURL, Seq: s.seq, TargetDuration: st.windowTD,
		Evidence: append([]byte(nil), resp.Body...), EvidenceWhole: whole, Status: resp.Status,
	}
	rq.Server = resp.RemoteAddr.Addr()
	rq.Software = resp.Header.Get("Server")
	if plResp != nil {
		rq.Playlist = append([]byte(nil), plResp.Body...)
	}
	if entry != nil && entry != plResp {
		rq.MasterURL, rq.Master = c.t.URL, append([]byte(nil), entry.Body...)
	}
	for _, w := range st.window {
		u, err := resolve(plURL, w.seg.URI)
		if err != nil {
			continue
		}
		cs := CaptureSegment{Seq: w.seq, URL: u, Duration: w.seg.Duration, Offset: w.seg.Offset, Limit: w.seg.Limit,
			Discontinuity: w.seg.Discontinuity, Encrypted: w.encrypted}
		if w.initMap != nil && w.initMap.URI != "" {
			cs.InitURL, _ = resolve(plURL, w.initMap.URI)
		}
		rq.Window = append(rq.Window, cs)
	}
	// The capturer fetches after the cycle is over, and on its own time:
	// what it needs of the checker is taken now.
	hc, headers, headersFor, inside := c.hc, c.t.Headers, c.t.URL, c.inside
	rq.Get = func(ctx context.Context, url string, offset, limit int64) (*httpx.Response, error) {
		return hc.Get(ctx, httpx.Request{URL: url, Headers: headers, HeadersFor: headersFor, MaxBody: captureMaxBody,
			Reach: Reach(false, inside), RangeOffset: offset, RangeLength: limit})
	}
	c.Capturer.Capture(rq)
}

// confirmCaptures tells the capturer which of the problems worth
// capturing were reported in this cycle.
func (c *Checker) confirmCaptures(cy *cycle) {
	if c.Capturer == nil {
		return
	}
	cy.mu.Lock()
	var kinds []problemkind.Kind
	for _, p := range cy.probs {
		if Capturable(p.Kind) {
			kinds = append(kinds, p.Kind)
		}
	}
	cy.mu.Unlock()
	if len(kinds) > 0 {
		c.Capturer.Confirm(c.t.ID, kinds)
	}
}
