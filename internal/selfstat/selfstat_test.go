package selfstat

import (
	"math"
	"os"
	"path/filepath"
	"runtime/metrics"
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/types/overload"
)

func TestQuantile(t *testing.T) {
	buckets := []float64{math.Inf(-1), 0.001, 0.01, 0.1, math.Inf(1)}
	prev := &metrics.Float64Histogram{Counts: []uint64{100, 0, 0, 0}, Buckets: buckets}
	cur := &metrics.Float64Histogram{Counts: []uint64{190, 9, 1, 0}, Buckets: buckets}
	// Of the hundred added, 90 are under 1 ms, 9 under 10 ms, one under 100 ms.
	for q, want := range map[float64]float64{0.5: 0.001, 0.95: 0.01, 0.99: 0.01, 1: 0.1} {
		if got := quantile(prev, cur, q); got != want {
			t.Errorf("quantile %v = %v, want %v", q, got, want)
		}
	}
	if got := quantile(cur, cur, 0.99); got != 0 {
		t.Errorf("nothing added: %v", got)
	}
	// The last bucket has no upper bound: its lower one is reported.
	over := &metrics.Float64Histogram{Counts: []uint64{100, 0, 0, 5}, Buckets: buckets}
	if got := quantile(prev, over, 1); got != 0.1 {
		t.Errorf("unbounded bucket: %v", got)
	}
}

func TestControlGroup(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("cpu.max", "150000 100000\n")
	write("cpu.stat", "usage_usec 10\nthrottled_usec 3000000\n")
	write("memory.max", "536870912\n")
	write("memory.current", "1048576\n")
	if q, ok := cpuQuota(filepath.Join(dir, "cpu.max")); !ok || q != 1.5 {
		t.Errorf("quota %v %v", q, ok)
	}
	m := &Monitor{cgroup: dir}
	now := time.Now()
	st := m.evaluate(sample{at: now.Add(-10 * time.Second)}, sample{at: now, cpu: 14 * time.Second, throttled: 3 * time.Second, late: 150 * time.Millisecond, lateMax: time.Second})
	if st.CPUCores != 1.4 || st.CPULimit != 1.5 || st.CPUThrottled != 0.3 || !st.Overloaded || st.Reason != overload.Processor() || st.Delay != 0.15 || st.DelayMax != 1 {
		t.Errorf("unexpected status %+v", st)
	}
	if st.MemoryBytes != 1<<20 || st.MemoryLimit != 512<<20 {
		t.Errorf("memory %d of %d", st.MemoryBytes, st.MemoryLimit)
	}

	write("cpu.max", "max 100000\n")
	write("memory.max", "max\n")
	// Throttling alone is not overload: a process may use its whole quota
	// and still be on time.
	st = m.evaluate(sample{at: now.Add(-10 * time.Second)}, sample{at: now, cpu: time.Second, throttled: 5 * time.Second, late: 20 * time.Millisecond})
	if st.Overloaded || st.MemoryLimit != 0 || st.MemoryBytes == 0 {
		t.Errorf("no limits: %+v", st)
	}
}

func TestSchedulerDelayIsOverload(t *testing.T) {
	buckets := []float64{math.Inf(-1), 0.001, 0.01, 0.1, 1, math.Inf(1)}
	hist := func(counts ...uint64) *metrics.Float64Histogram {
		return &metrics.Float64Histogram{Counts: counts, Buckets: buckets}
	}
	m := &Monitor{cgroup: t.TempDir()}
	now := time.Now()
	// Two goroutines in a hundred waited up to a second for a processor.
	st := m.evaluate(sample{at: now.Add(-5 * time.Second), sched: hist(0, 0, 0, 0, 0)}, sample{at: now, sched: hist(90, 8, 0, 2, 0)})
	if !st.Overloaded || st.Reason != overload.Processor() || st.SchedulerDelay != 1 {
		t.Errorf("status %+v", st)
	}
	st = m.evaluate(sample{at: now.Add(-5 * time.Second), sched: hist(0, 0, 0, 0, 0)}, sample{at: now, sched: hist(900, 99, 1, 0, 0)})
	if st.Overloaded {
		t.Errorf("a few milliseconds are not overload: %+v", st)
	}
}

func TestWakeUpDelay(t *testing.T) {
	m := &Monitor{}
	if p90, most := m.takeLate(); p90 != 0 || most != 0 {
		t.Errorf("nothing measured: %s %s", p90, most)
	}
	for i := 1; i <= 20; i++ {
		m.late = append(m.late, time.Duration(i)*time.Millisecond)
	}
	if p90, most := m.takeLate(); p90 != 19*time.Millisecond || most != 20*time.Millisecond {
		t.Errorf("p90 %s, max %s", p90, most)
	}
	if len(m.late) != 0 {
		t.Error("the delays must be taken, not copied")
	}
}

func TestOverloadedSince(t *testing.T) {
	var none *Monitor
	if !none.Overloaded(time.Now()).IsZero() || none.Status().Overloaded {
		t.Error("a nil monitor is never overloaded")
	}
	now := time.Now()
	m := &Monitor{lastOver: now.Add(-time.Minute), reason: overload.Processor()}
	if got := m.Overloaded(now.Add(-2 * time.Minute)); got != overload.Processor() {
		t.Errorf("a cycle that began before the overload: %q", got)
	}
	if got := m.Overloaded(now.Add(-time.Minute + 2*time.Second)); got != overload.Processor() {
		t.Errorf("a cycle that began right after an overloaded sample: %q", got)
	}
	if got := m.Overloaded(now.Add(-30 * time.Second)); !got.IsZero() {
		t.Errorf("a cycle that began after the overload had passed: %s", got)
	}
}
