// Package runner keeps one checking loop per stream target and follows
// changes of the target list. The server uses it for local checks, the
// agent for the streams assigned to it.
package runner

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/probe"
)

// Sink receives the result of every check cycle. It is called from the
// stream's own goroutine and may block; the next cycle waits for it.
type Sink func(ctx context.Context, r *probe.Result)

// Runner runs the checking loops. It is safe for concurrent use.
type Runner struct {
	sink Sink
	log  *slog.Logger

	secure   *httpx.Client
	insecure *httpx.Client

	mu   sync.Mutex
	jobs map[int64]*job
	wg   sync.WaitGroup
}

type job struct {
	target  probe.Target
	cancel  context.CancelFunc
	trigger chan struct{}
}

// New creates a runner with no targets.
func New(sink Sink, userAgent string, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		sink:     sink,
		log:      log,
		secure:   httpx.New(httpx.Options{UserAgent: userAgent}),
		insecure: httpx.New(httpx.Options{UserAgent: userAgent, InsecureSkipVerify: true}),
		jobs:     map[int64]*job{},
	}
}

// Sync makes the running loops match targets: new streams start, removed
// ones stop, and streams whose revision changed restart with a clean state.
func (r *Runner) Sync(targets []probe.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[int64]bool{}
	started := 0
	for _, t := range targets {
		want[t.ID] = true
		if j, ok := r.jobs[t.ID]; ok {
			if j.target.Revision == t.Revision {
				continue
			}
			j.cancel()
		}
		// 25 ms apart, at most 15 s for the whole batch.
		r.start(t, min(time.Duration(started)*25*time.Millisecond, 15*time.Second))
		started++
	}
	for id, j := range r.jobs {
		if !want[id] {
			j.cancel()
			delete(r.jobs, id)
		}
	}
}

func (r *Runner) start(t probe.Target, startDelay time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{target: t, cancel: cancel, trigger: make(chan struct{}, 1)}
	r.jobs[t.ID] = j
	hc := r.secure
	if t.Insecure {
		hc = r.insecure
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(ctx, j, probe.New(t, hc), startDelay)
	}()
}

func (r *Runner) loop(ctx context.Context, j *job, c *probe.Checker, startDelay time.Duration) {
	// A batch of streams started together is staggered so that the first
	// cycles do not hit the origin in the same instant.
	if !sleep(ctx, startDelay+time.Duration(rand.Int64N(int64(time.Second))), j.trigger) {
		return
	}
	first := true
	next := time.Now()
	for {
		lag := time.Since(next)
		start := time.Now()
		res := c.Run(ctx)
		if ctx.Err() != nil {
			return // a cancelled cycle reports failures that are not real
		}
		res.Metrics = append(res.Metrics, probe.Metric{Name: "hls_check_lag_seconds", Value: max(lag.Seconds(), 0)})
		r.sink(ctx, res)

		// The interval counts from the start of a cycle; a cycle that took
		// longer is followed by the next one at once.
		next = start.Add(c.Interval())
		if first {
			// Streams with a fixed interval keep the phase they start with.
			// Shortening the first pause by a random share spreads them
			// evenly over the interval instead of leaving them in step.
			first = false
			next = start.Add(time.Duration(rand.Int64N(int64(c.Interval()) + 1)))
		}
		if !sleep(ctx, time.Until(next), j.trigger) {
			return
		}
		if time.Now().Before(next) {
			next = time.Now() // woken early by CheckNow
		}
	}
}

// sleep waits for d, an early trigger or cancellation; it reports false on cancellation.
func sleep(ctx context.Context, d time.Duration, trigger <-chan struct{}) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-trigger:
		return true
	case <-t.C:
		return true
	}
}

// CheckNow makes the stream's next cycle start immediately. It reports
// whether the stream is being checked by this runner.
func (r *Runner) CheckNow(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return false
	}
	select {
	case j.trigger <- struct{}{}:
	default:
	}
	return true
}

// Checker returns a checker for t that shares the runner's HTTP clients but
// is not one of its loops: the caller runs it and keeps what it finds.
func (r *Runner) Checker(t probe.Target) *probe.Checker {
	if t.Insecure {
		return probe.New(t, r.insecure)
	}
	return probe.New(t, r.secure)
}

// Count returns the number of streams being checked.
func (r *Runner) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// Stop ends all loops and waits for running cycles to return.
func (r *Runner) Stop() {
	r.mu.Lock()
	for id, j := range r.jobs {
		j.cancel()
		delete(r.jobs, id)
	}
	r.mu.Unlock()
	r.wg.Wait()
	r.secure.CloseIdle()
	r.insecure.CloseIdle()
}
