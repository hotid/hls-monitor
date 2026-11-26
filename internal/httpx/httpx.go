// Package httpx is an HTTP client that records per-request network timings
// and connection details needed for stream monitoring.
package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/hotid/hls-monitor/internal/types/cachestatus"
)

const (
	DefaultUserAgent = "hls-monitor/1.0"
	maxRedirects     = 10
)

// Options configures a Client.
type Options struct {
	UserAgent          string
	InsecureSkipVerify bool
	// DialTimeout bounds DNS + TCP connect. Zero means 10s.
	DialTimeout time.Duration
}

// Client performs timed GET requests. It is safe for concurrent use.
type Client struct {
	hc *http.Client
	// fresh keeps no connections: see Request.Fresh.
	fresh    *http.Client
	ua       string
	counters connCounters
}

// redirectTrace collects what happened on the way to the final response.
type redirectTrace struct {
	addrs []netip.AddrPort // remote address of every connection used, in order
	hops  []Hop
}
type redirectKey struct{}

// New creates a client with its own connection pool.
func New(o Options) *Client {
	if o.UserAgent == "" {
		o.UserAgent = DefaultUserAgent
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: o.DialTimeout, KeepAlive: 30 * time.Second, ControlContext: controlDial}
	tr := &http.Transport{
		Proxy:             proxyOf,
		DialContext:       dialer.DialContext,
		ForceAttemptHTTP2: true,
		// One client serves every stream, and checks come in bursts: dozens
		// of requests to one server at once. A connection that finds no
		// room among the idle ones is closed and opened again a moment
		// later, so the room is for a burst, not for the average.
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: o.InsecureSkipVerify}, //nolint:gosec // opt-in per stream
	}
	// A second pool that keeps nothing: a request marked Fresh makes its own
	// connections, which is the only way to see how long that takes.
	once := tr.Clone()
	once.DisableKeepAlives = true
	hc := &http.Client{Transport: tr}
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			// Most often it is not a long way but a circle.
			for i := len(via) - 1; i >= 0; i-- {
				if via[i].URL.String() != req.URL.String() {
					continue
				}
				if i == len(via)-1 {
					return fmt.Errorf("redirect loop: %s redirects to itself (stopped after %d redirects)", req.URL.Host, maxRedirects)
				}
				return fmt.Errorf("redirect loop: the request came back to %s (stopped after %d redirects)", req.URL.Host, maxRedirects)
			}
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		// Go carries headers over to the redirect target; the configured
		// ones do not leave their site.
		if sc, ok := req.Context().Value(headerScopeKey{}).(headerScope); ok && !SameSite(req.URL.Hostname(), sc.host) {
			for _, name := range sc.names {
				req.Header.Del(name)
			}
		}
		if t, ok := req.Context().Value(redirectKey{}).(*redirectTrace); ok && req.Response != nil {
			h := Hop{Status: req.Response.StatusCode, Server: req.Response.Header.Get("Server")}
			if prev := via[len(via)-1]; prev.URL != nil {
				h.URL = prev.URL.String()
			}
			if n := len(via) - 1; n < len(t.addrs) {
				h.RemoteAddr = t.addrs[n]
			}
			t.hops = append(t.hops, h)
		}
		return nil
	}
	return &Client{hc: hc, fresh: &http.Client{Transport: once, CheckRedirect: hc.CheckRedirect}, ua: o.UserAgent}
}

// CloseIdle closes idle connections of the client's pool.
func (c *Client) CloseIdle() { c.hc.CloseIdleConnections() }

// Request describes a single GET.
type Request struct {
	URL     string
	Headers map[string]string
	// HeadersFor is the URL the headers were configured for. When set,
	// they are sent only to its site: they may carry a token, and a
	// playlist or a redirect can name any host.
	HeadersFor string
	// MaxBody limits how many body bytes are kept and read. Zero means 64 MiB.
	MaxBody int64
	// Reach says which addresses the request may connect to; ReachAny by
	// default. It is not applied when the client goes through a proxy.
	Reach Reach
	// RangeOffset/RangeLength request a byte range when RangeLength > 0.
	RangeOffset int64
	RangeLength int64
	// Fresh makes the request, and every redirect it follows, on a new
	// connection that is closed afterwards: the way to measure the name
	// lookup, the connection and the TLS handshake, which a kept
	// connection never repeats.
	Fresh bool
}

// Hop is one redirect on the way to the final response.
type Hop struct {
	URL        string         `json:"url"`
	Status     int            `json:"status"`
	RemoteAddr netip.AddrPort `json:"remote_addr,omitzero" swaggertype:"string"`
	Server     string         `json:"server,omitempty"`
}

// Response is the outcome of a request that reached the server.
type Response struct {
	URL       string
	FinalURL  string
	Status    int
	Redirects int
	// Hops lists the redirects that were followed, in order. The server
	// that produced the final response is described by FinalURL,
	// RemoteAddr and Header.
	Hops []Hop

	RemoteAddr netip.AddrPort // the address of the server that answered; not valid when none did
	// Inside tells that the first connection of the request was made to
	// an address that is not public.
	Inside     bool
	Proto      string // HTTP/1.1, HTTP/2.0
	TLSVersion string
	// CertNotAfter is the leaf certificate expiry, zero for plain HTTP.
	CertNotAfter time.Time
	ConnReused   bool
	// TCP is what the kernel knew about the connection that gave the final
	// response, read once the body was in; nil where that cannot be told.
	TCP *TCPInfo

	DNS     time.Duration
	Connect time.Duration
	// Lookups and Connects count the name lookups and the connections
	// made, redirects included: DNS and Connect are their sums.
	Lookups  int
	Connects int
	TLS      time.Duration
	TTFB     time.Duration // from request start to first response byte
	Total    time.Duration // from request start to end of body

	Size          int64 // body bytes actually read
	ContentLength int64 // declared length, -1 when unknown
	// TotalSize is the size of the whole resource when the server told it:
	// from Content-Range for a partial response, otherwise Content-Length.
	// Zero when unknown.
	TotalSize int64
	// Truncated is set when the body was cut at MaxBody.
	Truncated bool
	// LengthMismatch is set when fewer bytes arrived than Content-Length declared.
	LengthMismatch bool

	Header http.Header
	Body   []byte
}

// OK reports whether the status is a successful one for a GET or range GET.
func (r *Response) OK() bool {
	return r.Status == http.StatusOK || r.Status == http.StatusPartialContent
}

// identityHeaders are response headers that tell which server, cache node or
// software version produced a response.
var identityHeaders = []string{"Server", "Via", "X-Served-By", "X-Cache", "X-Cache-Status", "CF-Cache-Status", "CF-Ray", "X-Amz-Cf-Pop", "X-Node", "X-Backend", "X-Powered-By"}

// Identity returns the response headers that identify the responding
// server, by canonical name. Absent headers are omitted.
func (r *Response) Identity() map[string]string {
	var out map[string]string
	for _, h := range identityHeaders {
		if v := r.Header.Get(h); v != "" {
			if out == nil {
				out = map[string]string{}
			}
			if len(v) > 200 {
				v = v[:200]
			}
			out[h] = v
		}
	}
	return out
}

// Bps is the average download rate in bits per second over the whole request.
func (r *Response) Bps() float64 {
	if r.Total <= 0 {
		return 0
	}
	return float64(r.Size*8) / r.Total.Seconds()
}

// AgeSeconds returns the Age response header, ok=false when absent.
func (r *Response) AgeSeconds() (float64, bool) {
	v := r.Header.Get("Age")
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// CacheStatus interprets common CDN cache headers.
// The value is not set when nothing conclusive is present.
func (r *Response) CacheStatus() cachestatus.Status {
	for _, h := range []string{"X-Cache", "CF-Cache-Status", "X-Cache-Status", "Cache-Status", "X-Proxy-Cache"} {
		v := strings.ToLower(r.Header.Get(h))
		if v == "" {
			continue
		}
		switch {
		case strings.Contains(v, "hit"):
			return cachestatus.Hit()
		case strings.Contains(v, "miss"), strings.Contains(v, "expired"), strings.Contains(v, "bypass"):
			return cachestatus.Miss()
		}
	}
	return cachestatus.Status{}
}

// Get performs the request. A non-nil error means no HTTP response was
// obtained or the body could not be read; a partially filled Response is
// still returned in that case when the server answered.
func (c *Client) Get(ctx context.Context, rq Request) (*Response, error) {
	if rq.MaxBody <= 0 {
		rq.MaxBody = 64 << 20
	}
	rt := &redirectTrace{}
	ctx = context.WithValue(ctx, redirectKey{}, rt)
	reach := &reachState{mode: rq.Reach}
	ctx, reach.cancel = context.WithCancelCause(ctx)
	defer reach.cancel(nil)
	ctx = context.WithValue(ctx, reachKey{}, reach)
	// refused replaces the error of a request ended for where it connected.
	refused := func(err error) error {
		if cause := context.Cause(ctx); errors.Is(cause, ErrNotPublic) {
			return cause
		}
		return err
	}

	res := &Response{URL: rq.URL, ContentLength: -1}
	var start time.Time
	var lastConn net.Conn
	// The library dials on a goroutine of its own, and a dial the request
	// did not wait for — it was given a connection that came free first —
	// goes on after the request is over and tells of itself then. What
	// the dials tell is gathered apart, under a lock, and goes into the
	// answer once: settle ends the gathering before the answer is given
	// to who asked, and what is told after that is not of this request.
	var told struct {
		sync.Mutex
		over                          bool
		dnsStart, connStart, tlsStart time.Time
		dns, connect, tls             time.Duration
		lookups, connects             int
	}
	note := func(f func()) {
		told.Lock()
		defer told.Unlock()
		if !told.over {
			f()
		}
	}
	settle := func() {
		told.Lock()
		defer told.Unlock()
		told.over = true
		res.DNS, res.Lookups, res.Connect, res.Connects, res.TLS = told.dns, told.lookups, told.connect, told.connects, told.tls
	}
	defer settle()
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { note(func() { told.dnsStart = time.Now() }) },
		DNSDone: func(httptrace.DNSDoneInfo) {
			note(func() {
				if !told.dnsStart.IsZero() {
					told.dns += time.Since(told.dnsStart)
					told.lookups++
				}
			})
		},
		ConnectStart: func(string, string) { note(func() { told.connStart = time.Now() }) },
		ConnectDone: func(_, _ string, err error) {
			note(func() {
				if err == nil && !told.connStart.IsZero() {
					told.connect += time.Since(told.connStart)
					told.connects++
				}
			})
		},
		TLSHandshakeStart: func() { note(func() { told.tlsStart = time.Now() }) },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			note(func() {
				if err == nil && !told.tlsStart.IsZero() {
					told.tls += time.Since(told.tlsStart)
				}
			})
		},
		GotConn: func(i httptrace.GotConnInfo) {
			res.ConnReused = i.Reused
			if i.Conn != nil {
				res.RemoteAddr = AddrPortOf(i.Conn.RemoteAddr())
				lastConn = i.Conn
			}
			rt.addrs = append(rt.addrs, res.RemoteAddr)
			// Ending the request is not enough for a connection kept
			// from before: the request is written to it at once, and may
			// be answered before the end is noticed. The connection is
			// closed, so that nothing is written.
			if res.RemoteAddr.IsValid() && !reach.use(res.RemoteAddr.Addr()) && i.Conn != nil {
				_ = i.Conn.Close()
			}
		},
		GotFirstResponseByte: func() { res.TTFB = time.Since(start) },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, rq.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	if len(rq.Headers) > 0 {
		home := req.URL.Hostname()
		if u, err := url.Parse(rq.HeadersFor); err == nil && rq.HeadersFor != "" {
			home = u.Hostname()
		}
		if SameSite(req.URL.Hostname(), home) {
			sc := headerScope{host: home}
			for k, v := range rq.Headers {
				req.Header.Set(k, v)
				sc.names = append(sc.names, k)
			}
			req = req.WithContext(context.WithValue(req.Context(), headerScopeKey{}, sc))
		}
	}
	if rq.RangeLength > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rq.RangeOffset, rq.RangeOffset+rq.RangeLength-1))
	}

	start = time.Now()
	hc := c.hc
	if rq.Fresh {
		hc = c.fresh
	}
	resp, err := hc.Do(req)
	if err != nil {
		res.Total = time.Since(start)
		// The way to the failure is worth knowing, a redirect loop above all.
		res.Hops = rt.hops
		res.Redirects = len(rt.hops)
		return res, refused(cleanErr(err))
	}
	defer resp.Body.Close()
	// An answer that came all the same is not given to who asked.
	if cause := context.Cause(ctx); errors.Is(cause, ErrNotPublic) {
		return res, cause
	}
	if len(rt.addrs) > 0 {
		res.Inside = !addrPublic(rt.addrs[0].Addr())
	}

	res.Status = resp.StatusCode
	res.Proto = resp.Proto
	res.Header = resp.Header
	res.Hops = rt.hops
	res.Redirects = len(rt.hops)
	res.ContentLength = resp.ContentLength
	if resp.Request != nil && resp.Request.URL != nil {
		res.FinalURL = resp.Request.URL.String()
	}
	if resp.TLS != nil {
		res.TLSVersion = tls.VersionName(resp.TLS.Version)
		if len(resp.TLS.PeerCertificates) > 0 {
			res.CertNotAfter = resp.TLS.PeerCertificates[0].NotAfter
		}
	}

	if resp.StatusCode == http.StatusPartialContent {
		// "bytes 0-187/3775040"; the total may be "*".
		if _, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/"); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(total), 10, 64); err == nil && n > 0 {
				res.TotalSize = n
			}
		}
	} else if resp.ContentLength > 0 {
		res.TotalSize = resp.ContentLength
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, rq.MaxBody+1))
	res.Total = time.Since(start)
	if lastConn != nil {
		if info, ok := ConnInfo(lastConn); ok {
			c.counters.grown(lastConn, &info)
			res.TCP = &info
		}
	}
	if int64(len(body)) > rq.MaxBody {
		body = body[:rq.MaxBody]
		res.Truncated = true
	}
	res.Body = body
	res.Size = int64(len(body))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			res.LengthMismatch = true
		}
		return res, refused(cleanErr(err))
	}
	// Transparent gzip decoding makes ContentLength -1, so a mismatch can only
	// be judged when the length is known.
	if res.ContentLength >= 0 && !res.Truncated && res.Size != res.ContentLength {
		res.LengthMismatch = true
	}
	return res, nil
}

// cleanErr strips the method and URL prefix from transport errors: the URL
// is already known to the caller.
func cleanErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

type headerScopeKey struct{}

// headerScope says which request headers were configured and for which host.
type headerScope struct {
	host  string
	names []string
}

// SameSite reports whether two hosts belong to one site: the same host, or
// names under one registrable domain (cdn.example.com and
// edge-3.example.com). IP addresses are a site each.
func SameSite(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSuffix(a, ".")), strings.ToLower(strings.TrimSuffix(b, "."))
	if a == b {
		return true
	}
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	da, errA := publicsuffix.EffectiveTLDPlusOne(a)
	db, errB := publicsuffix.EffectiveTLDPlusOne(b)
	return errA == nil && errB == nil && da == db
}

// proxyOf is the proxy of the environment, if a request goes through one.
// The connection is then made to the proxy, which connects where it
// pleases, and Reach cannot be applied — to that request only: one that
// the environment sends directly is held to it as any other.
func proxyOf(r *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(r)
	if st, ok := r.Context().Value(reachKey{}).(*reachState); ok {
		st.viaProxy.Store(u != nil)
	}
	return u, err
}

// AddrPortOf is the address of the far end of a connection; not valid
// for a connection that is not one over TCP.
func AddrPortOf(a net.Addr) netip.AddrPort {
	if t, ok := a.(*net.TCPAddr); ok {
		ap := t.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}

// IsWeb tells whether an address is one of the web: http or https. It is
// the one place that says which schemes the monitor goes to.
func IsWeb(u *url.URL) bool { return u != nil && (u.Scheme == "http" || u.Scheme == "https") }

// ParseWebURL reads an absolute address of the web: http or https, with a
// host.
func ParseWebURL(text string) (*url.URL, error) {
	u, err := url.Parse(text)
	if err != nil {
		return nil, err
	}
	if !IsWeb(u) || u.Host == "" {
		return nil, errors.New("not an absolute http or https URL")
	}
	return u, nil
}
