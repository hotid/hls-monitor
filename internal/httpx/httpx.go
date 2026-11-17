// Package httpx is an HTTP client that records per-request network timings
// and connection details needed for stream monitoring.
package httpx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/net/publicsuffix"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
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
	ua string
	// proxied is set when requests go through a proxy: the connection is
	// then made to the proxy, and Reach cannot be applied.
	proxied bool
}

// redirectTrace collects what happened on the way to the final response.
type redirectTrace struct {
	addrs []string // remote address of every connection used, in order
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
	proxied := false
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		proxied = proxied || os.Getenv(name) != ""
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: o.InsecureSkipVerify}, //nolint:gosec // opt-in per stream
	}
	hc := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
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
		},
	}
	return &Client{hc: hc, ua: o.UserAgent, proxied: proxied}
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
}

// Hop is one redirect on the way to the final response.
type Hop struct {
	URL        string `json:"url"`
	Status     int    `json:"status"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	Server     string `json:"server,omitempty"`
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

	RemoteAddr string // ip:port of the server that answered
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
	TLS     time.Duration
	TTFB    time.Duration // from request start to first response byte
	Total   time.Duration // from request start to end of body

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
// It returns "hit", "miss" or "" when nothing conclusive is present.
func (r *Response) CacheStatus() string {
	for _, h := range []string{"X-Cache", "CF-Cache-Status", "X-Cache-Status", "Cache-Status", "X-Proxy-Cache"} {
		v := strings.ToLower(r.Header.Get(h))
		if v == "" {
			continue
		}
		switch {
		case strings.Contains(v, "hit"):
			return "hit"
		case strings.Contains(v, "miss"), strings.Contains(v, "expired"), strings.Contains(v, "bypass"):
			return "miss"
		}
	}
	return ""
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
	if c.proxied {
		reach.mode = ReachAny
	}
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
	var start, dnsStart, connStart, tlsStart time.Time
	var lastConn net.Conn
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				res.DNS += time.Since(dnsStart)
			}
		},
		ConnectStart: func(string, string) { connStart = time.Now() },
		ConnectDone: func(_, _ string, err error) {
			if err == nil && !connStart.IsZero() {
				res.Connect += time.Since(connStart)
			}
		},
		TLSHandshakeStart: func() { tlsStart = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil && !tlsStart.IsZero() {
				res.TLS += time.Since(tlsStart)
			}
		},
		GotConn: func(i httptrace.GotConnInfo) {
			res.ConnReused = i.Reused
			if i.Conn != nil {
				res.RemoteAddr = i.Conn.RemoteAddr().String()
				lastConn = i.Conn
			}
			rt.addrs = append(rt.addrs, res.RemoteAddr)
			if res.RemoteAddr != "" {
				reach.use(res.RemoteAddr)
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
	resp, err := c.hc.Do(req)
	if err != nil {
		res.Total = time.Since(start)
		// The way to the failure is worth knowing, a redirect loop above all.
		res.Hops = rt.hops
		res.Redirects = len(rt.hops)
		return res, refused(cleanErr(err))
	}
	defer resp.Body.Close()
	if len(rt.addrs) > 0 {
		res.Inside = !addrPublic(rt.addrs[0])
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
