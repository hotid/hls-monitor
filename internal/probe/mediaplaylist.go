package probe

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/media"
)

// variantState is what a checker remembers about one media playlist between cycles.
type variantState struct {
	seen bool // at least one successful cycle

	haveSeq bool
	lastSeq uint64 // sequence number of the newest segment seen
	recent  map[uint64]string

	signature      string
	lastChange     time.Time
	targetDuration float64
	closed         bool
	vodCursor      int

	// segSource is the server that answered for the last segment.
	segSource *Endpoint

	initURL string
	init    *media.Init
	keyOK   map[string]bool

	bufInit bool
	stalled bool
	buffer  float64
	bufTime time.Time
	// Since the modelled player last (re)started: how long it has played
	// and how much media became available to it.
	playWall, playMedia float64
	playSegments        int
	// stallDetails is the calculation that led to the current stall.
	stallDetails []any
	// lastCheck is when the playlist was last examined, lastText what it
	// said then.
	lastCheck time.Time
	lastText  []byte
	// keeps is how long a segment listed then was sure to stay listed: the
	// playlist window without its longest segment.
	keeps float64
	// failedChecks counts the checks since then that could not read the
	// playlist; lastFailure says why the latest of them could not.
	failedChecks int
	lastFailure  string

	prev prevSegment
}

// failed notes a check that could not read the playlist.
func (st *variantState) failed(why string) {
	if st.lastCheck.IsZero() {
		return // nothing to compare with yet
	}
	st.failedChecks++
	st.lastFailure = why
}

// prevSegment is the tail of the last analysed segment, used to check that
// the next one continues where it ended.
type prevSegment struct {
	ok            bool
	seq           uint64
	wrap          float64
	hasV, hasA    bool
	vEnd, aEnd    float64
	width, height int
	// estimated is set when the ends were not measured but derived from the
	// start timestamp and the playlist duration.
	estimated bool
}

type plSegment struct {
	seq       uint64
	seg       *m3u8.MediaSegment
	encrypted bool
	keys      []m3u8.Key
	initMap   *m3u8.Map
}

func (c *Checker) checkMediaPlaylist(ctx context.Context, cy *cycle, v Variant, plURL string, p *m3u8.MediaPlaylist, resp, entry *httpx.Response) edge {
	st := c.vars[v.Key]
	now := c.now()
	td := float64(p.TargetDuration)

	// Carry EXT-X-KEY and EXT-X-MAP forward: each applies until replaced.
	var segs []plSegment
	curKeys := p.Keys
	curMap := p.Map
	seq := p.SeqNo
	for _, s := range p.GetAllSegments() {
		if s == nil {
			continue
		}
		if len(s.Keys) > 0 {
			curKeys = s.Keys
		}
		if s.Map != nil {
			curMap = s.Map
		}
		segs = append(segs, plSegment{seq: seq, seg: s, keys: curKeys, initMap: curMap, encrypted: isEncrypted(curKeys)})
		seq++
	}
	n := len(segs)

	cy.metric("hls_target_duration_seconds", v.Key, td)
	cy.metric("hls_playlist_segments", v.Key, float64(n))
	cy.metric("hls_discontinuity_sequence", v.Key, float64(p.DiscontinuitySeq))
	cy.metric("hls_playlist_live", v.Key, b2f(!p.Closed))
	if !p.Closed {
		cy.live(td)
	}

	if !st.lastCheck.IsZero() {
		cy.previous(v.Key, st.lastText, st.lastCheck)
	}
	if n == 0 {
		cy.problem(ProbPlaylistEmpty, SevCrit, v.Key, "media playlist has no segments")
		cy.snapshot(v.Key, plURL, resp.Body)
		st.seen = true
		return edge{}
	}
	first, last := segs[0].seq, segs[n-1].seq
	cy.metric("hls_media_sequence", v.Key, float64(last))
	cy.about(v.Key, "target_duration", td, "playlist_segments", n, "first_sequence", first, "last_sequence", last)
	// Checks in between that could not read the playlist: what happened to
	// it meanwhile is their consequence, not a finding of its own.
	failedBetween := st.failedChecks
	if !st.lastCheck.IsZero() {
		cy.about(v.Key, "since_previous_check", now.Sub(st.lastCheck).Seconds())
		if failedBetween > 0 {
			cy.about(v.Key, "failed_checks_between", failedBetween, "last_failure", st.lastFailure)
		}
	}
	st.lastCheck, st.lastText = now, resp.Body
	st.failedChecks, st.lastFailure = 0, ""
	// The pause before this check was chosen by what the playlist promised then.
	promised := st.keeps

	c.inspectPlaylist(cy, v, plURL, p, segs, resp, st, now)

	// Sequence continuity against the previous cycle.
	if st.haveSeq {
		switch {
		case last < st.lastSeq:
			cy.problem(ProbSeqRegression, SevCrit, v.Key, "media sequence went back from %d to %d", st.lastSeq, last)
			cy.detail(ProbSeqRegression, v.Key, "previous_last_sequence", st.lastSeq)
			cy.snapshot(v.Key, plURL, resp.Body)
			st.haveSeq = false
			st.prev = prevSegment{}
		case first > st.lastSeq+1:
			missed := first - st.lastSeq - 1
			cy.metric("hls_segments_missed", v.Key, float64(missed))
			// After an outage the gap says nothing new: the outage is reported.
			sev := SevWarn
			if failedBetween > 0 {
				sev = SevInfo
			}
			cy.problem(ProbSeqJump, sev, v.Key, "%d segments left the playlist between checks (%d → %d)", missed, st.lastSeq, first)
			cy.detail(ProbSeqJump, v.Key, "previous_last_sequence", st.lastSeq, "missed_segments", missed, "previous_playlist_keeps", promised)
			cy.snapshot(v.Key, plURL, resp.Body)
		}
		for _, s := range segs {
			if old, ok := st.recent[s.seq]; ok && old != s.seg.URI {
				cy.problem(ProbSegmentChanged, SevWarn, v.Key, "segment %d changed URI from %q to %q", s.seq, old, s.seg.URI)
				cy.detail(ProbSegmentChanged, v.Key, "segment_sequence", s.seq, "uri_before", old, "uri_now", s.seg.URI)
				cy.snapshot(v.Key, plURL, resp.Body)
			}
		}
	}
	if p.Closed && !st.closed && st.seen {
		cy.problem(ProbStreamEnded, SevWarn, v.Key, "live playlist ended (EXT-X-ENDLIST appeared)")
	}

	// Which segments to download this cycle.
	var selected, fresh []plSegment
	switch {
	case p.Closed:
		selected = []plSegment{segs[st.vodCursor%n]}
		st.vodCursor++
	case !st.haveSeq:
		selected = segs[n-1:]
	default:
		for _, s := range segs {
			if s.seq > st.lastSeq {
				fresh = append(fresh, s)
			}
		}
		selected = fresh
		if len(selected) > c.t.MaxSegments {
			cy.metric("hls_segments_skipped", v.Key, float64(len(selected)-c.t.MaxSegments))
			selected = selected[len(selected)-c.t.MaxSegments:]
		}
	}
	cy.metric("hls_playlist_new_segments", v.Key, float64(len(fresh)))

	failed := map[uint64]bool{}
	if c.t.Level != LevelPlaylist {
		c.checkKeys(ctx, cy, v, plURL, selected, st)
		c.checkSegments(ctx, cy, v, plURL, selected, st, resp, entry, failed)
	}

	if !p.Closed && c.t.Level != LevelPlaylist && v.Kind != KindSubtitles {
		c.playerModel(cy, v, st, td, fresh, failed)
	}

	// Remember the state for the next cycle.
	st.seen = true
	st.closed = p.Closed
	st.haveSeq = true
	st.lastSeq = last
	st.targetDuration = td
	st.recent = make(map[uint64]string, n)
	for _, s := range segs {
		st.recent[s.seq] = s.seg.URI
	}

	e := edge{ok: true, kind: v.Kind, lastSeq: last}
	if t := segs[n-1].seg.ProgramDateTime; !t.IsZero() {
		e.pdt = t.Add(time.Duration(segs[n-1].seg.Duration * float64(time.Second)))
	}
	return e
}

// inspectPlaylist checks what can be judged from the playlist text alone.
func (c *Checker) inspectPlaylist(cy *cycle, v Variant, plURL string, p *m3u8.MediaPlaylist, segs []plSegment, resp *httpx.Response, st *variantState, now time.Time) {
	td := float64(p.TargetDuration)
	n := len(segs)

	var window, maxDur, maxDrift float64
	minDur := math.MaxFloat64
	discontinuities, adMarkers, encrypted, gaps := 0, 0, 0, 0
	for i, s := range segs {
		d := s.seg.Duration
		window += d
		maxDur = max(maxDur, d)
		minDur = min(minDur, d)
		if s.seg.Discontinuity {
			discontinuities++
		}
		if s.seg.SCTE != nil || len(s.seg.SCTE35DateRanges) > 0 {
			adMarkers++
		}
		if s.encrypted {
			encrypted++
		}
		if s.seg.Gap {
			gaps++
		}
		if td > 0 && d > td+c.th.TargetDurationSlack {
			cy.problem(ProbTargetDuration, SevWarn, v.Key, "segment %d lasts %.3fs, target duration is %.0fs", s.seq, d, td)
			cy.detail(ProbTargetDuration, v.Key, "segment_sequence", s.seq, "segment_uri", s.seg.URI, "segment_duration", d, "allowed_excess", c.th.TargetDurationSlack)
			cy.snapshot(v.Key, plURL, resp.Body)
		}
		if i > 0 && !s.seg.Discontinuity {
			a, b := segs[i-1].seg.ProgramDateTime, s.seg.ProgramDateTime
			if !a.IsZero() && !b.IsZero() {
				drift := b.Sub(a).Seconds() - segs[i-1].seg.Duration
				if math.Abs(drift) > math.Abs(maxDrift) {
					maxDrift = drift
				}
				if math.Abs(drift) > c.th.PDTDrift {
					cy.problem(ProbPDTDiscontinuity, SevWarn, v.Key, "program date-time jumps by %+.3fs at segment %d without a discontinuity tag", drift, s.seq)
					cy.snapshot(v.Key, plURL, resp.Body)
				}
			}
		}
	}
	cy.metric("hls_playlist_window_seconds", v.Key, window)
	cy.about(v.Key, "playlist_window", window, "segment_duration_min", minDur, "segment_duration_max", maxDur)
	st.keeps = max(window-maxDur, 0)
	if !p.Closed {
		cy.slack(window - maxDur)
	}
	cy.metric("hls_segment_duration_max_seconds", v.Key, maxDur)
	cy.metric("hls_segment_duration_min_seconds", v.Key, minDur)
	cy.metric("hls_playlist_discontinuities", v.Key, float64(discontinuities))
	cy.metric("hls_playlist_ad_markers", v.Key, float64(adMarkers))
	cy.metric("hls_playlist_encrypted", v.Key, b2f(encrypted > 0))
	cy.metric("hls_playlist_gap_segments", v.Key, float64(gaps))
	cy.metric("hls_pdt_drift_seconds", v.Key, maxDrift)
	if p.HasPartialSegments() {
		cy.metric("hls_playlist_parts", v.Key, float64(len(p.PartialSegments)))
	}

	lastSeg := segs[n-1].seg
	if !p.Closed && !lastSeg.ProgramDateTime.IsZero() {
		edge := lastSeg.ProgramDateTime.Add(time.Duration(lastSeg.Duration * float64(time.Second)))
		cy.metric("hls_live_latency_seconds", v.Key, now.Sub(edge).Seconds())
	}

	if st.seen && st.targetDuration > 0 && td != st.targetDuration {
		// Not allowed by the specification, but some packagers do it all the
		// time without harm to players: recorded, not treated as trouble.
		cy.problem(ProbTargetDurChanged, SevInfo, v.Key, "target duration changed from %.0fs to %.0fs", st.targetDuration, td)
		cy.detail(ProbTargetDurChanged, v.Key, "target_duration_before", st.targetDuration)
	}

	// Freshness: a live playlist must keep changing.
	sig := fmt.Sprintf("%d/%d/%s/%v", p.SeqNo, n, lastSeg.URI, p.Closed)
	if sig != st.signature {
		if !st.lastChange.IsZero() {
			cy.metric("hls_playlist_update_interval_seconds", v.Key, now.Sub(st.lastChange).Seconds())
		}
		st.signature = sig
		st.lastChange = now
	}
	stale := now.Sub(st.lastChange)
	if !p.Closed {
		cy.metric("hls_playlist_stale_seconds", v.Key, stale.Seconds())
		limit := max(time.Duration(c.th.StaleFactor*td*float64(time.Second)), c.th.StaleMin)
		if stale > limit {
			cy.problem(ProbPlaylistStale, SevCrit, v.Key, "playlist has not changed for %s (target duration %.0fs)", stale.Round(time.Second), td)
			cy.detail(ProbPlaylistStale, v.Key, "unchanged_for", stale, "unchanged_limit", limit, "last_segment_uri", lastSeg.URI)
			cy.snapshot(v.Key, plURL, resp.Body)
		}
	}
}

func isEncrypted(keys []m3u8.Key) bool {
	for _, k := range keys {
		if k.Method != "" && !strings.EqualFold(k.Method, "NONE") {
			return true
		}
	}
	return false
}

// checkKeys makes sure AES-128 keys of the selected segments can be fetched.
func (c *Checker) checkKeys(ctx context.Context, cy *cycle, v Variant, plURL string, selected []plSegment, st *variantState) {
	for _, s := range selected {
		for _, k := range s.keys {
			if !strings.EqualFold(k.Method, "AES-128") || k.URI == "" {
				continue
			}
			u, err := resolve(plURL, k.URI)
			if err != nil || !(strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) {
				continue
			}
			if st.keyOK[u] {
				continue
			}
			resp, err := c.get(ctx, httpx.Request{URL: u, MaxBody: 1 << 16}, c.timeout())
			switch {
			case err != nil:
				cy.problem(ProbKeyUnavailable, SevCrit, v.Key, "encryption key request failed: %v", err)
			case !resp.OK():
				cy.problem(ProbKeyUnavailable, SevCrit, v.Key, "encryption key returned HTTP %d", resp.Status)
			default:
				if st.keyOK == nil || len(st.keyOK) > 64 {
					st.keyOK = map[string]bool{}
				}
				st.keyOK[u] = true
			}
		}
	}
}

// playerModel estimates what a player three target durations behind the
// live edge would experience: its buffer drains in real time and grows with
// every segment that became available. When the buffer runs dry the player
// stalls; it resumes, again three target durations behind the edge, once
// new segments appear.
func (c *Checker) playerModel(cy *cycle, v Variant, st *variantState, td float64, fresh []plSegment, failed map[uint64]bool) {
	now := c.now()
	gained, got, lost := 0.0, 0, 0
	for _, s := range fresh {
		if failed[s.seq] {
			lost++
			continue
		}
		gained += s.seg.Duration
		got++
	}
	stalls := 0.0
	restart := func() {
		st.buffer = 3 * td
		st.playWall, st.playMedia, st.playSegments = 0, 0, 0
		st.stallDetails = nil
	}
	switch {
	case !st.bufInit:
		st.bufInit = true
		restart()
	case st.stalled:
		if gained > 0 {
			st.stalled = false
			restart()
		}
	default:
		elapsed := now.Sub(st.bufTime).Seconds()
		before := st.buffer
		st.playWall += elapsed
		st.playMedia += gained
		st.playSegments += got
		st.buffer += gained - elapsed
		if st.buffer < 0 {
			stalls = 1
			st.stalled = true
			st.stallDetails = []any{
				"buffer_at_start", 3 * td,
				"buffer_before", before,
				"cycle_elapsed", elapsed,
				"cycle_media", gained,
				"cycle_segments", got,
				"cycle_failed_segments", lost,
				"buffer_shortfall", -st.buffer,
				"played_for", st.playWall,
				"media_received", st.playMedia,
				"segments_received", st.playSegments,
			}
			if st.playWall > 0 {
				st.stallDetails = append(st.stallDetails, "media_rate", st.playMedia/st.playWall)
			}
			st.buffer = 0
		}
	}
	st.bufTime = now
	st.buffer = min(st.buffer, max(6*td, 30))
	cy.metric("hls_buffer_seconds", v.Key, st.buffer)
	cy.metric("hls_stalls", v.Key, stalls)
	if st.stalled {
		cy.problem(ProbPlaybackStall, SevCrit, v.Key, "a player would have run out of buffer: segments arrive slower than real time")
		cy.detail(ProbPlaybackStall, v.Key, st.stallDetails...)
	}
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
