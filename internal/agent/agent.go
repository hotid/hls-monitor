// Package agent is the remote vantage point: it enrolls with the server,
// keeps checking the streams assigned to it and delivers the results. It
// only makes outgoing connections and survives the server being away, and
// its own restarts, by queueing results on disk.
package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hotid/hls-monitor/internal/agentproto"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/runner"
	"github.com/hotid/hls-monitor/internal/selfstat"
	"github.com/hotid/hls-monitor/internal/spool"
)

const (
	// DefaultQueueBytes is how much disk the queue of undelivered results
	// may take by default.
	DefaultQueueBytes = 1 << 30
	maxBatchBytes     = 4 << 20
	sendEvery         = 2 * time.Second
)

// Options configures an agent.
type Options struct {
	ServerURL string
	// EnrollToken is needed only until the agent has a key.
	EnrollToken string
	// KeyFile is where the permanent key is kept between restarts.
	KeyFile string
	Version string
	Log     *slog.Logger
	// QueueDir is where undelivered results are kept; "queue" next to the
	// key file when empty. QueueBytes bounds its size, DefaultQueueBytes
	// when zero: beyond it the oldest results are dropped.
	QueueDir   string
	QueueBytes int64
	// PollInterval overrides the configuration poll period the server suggests.
	PollInterval time.Duration
}

// ErrRejected means the server does not accept this agent's credentials
// and there is nothing the agent can do about it on its own.
var ErrRejected = errors.New("the server rejected the agent's credentials")

// Agent is a running agent.
type Agent struct {
	opt  Options
	base string
	hc   *http.Client
	log  *slog.Logger
	key  string
	run  *runner.Runner

	revision string

	spool *spool.Spool
	// writeFailed is set while results cannot be written to the queue, so
	// that the log says it once.
	writeFailed atomic.Bool
	load        *selfstat.Monitor
	link        linkWatch
}

// status is what the agent reports about itself.
func (a *Agent) status() agentproto.Status {
	var st agentproto.Status
	if a.spool != nil {
		var oldest time.Duration
		st.QueueResults, st.QueueBytes, oldest, st.QueueDropped = a.spool.Stats()
		st.QueueOldestSec = float64(oldest.Milliseconds()) / 1000
	}
	st.QueueFailing = a.writeFailed.Load()
	st.Link = a.link.report()
	st.Fetches = true
	load := a.load.Status()
	if load.Overloaded {
		st.Overloaded = load.Reason
	}
	st.CPUCores, st.CPULimit, st.MemoryBytes = load.CPUCores, load.CPULimit, load.MemoryBytes
	if a.run != nil {
		rs := a.run.Stats()
		st.Streams, st.Behind = rs.Streams, rs.Behind
	}
	return st
}

// Run starts the agent and blocks until ctx is done or the server rejects it.
func Run(ctx context.Context, opt Options) error {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	u, err := url.Parse(opt.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("server URL must be an absolute http or https URL, got %q", opt.ServerURL)
	}
	a := &Agent{
		opt:  opt,
		base: strings.TrimRight(opt.ServerURL, "/"),
		hc:   &http.Client{Timeout: 60 * time.Second},
		log:  opt.Log,
	}
	if err := a.loadOrEnroll(ctx); err != nil {
		return err
	}
	dir := opt.QueueDir
	if dir == "" {
		dir = filepath.Join(filepath.Dir(opt.KeyFile), "queue")
	}
	if opt.QueueBytes <= 0 {
		opt.QueueBytes = DefaultQueueBytes
	}
	if a.spool, err = spool.Open(dir, opt.QueueBytes, maxBatchBytes); err != nil {
		return fmt.Errorf("open the result queue in %s: %w", dir, err)
	}
	defer a.spool.Close()
	if n, size := a.spool.Queued(); n > 0 {
		a.log.Info("undelivered results from the previous run will be sent", "results", n, "bytes", size)
	}
	a.run = runner.New(a.enqueue, "hls-monitor-agent/"+opt.Version, opt.Log)
	defer a.run.Stop()
	a.load = selfstat.Start(ctx)
	a.run.SetOverloaded(a.load.Overloaded)

	sendDone := make(chan struct{})
	sctx, stopSend := context.WithCancel(context.Background())
	go func() { a.sendLoop(sctx); close(sendDone) }()
	for range fetchers {
		go a.fetchLoop(ctx)
	}
	err = a.configLoop(ctx)
	a.run.Stop()
	stopSend()
	<-sendDone
	return err
}

func (a *Agent) loadOrEnroll(ctx context.Context) error {
	b, err := os.ReadFile(a.opt.KeyFile)
	switch {
	case err == nil && len(bytes.TrimSpace(b)) > 0:
		a.key = string(bytes.TrimSpace(b))
		return nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read key file: %w", err)
	}
	if a.opt.EnrollToken == "" {
		return fmt.Errorf("no agent key in %s and no enrollment token given", a.opt.KeyFile)
	}
	return a.enroll(ctx)
}

// enroll trades the enrollment token for a key and saves it. It keeps
// trying while the server is unreachable; a refusal is final.
func (a *Agent) enroll(ctx context.Context) error {
	body, _ := json.Marshal(agentproto.EnrollRequest{Token: a.opt.EnrollToken, Version: a.opt.Version})
	for {
		var resp agentproto.EnrollResponse
		status, err := a.request(ctx, http.MethodPost, "/api/agent/v1/enroll", "", body, false, &resp)
		switch {
		case err == nil && status == http.StatusOK && resp.Key != "":
			if err := writeKey(a.opt.KeyFile, resp.Key); err != nil {
				return fmt.Errorf("save agent key: %w", err)
			}
			a.key = resp.Key
			a.log.Info("enrolled with the server", "agent", resp.Name, "id", resp.AgentID)
			return nil
		case err == nil && (status == http.StatusUnauthorized || status == http.StatusBadRequest):
			return fmt.Errorf("%w: the enrollment token is wrong, already used or expired", ErrRejected)
		case err == nil:
			err = fmt.Errorf("HTTP %d", status)
		}
		a.log.Warn("cannot enroll yet, will retry", "error", err)
		if !sleepCtx(ctx, 10*time.Second) {
			return ctx.Err()
		}
	}
}

// writeKey stores the key so that a crash cannot leave a partial file.
func writeKey(path, key string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// request performs one API call. A non-nil error means no HTTP response.
func (a *Agent) request(ctx context.Context, method, path, query string, body []byte, gz bool, out any) (int, error) {
	u := a.base + path
	if query != "" {
		u += "?" + query
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		if gz {
			req.Header.Set("Content-Encoding", "gzip")
		}
	}
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	req.Header.Set(agentproto.HeaderVersion, a.opt.Version)
	req.Header.Set(agentproto.HeaderTime, strconv.FormatInt(time.Now().UnixMilli(), 10))
	if a.key != "" {
		if b, err := json.Marshal(a.status()); err == nil {
			req.Header.Set(agentproto.HeaderStatus, string(b))
		}
	}
	// Requests held open by the server while it has no work say nothing
	// about how long the way to it takes.
	conn, done := a.link.watch(req, path != "/api/agent/v1/fetch" || method != http.MethodGet)
	resp, err := a.hc.Do(conn)
	if err != nil {
		done(false)
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	done(err == nil)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return 0, fmt.Errorf("unexpected response from the server: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// configLoop polls the configuration, which doubles as the heartbeat, and
// applies it to the runner.
func (a *Agent) configLoop(ctx context.Context) error {
	poll := 15 * time.Second
	reachable := true
	for {
		var cfg agentproto.Config
		status, err := a.request(ctx, http.MethodGet, "/api/agent/v1/config", "revision="+url.QueryEscape(a.revision), nil, false, &cfg)
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil || status >= 500:
			if reachable {
				a.log.Warn("server is unreachable; checks continue, results are queued", "error", err, "status", status)
			}
			reachable = false
		case status == http.StatusUnauthorized:
			// The key was revoked. A fresh enrollment token may have been supplied.
			a.run.Sync(nil)
			a.revision = ""
			if a.opt.EnrollToken == "" {
				return fmt.Errorf("%w: the agent key is no longer valid; start the agent with a new enrollment token", ErrRejected)
			}
			a.log.Warn("the agent key is no longer valid, enrolling again")
			a.key = ""
			if err := a.enroll(ctx); err != nil {
				return err
			}
			continue
		case status == http.StatusOK:
			if !reachable {
				a.log.Info("server is reachable again")
			}
			reachable = true
			targets := cfg.Targets
			if !cfg.Enabled {
				targets = nil
			}
			a.run.Sync(targets)
			a.revision = cfg.Revision
			a.log.Info("configuration applied", "streams", len(targets), "enabled", cfg.Enabled)
			if cfg.PollSec > 0 {
				poll = time.Duration(cfg.PollSec) * time.Second
			}
		case status == http.StatusNotModified:
			if !reachable {
				a.log.Info("server is reachable again")
			}
			reachable = true
		default:
			a.log.Warn("unexpected answer to the configuration request", "status", status)
		}
		wait := poll
		if a.opt.PollInterval > 0 {
			wait = a.opt.PollInterval
		}
		if !sleepCtx(ctx, wait) {
			return nil
		}
	}
}

// enqueue is the runner's sink.
func (a *Agent) enqueue(_ context.Context, r *probe.Result) {
	b, err := json.Marshal(r)
	if err != nil {
		a.log.Error("cannot encode a result", "error", err)
		return
	}
	if err := a.spool.Add(b); err != nil {
		if !a.writeFailed.Swap(true) {
			a.log.Error("cannot write to the result queue, results are being lost", "error", err)
		}
		return
	}
	if a.writeFailed.Swap(false) {
		a.log.Info("the result queue can be written again")
	}
}

func (a *Agent) sendLoop(ctx context.Context) {
	for {
		if !sleepCtx(ctx, sendEvery) {
			// One last attempt; what it does not deliver stays on disk.
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			a.flush(fctx)
			cancel()
			return
		}
		a.flush(ctx)
	}
}

// flush delivers queued results batch by batch until the queue is empty or
// a delivery fails.
func (a *Agent) flush(ctx context.Context) {
	for ctx.Err() == nil {
		if dropped := a.spool.TakeDropped(); dropped > 0 {
			a.log.Warn("the result queue reached its size limit, oldest results were dropped", "dropped", dropped)
		}
		seq, batch := a.spool.Next()
		n := len(batch)
		if n == 0 {
			return
		}

		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(`{"results":[`))
		_, _ = zw.Write(bytes.Join(batch, []byte(",")))
		_, _ = zw.Write([]byte("]}"))
		_ = zw.Close()

		var resp agentproto.ResultsResponse
		status, err := a.request(ctx, http.MethodPost, "/api/agent/v1/results", "", buf.Bytes(), true, &resp)
		done := n
		switch {
		case err != nil || status >= 500 || status == http.StatusUnauthorized:
			return // keep everything; configLoop reports and handles the cause
		case status == http.StatusOK:
			done = n - min(max(resp.Failed, 0), n)
		default:
			// The server understood the request and refused it (the agent is
			// disabled, the batch is malformed): sending it again cannot help.
			a.log.Warn("the server refused a batch of results", "status", status, "results", n)
		}
		if err := a.spool.Done(seq, batch, done); err != nil {
			a.log.Error("cannot update the result queue; some results may be sent twice", "error", err)
			return
		}
		if done < n {
			return // the server could not store the rest right now
		}
	}
}
