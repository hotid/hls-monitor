package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/types/cachestatus"
)

func TestGet(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != DefaultUserAgent || r.Header.Get("X-Token") != "secret" {
			http.Error(w, "missing headers", http.StatusBadRequest)
			return
		}
		w.Header().Set("Age", "7")
		w.Header().Set("X-Cache", "HIT from edge-3")
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	})
	mux.HandleFunc("/hop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/hop2", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/hop", http.StatusFound) })
	mux.HandleFunc("/range", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=10-29" {
			http.Error(w, "bad range: "+r.Header.Get("Range"), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(make([]byte, 20))
	})
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "500")
		_, _ = w.Write(make([]byte, 100))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(Options{})
	ctx := context.Background()
	hdr := map[string]string{"X-Token": "secret"}

	t.Run("plain", func(t *testing.T) {
		r, err := c.Get(ctx, Request{URL: srv.URL + "/ok", Headers: hdr})
		if err != nil {
			t.Fatal(err)
		}
		if !r.OK() || r.Size != 1000 || len(r.Body) != 1000 || r.LengthMismatch || r.Truncated {
			t.Fatalf("unexpected response: status=%d size=%d mismatch=%v", r.Status, r.Size, r.LengthMismatch)
		}
		if r.TTFB < 20*time.Millisecond || r.Total < r.TTFB {
			t.Errorf("timings ttfb=%s total=%s do not reflect the 20ms server delay", r.TTFB, r.Total)
		}
		if r.RemoteAddr == "" || r.Proto != "HTTP/1.1" || r.Redirects != 0 {
			t.Errorf("connection details: addr=%q proto=%q redirects=%d", r.RemoteAddr, r.Proto, r.Redirects)
		}
		if age, ok := r.AgeSeconds(); !ok || age != 7 {
			t.Errorf("age = %v/%v, want 7", age, ok)
		}
		if r.CacheStatus() != cachestatus.Hit() {
			t.Errorf("cache status = %q, want hit", r.CacheStatus())
		}
		if r.Bps() <= 0 {
			t.Error("rate must be positive")
		}
	})

	t.Run("redirects", func(t *testing.T) {
		r, err := c.Get(ctx, Request{URL: srv.URL + "/hop2", Headers: hdr})
		if err != nil {
			t.Fatal(err)
		}
		if r.Redirects != 2 || r.FinalURL != srv.URL+"/ok" || !r.OK() {
			t.Errorf("redirects=%d final=%s status=%d", r.Redirects, r.FinalURL, r.Status)
		}
		if len(r.Hops) != 2 || r.Hops[0].URL != srv.URL+"/hop2" || r.Hops[1].URL != srv.URL+"/hop" || r.Hops[0].Status != 302 || r.Hops[0].RemoteAddr == "" {
			t.Errorf("hops = %+v", r.Hops)
		}
		if id := r.Identity(); id["X-Cache"] != "HIT from edge-3" || len(id) != 1 {
			t.Errorf("identity = %v", id)
		}
	})

	t.Run("range", func(t *testing.T) {
		r, err := c.Get(ctx, Request{URL: srv.URL + "/range", RangeOffset: 10, RangeLength: 20})
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != http.StatusPartialContent || !r.OK() || r.Size != 20 {
			t.Errorf("status=%d size=%d", r.Status, r.Size)
		}
	})

	t.Run("body limit", func(t *testing.T) {
		r, err := c.Get(ctx, Request{URL: srv.URL + "/ok", Headers: hdr, MaxBody: 300})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Truncated || r.Size != 300 || r.LengthMismatch {
			t.Errorf("truncated=%v size=%d mismatch=%v", r.Truncated, r.Size, r.LengthMismatch)
		}
	})

	t.Run("short body", func(t *testing.T) {
		r, err := c.Get(ctx, Request{URL: srv.URL + "/short"})
		if err == nil {
			t.Fatal("a body shorter than Content-Length must be an error")
		}
		if r == nil || r.Status != 200 || !r.LengthMismatch || r.Size != 100 {
			t.Errorf("partial response not reported: %+v", r)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		r, err := c.Get(tctx, Request{URL: srv.URL + "/slow"})
		if err == nil {
			t.Fatal("want a timeout error")
		}
		if strings.Contains(err.Error(), srv.URL) {
			t.Errorf("error should not repeat the URL: %v", err)
		}
		if r == nil || r.Status != 0 {
			t.Errorf("no HTTP status expected, got %+v", r)
		}
	})

	t.Run("refused", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		dead.Close()
		if _, err := c.Get(ctx, Request{URL: dead.URL}); err == nil {
			t.Fatal("want a connection error")
		}
	})
}

// Configured headers may carry a token: they go to the site they were
// configured for and nowhere else, whoever names the other host.
func TestHeadersStayOnTheirSite(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"cdn.example.com", "edge-3.example.com", true},
		{"example.com", "EXAMPLE.com.", true},
		{"a.example.co.uk", "b.other.co.uk", false},
		{"example.com", "example.org", false},
		{"10.0.0.1", "10.0.0.1", true},
		{"10.0.0.1", "10.0.0.2", false},
		{"localhost", "localhost", true},
	} {
		if got := SameSite(c.a, c.b); got != c.want {
			t.Errorf("SameSite(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}

	var got []string
	record := func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Host+r.URL.Path+" token="+r.Header.Get("X-Token"))
	}
	foreign := httptest.NewServer(http.HandlerFunc(record))
	defer foreign.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(w, r)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, strings.Replace(foreign.URL, "127.0.0.1", "localhost", 1)+"/landed", http.StatusFound)
		}
	}))
	defer home.Close()
	c := New(Options{})
	headers := map[string]string{"X-Token": "secret"}
	foreignURL := strings.Replace(foreign.URL, "127.0.0.1", "localhost", 1)
	for _, u := range []string{home.URL + "/playlist", home.URL + "/redirect", foreignURL + "/segment"} {
		if _, err := c.Get(context.Background(), Request{URL: u, Headers: headers, HeadersFor: home.URL + "/playlist"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range got {
		at127 := strings.HasPrefix(line, "127.0.0.1")
		if has := strings.HasSuffix(line, "token=secret"); has != at127 {
			t.Errorf("%s: the token must reach the stream's own host only", line)
		}
	}
	if len(got) != 4 {
		t.Errorf("requests seen: %v", got)
	}
}

func TestPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "93.184.216.34": true, "2a00:1450:4010:c0b::8a": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "198.18.0.1": false, "0.0.0.0": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "::ffff:10.0.0.1": false, "224.0.0.1": false,
		// Addresses of IPv6 that carry one of IPv4 are judged by it.
		"64:ff9b::a9fe:a9fe": false, "64:ff9b::808:808": true, "2002:7f00:1::": false, "2002:808:808::1": true,
		"fec0::1": false, "2001:0:4136:e378:8000:63bf:3fff:fdd2": false,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %v, want %v", addr, got, want)
		}
	}
}

// What a request may connect to, connection by connection.
func TestReachDecisions(t *testing.T) {
	const pub, in = "93.184.216.34:443", "10.0.0.5:80"
	for _, tc := range []struct {
		name  string
		mode  Reach
		conns []string
		want  []bool
	}{
		{"no restriction", ReachAny, []string{pub, in, in}, []bool{true, true, true}},
		{"public only", ReachPublic, []string{pub, in}, []bool{true, false}},
		{"public only, inside at once", ReachPublic, []string{in}, []bool{false}},
		{"a public stream redirects inside", ReachFirstAny, []string{pub, pub, in}, []bool{true, true, false}},
		{"a stream that is inside may stay there and leave", ReachFirstAny, []string{in, in, pub, in}, []bool{true, true, true, true}},
	} {
		st := &reachState{mode: tc.mode, cancel: func(error) {}}
		via := &reachState{mode: tc.mode, cancel: func(error) {}}
		via.viaProxy.Store(true)
		for i, addr := range tc.conns {
			if !via.allowed(addr) {
				t.Errorf("%s: a connection to the proxy at %s was refused", tc.name, addr)
			}
			got := st.allowed(addr)
			if got != tc.want[i] {
				t.Errorf("%s: connection %d to %s allowed = %v, want %v", tc.name, i+1, addr, got, tc.want[i])
			}
			if got {
				st.use(addr)
			}
		}
	}
}

func TestReachIsEnforced(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()
	// A proxy in the environment takes the rule off the requests that go
	// through it, and off no other: one to the loopback never does.
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	c := New(Options{})
	ctx := context.Background()

	// The test server is on the loopback: not a public address.
	resp, err := c.Get(ctx, Request{URL: srv.URL, Reach: ReachPublic})
	if !errors.Is(err, ErrNotPublic) || hits.Load() != 0 || (resp != nil && len(resp.Body) > 0) {
		t.Fatalf("a request that may only go to public addresses reached the loopback: err=%v hits=%d", err, hits.Load())
	}
	resp, err = c.Get(ctx, Request{URL: srv.URL, Reach: ReachFirstAny})
	if err != nil || !resp.Inside || string(resp.Body) != "secret" {
		t.Fatalf("the stream's own address may be inside: %v %+v", err, resp)
	}
	// The connection is kept by now; a request that may not have it does
	// not get it from the pool either.
	_, err = c.Get(ctx, Request{URL: srv.URL, Reach: ReachPublic})
	if !errors.Is(err, ErrNotPublic) || hits.Load() != 1 {
		t.Fatalf("a kept connection was given to a request that may not use it: err=%v hits=%d", err, hits.Load())
	}
	if resp, err = c.Get(ctx, Request{URL: srv.URL}); err != nil || resp.Status != 200 {
		t.Fatalf("no restriction by default: %v", err)
	}
}

func TestConnectionFigures(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the kernel's figures of a connection are read on Linux only")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 64<<10))
	}))
	defer srv.Close()
	c := New(Options{})
	for i := range 2 {
		resp, err := c.Get(context.Background(), Request{URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		if resp.TCP == nil || resp.TCP.RTT <= 0 || resp.TCP.RTT > time.Second {
			t.Fatalf("request %d: connection figures = %+v", i+1, resp.TCP)
		}
		if i == 1 && !resp.ConnReused {
			t.Error("the second request must reuse the connection, and still have its figures")
		}
	}
}

func TestConnectionCountersGrowth(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	var c connCounters
	first := TCPInfo{Retransmits: 5, OutOfOrder: 7}
	c.grown(a, &first)
	if first.NewRetransmits != 5 || first.NewOutOfOrder != 7 {
		t.Errorf("a connection seen for the first time counts whole: %+v", first)
	}
	next := TCPInfo{Retransmits: 6, OutOfOrder: 7}
	c.grown(a, &next)
	if next.NewRetransmits != 1 || next.NewOutOfOrder != 0 {
		t.Errorf("growth since the previous look: %+v", next)
	}
	// A smaller figure under the same addresses is another connection.
	again := TCPInfo{Retransmits: 2, OutOfOrder: 1}
	c.grown(a, &again)
	if again.NewRetransmits != 2 || again.NewOutOfOrder != 1 {
		t.Errorf("a new connection under the same addresses: %+v", again)
	}
}

func TestFreshRequestMakesItsOwnConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/in" {
			http.Redirect(w, r, "/there", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := New(Options{})
	ctx := context.Background()

	// Kept connections are used again, and nothing is measured on them.
	if _, err := c.Get(ctx, Request{URL: srv.URL + "/in"}); err != nil {
		t.Fatal(err)
	}
	kept, err := c.Get(ctx, Request{URL: srv.URL + "/in"})
	if err != nil {
		t.Fatal(err)
	}
	if kept.Connects != 0 || kept.Connect != 0 {
		t.Errorf("a kept connection: %d connections made in %v", kept.Connects, kept.Connect)
	}

	// A fresh request makes one for every hop, whatever is kept.
	for range 2 {
		fresh, err := c.Get(ctx, Request{URL: srv.URL + "/in", Fresh: true})
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Connects != 2 || fresh.Connect <= 0 || fresh.ConnReused {
			t.Errorf("a fresh request: %d connections in %v, reused %v; want 2", fresh.Connects, fresh.Connect, fresh.ConnReused)
		}
	}
}
