package metrics

import (
	"compress/gzip"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteAndRetry(t *testing.T) {
	var mu sync.Mutex
	var got []string
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/api/v1/import/prometheus" {
			http.NotFound(w, r)
			return
		}
		if fail {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, string(b))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	ts := time.UnixMilli(1700000000123)
	c.Write([]Sample{
		{Name: "hls_up", Stream: 3, Agent: 1, Variant: 7, Value: 1, Time: ts},
		{Name: "hls_stream_status", Stream: 3, Agent: 1, Value: 2, Time: ts},
		{Name: "bad name", Stream: 3, Agent: 1, Value: 1, Time: ts},
		{Name: "hls_nan", Stream: 3, Agent: 1, Value: math.NaN(), Time: ts},
	})

	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("flush must fail while the server is down")
	}
	if c.Stats().QueuedBytes == 0 {
		t.Fatal("failed data must stay queued")
	}

	mu.Lock()
	fail = false
	mu.Unlock()
	c.Write([]Sample{{Name: "hls_up", Stream: 3, Agent: 1, Variant: 7, Value: 0, Time: ts.Add(time.Second)}})
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c.Stats().QueuedBytes != 0 {
		t.Errorf("queue not drained: %+v", c.Stats())
	}

	want := `hls_up{stream="3",agent="1",variant="7"} 1 1700000000123
hls_stream_status{stream="3",agent="1"} 2 1700000000123
hls_up{stream="3",agent="1",variant="7"} 0 1700000001123
`
	if all := strings.Join(got, ""); all != want {
		t.Errorf("sent:\n%s\nwant:\n%s", all, want)
	}
}

func TestQueryRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("query") == "boom" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"status":"error","error":"cannot parse"}`))
			return
		}
		if r.Form.Get("step") != "15s" || r.Form.Get("start") != "100.000" || r.Form.Get("end") != "200.000" {
			http.Error(w, "bad params "+r.Form.Encode(), http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"__name__":"hls_up","variant":"7"},"values":[[100,"1"],[115.5,"0.25"],[130,"NaN"]]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, nil)
	series, err := c.QueryRange(context.Background(), "hls_up", time.Unix(100, 0), time.Unix(200, 0), 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 || series[0].Labels["variant"] != "7" {
		t.Fatalf("series = %+v", series)
	}
	if p := series[0].Points; len(p) != 2 || p[1] != [2]float64{115.5, 0.25} {
		t.Errorf("points = %v, want two points with NaN dropped", p)
	}
	if _, err := c.QueryRange(context.Background(), "boom", time.Unix(100, 0), time.Unix(200, 0), 15*time.Second); err == nil || !strings.Contains(err.Error(), "cannot parse") {
		t.Errorf("query error not propagated: %v", err)
	}
}

func TestDisabledClient(t *testing.T) {
	c := New("", nil)
	c.Write([]Sample{{Name: "hls_up", Value: 1, Time: time.Now()}})
	if c.Stats().QueuedBytes != 0 {
		t.Error("a disabled client must not queue")
	}
	if s, err := c.QueryRange(context.Background(), "x", time.Now(), time.Now(), time.Second); err != nil || len(s) != 0 {
		t.Errorf("disabled query = %v, %v", s, err)
	}
}

// importServer stands in for VictoriaMetrics: it can be down, and keeps
// the lines it was given, whether compressed or not.
type importServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	lines  []string
}

func newImportServer(t *testing.T) *importServer {
	s := &importServer{status: http.StatusNoContent}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.status != http.StatusNoContent {
			http.Error(w, "no", s.status)
			return
		}
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("bad gzip body: %v", err)
				http.Error(w, "bad gzip", http.StatusBadRequest)
				return
			}
			body = zr
		}
		b, err := io.ReadAll(body)
		if err != nil {
			t.Errorf("reading the body: %v", err)
		}
		s.lines = append(s.lines, strings.Split(strings.TrimSpace(string(b)), "\n")...)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *importServer) set(status int) { s.mu.Lock(); s.status = status; s.mu.Unlock() }
func (s *importServer) count() int     { s.mu.Lock(); defer s.mu.Unlock(); return len(s.lines) }

func manySamples(n int, from time.Time) []Sample {
	out := make([]Sample, n)
	for i := range out {
		out[i] = Sample{Name: "hls_segment_total_seconds", Stream: int64(i%400 + 1), Agent: 1, Variant: int64(i%900 + 1), Value: float64(i) / 7, Time: from.Add(time.Duration(i) * time.Millisecond)}
	}
	return out
}

// VictoriaMetrics is away for longer than memory can hold, and the server
// is restarted meanwhile: nothing is lost.
func TestSamplesWaitOnDisk(t *testing.T) {
	vm := newImportServer(t)
	vm.set(http.StatusServiceUnavailable)
	dir := t.TempDir()
	ts := time.UnixMilli(1700000000000)

	c := New(vm.URL, nil)
	if err := c.UseDisk(dir, 0); err != nil {
		t.Fatal(err)
	}
	// More than is kept in memory: it goes to disk as it comes.
	const n = 150000
	for i := 0; i < n; i += 1000 {
		c.Write(manySamples(1000, ts.Add(time.Duration(i)*time.Second)))
	}
	st := c.Stats()
	if st.DiskBytes == 0 || st.QueuedBytes > spillBytes || st.DroppedBatches != 0 || !c.Backlog() {
		t.Fatalf("samples must have gone to disk: %+v", st)
	}
	if raw := n * 80; st.DiskBytes > int64(raw/4) {
		t.Errorf("the queue on disk takes %d bytes for about %d of samples: not compressed?", st.DiskBytes, raw)
	}
	// The server stops: what is still in memory is written out.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Run(ctx)

	c = New(vm.URL, nil)
	if err := c.UseDisk(dir, 0); err != nil {
		t.Fatal(err)
	}
	if !c.Backlog() {
		t.Fatal("the samples of the previous run are gone")
	}
	if err := c.Flush(context.Background()); err == nil {
		t.Fatal("flush must fail while VictoriaMetrics is away")
	}
	vm.set(http.StatusNoContent)
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := vm.count(); got != n {
		t.Errorf("VictoriaMetrics received %d samples, want %d", got, n)
	}
	if st := c.Stats(); st.DiskBytes != 0 || st.QueuedBytes != 0 || st.DroppedBatches != 0 || c.Backlog() {
		t.Errorf("after delivery: %+v", st)
	}
}

// A batch VictoriaMetrics will not take is dropped and counted; it does
// not hold up what comes after it.
func TestRefusedBatchIsDropped(t *testing.T) {
	vm := newImportServer(t)
	vm.set(http.StatusBadRequest)
	c := New(vm.URL, nil)
	c.Write(manySamples(10, time.UnixMilli(1700000000000)))
	if err := c.Flush(context.Background()); err != nil {
		t.Fatalf("a refused batch must not be an error to retry: %v", err)
	}
	if st := c.Stats(); st.QueuedBytes != 0 || st.DroppedBatches != 1 {
		t.Errorf("stats = %+v", st)
	}
	vm.set(http.StatusNoContent)
	c.Write(manySamples(5, time.UnixMilli(1700000100000)))
	if err := c.Flush(context.Background()); err != nil || vm.count() != 5 {
		t.Errorf("what came after must be delivered: %v, %d", err, vm.count())
	}
	// Too many requests is a fault of the moment: the batch waits.
	vm.set(http.StatusTooManyRequests)
	c.Write(manySamples(5, time.UnixMilli(1700000200000)))
	if err := c.Flush(context.Background()); err == nil || c.Stats().QueuedBytes == 0 {
		t.Errorf("a busy storage must be retried: %v %+v", err, c.Stats())
	}
}

func TestServerLabels(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got += string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	ts := time.UnixMilli(1700000000123)
	c.Write([]Sample{
		{Name: "hls_server_requests", Stream: 3, Agent: 1, Server: "edge-1.example.com:8080", IP: "2001:db8::1", Value: 4, Time: ts},
		// A name from a playlist is text of somebody else's: what cannot
		// be a host is not written at all.
		{Name: "hls_server_requests", Stream: 3, Agent: 1, Server: `a"} 1 1` + "\nevil", IP: "10.0.0.1", Value: 5, Time: ts},
	})
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := `hls_server_requests{stream="3",agent="1",server="edge-1.example.com:8080",ip="2001:db8::1"} 4 1700000000123` + "\n"
	if got != want {
		t.Errorf("written:\n%s\nwant:\n%s", got, want)
	}
}
