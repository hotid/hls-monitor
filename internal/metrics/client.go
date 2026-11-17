// Package metrics writes numeric time series to VictoriaMetrics and reads
// them back for charts.
package metrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/hotid/hls-monitor/internal/spool"
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
	// maxQueuedBytes is how much may wait in memory when there is no
	// queue on disk; with one, spillBytes is: what does not go out at
	// once is written to disk rather than kept.
	maxQueuedBytes = 64 << 20
	spillBytes     = 8 << 20
	maxBatchBytes  = 4 << 20
	// diskBatchBytes bounds one file of the queue on disk, compressed.
	diskBatchBytes = 1 << 20
	// DefaultQueueBytes is how much disk the queue may take by default.
	// Samples compress about tenfold: a gigabyte holds some ten gigabytes
	// of them.
	DefaultQueueBytes = 1 << 30
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

	// disk holds, compressed, what did not go out while VictoriaMetrics
	// was away, and what was left when the server stopped. Nil without
	// UseDisk.
	disk *spool.Spool
}

// UseDisk makes the client keep samples it cannot deliver in dir instead
// of dropping them when memory fills up, and across restarts. maxBytes
// bounds the directory; beyond it the oldest samples are dropped. Call it
// before Run.
func (c *Client) UseDisk(dir string, maxBytes int64) error {
	if !c.Enabled() {
		return nil
	}
	if maxBytes <= 0 {
		maxBytes = DefaultQueueBytes
	}
	sp, err := spool.Open(dir, maxBytes, diskBatchBytes)
	if err != nil {
		return err
	}
	c.disk = sp
	if n, size := sp.Queued(); n > 0 {
		c.log.Info("samples left from the previous run will be written to VictoriaMetrics", "batches", n, "bytes", size)
	}
	return nil
}

// spill moves what waits in memory to disk. The order of samples does not
// matter: each carries its time.
func (c *Client) spill() {
	c.mu.Lock()
	batches := c.queue
	c.queue, c.queued = nil, 0
	c.mu.Unlock()
	var raw, packed bytes.Buffer
	flush := func() {
		if raw.Len() == 0 {
			return
		}
		packed.Reset()
		zw := gzip.NewWriter(&packed)
		_, _ = zw.Write(raw.Bytes())
		_ = zw.Close()
		raw.Reset()
		if err := c.disk.Add(bytes.Clone(packed.Bytes())); err != nil {
			c.mu.Lock()
			c.dropped++
			c.mu.Unlock()
			c.log.Error("cannot write samples to the queue on disk, they are lost", "error", err)
		}
	}
	for _, b := range batches {
		raw.Write(b)
		if raw.Len() >= maxBatchBytes {
			flush()
		}
	}
	flush()
	if n := c.disk.TakeDropped(); n > 0 {
		c.mu.Lock()
		c.dropped += int64(n)
		c.mu.Unlock()
		c.log.Warn("the queue of samples on disk reached its size limit, the oldest were dropped", "batches", n)
	}
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
	spill := c.disk != nil && c.queued > spillBytes
	for c.disk == nil && c.queued > maxQueuedBytes && len(c.queue) > 1 {
		c.queued -= len(c.queue[0])
		c.queue = c.queue[1:]
		c.dropped++
	}
	c.mu.Unlock()
	if spill {
		c.spill()
	}
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
			if c.disk != nil {
				c.spill() // what did not go out waits for the next start
				c.disk.Close()
			}
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
			return c.flushDisk(ctx)
		}
		n, size := 0, 0
		for n < len(c.queue) && (n == 0 || size+len(c.queue[n]) <= maxBatchBytes) {
			size += len(c.queue[n])
			n++
		}
		batch := bytes.Join(c.queue[:n], nil)
		c.mu.Unlock()

		if err := c.post(ctx, "/api/v1/import/prometheus", "text/plain", "", batch); err != nil {
			if !refused(err) {
				return err
			}
			// Sending it again cannot help, and it must not hold up what is
			// behind it.
			c.log.Error("VictoriaMetrics refused a batch of samples, it is dropped", "error", err, "bytes", len(batch))
			c.mu.Lock()
			c.dropped++
			c.mu.Unlock()
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

// flushDisk sends what is queued on disk, oldest first.
func (c *Client) flushDisk(ctx context.Context) error {
	if c.disk == nil {
		return nil
	}
	for {
		seq, batch := c.disk.Next()
		if len(batch) == 0 {
			return nil
		}
		// Each record is a gzip stream; several in a row are one as well.
		err := c.post(ctx, "/api/v1/import/prometheus", "text/plain", "gzip", bytes.Join(batch, nil))
		if err != nil && !refused(err) {
			return err
		}
		if err != nil {
			c.log.Error("VictoriaMetrics refused a batch of samples from the queue on disk, it is dropped", "error", err)
			c.mu.Lock()
			c.dropped++
			c.mu.Unlock()
		}
		if err := c.disk.Done(seq, batch, len(batch)); err != nil {
			return err
		}
	}
}

// statusError is an answer of VictoriaMetrics that is not a success.
type statusError struct {
	status int
	text   string
}

func (e *statusError) Error() string { return e.text }

// refused reports whether VictoriaMetrics understood the request and will
// not take it: a fault of what was sent, not of the moment.
func refused(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.status/100 == 4 && se.status != http.StatusTooManyRequests && se.status != http.StatusRequestTimeout
}

// Backlog reports whether samples wait on disk: VictoriaMetrics is, or
// was until now, behind on what the server has to tell it.
func (c *Client) Backlog() bool {
	if c.disk == nil {
		return false
	}
	n, _ := c.disk.Queued()
	return n > 0
}

func (c *Client) post(ctx context.Context, path, contentType, encoding string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &statusError{status: resp.StatusCode, text: fmt.Sprintf("victoriametrics %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))}
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

// DeleteSeries removes every series that has the label with this value:
// label is "stream" or "agent".
func (c *Client) DeleteSeries(ctx context.Context, label string, id int64) error {
	if !c.Enabled() {
		return nil
	}
	if !ValidName(label) {
		return fmt.Errorf("invalid label %q", label)
	}
	q := url.Values{}
	q.Set("match[]", fmt.Sprintf(`{%s="%d"}`, label, id))
	return c.post(ctx, "/api/v1/admin/tsdb/delete_series?"+q.Encode(), "", "", nil)
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
	Enabled bool `json:"enabled"`
	// QueuedBytes wait in memory, DiskBytes (compressed) on disk.
	QueuedBytes    int   `json:"queued_bytes"`
	DiskBytes      int64 `json:"disk_bytes"`
	DroppedBatches int64 `json:"dropped_batches"`
}

func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Stats{Enabled: c.Enabled(), QueuedBytes: c.queued, DroppedBatches: c.dropped}
	if c.disk != nil {
		_, st.DiskBytes = c.disk.Queued()
	}
	return st
}
