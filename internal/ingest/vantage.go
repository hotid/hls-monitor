package ingest

import (
	"math"
	"strconv"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
)

// One stream is checked from several places, and each sees where the
// stream has got to: the end of its playlist by EXT-X-PROGRAM-DATE-TIME. A
// place that sees an end well behind the others is served an old playlist
// — by another server, a cache or a path of its own — and a viewer there
// watches later than the rest, or runs out of segments.
const (
	// vantageFresh is how new a result must be, by its own time against
	// the server's clock, to say where the stream is now: one delivered
	// late says where it was.
	vantageFresh = 30 * time.Second
	// vantageKept is how long what a place saw stands for comparison.
	vantageKept = 2 * time.Minute
	// vantageSpread is how many target durations behind another place a
	// place may be: the checks are not made at one moment, and a segment
	// is published between them.
	vantageSpread = 2.0
	// vantageRuns results in a row must find a place behind.
	vantageRuns = 3
)

// seenEdge is where one playlist ended as one place saw it.
type seenEdge struct {
	// edge is the end of the playlist on the stream's own clock, at the
	// moment when on the server's clock.
	edge, when time.Time
	target     float64
}

type vantageKey struct {
	stream  int64
	variant string
}

type vantageState struct {
	edges map[int64]seenEdge
	// behind counts, by place, the results in a row that found it behind.
	behind map[int64]int
}

// compareVantages notes where the playlists of a result end and adds a
// problem to it when the place it comes from has for some checks been
// seeing the stream behind what another place sees.
func (in *Ingester) compareVantages(agentID int64, r *probe.Result, now time.Time) {
	if d := now.Sub(r.Time); d > vantageFresh || d < -vantageFresh {
		return
	}
	latency, target := map[string]float64{}, map[string]float64{}
	for _, m := range r.Metrics {
		switch m.Name {
		case "hls_live_latency_seconds":
			latency[m.Variant] = m.Value
		case "hls_target_duration_seconds":
			target[m.Variant] = m.Value
		}
	}
	in.vantageMu.Lock()
	defer in.vantageMu.Unlock()
	if in.vantages == nil {
		in.vantages = map[vantageKey]*vantageState{}
	}
	for variant, lat := range latency {
		k := vantageKey{r.TargetID, variant}
		st := in.vantages[k]
		if st == nil {
			st = &vantageState{edges: map[int64]seenEdge{}, behind: map[int64]int{}}
			in.vantages[k] = st
		}
		mine := seenEdge{edge: r.Time.Add(-time.Duration(lat * float64(time.Second))), when: now, target: target[variant]}
		st.edges[agentID] = mine
		// How far behind the place that is furthest ahead this one is: the
		// ends are brought to one moment by the time between the checks.
		worst, ahead := 0.0, int64(0)
		for other, e := range st.edges {
			if other == agentID {
				continue
			}
			if now.Sub(e.when) > vantageKept {
				delete(st.edges, other)
				delete(st.behind, other)
				continue
			}
			if d := e.edge.Sub(mine.edge).Seconds() + now.Sub(e.when).Seconds(); d > worst {
				worst, ahead = d, other
			}
		}
		limit := vantageSpread * math.Max(mine.target, 1)
		if worst <= limit {
			st.behind[agentID] = 0
			continue
		}
		if st.behind[agentID]++; st.behind[agentID] < vantageRuns {
			continue
		}
		r.Problems = append(r.Problems, probe.Problem{
			Kind: probe.ProbVantageBehind, Severity: probe.SevWarn, Variant: variant, Count: 1,
			Message: "the playlist seen from here ends " + round1(worst) + "s behind what another vantage point sees",
			Details: map[string]any{"behind_seconds": math.Round(worst*10) / 10, "behind_limit": limit, "ahead_agent_id": ahead, "checks_in_a_row": st.behind[agentID]},
		})
	}
}

func round1(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', 1, 64)
}
