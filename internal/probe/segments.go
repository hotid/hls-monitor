package probe

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/media"
)

// minBitrateSpan is the shortest segment, in seconds, whose bitrate is
// compared with the declared one.
const minBitrateSpan = 1.0

// headerBytes is how much of a segment is requested at LevelHeader: a whole
// number of MPEG-TS packets, enough for the tables and the first frames.
const headerBytes = 188 * 700

// segAgg averages per-segment observations into per-cycle metrics.
type segAgg struct {
	// partial is set when only the beginning of each segment was fetched.
	partial                                   bool
	sourceChanges                             int
	checked, ok                               int
	dns, connect, tls, ttfb, total, size, bps float64
	maxTotal, minBps, maxRatio, bitrate       float64

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

func (c *Checker) checkSegments(ctx context.Context, cy *cycle, v Variant, plURL string, selected []plSegment, st *variantState, plResp, entry *httpx.Response, failed map[uint64]bool) {
	if len(selected) == 0 {
		return
	}
	header := c.t.Level == LevelHeader
	agg := &segAgg{minBps: math.MaxFloat64, partial: header}
	startupDone := false
	// held is what the body of the segment being looked at takes of the
	// memory budget; it is given back when the next one is started.
	var held int64
	defer func() { bodies.release(held) }()

	for _, s := range selected {
		bodies.release(held)
		held = 0
		if ctx.Err() != nil {
			return
		}
		if s.seg.Gap {
			continue // EXT-X-GAP: the segment is declared missing on purpose
		}
		rec := Segment{
			Variant:       v.Key,
			Seq:           s.seq,
			Time:          c.now(),
			Duration:      s.seg.Duration,
			Discontinuity: s.seg.Discontinuity,
		}
		if !s.at.IsZero() {
			t := s.at
			rec.ProgramTime = &t
		}
		// suspected is what makes the segment worth capturing, and got
		// the answer it came in, once there is one.
		var suspected []string
		var got *httpx.Response
		var segURL string
		flag := func(kind, sev, format string, args ...any) {
			cy.problem(kind, sev, v.Key, format, args...)
			rec.Problems = append(rec.Problems, kind)
			if Capturable(kind) {
				suspected = append(suspected, kind)
			}
			// Whether the playlist announced a break is the question here,
			// so its text, now and before, belongs to the finding.
			if (kind == ProbPTSDiscontinuity || kind == ProbMediaParamsChanged) && plResp != nil {
				cy.snapshot(v.Key, plURL, plResp.Body)
			}
		}
		// emit files the record and attaches the segment to its problems.
		emit := func() {
			for _, kind := range rec.Problems {
				cy.detail(kind, v.Key,
					"segment_sequence", rec.Seq, "segment_url", rec.URL, "segment_duration", rec.Duration,
					"segment_http_status", rec.Status, "segment_error", rec.Error,
					"segment_bytes", rec.Size, "segment_fetched_bytes", rec.Fetched)
				if rec.Source != nil {
					cy.detail(kind, v.Key, "segment_server", rec.Source.Node(), "segment_software", rec.Source.Headers["server"])
				}
			}
			cy.segment(rec)
			if c.Capturer == nil || got == nil || s.encrypted {
				return
			}
			// Somebody asked for a capture: the first segment of the
			// picture, or of the stream itself, will do.
			manual := (v.Kind == KindVideo || v.Kind == KindMedia) && c.captureNow.CompareAndSwap(true, false)
			if len(suspected) > 0 || manual {
				whole := !header || (got.TotalSize > 0 && int64(len(got.Body)) >= got.TotalSize)
				c.capture(v, plURL, plResp, entry, st, s, segURL, got, suspected, manual, whole && got.OK() && !got.Truncated)
			}
		}

		u, err := resolve(plURL, s.seg.URI)
		if err != nil {
			rec.URL = s.seg.URI
			rec.Error = err.Error()
			flag(ProbSegmentUnreachable, SevCrit, "segment %d has a bad URI: %v", s.seq, err)
			failed[s.seq] = true
			agg.checked++
			emit()
			continue
		}
		rec.URL = u
		segURL = u

		// Initialization section for fMP4, fetched once per URL.
		var init *media.Init
		if s.initMap != nil && s.initMap.URI != "" {
			init = c.initSection(ctx, cy, v, plURL, s, st)
		}

		timeout := max(c.timeout(), time.Duration(3*s.seg.Duration*float64(time.Second)))
		rq := httpx.Request{URL: u, MaxBody: segmentMaxBody, RangeOffset: s.seg.Offset, RangeLength: s.seg.Limit}
		// One segment of a cycle that is due is also looked at.
		look := !s.encrypted && c.wantsLook(v)
		// And one of a playlist of sound alone is listened to, where the
		// look found the picture without its sound.
		listen := !look && c.wantsListen(v, s.encrypted)
		if header {
			// A server that ignores Range answers with the whole segment;
			// MaxBody then stops the download at the same point. A look
			// needs a little more than the beginning: the whole of the
			// first keyframe and a moment of sound.
			head := int64(headerBytes)
			if look {
				head = c.lookSize()
			} else if listen {
				head = lookBytes
			}
			rq.MaxBody = head
			rq.RangeLength = head
			if s.seg.Limit > 0 {
				rq.RangeLength = min(s.seg.Limit, head)
			}
		}
		if !header {
			if held = bodies.acquire(ctx, expectedBody(v, s)); held == 0 {
				return // the cycle was cancelled while waiting for memory
			}
		}
		resp, err := c.get(ctx, rq, timeout)
		got = resp
		if resp != nil && held > 0 && resp.Size > held {
			bodies.grow(resp.Size - held)
			held = resp.Size
		}
		agg.checked++
		size := int64(0)
		if resp != nil {
			size = resp.Size
			if header {
				switch {
				case s.seg.Limit > 0:
					size = s.seg.Limit
				case resp.TotalSize > 0:
					size = resp.TotalSize
				}
			}
			rec.Status = resp.Status
			rec.RemoteAddr = resp.RemoteAddr
			rec.Proto = resp.Proto
			rec.Cache = resp.CacheStatus()
			rec.Size = size
			rec.Fetched = resp.Size
			rec.Partial = header
			rec.DNSMs = ms(resp.DNS)
			rec.ConnectMs = ms(resp.Connect)
			rec.TLSMs = ms(resp.TLS)
			rec.TTFBMs = ms(resp.TTFB)
			rec.TotalMs = ms(resp.Total)
			rec.Bps = resp.Bps()
			if resp.Status != 0 {
				ep := endpoint(u, resp)
				rec.Source = ep
				cy.source(v.Key, ep, true)
				if prev := st.segSource; prev != nil && prev.Node() != ep.Node() {
					agg.sourceChanges++
					flag(ProbSegmentSource, SevInfo, "%s", describeChange("segments", prev, ep))
				}
				st.segSource = ep
			}
		}
		cy.network(v.Key, resp)
		broken := true
		switch {
		case err != nil && (resp == nil || resp.Status == 0):
			rec.Error = err.Error()
			flag(ProbSegmentUnreachable, SevCrit, "segment %d request failed: %v", s.seq, err)
			if timedOut(err) {
				cy.doubt(ProbSegmentUnreachable, v.Key)
			}
		case err != nil:
			rec.Error = err.Error()
			flag(ProbSegmentTruncated, SevCrit, "segment %d download broke off after %d bytes: %v", s.seq, resp.Size, err)
			if timedOut(err) {
				cy.doubt(ProbSegmentTruncated, v.Key)
			}
		case !resp.OK():
			flag(ProbSegmentHTTP, SevCrit, "segment %d returned HTTP %d", s.seq, resp.Status)
		case resp.LengthMismatch && !header:
			flag(ProbSegmentTruncated, SevCrit, "segment %d is %d bytes, Content-Length declared %d", s.seq, resp.Size, resp.ContentLength)
		default:
			broken = false
		}
		if broken {
			failed[s.seq] = true
			st.prev = prevSegment{}
			emit()
			continue
		}

		agg.ok++
		agg.dns += resp.DNS.Seconds()
		agg.connect += resp.Connect.Seconds()
		agg.tls += resp.TLS.Seconds()
		agg.ttfb += resp.TTFB.Seconds()
		agg.size += float64(size)
		if !header {
			// Timing of the whole download means something only for a whole download.
			agg.total += resp.Total.Seconds()
			agg.bps += resp.Bps()
			agg.maxTotal = max(agg.maxTotal, resp.Total.Seconds())
			agg.minBps = min(agg.minBps, resp.Bps())
		}

		if s.seg.Duration > 0 {
			if !header {
				ratio := resp.Total.Seconds() / s.seg.Duration
				agg.maxRatio = max(agg.maxRatio, ratio)
				if ratio > 1 {
					flag(ProbSegmentSlow, SevWarn, "segment %d (%.1fs of media) took %.1fs to download", s.seq, s.seg.Duration, resp.Total.Seconds())
					cy.doubt(ProbSegmentSlow, v.Key)
				}
			}
			bitrate := float64(size*8) / s.seg.Duration
			agg.bitrate += bitrate
			// A stub of a frame or two, left by an encoder restart, has no
			// bitrate to speak of: its size divided by 40 ms says nothing.
			if v.Bandwidth > 0 && s.seg.Duration >= minBitrateSpan && bitrate > float64(v.Bandwidth)*c.th.BitrateFactor {
				flag(ProbBitrateExceeds, SevWarn, "segment %d is %.0f kbit/s, playlist declares %.0f kbit/s", s.seq, bitrate/1000, float64(v.Bandwidth)/1000)
				cy.detail(ProbBitrateExceeds, v.Key, "segment_bitrate", bitrate, "declared_bandwidth", v.Bandwidth, "bitrate_ratio", bitrate/float64(v.Bandwidth), "bitrate_ratio_limit", c.th.BitrateFactor)
			}
		}

		if !startupDone && !header {
			startupDone = true
			startup := plResp.Total.Seconds() + resp.Total.Seconds()
			if entry != nil {
				startup += entry.Total.Seconds()
			}
			cy.metric("hls_startup_seconds", v.Key, startup)
		}

		var info *media.Info
		if (c.t.Level == LevelContainer || header) && !s.encrypted && v.Kind != KindSubtitles {
			info = media.Analyze(resp.Body, init)
			rec.Media = info
			c.judgeMedia(v, s, info, st, agg, flag, !header || (rec.Size > 0 && rec.Fetched >= rec.Size))
			c.noteCoded(v, st, info, c.now())
			c.noteSizes(v, st, info, c.now())
		} else {
			st.prev = prevSegment{}
			if v.Kind == KindSubtitles && !s.encrypted && (!header || (rec.Size > 0 && rec.Fetched >= rec.Size)) {
				// Subtitles are text, and the whole of a segment is read at
				// every level that reads segments at all.
				name := strings.ToLower(s.seg.URI)
				if q := strings.IndexAny(name, "?#"); q >= 0 {
					name = name[:q]
				}
				if media.IsWebVTT(resp.Body) || strings.HasSuffix(name, ".vtt") || strings.HasSuffix(name, ".webvtt") {
					sub, err := media.ParseWebVTT(resp.Body)
					if err != nil {
						flag(ProbSubtitlesInvalid, SevWarn, "segment %d is not valid WebVTT: %v", s.seq, err)
					} else {
						agg.subtitles = true
						agg.cues += sub.Cues
					}
				}
			}
		}
		if look && err == nil && resp != nil && resp.OK() {
			c.look(ctx, cy, v, resp.Body, st.initRaw, info, c.now())
		}
		if listen && err == nil && resp != nil && resp.OK() {
			c.listen(ctx, cy, v, resp.Body, st.initRaw, info, c.now())
		}
		if look || listen {
			suspected = append(suspected, c.suspicions()...)
		}
		emit()
	}

	// A clock that slips once, where an encoder was switched or restarted,
	// plays on; one that is wrong check after check does not on a set-top box.
	switch {
	case agg.clockFault != "":
		if st.clockRuns++; st.clockRuns >= clockRuns {
			cy.problem(ProbTSClock, SevWarn, v.Key, "%s; wrong for %d checks in a row", agg.clockFault, st.clockRuns)
		}
	case agg.clocked:
		st.clockRuns = 0
	}
	c.emitSegmentMetrics(cy, v, agg)
}

// initSection returns the parsed initialization section a segment refers
// to, fetching it when the URL differs from the cached one.
func (c *Checker) initSection(ctx context.Context, cy *cycle, v Variant, plURL string, s plSegment, st *variantState) *media.Init {
	u, err := resolve(plURL, s.initMap.URI)
	if err != nil {
		cy.problem(ProbInitUnavailable, SevCrit, v.Key, "bad EXT-X-MAP URI %q: %v", s.initMap.URI, err)
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
		cy.problem(ProbInitUnavailable, SevCrit, v.Key, "initialization section request failed: %v", err)
		return nil
	case !resp.OK():
		cy.problem(ProbInitUnavailable, SevCrit, v.Key, "initialization section returned HTTP %d", resp.Status)
		return nil
	}
	st.initURL = cacheKey
	st.init = nil
	st.initRaw = resp.Body
	if c.t.Level == LevelContainer || c.t.Level == LevelHeader {
		in, err := media.ParseInit(resp.Body)
		if err != nil {
			cy.problem(ProbMediaParse, SevWarn, v.Key, "cannot parse initialization section: %v", err)
		}
		st.init = in
	}
	return st.init
}

// judgeMedia turns container analysis of one segment into problems and
// updates the continuity state.
func (c *Checker) judgeMedia(v Variant, s plSegment, info *media.Info, st *variantState, agg *segAgg, flag func(kind, sev, format string, args ...any), whole bool) {
	agg.analysed++
	// With only the beginning of a segment, a parser running off its end is expected.
	partial := agg.partial
	if info.Error != "" && !partial {
		flag(ProbMediaParse, SevWarn, "segment %d: %s", s.seq, info.Error)
	}
	if info.Container == media.ContainerUnknown {
		flag(ProbMediaParse, SevWarn, "segment %d is not a recognised media container", s.seq)
		st.prev = prevSegment{}
		return
	}
	if info.Container == media.ContainerTS {
		agg.syncErrors += info.SyncErrors
		agg.ccErrors += info.CCErrors
		if info.SyncErrors > 0 {
			flag(ProbTSSync, SevCrit, "segment %d: %d MPEG-TS sync errors", s.seq, info.SyncErrors)
		}
		if info.CCErrors > 0 {
			flag(ProbTSContinuity, SevWarn, "segment %d: %d continuity counter errors", s.seq, info.CCErrors)
		}
		if info.TransportErrors > 0 {
			flag(ProbTSTransport, SevWarn, "segment %d: %d packets flagged with transport errors", s.seq, info.TransportErrors)
		}
		if k := info.Clock; k != nil && k.PCRs >= 2 {
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
				agg.clockFault = fmt.Sprintf("segment %d: %s", s.seq, strings.Join(wrong, "; "))
			}
		}
		if !info.HasPAT || !info.HasPMT {
			flag(ProbTSNoTables, SevWarn, "segment %d has no PAT/PMT, it cannot be decoded on its own", s.seq)
		}
	}

	vt, at := info.Video(), info.Audio()
	// Where the segment ends is measured when all of it was read — a short
	// segment fits into the beginning that is requested — and otherwise
	// derived from where it starts and how long the playlist says it is.
	guessEnd := partial && !whole
	cur := prevSegment{ok: true, seq: s.seq, wrap: info.TimeWrap, estimated: guessEnd}

	if vt != nil {
		agg.videos++
		agg.width, agg.height = vt.Width, vt.Height
		if !partial {
			agg.fps = vt.FPS
			agg.vBitrate += vt.Bitrate()
			agg.maxGOP = max(agg.maxGOP, vt.MaxGOP)
		}
		if vt.FirstKeyframe {
			agg.keyframeStarts++
		} else {
			flag(ProbNoKeyframeStart, SevWarn, "segment %d does not start with a keyframe", s.seq)
		}
		cur.width, cur.height = vt.Width, vt.Height
		switch vt.Range {
		case "PQ":
			agg.hdr = 1
		case "HLG":
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
		if !partial {
			agg.aBitrate += at.Bitrate()
		}
		if at.HasTiming {
			cur.hasA, cur.aEnd = true, at.EndPTS
			if guessEnd {
				cur.aEnd = at.StartPTS + s.seg.Duration
			}
		}
	}

	if v.Kind == KindVideo {
		if diff := declaredDiffers(v, vt, at, partial); len(diff) > 0 {
			flag(ProbDeclaredMismatch, SevWarn, "segment %d: %s", s.seq, strings.Join(diff, "; "))
		}
	}

	// EXTINF against the real media duration. Video is authoritative; audio
	// frames rarely line up with segment boundaries exactly.
	if ref := refTrack(vt, at); ref != nil && s.seg.Duration > 0 && !partial {
		diff := ref.Duration() - s.seg.Duration
		if math.Abs(diff) > math.Abs(agg.durDiff) {
			agg.durDiff = diff
		}
		if math.Abs(diff) > c.th.DurationMismatch {
			flag(ProbDurationMismatch, SevWarn, "segment %d: playlist says %.3fs, media lasts %.3fs", s.seq, s.seg.Duration, ref.Duration())
		}
	}

	if vt != nil && at != nil && vt.HasTiming && at.HasTiming {
		off := vt.StartPTS - at.StartPTS
		agg.hasAV = true
		if math.Abs(off) > math.Abs(agg.avOffset) {
			agg.avOffset = off
		}
		if math.Abs(off) > c.th.AVOffset {
			flag(ProbAVDesync, SevWarn, "segment %d: video starts %+.3fs relative to audio", s.seq, off)
		}
	}

	// Continuity with the previous segment of this variant.
	if whole && info.Container == media.ContainerTS {
		cur.lastCC = info.LastCC
	}
	if p := st.prev; p.ok && p.seq+1 == s.seq && !s.seg.Discontinuity {
		// Within a segment the parser counts the breaks; the joint between
		// two segments is checked here. Without a discontinuity tag the
		// counters must run on (RFC 8216, section 3).
		if n, pid, was, now := ccBreaks(p.lastCC, info.FirstCC); n > 0 {
			agg.ccErrors += n
			flag(ProbTSContinuity, SevWarn, "segment %d: continuity counters break at the joint with the previous segment on %d PIDs (PID %d: %d after %d)", s.seq, n, pid, now, was)
		}
		check := func(has bool, start, prevEnd float64, what string) {
			if !has {
				return
			}
			gap := wrapDiff(start-prevEnd, info.TimeWrap)
			if math.Abs(gap) > math.Abs(agg.ptsGap) {
				agg.ptsGap = gap
			}
			limit := c.th.PTSGap
			if p.estimated {
				limit = c.th.PTSGapEstimated
			}
			if math.Abs(gap) > limit {
				flag(ProbPTSDiscontinuity, SevWarn, "segment %d: %s timestamps jump by %+.3fs without a discontinuity tag", s.seq, what, gap)
			}
		}
		if vt != nil && vt.HasTiming {
			check(p.hasV, vt.StartPTS, p.vEnd, "video")
		} else if at != nil && at.HasTiming {
			check(p.hasA, at.StartPTS, p.aEnd, "audio")
		}
		if vt != nil && p.width > 0 && (p.width != vt.Width || p.height != vt.Height) {
			flag(ProbMediaParamsChanged, SevWarn, "segment %d: resolution changed from %dx%d to %dx%d without a discontinuity tag", s.seq, p.width, p.height, vt.Width, vt.Height)
		}
	}
	st.prev = cur
	if ref := refTrack(vt, at); ref != nil {
		st.anchor = anchor{ok: true, seq: s.seq, pts: ref.StartPTS, wrap: info.TimeWrap}
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
	cy.metric("hls_segments_checked", k, float64(a.checked))
	cy.metric("hls_segment_errors", k, float64(a.checked-a.ok))
	cy.metric("hls_segment_source_changed", k, float64(a.sourceChanges))
	if a.ok > 0 {
		n := float64(a.ok)
		cy.metric("hls_segment_dns_seconds", k, a.dns/n)
		cy.metric("hls_segment_connect_seconds", k, a.connect/n)
		cy.metric("hls_segment_tls_seconds", k, a.tls/n)
		cy.metric("hls_segment_ttfb_seconds", k, a.ttfb/n)
		cy.metric("hls_segment_size_bytes", k, a.size/n)
		cy.metric("hls_segment_bitrate_bps", k, a.bitrate/n)
		if v.Bandwidth > 0 {
			cy.metric("hls_segment_bitrate_ratio", k, a.bitrate/n/float64(v.Bandwidth))
		}
		if !a.partial {
			cy.metric("hls_segment_total_seconds", k, a.total/n)
			cy.metric("hls_segment_total_seconds_max", k, a.maxTotal)
			cy.metric("hls_segment_download_bps", k, a.bps/n)
			cy.metric("hls_segment_download_bps_min", k, a.minBps)
			cy.metric("hls_segment_download_ratio", k, a.maxRatio)
		}
	}
	if a.subtitles {
		cy.metric("hls_subtitle_cues", k, float64(a.cues))
	}
	if a.analysed == 0 {
		return
	}
	if !a.partial {
		cy.metric("hls_media_duration_diff_seconds", k, a.durDiff)
	}
	cy.metric("hls_media_pts_gap_seconds", k, a.ptsGap)
	cy.metric("hls_media_sync_errors", k, float64(a.syncErrors))
	cy.metric("hls_media_cc_errors", k, float64(a.ccErrors))
	if a.clocked {
		cy.metric("hls_ts_pcr_gap_seconds", k, a.pcrGap)
		if a.hasLead {
			cy.metric("hls_ts_decode_lead_seconds", k, a.lead)
		}
	}
	if a.hasAV {
		cy.metric("hls_media_av_offset_seconds", k, a.avOffset)
	}
	if a.videos > 0 {
		n := float64(a.videos)
		cy.metric("hls_media_keyframe_start", k, float64(a.keyframeStarts)/n)
		cy.metric("hls_media_width", k, float64(a.width))
		cy.metric("hls_media_height", k, float64(a.height))
		cy.metric("hls_media_hdr", k, float64(a.hdr))
		if !a.partial {
			cy.metric("hls_media_gop_seconds", k, a.maxGOP)
			cy.metric("hls_media_fps", k, a.fps)
			cy.metric("hls_media_video_bitrate_bps", k, a.vBitrate/n)
		}
	}
	if a.aTracks > 0 && !a.partial {
		cy.metric("hls_media_audio_bitrate_bps", k, a.aBitrate/float64(a.aTracks))
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
