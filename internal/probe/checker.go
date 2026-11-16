package probe

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/hls-m3u8/m3u8"

	"github.com/hotid/hls-monitor/internal/httpx"
)

const (
	playlistMaxBody    = 8 << 20
	segmentMaxBody     = 256 << 20
	variantConcurrency = 4
	defaultInterval    = 5 * time.Second
	vodInterval        = 30 * time.Second
)

// Checker checks one stream cycle after cycle. Run must not be called
// concurrently with itself.
type Checker struct {
	t   Target
	hc  *httpx.Client
	th  Thresholds
	now func() time.Time

	// plSource is the server that last answered for each playlist.
	mu       sync.Mutex
	plSource map[string]*Endpoint

	entryKind string // KindMaster or KindMedia once known
	// entryText is the entry playlist as last read, entryAt when.
	entryText   []byte
	entryAt     time.Time
	variantKeys []string
	vars        map[string]*variantState
	interval    time.Duration
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
	if !ValidLevel(t.Level) {
		t.Level = LevelSegments
	}
	if t.MaxSegments <= 0 {
		t.MaxSegments = 3
	}
	return &Checker{
		t:        t,
		hc:       hc,
		th:       DefaultThresholds(),
		now:      time.Now,
		vars:     map[string]*variantState{},
		plSource: map[string]*Endpoint{},
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

func (c *Checker) timeout() time.Duration {
	if c.t.TimeoutSec > 0 {
		return time.Duration(c.t.TimeoutSec * float64(time.Second))
	}
	return 10 * time.Second
}

func (c *Checker) get(ctx context.Context, rq httpx.Request, timeout time.Duration) (*httpx.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rq.Headers = c.t.Headers
	return c.hc.Get(ctx, rq)
}

// Run performs one check cycle.
func (c *Checker) Run(ctx context.Context) *Result {
	start := c.now()
	cy := newCycle(c.t.ID, start)

	resp, err := c.get(ctx, httpx.Request{URL: c.t.URL, MaxBody: playlistMaxBody}, c.timeout())

	var pl m3u8.Playlist
	var typ m3u8.ListType
	var syntaxErr, parseErr error
	fetched := err == nil && resp.OK()
	if fetched {
		pl, typ, syntaxErr, parseErr = decodePlaylist(resp.Body)
	}

	// The entry playlist is reported under "master" or "media" depending on
	// what it is; when it cannot be read, under what it was last time.
	key := VariantMedia
	if (fetched && parseErr == nil && typ == m3u8.MASTER) || ((!fetched || parseErr != nil) && c.entryKind == KindMaster) {
		key = VariantMaster
	}

	if c.entryText != nil {
		cy.previous(key, c.entryText, c.entryAt)
	}
	if fetched {
		c.entryText, c.entryAt = resp.Body, start
	}
	ok := c.playlistResponse(cy, key, c.t.URL, resp, err, true)
	if ok && parseErr != nil {
		cy.problem(ProbPlaylistParse, SevCrit, key, "cannot parse playlist: %v", parseErr)
		cy.snapshot(key, c.t.URL, resp.Body)
		ok = false
	}
	if !ok {
		cy.metric("hls_up", key, 0)
		// No playlist under the entry one was read in this cycle.
		for _, st := range c.vars {
			st.failed(cy.lastMessage(key))
		}
		return c.finish(cy, start)
	}
	cy.metric("hls_up", key, 1)
	if syntaxErr != nil {
		cy.problem(ProbPlaylistSyntax, SevWarn, key, "playlist syntax: %v", syntaxErr)
		cy.snapshot(key, c.t.URL, resp.Body)
	}

	base := resp.FinalURL
	if base == "" {
		base = c.t.URL
	}

	switch typ {
	case m3u8.MASTER:
		c.entryKind = KindMaster
		c.checkMaster(ctx, cy, pl.(*m3u8.MasterPlaylist), base, resp)
	default:
		c.entryKind = KindMedia
		v := Variant{Key: VariantMedia, Kind: KindMedia, URI: c.t.URL}
		cy.res.Variants = []Variant{v}
		c.pruneStates([]string{VariantMedia})
		c.checkMediaPlaylist(ctx, cy, v, base, pl.(*m3u8.MediaPlaylist), resp, nil)
	}
	return c.finish(cy, start)
}

func (c *Checker) finish(cy *cycle, start time.Time) *Result {
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
	pl, typ, err = m3u8.DecodeFrom(bytes.NewReader(body), true)
	if err == nil {
		return pl, typ, nil, nil
	}
	strictErr := err
	pl, typ, err = m3u8.DecodeFrom(bytes.NewReader(body), false)
	if err != nil {
		return nil, 0, nil, err
	}
	return pl, typ, strictErr, nil
}

// playlistResponse records the network metrics of a playlist request and
// raises problems for failures. It reports whether a body is available.
func (c *Checker) playlistResponse(cy *cycle, variant, reqURL string, resp *httpx.Response, err error, entry bool) bool {
	cy.about(variant, "playlist_url", reqURL)
	if resp != nil {
		if resp.Status != 0 {
			cy.metric("hls_playlist_http_status", variant, float64(resp.Status))
			cy.about(variant, "playlist_http_status", resp.Status)
		}
		cy.metric("hls_playlist_total_seconds", variant, resp.Total.Seconds())
		cy.about(variant, "playlist_seconds", resp.Total.Seconds())
	}
	if err != nil {
		cy.about(variant, "playlist_error", err.Error())
		if resp != nil && len(resp.Hops) > 0 {
			cy.about(variant, "playlist_redirects", hopLines(resp.Hops))
		}
		cy.metric("hls_playlist_up", variant, 0)
		cy.problem(ProbPlaylistUnreachable, SevCrit, variant, "playlist request failed: %v", err)
		return false
	}
	cy.metric("hls_playlist_dns_seconds", variant, resp.DNS.Seconds())
	cy.metric("hls_playlist_connect_seconds", variant, resp.Connect.Seconds())
	cy.metric("hls_playlist_tls_seconds", variant, resp.TLS.Seconds())
	cy.metric("hls_playlist_ttfb_seconds", variant, resp.TTFB.Seconds())
	cy.metric("hls_playlist_size_bytes", variant, float64(resp.Size))
	cy.metric("hls_playlist_redirects", variant, float64(resp.Redirects))
	if age, ok := resp.AgeSeconds(); ok {
		cy.metric("hls_playlist_age_seconds", variant, age)
	}
	switch resp.CacheStatus() {
	case "hit":
		cy.metric("hls_playlist_cache_hit", variant, 1)
	case "miss":
		cy.metric("hls_playlist_cache_hit", variant, 0)
	}
	if entry && !resp.CertNotAfter.IsZero() {
		left := resp.CertNotAfter.Sub(c.now())
		cy.metric("hls_tls_cert_expiry_seconds", variant, left.Seconds())
		switch {
		case left < c.th.CertCrit:
			cy.problem(ProbCertExpiring, SevCrit, variant, "TLS certificate expires in %s", roundDur(left))
		case left < c.th.CertWarn:
			cy.problem(ProbCertExpiring, SevWarn, variant, "TLS certificate expires in %s", roundDur(left))
		}
	}
	// Which server answered: behind a redirecting balancer or an anycast
	// address consecutive requests may reach different ones.
	ep := endpoint(reqURL, resp)
	cy.source(variant, ep, false)
	cy.about(variant, "playlist_server", ep.Node(), "playlist_software", ep.Headers["server"], "playlist_redirects", hopLines(ep.Redirects), "playlist_bytes", resp.Size)
	if ep.URL != reqURL {
		cy.about(variant, "playlist_final_url", ep.URL)
	}
	changed := 0.0
	c.mu.Lock()
	prev := c.plSource[variant]
	c.plSource[variant] = ep
	c.mu.Unlock()
	if prev != nil && prev.Node() != ep.Node() {
		changed = 1
		cy.problem(ProbPlaylistSource, SevInfo, variant, "%s", describeChange("playlist", prev, ep))
	}
	cy.metric("hls_playlist_source_changed", variant, changed)

	if !resp.OK() {
		cy.metric("hls_playlist_up", variant, 0)
		cy.problem(ProbPlaylistHTTP, SevCrit, variant, "playlist returned HTTP %d from %s (%s)", resp.Status, ep.Host, ep.IP)
		return false
	}
	cy.metric("hls_playlist_up", variant, 1)
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
		kind := KindVideo
		if v.Resolution == "" && v.Codecs != "" && !hasVideoCodec(v.Codecs) {
			kind = KindAudio
		}
		out = append(out, Variant{
			Key:          v.URI,
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
		var kind string
		switch strings.ToUpper(a.Type) {
		case "AUDIO":
			kind = KindAudio
		case "SUBTITLES":
			kind = KindSubtitles
		case "VIDEO":
			kind = KindVideo
		default:
			continue
		}
		seen[a.URI] = true
		out = append(out, Variant{Key: a.URI, Kind: kind, URI: a.URI, Name: a.Name, Group: a.GroupId, Language: a.Language})
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
func describeSetChange(before, after []string) string {
	was := map[string]bool{}
	for _, k := range before {
		was[k] = true
	}
	is := map[string]bool{}
	var added, removed []string
	for _, k := range after {
		is[k] = true
		if !was[k] {
			added = append(added, k)
		}
	}
	for _, k := range before {
		if !is[k] {
			removed = append(removed, k)
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
func (c *Checker) pruneStates(keys []string) {
	keep := map[string]bool{}
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
		if !keep[k] && k != VariantMaster && k != VariantMedia {
			delete(c.plSource, k)
		}
	}
	c.mu.Unlock()
}

type edge struct {
	ok      bool
	kind    string
	lastSeq uint64
	pdt     time.Time
}

func (c *Checker) checkMaster(ctx context.Context, cy *cycle, mp *m3u8.MasterPlaylist, base string, entry *httpx.Response) {
	variants := buildVariants(mp)
	cy.res.Variants = append([]Variant{{Key: VariantMaster, Kind: KindMaster, URI: c.t.URL}}, variants...)

	keys := make([]string, 0, len(variants))
	video := 0
	for _, v := range variants {
		keys = append(keys, v.Key)
		if v.Kind == KindVideo {
			video++
			if v.Bandwidth > 0 {
				cy.metric("hls_variant_declared_bandwidth_bps", v.Key, float64(v.Bandwidth))
			}
		}
	}
	cy.metric("hls_variants", VariantMaster, float64(video))
	cy.metric("hls_renditions", VariantMaster, float64(len(variants)-video))
	if len(variants) == 0 {
		cy.problem(ProbPlaylistEmpty, SevCrit, VariantMaster, "master playlist lists no variants")
		cy.snapshot(VariantMaster, c.t.URL, entry.Body)
	}

	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	if c.variantKeys != nil && strings.Join(sorted, "\n") != strings.Join(c.variantKeys, "\n") {
		cy.problem(ProbVariantsChanged, SevWarn, VariantMaster, "set of variants changed: %s", describeSetChange(c.variantKeys, sorted))
		cy.snapshot(VariantMaster, c.t.URL, entry.Body)
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

	// Variants of one stream are expected to advance together.
	var minSeq, maxSeq uint64
	var minPDT, maxPDT time.Time
	n, withPDT := 0, 0
	for _, e := range edges {
		if !e.ok || e.kind != KindVideo {
			continue
		}
		if n == 0 || e.lastSeq < minSeq {
			minSeq = e.lastSeq
		}
		if n == 0 || e.lastSeq > maxSeq {
			maxSeq = e.lastSeq
		}
		n++
		if !e.pdt.IsZero() {
			if withPDT == 0 || e.pdt.Before(minPDT) {
				minPDT = e.pdt
			}
			if withPDT == 0 || e.pdt.After(maxPDT) {
				maxPDT = e.pdt
			}
			withPDT++
		}
	}
	if n > 1 {
		spread := float64(maxSeq - minSeq)
		cy.metric("hls_variants_sequence_spread", VariantMaster, spread)
		if spread >= c.th.VariantSeqSpread {
			cy.problem(ProbVariantsOutOfSync, SevWarn, VariantMaster, "variants are %d segments apart", int(spread))
		}
		if withPDT == n {
			cy.metric("hls_variants_edge_spread_seconds", VariantMaster, maxPDT.Sub(minPDT).Seconds())
		}
	}
}

func (c *Checker) checkVariant(ctx context.Context, cy *cycle, v Variant, base string, entry *httpx.Response) edge {
	u, err := resolve(base, v.URI)
	if err != nil {
		cy.metric("hls_up", v.Key, 0)
		cy.problem(ProbPlaylistUnreachable, SevCrit, v.Key, "bad variant URI %q: %v", v.URI, err)
		return edge{}
	}
	resp, err := c.get(ctx, httpx.Request{URL: u, MaxBody: playlistMaxBody}, c.timeout())
	if !c.playlistResponse(cy, v.Key, u, resp, err, false) {
		cy.metric("hls_up", v.Key, 0)
		if st := c.vars[v.Key]; st != nil {
			st.failed(cy.lastMessage(v.Key))
		}
		return edge{}
	}
	pl, typ, syntaxErr, perr := decodePlaylist(resp.Body)
	if perr != nil || typ != m3u8.MEDIA {
		cy.metric("hls_up", v.Key, 0)
		if perr == nil {
			perr = fmt.Errorf("expected a media playlist")
		}
		cy.problem(ProbPlaylistParse, SevCrit, v.Key, "cannot parse playlist: %v", perr)
		cy.snapshot(v.Key, u, resp.Body)
		if st := c.vars[v.Key]; st != nil {
			st.failed(cy.lastMessage(v.Key))
		}
		return edge{}
	}
	cy.metric("hls_up", v.Key, 1)
	if syntaxErr != nil {
		cy.problem(ProbPlaylistSyntax, SevWarn, v.Key, "playlist syntax: %v", syntaxErr)
		cy.snapshot(v.Key, u, resp.Body)
	}
	if resp.FinalURL != "" {
		u = resp.FinalURL
	}
	return c.checkMediaPlaylist(ctx, cy, v, u, pl.(*m3u8.MediaPlaylist), resp, entry)
}

// hopLines renders the redirects that were followed, one per line.
func hopLines(hops []httpx.Hop) []string {
	out := make([]string, 0, len(hops))
	for _, h := range hops {
		line := fmt.Sprintf("HTTP %d %s", h.Status, h.URL)
		if h.Server != "" {
			line += " (" + h.Server + ")"
		}
		out = append(out, line)
	}
	return out
}
