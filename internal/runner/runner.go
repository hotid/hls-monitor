// Package runner keeps one checking loop per stream target and follows
// changes of the target list. The server uses it for local checks, the
// agent for the streams assigned to it.
package runner

import (
	"context"
	"log/slog"
	"math"
	"math/rand/v2"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
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

	// overloaded is given to every checker; see SetOverloaded.
	overloaded func(since time.Time) string
	// looker and lookEvery are given to every checker; see SetLooker.
	looker    probe.Looker
	lookEvery time.Duration
	slates    func() []probe.Slate

	mu   sync.Mutex
	jobs map[int64]*job
	wg   sync.WaitGroup

	cycles, unreliable, short, crashed atomic.Int64
}

type job struct {
	target  probe.Target
	cancel  context.CancelFunc
	trigger chan struct{}

	// Of the latest cycle, guarded by the runner's lock: how late it
	// started for reasons of the runner's own (see Stats.Behind), how long
	// the pause before it was meant to be, and whether it is still running.
	lag, interval time.Duration
	running       bool
}

// Stats tells how the runner is doing.
type Stats struct {
	Streams int `json:"streams"`
	// Running is how many cycles are being made right now.
	Running int `json:"running"`
	// Behind counts the streams whose latest cycle started later than it
	// should have by more than its own interval, not counting the time the
	// check before it took: a stream that answers slowly makes its own
	// checks late, and that is reported as its problem. What is left is
	// the runner's: results that wait to be stored, a processor that is
	// busy.
	Behind int `json:"behind"`
	// LagP95 and LagMax tell how late cycles start for such reasons, in
	// seconds.
	LagP95 float64 `json:"lag_p95_seconds"`
	LagMax float64 `json:"lag_max_seconds"`
	// Cycles counts the cycles made since the start, Unreliable those of
	// them made while the process was overloaded.
	Cycles     int64 `json:"cycles"`
	Unreliable int64 `json:"unreliable_cycles"`
	// Short counts the cycles that started late and therefore looked only
	// at the latest segment of each playlist, to catch up.
	Short int64 `json:"short_cycles"`
	// Crashed counts the cycles that ended in a fault of the checking
	// code itself; each is in the log with its stack.
	Crashed int64 `json:"crashed_cycles"`
}

// Stats returns the current figures.
func (r *Runner) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Stats{Streams: len(r.jobs), Cycles: r.cycles.Load(), Unreliable: r.unreliable.Load(), Short: r.short.Load(), Crashed: r.crashed.Load()}
	lags := make([]float64, 0, len(r.jobs))
	for _, j := range r.jobs {
		if j.running {
			st.Running++
		}
		if j.lag > max(j.interval, time.Second) {
			st.Behind++
		}
		lags = append(lags, j.lag.Seconds())
	}
	if len(lags) > 0 {
		sort.Float64s(lags)
		st.LagP95 = math.Round(lags[min(len(lags)-1, len(lags)*95/100)]*1000) / 1000
		st.LagMax = math.Round(lags[len(lags)-1]*1000) / 1000
	}
	return st
}

// SetOverloaded gives the runner the means to learn that the process
// could not keep up (see probe.Checker.Overloaded). Call it before Sync.
func (r *Runner) SetOverloaded(f func(since time.Time) string) { r.overloaded = f }

// SetLooker gives the checkers the means to look at the picture of a
// stream and listen to its sound, once in every period. Call it before Sync.
func (r *Runner) SetLooker(l probe.Looker, every time.Duration) { r.looker, r.lookEvery = l, every }

// SetSlates tells the checkers where to learn which pictures are slates.
// Call it before Sync.
func (r *Runner) SetSlates(f func() []probe.Slate) { r.slates = f }

func (r *Runner) checker(t probe.Target) *probe.Checker {
	hc := r.secure
	if t.Insecure {
		hc = r.insecure
	}
	c := probe.New(t, hc)
	c.Overloaded = r.overloaded
	if r.looker != nil && r.lookEvery > 0 {
		c.Looker, c.PictureEvery = r.looker, r.lookEvery
		c.Slates = r.slates
	}
	return c
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
	c := r.checker(t)
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop(ctx, j, c, startDelay)
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
	var overran time.Duration
	for {
		lag := max(time.Since(next), 0)
		start := time.Now()
		r.mu.Lock()
		// overran is how much of the lag the previous check explains by
		// having taken longer than the interval.
		j.lag, j.interval, j.running = max(lag-overran, 0), c.Interval(), true
		r.mu.Unlock()
		// A cycle that starts late, whoever is to blame, is a short one.
		if c.CatchUp = lag > max(c.Interval(), time.Second); c.CatchUp {
			r.short.Add(1)
		}
		res := r.run(ctx, c)
		overran = max(time.Since(start)-c.Interval(), 0)
		r.mu.Lock()
		j.running = false
		r.mu.Unlock()
		if ctx.Err() != nil {
			return // a cancelled cycle reports failures that are not real
		}
		if res == nil {
			// The cycle crashed; the next one may fare better.
			if !sleep(ctx, max(c.Interval(), 5*time.Second), j.trigger) {
				return
			}
			next = time.Now()
			continue
		}
		r.cycles.Add(1)
		if res.Overloaded != "" {
			r.unreliable.Add(1)
		}
		res.Metrics = append(res.Metrics, probe.Metric{Name: "hls_check_lag_seconds", Value: lag.Seconds()})
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

// run makes one cycle. What is checked comes from servers the runner does
// not control; a fault in the checking code that such data brings out must
// stop one cycle of one stream, not every check the process makes. It
// returns nil for a cycle that crashed.
func (r *Runner) run(ctx context.Context, c *probe.Checker) (res *probe.Result) {
	defer func() {
		if p := recover(); p != nil {
			r.crashed.Add(1)
			r.log.Error("a check crashed", "stream", c.Target().ID, "panic", p, "stack", string(debug.Stack()))
			res = nil
		}
	}()
	return c.Run(ctx)
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
	return r.checker(t)
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
