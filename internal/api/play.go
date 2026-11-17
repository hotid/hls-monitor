package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/relay"
	"github.com/hotid/hls-monitor/internal/store"
)

// Watching a stream through the server: the server requests the playlists
// and the segments itself, from where it makes its checks, and hands them
// to the browser. What is seen is therefore what the monitor sees, and a
// particular server behind a balancer can be asked directly.
//
// The server must not become a way to request arbitrary addresses, so the
// browser never names one. It is given opaque tokens: the address, the
// stream it belongs to and a time limit, signed with a key that lives as
// long as the process. Playlists are rewritten so that everything they
// name is a token as well.

const (
	// playTTL is how long a token works. A playlist is requested again
	// every few seconds and brings new ones; this bounds how long a page
	// left open can go on without asking for its sources again.
	playTTL = 12 * time.Hour
	// playMaxBody bounds one segment handed to the browser.
	playMaxBody = 256 << 20
)

// playToken is what a token says.
type playToken struct {
	URL    string `json:"u"`
	Stream int64  `json:"s"`
	// Agent is who fetches: zero for the server itself.
	Agent int64 `json:"a,omitempty"`
	// Expires is Unix seconds.
	Expires int64 `json:"e"`
	// Entry marks the address a source starts from; Inside tells that
	// this start was not a public address (see probe.Reach).
	Entry  bool `json:"r,omitempty"`
	Inside bool `json:"i,omitempty"`
}

func (s *Server) signPlay(t playToken) string {
	body, _ := json.Marshal(t)
	mac := hmac.New(sha256.New, s.playKey)
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

func (s *Server) openPlay(token string) (playToken, bool) {
	var t playToken
	body64, sig64, ok := strings.Cut(token, ".")
	if !ok {
		return t, false
	}
	body, err1 := base64.RawURLEncoding.DecodeString(body64)
	sig, err2 := base64.RawURLEncoding.DecodeString(sig64)
	if err1 != nil || err2 != nil {
		return t, false
	}
	mac := hmac.New(sha256.New, s.playKey)
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)[:16]) || json.Unmarshal(body, &t) != nil {
		return t, false
	}
	return t, s.now().Unix() <= t.Expires
}

// watchSource is one way to watch a stream.
type watchSource struct {
	// Kind is "configured" for the stream's own address, with whatever
	// balancing and redirects lie behind it, and "server" for one
	// particular server asked directly.
	Kind     string `json:"kind"`
	Host     string `json:"host,omitempty"`
	IP       string `json:"ip,omitempty"`
	Software string `json:"software,omitempty"`
	// Seen tells that this server answered for this stream lately;
	// otherwise it is known from other streams of the same site and may
	// not have this one.
	Seen     bool       `json:"seen"`
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// Address is what is requested, for the eye; URL is the path to give
	// to the player.
	Address string `json:"address"`
	URL     string `json:"url"`
}

// watchVantage is a place a stream can be watched from.
type watchVantage struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Local is the server itself.
	Local bool `json:"local"`
	// Available tells that it can be used now; otherwise Why is
	// "offline" or "too_old" (an agent that does not take such requests).
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
}

// watchResponse lists where a stream can be watched from and, for the
// chosen place, what can be asked.
type watchResponse struct {
	Vantages []watchVantage `json:"vantages"`
	// Vantage is the place the sources below are for.
	Vantage int64         `json:"vantage"`
	Sources []watchSource `json:"sources"`
}

// watchSources godoc
//
//	@Summary		Ways to watch a stream through the server
//	@Description	The stream's own address, and every server known to answer for it or for other streams of the same site, asked directly. Each comes with a path that works for twelve hours and serves the playlist with all its addresses replaced by such paths: the browser requests everything from the monitor, the monitor from the origin.
//	@Tags			streams
//	@Produce		json
//	@Param			id		path		int	true	"Stream ID"
//	@Param			agent	query		int	false	"Watch through this agent: the browser asks the server, the server the agent, the agent the origin (default: the server itself)"
//	@Success		200		{object}	watchResponse
//	@Failure		404	{object}	apiError
//	@Router			/api/v1/streams/{id}/watch [get]
func (s *Server) watchSources(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	st, err := s.st.GetStream(ctx, id)
	if s.storeError(c, err) {
		return
	}
	agents, err := s.st.ListAgents(ctx)
	if s.storeError(c, err) {
		return
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	now := s.now()
	resp := watchResponse{Vantage: store.LocalAgentID, Sources: []watchSource{}}
	want := queryInt(c, "agent")
	for _, a := range agents {
		v := watchVantage{ID: a.ID, Name: a.Name, Local: a.Kind == store.AgentLocal}
		switch {
		case v.Local:
			v.Available = true
		case !a.Enabled || !s.agentOnline(a, now):
			v.Why = "offline"
		default:
			if rep := s.agentStatus(a); rep == nil || !rep.Fetches {
				v.Why = "too_old"
			} else {
				v.Available = true
			}
		}
		if a.ID == want && v.Available {
			resp.Vantage = a.ID
		}
		resp.Vantages = append(resp.Vantages, v)
	}
	expires := now.Add(playTTL).Unix()
	path := func(u string) string {
		return "/api/v1/play/" + s.signPlay(playToken{URL: u, Stream: id, Agent: resp.Vantage, Expires: expires, Entry: true})
	}
	out := []watchSource{{Kind: "configured", Seen: true, Address: st.URL, URL: path(st.URL)}}

	// Where the stream's own address led last time: asking another server
	// directly means the same path on that server.
	final := s.entryFinalURL(ctx, id)
	if final == nil {
		resp.Sources = out
		c.JSON(http.StatusOK, resp)
		return
	}
	since := s.now().Add(-24 * time.Hour)
	own, err := s.st.ListSources(ctx, id, since)
	if s.storeError(c, err) {
		return
	}
	all, err := s.st.ListSources(ctx, 0, since)
	if s.storeError(c, err) {
		return
	}
	configured, _ := url.Parse(st.URL)
	servers := map[string]*watchSource{}
	add := func(src *store.SourceSeen, seen bool) {
		if src.What != "playlist" || src.Host == "" || !httpx.SameSite(hostname(src.Host), final.Hostname()) {
			return
		}
		// The stream's own address is the first source already.
		if configured != nil && strings.EqualFold(src.Host, configured.Host) {
			return
		}
		w := servers[src.Host]
		if w == nil {
			u := *final
			u.Host = src.Host
			w = &watchSource{Kind: "server", Host: src.Host, Address: u.String(), URL: path(u.String())}
			servers[src.Host] = w
		}
		if seen && !w.Seen {
			w.Seen = true
			w.LastSeen = nil
		}
		if w.Seen == seen && (w.LastSeen == nil || src.LastSeen.After(*w.LastSeen)) {
			at := src.LastSeen
			w.LastSeen, w.IP, w.Software = &at, src.IP, src.Software
		}
	}
	for _, src := range own {
		add(src, true)
	}
	for _, src := range all {
		if src.StreamID != id {
			add(src, false)
		}
	}
	list := make([]watchSource, 0, len(servers))
	for _, w := range servers {
		list = append(list, *w)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Seen != list[j].Seen {
			return list[i].Seen
		}
		return list[i].Host < list[j].Host
	})
	resp.Sources = append(out, list...)
	c.JSON(http.StatusOK, resp)
}

func hostname(hostport string) string {
	if u, err := url.Parse("//" + hostport); err == nil {
		return u.Hostname()
	}
	return hostport
}

// entryFinalURL is the address the stream's entry playlist was last served
// from, after redirects; nil when that is not known.
func (s *Server) entryFinalURL(ctx context.Context, streamID int64) *url.URL {
	variants, err := s.st.ListVariants(ctx, streamID)
	if err != nil {
		return nil
	}
	entry := map[int64]bool{}
	for _, v := range variants {
		if v.Key == probe.VariantMaster || v.Key == probe.VariantMedia {
			entry[v.ID] = true
		}
	}
	states, err := s.st.VariantStates(ctx, streamID)
	if err != nil {
		return nil
	}
	var best *store.VariantState
	for _, vs := range states {
		if entry[vs.VariantID] && vs.PlaylistSource != nil && vs.PlaylistSource.URL != "" && (best == nil || vs.CheckedAt.After(best.CheckedAt)) {
			best = vs
		}
	}
	if best == nil {
		return nil
	}
	u, err := url.Parse(best.PlaylistSource.URL)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

// play godoc
//
//	@Summary		A playlist or a segment of a watched stream
//	@Description	Serves what the token names, requested by the server itself. In a playlist every address is replaced by such a path. The X-Upstream header names the server that answered.
//	@Tags			streams
//	@Param			token	path	string	true	"Token from the list of sources or from a playlist served here"
//	@Success		200
//	@Success		206
//	@Failure		403	{object}	apiError
//	@Failure		502	{object}	apiError
//	@Router			/api/v1/play/{token} [get]
func (s *Server) play(c *gin.Context) {
	tok, ok := s.openPlay(c.Param("token"))
	if !ok {
		fail(c, http.StatusForbidden, "bad_token", "this address is not valid or has expired; open the stream again")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	st, err := s.st.GetStream(ctx, tok.Stream)
	if s.storeError(c, err) {
		return
	}
	job := agentproto.FetchRequest{URL: tok.URL, Headers: st.Headers, HeadersFor: st.URL, Insecure: st.Insecure,
		Entry: tok.Entry, Inside: tok.Inside, MaxBody: playMaxBody}
	if from, length, ok := parseRange(c.GetHeader("Range")); ok {
		job.RangeOffset, job.RangeLength = from, length
	}
	var res agentproto.FetchResult
	var body []byte
	info := fetchInfo{}
	if tok.Agent == 0 || tok.Agent == store.LocalAgentID {
		res, body = s.fetchLocal(ctx, job)
	} else {
		res, body, info.Relay = s.fetchThroughAgent(ctx, tok.Agent, job)
		s.reportsMu.Lock()
		if rep, ok := s.reports[tok.Agent]; ok {
			info.Link = rep.status.Link
		}
		s.reportsMu.Unlock()
	}
	info.Origin = res.Timing
	s.describeFetch(c, info)
	if errors.Is(c.Request.Context().Err(), context.Canceled) {
		c.Abort() // the viewer went away
		return
	}
	if res.RemoteAddr != "" {
		if e, err := url.Parse(res.FinalURL); err == nil {
			c.Header("X-Upstream", e.Host+" "+hostname(res.RemoteAddr))
		}
	}
	switch {
	case res.Status == 0:
		fail(c, http.StatusBadGateway, "upstream", "the origin did not answer: "+res.Error)
		return
	case res.Error != "":
		fail(c, http.StatusBadGateway, "upstream", "the answer of the origin broke off: "+res.Error)
		return
	}
	ok200 := res.Status == http.StatusOK || res.Status == http.StatusPartialContent
	if ok200 && isPlaylist(body) {
		base := res.FinalURL
		if base == "" {
			base = tok.URL
		}
		inside := tok.Inside
		if tok.Entry {
			inside = res.Inside
		}
		// What a playlist names lives as long as the playlist's own token,
		// so that the same address is the same token every time the
		// playlist is read: a player takes a segment whose address changed
		// between two readings for another segment under the same number.
		out := rewritePlaylist(body, base, func(u string) string {
			return s.signPlay(playToken{URL: u, Stream: tok.Stream, Agent: tok.Agent, Expires: tok.Expires, Inside: inside})
		})
		c.Data(http.StatusOK, "application/vnd.apple.mpegurl", out)
		return
	}
	if res.ContentRange != "" {
		c.Header("Content-Range", res.ContentRange)
	}
	if res.AcceptRanges != "" {
		c.Header("Accept-Ranges", res.AcceptRanges)
	}
	ct := res.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	c.Data(res.Status, ct, body)
}

// fetchInfo is how a fetch for a viewer went, hop by hop. The player's
// page draws its charts from it.
type fetchInfo struct {
	// Origin is the request to the origin, made by the server or by the
	// agent the stream is watched through.
	Origin *agentproto.FetchTiming `json:"origin,omitempty"`
	// Relay is the way through the agent, Link what the agent says of its
	// connection to the server; both absent when the server fetched itself.
	Relay *relayTiming     `json:"relay,omitempty"`
	Link  *agentproto.Link `json:"link,omitempty"`
}

// describeFetch tells the viewer's browser how the fetch went: in the
// standard Server-Timing header, which browsers show in their own tools,
// and in full in X-Fetch-Info, base64 of the JSON of a fetchInfo.
func (s *Server) describeFetch(c *gin.Context, info fetchInfo) {
	var parts []string
	if info.Origin != nil {
		parts = append(parts, "origin;dur="+strconv.FormatFloat(info.Origin.TotalMs, 'f', 1, 64))
	}
	if info.Relay != nil {
		parts = append(parts, "relay_wait;dur="+strconv.FormatFloat(info.Relay.WaitMs, 'f', 1, 64),
			"relay_upload;dur="+strconv.FormatFloat(info.Relay.UploadMs, 'f', 1, 64))
	}
	if len(parts) > 0 {
		c.Header("Server-Timing", strings.Join(parts, ", "))
	}
	if b, err := json.Marshal(info); err == nil {
		c.Header("X-Fetch-Info", base64.StdEncoding.EncodeToString(b))
	}
}

// fetchLocal fetches from the server itself.
func (s *Server) fetchLocal(ctx context.Context, job agentproto.FetchRequest) (agentproto.FetchResult, []byte) {
	hc := s.playSecure
	if job.Insecure {
		hc = s.playInsecure
	}
	return relay.Fetch(ctx, hc, job)
}

func isPlaylist(body []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(body[:min(len(body), 16)], "\xef\xbb\xbf \r\n"), []byte("#EXTM3U"))
}

// parseRange reads "bytes=a-b"; other forms are not passed on.
func parseRange(h string) (from, length int64, ok bool) {
	spec, found := strings.CutPrefix(h, "bytes=")
	a, b, cut := strings.Cut(spec, "-")
	if !found || !cut || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	from, err1 := strconv.ParseInt(a, 10, 64)
	to, err2 := strconv.ParseInt(b, 10, 64)
	if err1 != nil || err2 != nil || from < 0 || to < from {
		return 0, 0, false
	}
	return from, to - from + 1, true
}

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// rewritePlaylist replaces every address in a playlist, whether on a line
// of its own or in a URI attribute of a tag, by what token makes of it.
// Addresses are resolved against base first. What is not an http address
// (a data URI, a key delivered by other means) is left as it is.
func rewritePlaylist(body []byte, base string, token func(absolute string) string) []byte {
	b, err := url.Parse(base)
	if err != nil {
		return body
	}
	rewrite := func(ref string) (string, bool) {
		u, err := b.Parse(strings.TrimSpace(ref))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return "", false
		}
		return token(u.String()), true
	}
	var out bytes.Buffer
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
		case strings.HasPrefix(line, "#"):
			line = uriAttr.ReplaceAllStringFunc(line, func(m string) string {
				if t, ok := rewrite(uriAttr.FindStringSubmatch(m)[1]); ok {
					return `URI="` + t + `"`
				}
				return m
			})
		default:
			if t, ok := rewrite(line); ok {
				line = t
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
