// Package selfstat watches the process itself: how much processor time and
// memory it uses and whether it gets enough of the first. A monitor that
// cannot keep up measures its own delay and would report it as the delay
// of the streams it checks; this is how it knows.
package selfstat

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hotid/hls-monitor/internal/types/overload"
)

const (
	period = 5 * time.Second
	// The process keeps asking to be woken after pulse and measures how
	// late that happens: the same delay is added to everything it times.
	pulse = 50 * time.Millisecond
	// Overloaded when one wake-up in ten is this late, or one goroutine
	// in a hundred waits this long for a processor after becoming ready.
	// Measured under load: a server that uses its whole quota and is still
	// on time shows a few milliseconds of either, one that cannot keep up
	// with what it downloads shows seconds of the second.
	delayLimit = 100 * time.Millisecond
)

// Status is the latest sample.
type Status struct {
	// Overloaded tells that the process did not get the processor time it
	// needed; Reason says how that showed.
	Overloaded bool            `json:"overloaded"`
	Reason     overload.Reason `json:"reason,omitzero" enums:"processor"`
	// CPUCores is how many processors the process used, CPULimit how many
	// it may use.
	CPUCores float64 `json:"cpu_cores"`
	CPULimit float64 `json:"cpu_limit"`
	// CPUAllCores is how many processors the whole container used, the
	// programs the process runs included (the decoder of pictures is
	// one); zero where the container cannot be asked.
	CPUAllCores float64 `json:"cpu_all_cores"`
	// CPUThrottled is how many seconds per second the kernel kept the
	// container's processors from it for having used up its quota.
	CPUThrottled float64 `json:"cpu_throttled"`
	// Delay is how late the process wakes up when it asked to be woken:
	// the slowest one in ten wake-ups of the last seconds. DelayMax is the
	// slowest of all. Seconds.
	Delay    float64 `json:"delay_seconds"`
	DelayMax float64 `json:"delay_max_seconds"`
	// SchedulerDelay is how long the slowest one in a hundred goroutines
	// waited for a processor after becoming ready, in seconds.
	SchedulerDelay float64 `json:"scheduler_delay_seconds"`
	// MemoryBytes is the memory of the container when it has a limit,
	// otherwise what the process took from the system. MemoryLimit is zero
	// when there is none.
	MemoryBytes int64 `json:"memory_bytes"`
	MemoryLimit int64 `json:"memory_limit_bytes"`
	Goroutines  int   `json:"goroutines"`
}

// Monitor samples the process every few seconds.
type Monitor struct {
	cgroup string // directory of the control group files

	mu       sync.Mutex
	status   Status
	lastOver time.Time // when the latest overloaded sample was taken
	reason   overload.Reason

	prev sample

	// late collects how late the wake-ups since the last sample were.
	lateMu sync.Mutex
	late   []time.Duration
}

type sample struct {
	at        time.Time
	cpu       time.Duration // processor time used by the process
	all       time.Duration // and by the whole container
	throttled time.Duration
	sched     *metrics.Float64Histogram
	// late and lateMax describe the wake-ups since the previous sample.
	late, lateMax time.Duration
}

// Start begins sampling; it stops when ctx is done.
func Start(ctx context.Context) *Monitor {
	m := &Monitor{cgroup: "/sys/fs/cgroup"}
	m.prev = m.read()
	go m.beat(ctx)
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.tick()
			}
		}
	}()
	return m
}

// beat measures how late the process is woken.
func (m *Monitor) beat(ctx context.Context) {
	t := time.NewTimer(pulse)
	defer t.Stop()
	for {
		asked := time.Now()
		t.Reset(pulse)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		late := max(time.Since(asked)-pulse, 0)
		m.lateMu.Lock()
		m.late = append(m.late, late)
		m.lateMu.Unlock()
	}
}

// takeLate returns the ninth decile and the maximum of the wake-up delays
// collected since the last call.
func (m *Monitor) takeLate() (p90, most time.Duration) {
	m.lateMu.Lock()
	late := m.late
	m.late = nil
	m.lateMu.Unlock()
	if len(late) == 0 {
		return 0, 0
	}
	sort.Slice(late, func(i, j int) bool { return late[i] < late[j] })
	return late[min(len(late)-1, len(late)*9/10)], late[len(late)-1]
}

func (m *Monitor) read() sample {
	s := sample{at: time.Now(), cpu: processCPU()}
	if v, ok := cgroupStat(filepath.Join(m.cgroup, "cpu.stat"), "throttled_usec"); ok {
		s.throttled = time.Duration(v) * time.Microsecond
	}
	if v, ok := cgroupStat(filepath.Join(m.cgroup, "cpu.stat"), "usage_usec"); ok {
		s.all = time.Duration(v) * time.Microsecond
	}
	set := []metrics.Sample{{Name: "/sched/latencies:seconds"}}
	metrics.Read(set)
	if set[0].Value.Kind() == metrics.KindFloat64Histogram {
		s.sched = set[0].Value.Float64Histogram()
	}
	return s
}

func (m *Monitor) tick() {
	cur := m.read()
	cur.late, cur.lateMax = m.takeLate()
	st := m.evaluate(m.prev, cur)
	m.prev = cur
	m.mu.Lock()
	m.status = st
	if st.Overloaded {
		m.lastOver, m.reason = cur.at, st.Reason
	}
	m.mu.Unlock()
}

func (m *Monitor) evaluate(prev, cur sample) Status {
	st := Status{Goroutines: runtime.NumGoroutine(), CPULimit: float64(runtime.GOMAXPROCS(0))}
	if quota, ok := cpuQuota(filepath.Join(m.cgroup, "cpu.max")); ok && quota < st.CPULimit {
		st.CPULimit = quota
	}
	if wall := cur.at.Sub(prev.at).Seconds(); wall > 0 {
		st.CPUCores = round(max(cur.cpu-prev.cpu, 0).Seconds()/wall, 100)
		st.CPUThrottled = round(max(cur.throttled-prev.throttled, 0).Seconds()/wall, 1000)
		if prev.all > 0 && cur.all >= prev.all {
			st.CPUAllCores = round((cur.all-prev.all).Seconds()/wall, 100)
		}
	}
	st.Delay, st.DelayMax = round(cur.late.Seconds(), 1000), round(cur.lateMax.Seconds(), 1000)
	st.SchedulerDelay = quantile(prev.sched, cur.sched, 0.99)
	st.MemoryBytes, st.MemoryLimit = m.memory()
	if cur.late >= delayLimit || st.SchedulerDelay >= delayLimit.Seconds() {
		st.Overloaded, st.Reason = true, overload.Processor()
	}
	return st
}

func (m *Monitor) memory() (used, limit int64) {
	if v, ok := readInt(filepath.Join(m.cgroup, "memory.max")); ok {
		if cur, ok := readInt(filepath.Join(m.cgroup, "memory.current")); ok {
			return cur, v
		}
	}
	set := []metrics.Sample{{Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}
	metrics.Read(set)
	if set[0].Value.Kind() == metrics.KindUint64 && set[1].Value.Kind() == metrics.KindUint64 {
		used = int64(set[0].Value.Uint64() - set[1].Value.Uint64())
	}
	return used, 0
}

// Status returns the latest sample; the zero value before the first one,
// and from a nil monitor.
func (m *Monitor) Status() Status {
	if m == nil {
		return Status{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Overloaded tells what the process was short of at any time since the
// given moment, and nothing when it kept up. A nil monitor never is.
func (m *Monitor) Overloaded(since time.Time) overload.Reason {
	if m == nil {
		return overload.Reason{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// A sample describes the seconds before it, and the next one is not
	// taken yet: what began just after an overloaded sample belongs to it.
	if m.lastOver.IsZero() || since.Sub(m.lastOver) > period {
		return overload.Reason{}
	}
	return m.reason
}

// quantile returns the q-th quantile of what was added to a histogram
// between two readings: the upper bound of the bucket it falls in.
func quantile(prev, cur *metrics.Float64Histogram, q float64) float64 {
	if cur == nil {
		return 0
	}
	var total uint64
	delta := make([]uint64, len(cur.Counts))
	for i, n := range cur.Counts {
		if prev != nil && i < len(prev.Counts) && prev.Counts[i] <= n {
			n -= prev.Counts[i]
		}
		delta[i] = n
		total += n
	}
	if total == 0 {
		return 0
	}
	want := uint64(math.Ceil(q * float64(total)))
	var seen uint64
	for i, n := range delta {
		seen += n
		if seen < want {
			continue
		}
		// Buckets[i+1] is the upper bound of bucket i; the last may be infinite.
		for j := i + 1; j >= 0; j-- {
			if b := cur.Buckets[j]; !math.IsInf(b, 0) {
				return max(b, 0)
			}
		}
		return 0
	}
	return 0
}

// cpuQuota reads cpu.max of control groups v2: "max 100000" for no limit,
// "150000 100000" for one and a half processors.
func cpuQuota(path string) (float64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] == "max" {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(f[0], 64)
	per, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || quota <= 0 || per <= 0 {
		return 0, false
	}
	return quota / per, true
}

// cgroupStat reads one figure of a "name value" file.
func cgroupStat(path, name string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == name {
			v, err := strconv.ParseInt(f[1], 10, 64)
			return v, err == nil
		}
	}
	return 0, false
}

// readInt reads a file holding one number; "max" is not one.
func readInt(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

func round(v, to float64) float64 { return math.Round(v*to) / to }
