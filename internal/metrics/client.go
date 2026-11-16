// Package metrics writes numeric time series to VictoriaMetrics and reads
// them back for charts.
package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sample is one value of one series. Variant 0 means the series belongs to
// the stream as a whole.
type Sample struct {
	Name    string
	Stream  int64
	Agent   int64
	Variant int64
	Value   float64
	Time    time.Time
}

// Series is one time series returned by a range query.
type Series struct {
	Labels map[string]string `json:"labels"`
	// Points are [unix seconds, value] pairs.
	Points [][2]float64 `json:"points"`
}

const (
	maxQueuedBytes = 64 << 20
	maxBatchBytes  = 4 << 20
)

var nameRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// ValidName reports whether s is acceptable as a metric name.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Client talks to one VictoriaMetrics instance. A Client created with an
// empty URL is disabled: writes are dropped and queries return nothing.
type Client struct {
	base string
	hc   *http.Client
	log  *slog.Logger

	mu      sync.Mutex
	queue   [][]byte
	queued  int
	dropped int64
	wake    chan struct{}
	failing bool
}

// New creates a client for the given base URL, e.g. http://victoriametrics:8428.
func New(baseURL string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		hc:   &http.Client{Timeout: 30 * time.Second},
		log:  log,
		wake: make(chan struct{}, 1),
	}
}

// Enabled reports whether a VictoriaMetrics URL is configured.
func (c *Client) Enabled() bool { return c.base != "" }

// Write queues samples for delivery. It never blocks; when VictoriaMetrics
// has been unreachable long enough to fill the queue, the oldest batches
// are dropped.
func (c *Client) Write(samples []Sample) {
	if !c.Enabled() || len(samples) == 0 {
		return
	}
	var b bytes.Buffer
	for _, s := range samples {
		if !ValidName(s.Name) || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		b.WriteString(s.Name)
		b.WriteString(`{stream="`)
		b.WriteString(strconv.FormatInt(s.Stream, 10))
		b.WriteString(`",agent="`)
		b.WriteString(strconv.FormatInt(s.Agent, 10))
		if s.Variant != 0 {
			b.WriteString(`",variant="`)
			b.WriteString(strconv.FormatInt(s.Variant, 10))
		}
		b.WriteString(`"} `)
		b.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatInt(s.Time.UnixMilli(), 10))
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return
	}
	c.mu.Lock()
	c.queue = append(c.queue, b.Bytes())
	c.queued += b.Len()
	for c.queued > maxQueuedBytes && len(c.queue) > 1 {
		c.queued -= len(c.queue[0])
		c.queue = c.queue[1:]
		c.dropped++
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run delivers queued samples until ctx is done, then makes a last attempt.
func (c *Client) Run(ctx context.Context) {
	if !c.Enabled() {
		<-ctx.Done()
		return
	}
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.Flush(fctx)
			cancel()
			return
		case <-c.wake:
		case <-retry.C:
		}
		// Let a burst of results accumulate into one request.
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
		if err := c.Flush(ctx); err != nil {
			if !c.failing {
				c.log.Warn("cannot write to VictoriaMetrics, samples are queued", "error", err)
			}
			c.failing = true
		} else if c.failing {
			c.failing = false
			c.log.Info("writing to VictoriaMetrics resumed")
		}
	}
}

// Flush sends everything queued. On failure the data stays queued.
func (c *Client) Flush(ctx context.Context) error {
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return nil
		}
		n, size := 0, 0
		for n < len(c.queue) && (n == 0 || size+len(c.queue[n]) <= maxBatchBytes) {
			size += len(c.queue[n])
			n++
		}
		batch := bytes.Join(c.queue[:n], nil)
		c.mu.Unlock()

		if err := c.post(ctx, "/api/v1/import/prometheus", "text/plain", batch); err != nil {
			return err
		}
		c.mu.Lock()
		// Write may have dropped from the head meanwhile; never cut more than exists.
		n = min(n, len(c.queue))
		for _, b := range c.queue[:n] {
			c.queued -= len(b)
		}
		c.queue = c.queue[n:]
		c.mu.Unlock()
	}
}

func (c *Client) post(ctx context.Context, path, contentType string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("victoriametrics %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

type queryResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string    `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
			Value  [2]json.RawMessage   `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func (c *Client) query(ctx context.Context, path string, q url.Values) (*queryResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(q.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out queryResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("victoriametrics %s: HTTP %d: %w", path, resp.StatusCode, err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("victoriametrics %s: %s", path, out.Error)
	}
	return &out, nil
}

func parsePoint(p [2]json.RawMessage) (pt [2]float64, ok bool) {
	var t float64
	var v string
	if json.Unmarshal(p[0], &t) != nil || json.Unmarshal(p[1], &v) != nil {
		return pt, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return pt, false
	}
	return [2]float64{t, f}, true
}

// QueryRange evaluates a MetricsQL expression over a time range.
func (c *Client) QueryRange(ctx context.Context, expr string, from, to time.Time, step time.Duration) ([]Series, error) {
	if !c.Enabled() {
		return []Series{}, nil
	}
	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", strconv.FormatFloat(float64(from.UnixMilli())/1000, 'f', 3, 64))
	q.Set("end", strconv.FormatFloat(float64(to.UnixMilli())/1000, 'f', 3, 64))
	q.Set("step", strconv.FormatFloat(step.Seconds(), 'f', -1, 64)+"s")
	resp, err := c.query(ctx, "/api/v1/query_range", q)
	if err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		s := Series{Labels: r.Metric, Points: make([][2]float64, 0, len(r.Values))}
		for _, v := range r.Values {
			if pt, ok := parsePoint(v); ok {
				s.Points = append(s.Points, pt)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// Query evaluates a MetricsQL expression at one moment.
func (c *Client) Query(ctx context.Context, expr string, at time.Time) ([]Series, error) {
	if !c.Enabled() {
		return []Series{}, nil
	}
	q := url.Values{}
	q.Set("query", expr)
	q.Set("time", strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64))
	resp, err := c.query(ctx, "/api/v1/query", q)
	if err != nil {
		return nil, err
	}
	out := make([]Series, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		s := Series{Labels: r.Metric}
		if pt, ok := parsePoint(r.Value); ok {
			s.Points = [][2]float64{pt}
		}
		out = append(out, s)
	}
	return out, nil
}

// DeleteStream removes every series of a stream.
func (c *Client) DeleteStream(ctx context.Context, streamID int64) error {
	if !c.Enabled() {
		return nil
	}
	q := url.Values{}
	q.Set("match[]", fmt.Sprintf(`{stream="%d"}`, streamID))
	return c.post(ctx, "/api/v1/admin/tsdb/delete_series?"+q.Encode(), "", nil)
}

// Healthy reports whether VictoriaMetrics answers.
func (c *Client) Healthy(ctx context.Context) bool {
	if !c.Enabled() {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Stats describes the delivery queue.
type Stats struct {
	Enabled        bool  `json:"enabled"`
	QueuedBytes    int   `json:"queued_bytes"`
	DroppedBatches int64 `json:"dropped_batches"`
}

func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Enabled: c.Enabled(), QueuedBytes: c.queued, DroppedBatches: c.dropped}
}
