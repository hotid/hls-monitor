package probe

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/types/container"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
	"github.com/hotid/hls-monitor/internal/types/videorange"
)

// minBitrateSpan is the shortest segment, in seconds, whose bitrate is
// compared with the declared one.
const minBitrateSpan = 1.0

// headerBytes is how much of a segment is requested at the header level: a whole
// number of MPEG-TS packets, enough for the tables and the first frames.
const headerBytes = 188 * 700

// segAgg averages per-segment observations into per-cycle metrics.
type segAgg struct {
	// partial is set when only the beginning of each segment was fetched.
	partial                                   bool
	sourceChanges                             int
	checked, ok                               int
	dns, connect, tls, ttfb, total, size, bps float64
	maxTotal, minBps, maxRatio                float64
	// bits and span are the sizes and the lengths of the segments whose
	// size is known: their bitrate is one over the other, so that a stub
	// of a frame or two weighs what it lasts.
	bits, span float64

	analysed                                     int
	durDiff, ptsGap, avOffset                    float64
	hasAV                                        bool
	syncErrors, ccErrors, keyframeStarts, videos int
	maxGOP, fps, vBitrate, aBitrate              float64
	width, height                                int
	aTracks                                      int
	// What the program clock of MPEG-TS said: the longest time between
	// two references and the least lead of a frame over the clock.
	clocked, hasLead bool
	// clockFault is what was last found wrong with the clock.
	clockFault string
	// subtitles tells that segments of subtitles were read, cues how
	// many cues they had.
	subtitles bool
	// hdr tells a picture of high dynamic range: 1 for PQ, 2 for HLG.
	hdr          int
	cues         int
	pcrGap, lead float64
}

// segRun is what the segments of one variant checked in a cycle share:
// where they come from, the totals they add to, the memory held for the
// one being read.
type segRun struct {
	c             *Checker
	cy            *cycle
	v             Variant
	plURL         string
	st            *variantState
	plResp, entry *httpx.Response
	failed        map[uint64]bool
	// header tells that only the beginning of every segment is fetched.
	header      bool
	agg         *segAgg
	startupDone bool
	// held is what the body of the segment being looked at takes of the
	// memory budget; it is given back when the next one is started.
	held int64
}

// segCheck is the check of one segment.
type segCheck struct {
	*segRun
	s   plSegment
	rec Segment
	// suspected is what makes the segment worth capturing, got the answer
	// it came in, once there is one, and url its address.
	suspected []problemkind.Kind
	got       *httpx.Response
	// asked is how many bytes of the segment were asked for, when not all.
	asked int64
	// sized is set when the size of the whole segment is known: it came
	// whole, or the server told the size of what it sent a part of.
	sized bool
	url   string
}

// segWork is what is done with a segment besides checking it.
type segWork struct {
	// look at its picture, listen to its sound, measure its loudness.
	look, listen, loud bool
}

func (c *Checker) checkSegments(ctx context.Context, cy *cycle, v Variant, plURL string, selected []plSegment, st *variantState, plResp, entry *httpx.Response, failed map[uint64]bool) {
	if len(selected) == 0 {
		return
	}
	header := c.t.Level == level.Header()
	r := &segRun{c: c, cy: cy, v: v, plURL: plURL, st: st, plResp: plResp, entry: entry, failed: failed,
		header: header, agg: &segAgg{minBps: math.MaxFloat64, partial: header}}
	defer func() { bodies.release(r.held) }()
	for _, s := range selected {
		bodies.release(r.held)
		r.held = 0
		if ctx.Err() != nil {
			return
		}
		if s.seg.Gap {
			continue // EXT-X-GAP: the segment is declared missing on purpose
		}
		if !r.checkOne(ctx, s) {
			return // the cycle was cancelled while waiting for memory
		}
	}
	r.finish()
}

// checkOne checks one segment. It tells false when the cycle was
// cancelled while it waited for memory to read the segment into.
func (r *segRun) checkOne(ctx context.Context, s plSegment) bool {
	k := r.begin(s)
	u, err := resolve(r.plURL, s.seg.URI)
	if err != nil {
		k.rec.URL = s.seg.URI
		k.rec.Error = err.Error()
		k.flag(problemkind.SegmentUnreachable(), severity.Crit(), "segment %d has a bad URI: %v", s.seq, err)
		r.failed[s.seq] = true
		r.agg.checked++
		k.emit()
		return true
	}
	k.rec.URL, k.url = u, u

	// Initialization section for fMP4, fetched once per URL.
	var init *media.Init
	if s.initMap != nil && s.initMap.URI != "" {
		init = r.c.initSection(ctx, r.cy, r.v, r.plURL, s, r.st)
	}

	work := r.work(s)
	rq, timeout := r.request(u, s, work)
	if !r.header {
		if r.held = bodies.acquire(ctx, expectedBody(r.v, s)); r.held == 0 {
			return false
		}
	}
	resp, err := r.c.get(ctx, rq, timeout)
	k.got = resp
	k.asked = rq.RangeLength
	if resp != nil && r.held > 0 && resp.Size > r.held {
		bodies.grow(resp.Size - r.held)
		r.held = resp.Size
	}
	r.agg.checked++
	size := k.record(resp)
	r.cy.network(r.v.Key, resp)
	if k.broken(resp, err) {
		r.failed[s.seq] = true
		r.st.prev = prevSegment{}
		k.emit()
		return true
	}
	k.transfer(resp, size)
	if !k.ownBytes(resp) {
		// What came is not the segment: nothing is said of its contents.
		r.st.prev = prevSegment{}
		k.emit()
		return true
	}
	info := k.contents(resp, init)
	k.decode(ctx, work, resp, info)
	k.emit()
	return true
}

// begin starts the record of a segment.
func (r *segRun) begin(s plSegment) *segCheck {
	k := &segCheck{segRun: r, s: s, rec: Segment{
		Variant:       r.v.Key,
		Seq:           s.seq,
		Time:          r.c.now(),
		Duration:      s.seg.Duration,
		Discontinuity: s.seg.Discontinuity,
	}}
	if !s.at.IsZero() {
		t := s.at
		k.rec.ProgramTime = &t
	}
	return k
}

// flag tells a problem of the segment.
func (k *segCheck) flag(kind problemkind.Kind, sev severity.Severity, format string, args ...any) {
	k.cy.problem(kind, sev, k.v.Key, format, args...)
	k.rec.Problems = append(k.rec.Problems, kind)
	if Capturable(kind) {
		k.suspected = append(k.suspected, kind)
	}
	// Whether the playlist announced a break is the question here, so its
	// text, now and before, belongs to the finding.
	if (kind == problemkind.PTSDiscontinuity() || kind == problemkind.MediaParamsChanged()) && k.plResp != nil {
		k.cy.snapshot(k.v.Key, k.plURL, k.plResp.Body)
	}
}

// emit files the record, attaches the segment to its problems and has it
// captured when it is worth it.
func (k *segCheck) emit() {
	rec := k.rec
	for _, kind := range rec.Problems {
		k.cy.detail(kind, k.v.Key,
			detail.SegmentSequence(rec.Seq), detail.SegmentURL(rec.URL), detail.SegmentDuration(rec.Duration),
			detail.SegmentHTTPStatus(rec.Status), detail.SegmentError(rec.Error),
			detail.SegmentBytes(rec.Size), detail.SegmentFetchedBytes(rec.Fetched))
		if rec.Source != nil {
			k.cy.detail(kind, k.v.Key, detail.SegmentServer(rec.Source.Node()), detail.SegmentSoftware(rec.Source.Headers["Server"]))
		}
	}
	k.cy.segment(rec)
	if k.c.Capturer == nil || k.got == nil || k.s.encrypted {
		return
	}
	// Somebody asked for a capture: the first segment of the picture, or
	// of the stream itself, will do.
	manual := (k.v.Kind == variantkind.Video() || k.v.Kind == variantkind.Media()) && k.c.captureNow.CompareAndSwap(true, false)
	if len(k.suspected) > 0 || manual {
		got := k.got
		whole := !k.header || (got.TotalSize > 0 && int64(len(got.Body)) >= got.TotalSize)
		k.c.capture(k.v, k.plURL, k.plResp, k.entry, k.st, k.s, k.url, got, k.suspected, manual, whole && got.OK() && !got.Truncated)
	}
}

// work decides what else is done with a segment: one of a cycle that is
// due is looked at; one of a playlist of sound alone is listened to,
// where the look found the picture without its sound; and now and then
// three of them, apart, are measured for loudness.
func (r *segRun) work(s plSegment) segWork {
	var w segWork
	w.look = !s.encrypted && r.c.wantsLook(r.v)
	w.listen = !w.look && r.c.wantsListen(r.v, s.encrypted)
	w.loud = !w.look && !w.listen && r.c.wantsLoud(r.v, s.encrypted, r.c.now())
	return w
}

// request makes the request for a segment and the time it may take.
func (r *segRun) request(u string, s plSegment, w segWork) (httpx.Request, time.Duration) {
	timeout := max(r.c.timeout(), time.Duration(3*s.seg.Duration*float64(time.Second)))
	rq := httpx.Request{URL: u, MaxBody: segmentMaxBody, RangeOffset: s.seg.Offset, RangeLength: s.seg.Limit}
	if !r.header {
		return rq, timeout
	}
	// A server that ignores Range answers with the whole segment; MaxBody
	// then stops the download at the same point. A look needs a little
	// more than the beginning: the whole of the first keyframe and a
	// moment of sound.
	head := int64(headerBytes)
	switch {
	case w.look:
		head = r.c.lookSize()
	case w.listen:
		head = lookBytes
	case w.loud:
		// The beginning of a segment of a few megabits holds a fraction
		// of a second of sound; R 128 needs 400 ms at least.
		head = lookBytesMore
	}
	rq.MaxBody = head
	rq.RangeLength = head
	if s.seg.Limit > 0 {
		rq.RangeLength = min(s.seg.Limit, head)
	}
	return rq, timeout
}

// record notes the answer in the record of the segment and tells its size:
// the whole of it, where only its beginning was fetched.
func (k *segCheck) record(resp *httpx.Response) int64 {
	if resp == nil {
		return 0
	}
	size := resp.Size
	k.sized = !resp.Truncated
	if k.header {
		switch {
		case k.s.seg.Limit > 0:
			size, k.sized = k.s.seg.Limit, true
		case resp.TotalSize > 0:
			size, k.sized = resp.TotalSize, true
		case resp.Status == http.StatusPartialContent:
			// A part with its whole not told ("bytes 0-131599/*"): the
			// segment ends here only if less came than was asked for.
			k.sized = resp.Size < k.asked
		}
	}
	rec := &k.rec
	rec.Status = resp.Status
	rec.RemoteAddr = resp.RemoteAddr
	rec.Proto = resp.Proto
	rec.Cache = resp.CacheStatus()
	rec.Size = size
	rec.Fetched = resp.Size
	rec.Partial = k.header
	rec.DNSMs = ms(resp.DNS)
	rec.ConnectMs = ms(resp.Connect)
	rec.TLSMs = ms(resp.TLS)
	rec.TTFBMs = ms(resp.TTFB)
	rec.TotalMs = ms(resp.Total)
	rec.Bps = resp.Bps()
	if resp.Status != 0 {
		ep := endpoint(k.url, resp)
		rec.Source = ep
		k.cy.source(k.v.Key, ep, true)
		if prev := k.st.segSource; prev != nil && prev.Node() != ep.Node() {
			k.agg.sourceChanges++
			k.flag(problemkind.SegmentSource(), severity.Info(), "%s", describeChange("segments", prev, ep))
		}
		k.st.segSource = ep
	}
	return size
}

// broken tells the problems of an answer that brought no segment and
// whether it did not.
func (k *segCheck) broken(resp *httpx.Response, err error) bool {
	seq := k.s.seq
	switch {
	case err != nil && (resp == nil || resp.Status == 0):
		k.rec.Error = err.Error()
		k.flag(problemkind.SegmentUnreachable(), severity.Crit(), "segment %d request failed: %v", seq, err)
		if timedOut(err) {
			k.cy.doubt(problemkind.SegmentUnreachable(), k.v.Key)
		}
	case err != nil:
		k.rec.Error = err.Error()
		k.flag(problemkind.SegmentTruncated(), severity.Crit(), "segment %d download broke off after %d bytes: %v", seq, resp.Size, err)
		if timedOut(err) {
			k.cy.doubt(problemkind.SegmentTruncated(), k.v.Key)
		}
	case !resp.OK():
		k.flag(problemkind.SegmentHTTP(), severity.Crit(), "segment %d returned HTTP %d", seq, resp.Status)
	case resp.LengthMismatch && !k.header:
		k.flag(problemkind.SegmentTruncated(), severity.Crit(), "segment %d is %d bytes, Content-Length declared %d", seq, resp.Size, resp.ContentLength)
	default:
		return false
	}
	return true
}

// transfer adds how the segment came to the totals: the time it took
// against its length, its bitrate against the declared one, and how long
// a player would take to start.
func (k *segCheck) transfer(resp *httpx.Response, size int64) {
	agg, s := k.agg, k.s
	agg.ok++
	agg.dns += resp.DNS.Seconds()
	agg.connect += resp.Connect.Seconds()
	agg.tls += resp.TLS.Seconds()
	agg.ttfb += resp.TTFB.Seconds()
	agg.size += float64(size)
	if !k.header {
		// Timing of the whole download means something only for a whole download.
		agg.total += resp.Total.Seconds()
		agg.bps += resp.Bps()
		agg.maxTotal = max(agg.maxTotal, resp.Total.Seconds())
		agg.minBps = min(agg.minBps, resp.Bps())
	}

	if s.seg.Duration > 0 {
		// A stub of a frame or two, left by an encoder restart, is not
		// slow for taking longer than its 40 ms, and has no bitrate of
		// its own to speak of.
		if !k.header && s.seg.Duration >= minBitrateSpan {
			ratio := resp.Total.Seconds() / s.seg.Duration
			agg.maxRatio = max(agg.maxRatio, ratio)
			if ratio > 1 {
				k.flag(problemkind.SegmentSlow(), severity.Warn(), "segment %d (%.1fs of media) took %.1fs to download", s.seq, s.seg.Duration, resp.Total.Seconds())
				k.cy.doubt(problemkind.SegmentSlow(), k.v.Key)
			}
		}
		bitrate := float64(size*8) / s.seg.Duration
		if k.sized {
			agg.bits += float64(size * 8)
			agg.span += s.seg.Duration
		}
		if bw := k.v.Bandwidth; bw > 0 && k.sized && s.seg.Duration >= minBitrateSpan && bitrate > float64(bw)*k.c.th.BitrateFactor {
			k.flag(problemkind.BitrateExceeds(), severity.Warn(), "segment %d is %.0f kbit/s, playlist declares %.0f kbit/s", s.seq, bitrate/1000, float64(bw)/1000)
			k.cy.detail(problemkind.BitrateExceeds(), k.v.Key, detail.SegmentBitrate(bitrate), detail.DeclaredBandwidth(bw), detail.BitrateRatio(bitrate/float64(bw)), detail.BitrateRatioLimit(k.c.th.BitrateFactor))
		}
	}

	if !k.startupDone && !k.header {
		k.startupDone = true
		startup := k.plResp.Total.Seconds() + resp.Total.Seconds()
		if k.entry != nil {
			startup += k.entry.Total.Seconds()
		}
		k.cy.metric(metricname.StartupSeconds(), k.v.Key, startup)
	}
}

// contents reads what the segment holds: the container of picture and
// sound, or the text of subtitles.
func (k *segCheck) contents(resp *httpx.Response, init *media.Init) *media.Info {
	s, v := k.s, k.v
	whole := k.sized && (!k.header || (k.rec.Size > 0 && k.rec.Fetched >= k.rec.Size))
	if !k.c.blind && !s.encrypted && v.Kind != variantkind.Subtitles() {
		body := resp.Body
		// The level of whole segments reads of their contents what the
		// level below it does: the beginning. The whole of one is parsed
		// at the level that is for it.
		if k.c.t.Level == level.Segments() && len(body) > headerBytes {
			body, whole = body[:headerBytes], false
		}
		info := media.Analyze(body, init)
		k.rec.Media = info
		k.c.judgeMedia(v, s, info, k.st, k.agg, k.flag, whole)
		k.c.noteCoded(v, k.st, info, k.c.now())
		k.c.noteSizes(v, k.st, info, k.c.now())
		return info
	}
	k.st.prev = prevSegment{}
	if v.Kind == variantkind.Subtitles() && !s.encrypted && whole {
		k.subtitles(resp.Body)
	}
	return nil
}

// ownBytes leaves of the answer the bytes of the segment itself, and tells
// false when they are not in it. A segment that is a range of a larger
// file comes as the file from its beginning when the server takes no
// notice of Range.
func (k *segCheck) ownBytes(resp *httpx.Response) bool {
	off, limit := k.s.seg.Offset, k.s.seg.Limit
	if (off == 0 && limit == 0) || resp.Status != http.StatusOK {
		return true
	}
	end := int64(len(resp.Body))
	if limit > 0 {
		end = off + limit
		if k.header {
			end = off + min(limit, k.asked)
		}
	}
	if off >= int64(len(resp.Body)) || end > int64(len(resp.Body)) {
		return false
	}
	resp.Body = resp.Body[off:end]
	return true
}

// subtitles reads a segment of subtitles: the whole of one is read at
// every level that reads segments at all, being text.
func (k *segCheck) subtitles(body []byte) {
	name := strings.ToLower(k.s.seg.URI)
	if q := strings.IndexAny(name, "?#"); q >= 0 {
		name = name[:q]
	}
	if !media.IsWebVTT(body) && !strings.HasSuffix(name, ".vtt") && !strings.HasSuffix(name, ".webvtt") {
		return
	}
	sub, err := media.ParseWebVTT(body)
	if err != nil {
		k.flag(problemkind.SubtitlesInvalid(), severity.Warn(), "segment %d is not valid WebVTT: %v", k.s.seq, err)
		return
	}
	k.agg.subtitles = true
	k.agg.cues += sub.Cues
}

// decode hands the segment to the decoder for what it was taken for.
func (k *segCheck) decode(ctx context.Context, w segWork, resp *httpx.Response, info *media.Info) {
	now := k.c.now()
	if w.look {
		k.c.look(ctx, k.cy, k.v, resp.Body, k.st.initRaw, info, now)
	}
	if w.listen {
		k.c.listen(ctx, k.cy, k.v, resp.Body, k.st.initRaw, info, now)
	}
	if w.loud {
		k.c.measureLoud(ctx, k.cy, k.v, resp.Body, k.st.initRaw, true, now)
	}
	if w.look || w.listen {
		k.suspected = append(k.suspected, k.c.suspicions()...)
	}
}

// finish tells what the segments of the cycle came to together.
func (r *segRun) finish() {
	// A clock that slips once, where an encoder was switched or restarted,
	// plays on; one that is wrong check after check does not on a set-top box.
	switch {
	case r.agg.clockFault != "":
		if r.st.clockRuns++; r.st.clockRuns >= clockRuns {
			r.cy.problem(problemkind.TSClock(), severity.Warn(), r.v.Key, "%s; wrong for %d checks in a row", r.agg.clockFault, r.st.clockRuns)
		}
	case r.agg.clocked:
		r.st.clockRuns = 0
	}
	if r.agg.subtitles {
		r.st.noteCues(r.cy, r.v.Key, r.agg.cues, r.c.now())
	}
	r.c.emitSegmentMetrics(r.cy, r.v, r.agg)
}

// cuesSpan is how far back the cues of subtitles are counted, and cuesFrom
// how much of it must have been seen for the count to say anything: a
// film has minutes without a word.
const (
	cuesSpan = time.Hour
	cuesFrom = 10 * time.Minute
)

type cueCount struct {
	at time.Time
	n  int
}

// noteCues counts the cues of subtitles of the last hour: none in all of
// it are subtitles that are there and empty.
func (st *variantState) noteCues(cy *cycle, key variantkey.Key, n int, now time.Time) {
	if st.cuesSince.IsZero() {
		st.cuesSince = now
	}
	keep := st.cues[:0]
	total := n
	for _, c := range st.cues {
		if now.Sub(c.at) < cuesSpan {
			keep = append(keep, c)
			total += c.n
		}
	}
	st.cues = append(keep, cueCount{now, n})
	if now.Sub(st.cuesSince) >= cuesFrom {
		cy.metric(metricname.SubtitleCuesHour(), key, float64(total))
	}
}

// initSection returns the parsed initialization section a segment refers
// to, fetching it when the URL differs from the cached one.
func (c *Checker) initSection(ctx context.Context, cy *cycle, v Variant, plURL string, s plSegment, st *variantState) *media.Init {
	u, err := resolve(plURL, s.initMap.URI)
	if err != nil {
		cy.problem(problemkind.InitUnavailable(), severity.Crit(), v.Key, "bad EXT-X-MAP URI %q: %v", s.initMap.URI, err)
		return nil
	}
	cacheKey := u
	if s.initMap.Limit > 0 {
		cacheKey += "#" + time.Duration(s.initMap.Offset).String() + "+" + time.Duration(s.initMap.Limit).String()
	}
	if st.initURL == cacheKey {
		return st.init
	}
	resp, err := c.get(ctx, httpx.Request{URL: u, MaxBody: 16 << 20, RangeOffset: s.initMap.Offset, RangeLength: s.initMap.Limit}, c.timeout())
	switch {
	case err != nil:
		cy.problem(problemkind.InitUnavailable(), severity.Crit(), v.Key, "initialization section request failed: %v", err)
		if timedOut(err) {
			cy.doubt(problemkind.InitUnavailable(), v.Key)
		}
		return nil
	case !resp.OK():
		cy.problem(problemkind.InitUnavailable(), severity.Crit(), v.Key, "initialization section returned HTTP %d", resp.Status)
		return nil
	}
	st.initURL = cacheKey
	st.init = nil
	st.initRaw = resp.Body
	if c.t.Level == level.Container() || c.t.Level == level.Header() {
		in, err := media.ParseInit(resp.Body)
		if err != nil {
			cy.problem(problemkind.MediaParse(), severity.Warn(), v.Key, "cannot parse initialization section: %v", err)
		}
		st.init = in
	}
	return st.init
}

// judgeMedia turns container analysis of one segment into problems and
// updates the continuity state.
func (c *Checker) judgeMedia(v Variant, s plSegment, info *media.Info, st *variantState, agg *segAgg, flag func(kind problemkind.Kind, sev severity.Severity, format string, args ...any), whole bool) {
	agg.analysed++
	// With only the beginning of a segment, a parser running off its end is expected.
	m := &mediaCheck{c: c, v: v, s: s, info: info, st: st, agg: agg, flag: flag, whole: whole, partial: agg.partial}
	if info.Error != "" && !m.partial {
		flag(problemkind.MediaParse(), severity.Warn(), "segment %d: %s", s.seq, info.Error)
	}
	if info.Container == container.Unknown() {
		flag(problemkind.MediaParse(), severity.Warn(), "segment %d is not a recognised media container", s.seq)
		st.prev = prevSegment{}
		return
	}
	if info.Container == container.TS() {
		m.transport()
	}
	m.vt, m.at = info.Video(), info.Audio()
	cur := m.tracks()
	m.declared()
	m.duration()
	m.avSync()
	m.joint(cur)
}

// mediaCheck is the judging of what the container of one segment holds.
type mediaCheck struct {
	c       *Checker
	v       Variant
	s       plSegment
	info    *media.Info
	st      *variantState
	agg     *segAgg
	flag    func(kind problemkind.Kind, sev severity.Severity, format string, args ...any)
	whole   bool
	partial bool
	vt, at  *media.Track
}

// transport judges the packets of MPEG-TS: sync, continuity counters,
// error flags, the program clock and the tables.
func (m *mediaCheck) transport() {
	info, agg, seq := m.info, m.agg, m.s.seq
	agg.syncErrors += info.SyncErrors
	agg.ccErrors += info.CCErrors
	if info.SyncErrors > 0 {
		m.flag(problemkind.TSSync(), severity.Crit(), "segment %d: %d MPEG-TS sync errors", seq, info.SyncErrors)
	}
	if info.CCErrors > 0 {
		m.flag(problemkind.TSContinuity(), severity.Warn(), "segment %d: %d continuity counter errors", seq, info.CCErrors)
	}
	if info.TransportErrors > 0 {
		m.flag(problemkind.TSTransport(), severity.Warn(), "segment %d: %d packets flagged with transport errors", seq, info.TransportErrors)
	}
	if k := info.Clock; k != nil && k.PCRs >= 2 {
		m.clock(k)
	}
	if !info.HasPAT || !info.HasPMT {
		m.flag(problemkind.TSNoTables(), severity.Warn(), "segment %d has no PAT/PMT, it cannot be decoded on its own", seq)
	}
}

// clock notes what the program clock said; a fault is told once it
// repeats, when the cycle is over.
func (m *mediaCheck) clock(k *media.TSClock) {
	agg := m.agg
	agg.clocked = true
	agg.pcrGap = max(agg.pcrGap, k.MaxPCRGap)
	var wrong []string
	if k.MaxPCRGap > pcrGapLimit {
		wrong = append(wrong, fmt.Sprintf("%.0f ms between two program clock references, %.0f ms allowed", k.MaxPCRGap*1000, pcrGapLimit*1000))
	}
	if k.PCRJumps > 0 {
		wrong = append(wrong, fmt.Sprintf("the program clock jumps %d times with no discontinuity announced", k.PCRJumps))
	}
	if k.HasLead {
		if !agg.hasLead || k.MinLead < agg.lead {
			agg.lead, agg.hasLead = k.MinLead, true
		}
		if k.MinLead < 0 {
			wrong = append(wrong, fmt.Sprintf("a frame comes %.0f ms after the program clock passed the time to decode it", -k.MinLead*1000))
		}
	}
	if len(wrong) > 0 {
		agg.clockFault = fmt.Sprintf("segment %d: %s", m.s.seq, strings.Join(wrong, "; "))
	}
}

// tracks adds what the picture and the sound are to the totals and
// tells where the segment ends, for the next one to be joined to.
func (m *mediaCheck) tracks() prevSegment {
	vt, at, agg, s := m.vt, m.at, m.agg, m.s
	// Where the segment ends is measured when all of it was read — a short
	// segment fits into the beginning that is requested — and otherwise
	// derived from where it starts and how long the playlist says it is.
	guessEnd := m.partial && !m.whole
	cur := prevSegment{ok: true, seq: s.seq, wrap: m.info.TimeWrap, estimated: guessEnd}
	if vt != nil {
		agg.videos++
		agg.width, agg.height = vt.Width, vt.Height
		if !m.partial {
			agg.fps = vt.FPS
			agg.vBitrate += vt.Bitrate()
			agg.maxGOP = max(agg.maxGOP, vt.MaxGOP)
		}
		if vt.FirstKeyframe {
			agg.keyframeStarts++
		} else {
			m.flag(problemkind.NoKeyframeStart(), severity.Warn(), "segment %d does not start with a keyframe", s.seq)
		}
		cur.width, cur.height = vt.Width, vt.Height
		switch vt.Range {
		case videorange.PQ():
			agg.hdr = 1
		case videorange.HLG():
			agg.hdr = 2
		}
		if vt.HasTiming {
			cur.hasV, cur.vEnd = true, vt.EndPTS
			if guessEnd {
				cur.vEnd = vt.StartPTS + s.seg.Duration
			}
		}
	}
	if at != nil {
		agg.aTracks++
		if !m.partial {
			agg.aBitrate += at.Bitrate()
		}
		if at.HasTiming {
			cur.hasA, cur.aEnd = true, at.EndPTS
			if guessEnd {
				cur.aEnd = at.StartPTS + s.seg.Duration
			}
		}
	}
	return cur
}

// declared judges the segment against what the master playlist says of
// the variant.
func (m *mediaCheck) declared() {
	if m.v.Kind != variantkind.Video() {
		return
	}
	if diff := declaredDiffers(m.v, m.vt, m.at, m.partial); len(diff) > 0 {
		m.flag(problemkind.DeclaredMismatch(), severity.Warn(), "segment %d: %s", m.s.seq, strings.Join(diff, "; "))
	}
}

// duration judges EXTINF against the real media duration. Video is
// authoritative; audio frames rarely line up with segment boundaries.
func (m *mediaCheck) duration() {
	s := m.s
	ref := refTrack(m.vt, m.at)
	if ref == nil || s.seg.Duration <= 0 || m.partial {
		return
	}
	diff := ref.Duration() - s.seg.Duration
	if math.Abs(diff) > math.Abs(m.agg.durDiff) {
		m.agg.durDiff = diff
	}
	if math.Abs(diff) > m.c.th.DurationMismatch {
		m.flag(problemkind.DurationMismatch(), severity.Warn(), "segment %d: playlist says %.3fs, media lasts %.3fs", s.seq, s.seg.Duration, ref.Duration())
	}
}

// avSync judges where the picture starts against the sound.
func (m *mediaCheck) avSync() {
	vt, at := m.vt, m.at
	if vt == nil || at == nil || !vt.HasTiming || !at.HasTiming {
		return
	}
	off := vt.StartPTS - at.StartPTS
	m.agg.hasAV = true
	if math.Abs(off) > math.Abs(m.agg.avOffset) {
		m.agg.avOffset = off
	}
	if math.Abs(off) > m.c.th.AVOffset {
		m.flag(problemkind.AVDesync(), severity.Warn(), "segment %d: video starts %+.3fs relative to audio", m.s.seq, off)
	}
}

// joint judges the segment against the one before it in the playlist,
// when there is no discontinuity tag between them, and remembers it for
// the next.
func (m *mediaCheck) joint(cur prevSegment) {
	info, st, s, vt, at := m.info, m.st, m.s, m.vt, m.at
	if m.whole && info.Container == container.TS() {
		cur.lastCC = info.LastCC
	}
	if p := st.prev; p.ok && p.seq+1 == s.seq && !s.seg.Discontinuity {
		// Within a segment the parser counts the breaks; the joint between
		// two segments is checked here. Without a discontinuity tag the
		// counters must run on (RFC 8216, section 3).
		if n, pid, was, now := ccBreaks(p.lastCC, info.FirstCC); n > 0 {
			m.agg.ccErrors += n
			m.flag(problemkind.TSContinuity(), severity.Warn(), "segment %d: continuity counters break at the joint with the previous segment on %d PIDs (PID %d: %d after %d)", s.seq, n, pid, now, was)
		}
		if vt != nil && vt.HasTiming {
			m.timestampsGo(p, p.hasV, vt.StartPTS, p.vEnd, "video")
		} else if at != nil && at.HasTiming {
			m.timestampsGo(p, p.hasA, at.StartPTS, p.aEnd, "audio")
		}
		if vt != nil && p.width > 0 && (p.width != vt.Width || p.height != vt.Height) {
			m.flag(problemkind.MediaParamsChanged(), severity.Warn(), "segment %d: resolution changed from %dx%d to %dx%d without a discontinuity tag", s.seq, p.width, p.height, vt.Width, vt.Height)
		}
	}
	st.prev = cur
	if ref := refTrack(vt, at); ref != nil {
		st.anchor = anchor{ok: true, seq: s.seq, pts: ref.StartPTS, wrap: info.TimeWrap}
	}
}

// timestampsGo judges whether the timestamps of the segment go on from
// where those of the previous one ended.
func (m *mediaCheck) timestampsGo(p prevSegment, has bool, start, prevEnd float64, what string) {
	if !has {
		return
	}
	gap := wrapDiff(start-prevEnd, m.info.TimeWrap)
	if math.Abs(gap) > math.Abs(m.agg.ptsGap) {
		m.agg.ptsGap = gap
	}
	limit := m.c.th.PTSGap
	if p.estimated {
		limit = m.c.th.PTSGapEstimated
	}
	if math.Abs(gap) > limit {
		m.flag(problemkind.PTSDiscontinuity(), severity.Warn(), "segment %d: %s timestamps jump by %+.3fs without a discontinuity tag", m.s.seq, what, gap)
	}
}

func refTrack(v, a *media.Track) *media.Track {
	if v != nil && v.HasTiming {
		return v
	}
	if a != nil && a.HasTiming {
		return a
	}
	return nil
}

// wrapDiff folds a timestamp difference into (-wrap/2, wrap/2].
func wrapDiff(d, wrap float64) float64 {
	if wrap <= 0 {
		return d
	}
	d = math.Mod(d, wrap)
	if d > wrap/2 {
		d -= wrap
	} else if d < -wrap/2 {
		d += wrap
	}
	return d
}

func (c *Checker) emitSegmentMetrics(cy *cycle, v Variant, a *segAgg) {
	k := v.Key
	cy.metric(metricname.SegmentsChecked(), k, float64(a.checked))
	cy.metric(metricname.SegmentErrors(), k, float64(a.checked-a.ok))
	cy.metric(metricname.SegmentSourceChanged(), k, float64(a.sourceChanges))
	if a.ok > 0 {
		n := float64(a.ok)
		cy.metric(metricname.SegmentDNSSeconds(), k, a.dns/n)
		cy.metric(metricname.SegmentConnectSeconds(), k, a.connect/n)
		cy.metric(metricname.SegmentTLSSeconds(), k, a.tls/n)
		cy.metric(metricname.SegmentTTFBSeconds(), k, a.ttfb/n)
		cy.metric(metricname.SegmentSizeBytes(), k, a.size/n)
		bitrate := 0.0
		if a.span > 0 {
			bitrate = a.bits / a.span
		}
		cy.metric(metricname.SegmentBitrateBps(), k, bitrate)
		if v.Bandwidth > 0 {
			cy.metric(metricname.SegmentBitrateRatio(), k, bitrate/float64(v.Bandwidth))
		}
		if !a.partial {
			cy.metric(metricname.SegmentTotalSeconds(), k, a.total/n)
			cy.metric(metricname.SegmentTotalSecondsMax(), k, a.maxTotal)
			cy.metric(metricname.SegmentDownloadBps(), k, a.bps/n)
			cy.metric(metricname.SegmentDownloadBpsMin(), k, a.minBps)
			cy.metric(metricname.SegmentDownloadRatio(), k, a.maxRatio)
		}
	}
	if a.subtitles {
		cy.metric(metricname.SubtitleCues(), k, float64(a.cues))
	}
	if a.analysed == 0 {
		return
	}
	if !a.partial {
		cy.metric(metricname.MediaDurationDiffSeconds(), k, a.durDiff)
	}
	cy.metric(metricname.MediaPTSGapSeconds(), k, a.ptsGap)
	cy.metric(metricname.MediaSyncErrors(), k, float64(a.syncErrors))
	cy.metric(metricname.MediaCcErrors(), k, float64(a.ccErrors))
	if a.clocked {
		cy.metric(metricname.TSPCRGapSeconds(), k, a.pcrGap)
		if a.hasLead {
			cy.metric(metricname.TSDecodeLeadSeconds(), k, a.lead)
		}
	}
	if a.hasAV {
		cy.metric(metricname.MediaAVOffsetSeconds(), k, a.avOffset)
	}
	if a.videos > 0 {
		n := float64(a.videos)
		cy.metric(metricname.MediaKeyframeStart(), k, float64(a.keyframeStarts)/n)
		cy.metric(metricname.MediaWidth(), k, float64(a.width))
		cy.metric(metricname.MediaHeight(), k, float64(a.height))
		cy.metric(metricname.MediaHdr(), k, float64(a.hdr))
		if !a.partial {
			cy.metric(metricname.MediaGopSeconds(), k, a.maxGOP)
			cy.metric(metricname.MediaFps(), k, a.fps)
			cy.metric(metricname.MediaVideoBitrateBps(), k, a.vBitrate/n)
		}
	}
	if a.aTracks > 0 && !a.partial {
		cy.metric(metricname.MediaAudioBitrateBps(), k, a.aBitrate/float64(a.aTracks))
	}
}

func ms(d time.Duration) float64 {
	return math.Round(d.Seconds()*1e6) / 1e3
}

// ccBreaks compares the continuity counters a segment ended with and the
// ones the next begins with. It returns how many PIDs do not continue, and
// one of them (the lowest) with its two counters. A PID present on one side
// only says nothing.
func ccBreaks(last, first map[uint16]uint8) (n int, pid uint16, was, now uint8) {
	for p, f := range first {
		l, ok := last[p]
		if !ok || f == (l+1)&0x0f {
			continue
		}
		if n == 0 || p < pid {
			pid, was, now = p, l, f
		}
		n++
	}
	return n, pid, was, now
}
