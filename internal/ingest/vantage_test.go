package ingest

import (
	"testing"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

func TestVantagePointBehind(t *testing.T) {
	in := &Ingester{}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// seen makes the result of a check at now that found the playlist
	// ending lag seconds ago.
	seen := func(agent int64, lag float64) *probe.Result {
		r := &probe.Result{TargetID: id.StreamOf(7), Time: now, Metrics: []probe.Metric{
			{Name: metricname.LiveLatencySeconds(), Variant: variantkey.OfURI("v"), Value: lag},
			{Name: metricname.TargetDurationSeconds(), Variant: variantkey.OfURI("v"), Value: 6},
		}}
		in.compareVantages(id.AgentOf(agent), r, now)
		return r
	}
	told := func(r *probe.Result) bool {
		for _, p := range r.Problems {
			if p.Kind == problemkind.VantageBehind() {
				return true
			}
		}
		return false
	}
	// Both near the edge, a segment apart at most: nothing to tell.
	for i := 0; i < 6; i++ {
		now = now.Add(3 * time.Second)
		if told(seen(1, 4)) || told(seen(2, 9)) {
			t.Fatal("places a segment apart were told")
		}
	}
	// The second starts being served a playlist half a minute old.
	var last *probe.Result
	for i := 0; i < vantageRuns; i++ {
		now = now.Add(3 * time.Second)
		if told(seen(1, 4)) {
			t.Fatal("the place that is ahead was told")
		}
		if last = seen(2, 34); told(last) != (i == vantageRuns-1) {
			t.Fatalf("check %d of the place behind: told %v", i+1, told(last))
		}
	}
	if d := last.Problems[0].Details; num(d, detail.KeyBehindSeconds()) != 30.0 || num(d, detail.KeyAheadAgentID()) != 1 {
		t.Errorf("details %+v", d)
	}
	// It catches up, and is not told any more.
	now = now.Add(3 * time.Second)
	seen(1, 4)
	if told(seen(2, 5)) {
		t.Error("told after it caught up")
	}
	// A result delivered some seconds after its check, as an agent sends
	// them, or after a restart, is as close to the edge as it was then.
	for i := 0; i < vantageRuns+1; i++ {
		now = now.Add(3 * time.Second)
		seen(1, 4)
		r := &probe.Result{TargetID: id.StreamOf(7), Time: now.Add(-12 * time.Second), Metrics: []probe.Metric{
			{Name: metricname.LiveLatencySeconds(), Variant: variantkey.OfURI("v"), Value: 5},
			{Name: metricname.TargetDurationSeconds(), Variant: variantkey.OfURI("v"), Value: 6},
		}}
		in.compareVantages(id.AgentOf(2), r, now)
		if told(r) {
			t.Fatal("a result delivered 12 s after its check was told as 12 s behind")
		}
	}
	// A result delivered late says nothing of now.
	late := &probe.Result{TargetID: id.StreamOf(7), Time: now.Add(-5 * time.Minute), Metrics: []probe.Metric{{Name: metricname.LiveLatencySeconds(), Variant: variantkey.OfURI("v"), Value: 300}}}
	for i := 0; i < 5; i++ {
		in.compareVantages(id.AgentOf(2), late, now)
	}
	if told(late) {
		t.Error("a late result was compared")
	}
	// What a place saw long ago is not compared with.
	now = now.Add(10 * time.Minute)
	for i := 0; i < 5; i++ {
		if told(seen(2, 60)) {
			t.Error("compared with what another place saw ten minutes ago")
		}
	}
}

// num is a figure of a problem that is a number; zero when it has none.
func num(s detail.Set, k detail.Key) float64 {
	v, _ := s.Number(k)
	return v
}
