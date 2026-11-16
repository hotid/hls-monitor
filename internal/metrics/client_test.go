package metrics

import (
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
