package probe

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/types/cachestatus"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/overload"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

const (
	playlistMaxBody    = 8 << 20
	segmentMaxBody     = 256 << 20
	variantConcurrency = 4
	// freshEvery is how often a check makes its own connection.
	freshEvery      = 5 * time.Minute
	defaultInterval = 5 * time.Second
	vodInterval     = 30 * time.Second
)

// Checker checks one stream cycle after cycle. Run must not be called
// concurrently with itself.
type Checker struct {
	t   Target
	hc  *httpx.Client
	th  Thresholds
	now func() time.Time

	// CatchUp makes the next cycle a short one: of the segments that are
	// new, only the latest of each playlist is requested. The runner sets
	// it for a cycle that starts late. A checker that has fallen behind
	// finds more new segments at every cycle, takes longer over them and
	// falls further behind; looking at fewer of them is the way back.
	CatchUp bool

	// Overloaded, when set, tells what the process was short of at any time
	// since the given moment, or "" when it kept up. Set it before Run.
	Overloaded func(since time.Time) overload.Reason

	// Looker, when set, is what looks at the picture and listens to the
	// sound, every PictureEvery. Set both before Run.
	Looker       Looker
	PictureEvery time.Duration
	// blind leaves the container of the segments unread; for tests of
	// what is decoded alone.
	blind bool
	// Slates, when set, returns the pictures known to be slates.
	Slates func() []Slate
	looks  looks
	// Capturer, when set, saves the segments around a finding (see
	// capture.go). Set it before Run.
	Capturer   Capturer
	captureNow atomic.Bool
	// loops are the playlists that show the same pictures over and over.
	loops map[standKey]standingProblem
	// signs is what the sizes of coded frames tell: a picture that does
	// not seem to change, sound that seems to be silence.
	signs map[standKey]standingProblem
	// ownAt is when a playlist was last found to play a clip known to be
	// the channel's own.
	ownAt   time.Time
	ownName string
	// clipAt is when a clip going round, or a slate, was last told.
	clipAt time.Time

	// plSource is the server that last answered for each playlist.
	mu       sync.Mutex
	plSource map[variantkey.Key]*Endpoint

	entryKind variantkind.Kind // master or media, once known
	// freshAt is when the stream's address was last asked on a connection
	// made for it.
	freshAt time.Time
	// entryText is the entry playlist as last read, entryAt when.
	entryText   []byte
	entryAt     time.Time
	variantKeys []variantkey.Key
	// soundApart tells that the master playlist names playlists of sound.
	soundApart bool
	// loud is the measuring of loudness.
	loud loudState
	// steps is how every playlist stood against the picture.
	steps    map[variantkey.Key]*inStep
	vars     map[variantkey.Key]*variantState
	interval time.Duration
	// received counts the bytes of response bodies downloaded in the
	// current cycle, by every request it makes.
	received atomic.Int64
	// inside tells that the stream's own address is not a public one: it
	// was pointed inside on purpose, and so may its playlists.
	inside bool
	// maxInterval is the longest pause that still sees every segment of the
	// shortest live playlist window; zero until a live playlist was read.
	maxInterval time.Duration
}

// New creates a checker. hc may be nil, in which case a client matching the
// target's TLS setting is created.
func New(t Target, hc *httpx.Client) *Checker {
	if hc == nil {
		hc = httpx.New(httpx.Options{InsecureSkipVerify: t.Insecure})
	}
	if t.Level.IsZero() || t.Picture.IsZero() {
		panic("probe: a target that does not say how deep it is checked, or what its picture is checked for")
	}
	if t.MaxSegments <= 0 {
		t.MaxSegments = 3
	}
	return &Checker{
		t:        t,
		hc:       hc,
		th:       DefaultThresholds(),
		now:      time.Now,
		vars:     map[variantkey.Key]*variantState{},
		plSource: map[variantkey.Key]*Endpoint{},
		interval: defaultInterval,
	}
}

// Target returns the configuration the checker was created with.
func (c *Checker) Target() Target { return c.t }

// Interval is the pause to make before the next cycle.
// A configured interval is an upper bound: a playlist whose window is
// shorter is polled as often as needed not to miss segments in between.
func (c *Checker) Interval() time.Duration {
	if c.t.IntervalSec > 0 {
		d := time.Duration(c.t.IntervalSec * float64(time.Second))
		if c.maxInterval > 0 && c.maxInterval < d {
			return c.maxInterval
		}
		return d
	}
	return c.interval
}

// freshJitter picks a moment within a period; tests make it certain.
var freshJitter = func(d time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(d))) }

func (c *Checker) timeout() time.Duration {
	if c.t.TimeoutSec > 0 {
		return time.Duration(c.t.TimeoutSec * float64(time.Second))
	}
	return 10 * time.Second
}

func (c *Checker) get(ctx context.Context, rq httpx.Request, timeout time.Duration) (*httpx.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rq.Headers, rq.HeadersFor = c.t.Headers, c.t.URL
	// The request for the stream's own address comes marked ReachFirstAny.
	rq.Reach = Reach(rq.Reach == httpx.ReachFirstAny, c.inside)
	resp, err := c.hc.Get(ctx, rq)
	if resp != nil {
		c.received.Add(resp.Size)
	}
	return resp, err
}

// Run performs one check cycle.
func (c *Checker) Run(ctx context.Context) *Result {
	start := c.now()
	cy := newCycle(c.t.ID, start)

	// Connections are kept between checks, so the time it takes to make
	// one shows only when one is made on purpose: every few minutes the
	// stream's own address is asked on a fresh connection.
	c.beginLook(start)
	c.checkLoudGiveUp(cy, start)
	fresh := c.freshAt.IsZero() || start.Sub(c.freshAt) >= freshEvery
	switch {
	case c.freshAt.IsZero():
		// Streams start together; their next turns are spread over the
		// period, so that connections are opened evenly and not all at once.
		c.freshAt = start.Add(-freshJitter(freshEvery))
	case fresh:
		c.freshAt = start
	}
	resp, err := c.get(ctx, httpx.Request{URL: c.t.URL, MaxBody: playlistMaxBody, Reach: httpx.ReachFirstAny, Fresh: fresh}, c.timeout())

	var pl m3u8.Playlist
	var typ m3u8.ListType
	var syntaxErr, parseErr error
	fetched := err == nil && resp.OK()
	if fetched {
		pl, typ, syntaxErr, parseErr = decodePlaylist(resp.Body)
	}

	// The entry playlist is reported under "master" or "media" depending on
	// what it is; when it cannot be read, under what it was last time.
	key := variantkey.Media()
	if (fetched && parseErr == nil && typ == m3u8.MASTER) || ((!fetched || parseErr != nil) && c.entryKind == variantkind.Master()) {
		key = variantkey.Master()
	}

	if resp != nil && err == nil && !fresh {
		// The way in: a balancer often answers the stream's address with
		// a redirect, and how long that takes is not the stream's doing.
		// Not when the connections were made for it: that is another figure.
		cy.metric(metricname.EntrySeconds(), key, resp.Total.Seconds())
		cy.metric(metricname.EntryRedirects(), key, float64(resp.Redirects))
	}
	if fresh && resp != nil && resp.Connects > 0 {
		n := float64(resp.Connects)
		cy.metric(metricname.ConnectSeconds(), key, resp.Connect.Seconds()/n)
		if resp.Lookups > 0 {
			cy.metric(metricname.DNSSeconds(), key, resp.DNS.Seconds()/float64(resp.Lookups))
		}
		if resp.TLS > 0 {
			cy.metric(metricname.TLSSeconds(), key, resp.TLS.Seconds()/n)
		}
	}
	if resp != nil && resp.RemoteAddr != "" {
		c.inside = resp.Inside
	}

	if c.entryText != nil {
		cy.previous(key, c.entryText, c.entryAt)
	}
	if fetched {
		c.entryText, c.entryAt = resp.Body, start
	}
	ok := c.playlistResponse(cy, key, c.t.URL, resp, err, true)
	if ok && parseErr != nil {
		cy.problem(problemkind.PlaylistParse(), severity.Crit(), key, "cannot parse playlist: %v", parseErr)
		cy.snapshot(key, c.t.URL, resp.Body)
		ok = false
	}
	if !ok {
		cy.metric(metricname.Up(), key, 0)
		// No playlist under the entry one was read in this cycle.
		for _, st := range c.vars {
			st.failed(cy.lastMessage(key))
		}
		return c.finish(cy, start)
	}
	cy.metric(metricname.Up(), key, 1)
	if syntaxErr != nil {
		cy.problem(problemkind.PlaylistSyntax(), severity.Warn(), key, "playlist syntax: %v", syntaxErr)
		cy.snapshot(key, c.t.URL, resp.Body)
	}

	base := resp.FinalURL
	if base == "" {
		base = c.t.URL
	}

	switch typ {
	case m3u8.MASTER:
		c.entryKind = variantkind.Master()
		c.checkMaster(ctx, cy, pl.(*m3u8.MasterPlaylist), base, resp)
	default:
		c.entryKind = variantkind.Media()
		v := Variant{Key: variantkey.Media(), Kind: variantkind.Media(), URI: c.t.URL}
		cy.res.Variants = []Variant{v}
		c.pruneStates([]variantkey.Key{variantkey.Media()})
		c.checkMediaPlaylist(ctx, cy, v, base, pl.(*m3u8.MediaPlaylist), resp, nil)
	}
	return c.finish(cy, start)
}

func (c *Checker) finish(cy *cycle, start time.Time) *Result {
	// What the check itself cost the network: playlists, and as much of
	// the segments as its level downloads.
	c.reportLooks(cy)
	c.confirmCaptures(cy)
	cy.streamMetric(metricname.CheckBytes(), float64(c.received.Swap(0)))
	if c.Overloaded != nil {
		if reason := c.Overloaded(start); !reason.IsZero() {
			cy.overloaded(reason)
		}
	}
	switch {
	case cy.anyLive && cy.minTargetDuration > 0:
		c.interval = min(max(time.Duration(cy.minTargetDuration*float64(time.Second))/2, time.Second), 15*time.Second)
	case cy.res.Variants != nil && !cy.anyLive && len(cy.probs) == 0:
		c.interval = vodInterval
	}
	if cy.minSlack > 0 {
		// Twice per the time a segment is guaranteed to stay listed.
		c.maxInterval = max(time.Duration(cy.minSlack*float64(time.Second))/2, time.Second)
	}
	return cy.finish(c.now().Sub(start))
}

// decodePlaylist parses strictly first; when that fails it retries leniently
// and reports the strict failure as a syntax problem.
func decodePlaylist(body []byte) (pl m3u8.Playlist, typ m3u8.ListType, syntaxErr, err error) {
	pl, typ, err = decodeM3U8(body, true)
	if err == nil {
		return pl, typ, nil, nil
	}
	strictErr := err
	pl, typ, err = decodeM3U8(body, false)
	if err != nil {
		return nil, 0, nil, err
	}
	return pl, typ, strictErr, nil
}

// decodeM3U8 parses a playlist. The text comes from a server the monitor
// does not control, and the parser is known to panic on some of it (a tag
// without its attributes, for one): that is a playlist that cannot be
// parsed, not a reason for the process to die.
func decodeM3U8(body []byte, strict bool) (pl m3u8.Playlist, typ m3u8.ListType, err error) {
	defer func() {
		if r := recover(); r != nil {
			pl, typ, err = nil, 0, fmt.Errorf("the playlist parser failed: %v", r)
		}
	}()
	return m3u8.DecodeFrom(bytes.NewReader(body), strict)
}

// playlistResponse records the network metrics of a playlist request and
// raises problems for failures. It reports whether a body is available.
func (c *Checker) playlistResponse(cy *cycle, variant variantkey.Key, reqURL string, resp *httpx.Response, err error, entry bool) bool {
	cy.about(variant, detail.PlaylistURL(reqURL))
	if resp != nil {
		if resp.Status != 0 {
			cy.metric(metricname.PlaylistHTTPStatus(), variant, float64(resp.Status))
			cy.about(variant, detail.PlaylistHTTPStatus(resp.Status))
		}
		cy.metric(metricname.PlaylistTotalSeconds(), variant, resp.Total.Seconds())
		cy.network(variant, resp)
		cy.about(variant, detail.PlaylistSeconds(resp.Total.Seconds()))
	}
	if err != nil {
		cy.about(variant, detail.PlaylistError(err.Error()))
		if resp != nil && len(resp.Hops) > 0 {
			cy.about(variant, detail.PlaylistRedirects(hopLines(resp.Hops)))
		}
		cy.metric(metricname.PlaylistUp(), variant, 0)
		cy.problem(problemkind.PlaylistUnreachable(), severity.Crit(), variant, "playlist request failed: %v", err)
		if timedOut(err) {
			cy.doubt(problemkind.PlaylistUnreachable(), variant)
		}
		return false
	}
	cy.metric(metricname.PlaylistDNSSeconds(), variant, resp.DNS.Seconds())
	cy.metric(metricname.PlaylistConnectSeconds(), variant, resp.Connect.Seconds())
	cy.metric(metricname.PlaylistTLSSeconds(), variant, resp.TLS.Seconds())
	cy.metric(metricname.PlaylistTTFBSeconds(), variant, resp.TTFB.Seconds())
	cy.metric(metricname.PlaylistSizeBytes(), variant, float64(resp.Size))
	cy.metric(metricname.PlaylistRedirects(), variant, float64(resp.Redirects))
	if age, ok := resp.AgeSeconds(); ok {
		cy.metric(metricname.PlaylistAgeSeconds(), variant, age)
	}
	switch resp.CacheStatus() {
	case cachestatus.Hit():
		cy.metric(metricname.PlaylistCacheHit(), variant, 1)
	case cachestatus.Miss():
		cy.metric(metricname.PlaylistCacheHit(), variant, 0)
	}
	if entry && !resp.CertNotAfter.IsZero() {
		left := resp.CertNotAfter.Sub(c.now())
		cy.metric(metricname.TLSCertExpirySeconds(), variant, left.Seconds())
		switch {
		case left < c.th.CertCrit:
			cy.problem(problemkind.CertExpiring(), severity.Crit(), variant, "TLS certificate expires in %s", roundDur(left))
		case left < c.th.CertWarn:
			cy.problem(problemkind.CertExpiring(), severity.Warn(), variant, "TLS certificate expires in %s", roundDur(left))
		}
	}
	// Which server answered: behind a redirecting balancer or an anycast
	// address consecutive requests may reach different ones.
	ep := endpoint(reqURL, resp)
	cy.source(variant, ep, false)
	cy.about(variant, detail.PlaylistServer(ep.Node()), detail.PlaylistSoftware(ep.Headers["Server"]), detail.PlaylistRedirects(hopLines(ep.Redirects)), detail.PlaylistBytes(resp.Size))
	if ep.URL != reqURL {
		cy.about(variant, detail.PlaylistFinalURL(ep.URL))
	}
	changed := 0.0
	c.mu.Lock()
	prev := c.plSource[variant]
	c.plSource[variant] = ep
	c.mu.Unlock()
	if prev != nil && prev.Node() != ep.Node() {
		changed = 1
		cy.problem(problemkind.PlaylistSource(), severity.Info(), variant, "%s", describeChange("playlist", prev, ep))
	}
	cy.metric(metricname.PlaylistSourceChanged(), variant, changed)

	if !resp.OK() {
		cy.metric(metricname.PlaylistUp(), variant, 0)
		cy.problem(problemkind.PlaylistHTTP(), severity.Crit(), variant, "playlist returned HTTP %d from %s (%s)", resp.Status, ep.Host, ep.IP)
		return false
	}
	cy.metric(metricname.PlaylistUp(), variant, 1)
	return true
}

// endpoint describes the server that produced a response.
func endpoint(reqURL string, resp *httpx.Response) *Endpoint {
	e := &Endpoint{URL: resp.FinalURL, Proto: resp.Proto, Headers: resp.Identity(), Redirects: resp.Hops}
	if e.URL == "" {
		e.URL = reqURL
	}
	if u, err := url.Parse(e.URL); err == nil {
		e.Host = u.Host
		// A redirect may spell out the default port; it is the same server.
		if (u.Scheme == "http" && u.Port() == "80") || (u.Scheme == "https" && u.Port() == "443") {
			e.Host = u.Hostname()
		}
	}
	if host, _, err := net.SplitHostPort(resp.RemoteAddr); err == nil {
		e.IP = host
	} else {
		e.IP = resp.RemoteAddr
	}
	return e
}

// describeChange says in words how the answering server differs from the previous one.
func describeChange(what string, prev, cur *Endpoint) string {
	switch {
	case prev.Host != cur.Host && prev.IP != cur.IP:
		return fmt.Sprintf("%s now served by %s (%s), was %s (%s)", what, cur.Host, cur.IP, prev.Host, prev.IP)
	case prev.Host != cur.Host:
		return fmt.Sprintf("%s now served by %s, was %s (same address %s)", what, cur.Host, prev.Host, cur.IP)
	default:
		return fmt.Sprintf("%s now served from %s, was %s (same host %s)", what, cur.IP, prev.IP, cur.Host)
	}
}

func roundDur(d time.Duration) string {
	if d < 0 {
		return "the past (already expired)"
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
	return d.Round(time.Hour).String()
}

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u, err := b.Parse(ref)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// buildVariants lists the playlists a master playlist refers to: its
// variant streams and the alternative renditions that have their own URI.
func buildVariants(mp *m3u8.MasterPlaylist) []Variant {
	var out []Variant
	seen := map[string]bool{}
	for _, v := range mp.Variants {
		if v == nil || v.URI == "" || v.Iframe || seen[v.URI] {
			continue
		}
		seen[v.URI] = true
		kind := variantkind.Video()
		if v.Resolution == "" && v.Codecs != "" && !hasVideoCodec(v.Codecs) {
			kind = variantkind.Audio()
		}
		out = append(out, Variant{
			Key:          variantkey.OfURI(v.URI),
			Kind:         kind,
			URI:          v.URI,
			Name:         v.Name,
			Bandwidth:    int64(v.Bandwidth),
			AvgBandwidth: int64(v.AverageBandwidth),
			Resolution:   v.Resolution,
			Codecs:       v.Codecs,
			FrameRate:    v.FrameRate,
		})
	}
	for _, a := range mp.GetAllAlternatives() {
		if a == nil || a.URI == "" || seen[a.URI] {
			continue
		}
		var kind variantkind.Kind
		switch strings.ToUpper(a.Type) {
		case "AUDIO":
			kind = variantkind.Audio()
		case "SUBTITLES":
			kind = variantkind.Subtitles()
		case "VIDEO":
			kind = variantkind.Video()
		default:
			continue
		}
		seen[a.URI] = true
		out = append(out, Variant{Key: variantkey.OfURI(a.URI), Kind: kind, URI: a.URI, Name: a.Name, Group: a.GroupId, Language: a.Language})
	}
	return out
}

func hasVideoCodec(codecs string) bool {
	for _, c := range strings.Split(codecs, ",") {
		c = strings.TrimSpace(c)
		for _, p := range []string{"avc", "hvc", "hev", "av01", "vp0", "vp8", "vp9", "dvh", "mp4v"} {
			if strings.HasPrefix(c, p) {
				return true
			}
		}
	}
	return false
}

// describeSetChange names what left and what joined a set of variant keys.
func describeSetChange(before, after []variantkey.Key) string {
	was := map[variantkey.Key]bool{}
	for _, k := range before {
		was[k] = true
	}
	is := map[variantkey.Key]bool{}
	var added, removed []string
	for _, k := range after {
		is[k] = true
		if !was[k] {
			added = append(added, k.String())
		}
	}
	for _, k := range before {
		if !is[k] {
			removed = append(removed, k.String())
		}
	}
	list := func(keys []string) string {
		const most = 3
		if len(keys) > most {
			return fmt.Sprintf("%s and %d more", strings.Join(keys[:most], ", "), len(keys)-most)
		}
		return strings.Join(keys, ", ")
	}
	var parts []string
	if len(removed) > 0 {
		parts = append(parts, "removed "+list(removed))
	}
	if len(added) > 0 {
		parts = append(parts, "added "+list(added))
	}
	return strings.Join(parts, "; ")
}

// pruneStates drops the state of variants that no longer exist and makes
// sure every current variant has one.
func (c *Checker) pruneStates(keys []variantkey.Key) {
	keep := map[variantkey.Key]bool{}
	for _, k := range keys {
		keep[k] = true
		if _, ok := c.vars[k]; !ok {
			c.vars[k] = &variantState{}
		}
	}
	for k := range c.vars {
		if !keep[k] {
			delete(c.vars, k)
		}
	}
	c.mu.Lock()
	for k := range c.plSource {
		if !keep[k] && k != variantkey.Master() && k != variantkey.Media() {
			delete(c.plSource, k)
		}
	}
	c.mu.Unlock()
}

// edge is where a media playlist ends: the end of its last segment by
// EXT-X-PROGRAM-DATE-TIME, zero when the playlist has no date-times.
type edge struct {
	ok   bool
	key  variantkey.Key
	kind variantkind.Kind
	live bool
	// Where the playlist has got to: the number of its last segment and
	// which count that is of, how many discontinuities it has been
	// through, and the end of the last segment on the timeline of the
	// media, when timestamps were read.
	seq            uint64
	epoch          int
	disc           uint64
	media          float64
	hasMedia       bool
	wrap           float64
	pdt            time.Time
	targetDuration float64
}

func (c *Checker) checkMaster(ctx context.Context, cy *cycle, mp *m3u8.MasterPlaylist, base string, entry *httpx.Response) {
	variants := buildVariants(mp)
	c.soundApart = false
	for _, v := range variants {
		c.soundApart = c.soundApart || v.Kind == variantkind.Audio()
	}
	cy.res.Variants = append([]Variant{{Key: variantkey.Master(), Kind: variantkind.Master(), URI: c.t.URL}}, variants...)

	keys := make([]variantkey.Key, 0, len(variants))
	video := 0
	for _, v := range variants {
		keys = append(keys, v.Key)
		if v.Kind == variantkind.Video() {
			video++
			if v.Bandwidth > 0 {
				cy.metric(metricname.VariantDeclaredBandwidthBps(), v.Key, float64(v.Bandwidth))
			}
		}
	}
	cy.metric(metricname.Variants(), variantkey.Master(), float64(video))
	cy.metric(metricname.Renditions(), variantkey.Master(), float64(len(variants)-video))
	if len(variants) == 0 {
		cy.problem(problemkind.PlaylistEmpty(), severity.Crit(), variantkey.Master(), "master playlist lists no variants")
		cy.snapshot(variantkey.Master(), c.t.URL, entry.Body)
	}

	sorted := slices.Clone(keys)
	slices.SortFunc(sorted, variantkey.Key.Compare)
	if c.variantKeys != nil && !slices.Equal(sorted, c.variantKeys) {
		cy.problem(problemkind.VariantsChanged(), severity.Warn(), variantkey.Master(), "set of variants changed: %s", describeSetChange(c.variantKeys, sorted))
		cy.snapshot(variantkey.Master(), c.t.URL, entry.Body)
	}
	c.variantKeys = sorted
	c.pruneStates(keys)

	edges := make([]edge, len(variants))
	sem := make(chan struct{}, variantConcurrency)
	var wg sync.WaitGroup
	for i, v := range variants {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			edges[i] = c.checkVariant(ctx, cy, v, base, entry)
		}()
	}
	wg.Wait()

	if apart, limit, ok := edgesApart(edges, c.th.VariantEdgeSpread); ok {
		cy.metric(metricname.VariantsEdgeSpreadSeconds(), variantkey.Master(), apart)
		if apart > limit {
			cy.problem(problemkind.VariantsOutOfSync(), severity.Warn(), variantkey.Master(), "live edges of the video variants are %.1fs apart by EXT-X-PROGRAM-DATE-TIME", apart)
			cy.detail(problemkind.VariantsOutOfSync(), variantkey.Master(), detail.VariantsEdgeSpread(apart), detail.VariantsEdgeSpreadLimit(limit))
		}
	}
	c.renditionsInStep(cy, edges)
}

// edgesApart tells how far apart in time the live edges of the video
// variants are and how far they may be: factor target durations, since the
// playlists are read one after another and one of them may already list
// the next segment.
//
// Only EXT-X-PROGRAM-DATE-TIME says where a playlist is in the
// presentation. Media sequence numbers do not: variants may number their
// segments independently (RFC 8216, section 6.3.2). Without date-times in
// every variant there is nothing to compare, and ok is false.
func edgesApart(edges []edge, factor float64) (apart, limit float64, ok bool) {
	var first, last time.Time
	n := 0
	for _, e := range edges {
		if !e.ok || e.kind != variantkind.Video() {
			continue
		}
		if e.pdt.IsZero() {
			return 0, 0, false
		}
		if n == 0 || e.pdt.Before(first) {
			first = e.pdt
		}
		if n == 0 || e.pdt.After(last) {
			last = e.pdt
		}
		limit = max(limit, factor*e.targetDuration)
		n++
	}
	if n < 2 {
		return 0, 0, false
	}
	return last.Sub(first).Seconds(), limit, true
}

func (c *Checker) checkVariant(ctx context.Context, cy *cycle, v Variant, base string, entry *httpx.Response) edge {
	u, err := resolve(base, v.URI)
	if err != nil {
		cy.metric(metricname.Up(), v.Key, 0)
		cy.problem(problemkind.PlaylistUnreachable(), severity.Crit(), v.Key, "bad variant URI %q: %v", v.URI, err)
		return edge{}
	}
	resp, err := c.get(ctx, httpx.Request{URL: u, MaxBody: playlistMaxBody}, c.timeout())
	if !c.playlistResponse(cy, v.Key, u, resp, err, false) {
		cy.metric(metricname.Up(), v.Key, 0)
		if st := c.vars[v.Key]; st != nil {
			st.failed(cy.lastMessage(v.Key))
		}
		return edge{}
	}
	pl, typ, syntaxErr, perr := decodePlaylist(resp.Body)
	if perr != nil || typ != m3u8.MEDIA {
		cy.metric(metricname.Up(), v.Key, 0)
		if perr == nil {
			perr = fmt.Errorf("expected a media playlist")
		}
		cy.problem(problemkind.PlaylistParse(), severity.Crit(), v.Key, "cannot parse playlist: %v", perr)
		cy.snapshot(v.Key, u, resp.Body)
		if st := c.vars[v.Key]; st != nil {
			st.failed(cy.lastMessage(v.Key))
		}
		return edge{}
	}
	cy.metric(metricname.Up(), v.Key, 1)
	if syntaxErr != nil {
		cy.problem(problemkind.PlaylistSyntax(), severity.Warn(), v.Key, "playlist syntax: %v", syntaxErr)
		cy.snapshot(v.Key, u, resp.Body)
	}
	if resp.FinalURL != "" {
		u = resp.FinalURL
	}
	return c.checkMediaPlaylist(ctx, cy, v, u, pl.(*m3u8.MediaPlaylist), resp, entry)
}

// hopLines renders the redirects that were followed, one per line: who
// answered, and where it sent the request when that is known. The same
// redirect made several times in a row, as by a server that redirects to
// itself, is one line with a count.
func hopLines(hops []httpx.Hop) []string {
	out := make([]string, 0, len(hops))
	repeats := 1
	for i, h := range hops {
		line := fmt.Sprintf("HTTP %d %s", h.Status, h.URL)
		if h.Server != "" {
			line += " (" + h.Server + ")"
		}
		if i+1 < len(hops) {
			line += " → " + hops[i+1].URL
		} else if i > 0 && hops[i-1].URL == h.URL {
			line += " → " + h.URL // the last of a run to itself: it did it again
		}
		if n := len(out); n > 0 && strings.TrimSuffix(out[n-1], fmt.Sprintf(" ×%d", repeats)) == line {
			repeats++
			out[n-1] = fmt.Sprintf("%s ×%d", line, repeats)
			continue
		}
		repeats = 1
		out = append(out, line)
	}
	return out
}
