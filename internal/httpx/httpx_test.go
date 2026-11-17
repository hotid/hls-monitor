package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		if r.CacheStatus() != "hit" {
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
