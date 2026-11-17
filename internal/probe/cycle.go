package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
)

const snapshotMaxBytes = 64 << 10

// cycle accumulates the result of one check cycle. Variants are checked
// concurrently, so every method is safe for concurrent use.
type cycle struct {
	mu      sync.Mutex
	res     *Result
	probs   map[string]*Problem
	snapped map[string]bool
	// before is the previous text of each media playlist.
	before  map[string]playlistText
	sources map[string]*Source
	// context holds, per variant, the circumstances of the check that
	// every problem of that variant is reported with.
	context map[string]map[string]any
	// doubts counts, per problem, the occurrences that rest on timing alone:
	// a slow download, a request that timed out. See overloaded.
	doubts map[string]int
	// tcp holds, per variant, what the kernel told of the connections the
	// cycle used.
	tcp map[string]*tcpSeen

	minTargetDuration float64
	// minSlack is the shortest time, across live playlists, in which a
	// segment that is in the playlist now is still guaranteed to be there.
	minSlack float64
	anyLive  bool
}

func newCycle(targetID int64, now time.Time) *cycle {
	return &cycle{
		res:     &Result{TargetID: targetID, Time: now, Metrics: []Metric{}},
		probs:   map[string]*Problem{},
		snapped: map[string]bool{},
		sources: map[string]*Source{},
		context: map[string]map[string]any{},
	}
}

func (cy *cycle) metric(name, variant string, v float64) {
	cy.mu.Lock()
	cy.res.Metrics = append(cy.res.Metrics, Metric{Name: name, Variant: variant, Value: v})
	cy.mu.Unlock()
}

func (cy *cycle) problem(kind, severity, variant, format string, args ...any) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	key := kind + "\x00" + variant
	if p, ok := cy.probs[key]; ok {
		p.Count++
		if severityRank(severity) > severityRank(p.Severity) {
			p.Severity = severity
		}
		return
	}
	cy.probs[key] = &Problem{Kind: kind, Severity: severity, Variant: variant, Message: fmt.Sprintf(format, args...), Count: 1}
}

// tcpSeen sums up the connections used for one variant in a cycle.
type tcpSeen struct {
	rtt                     float64
	outOfOrder, retransmits float64
}

// network records the kernel's figures of the connection a response came
// over: the round trip time to the server, and how many segments arrived
// out of order or had to be sent again since the connection was last
// looked at. A receiver cannot count what was lost on the way to it;
// segments out of order are the sign of it that it has.
func (cy *cycle) network(variant string, resp *httpx.Response) {
	if resp == nil || resp.TCP == nil {
		return
	}
	cy.mu.Lock()
	defer cy.mu.Unlock()
	if cy.tcp == nil {
		cy.tcp = map[string]*tcpSeen{}
	}
	t := cy.tcp[variant]
	if t == nil {
		t = &tcpSeen{}
		cy.tcp[variant] = t
	}
	t.rtt = resp.TCP.RTT.Seconds()
	t.outOfOrder += float64(resp.TCP.NewOutOfOrder)
	t.retransmits += float64(resp.TCP.NewRetransmits)
}

// doubt marks the latest occurrence of a problem as resting on timing
// alone: it would look the same if the checking process were the slow one.
func (cy *cycle) doubt(kind, variant string) {
	cy.mu.Lock()
	if cy.doubts == nil {
		cy.doubts = map[string]int{}
	}
	cy.doubts[kind+"\x00"+variant]++
	cy.mu.Unlock()
}

// overloaded records that the process could not keep up during the cycle
// and drops the problems that every occurrence of rests on timing.
func (cy *cycle) overloaded(reason string) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	cy.res.Overloaded = reason
	for key, n := range cy.doubts {
		if p, ok := cy.probs[key]; ok && n >= p.Count {
			delete(cy.probs, key)
		}
	}
	cy.res.Metrics = append(cy.res.Metrics, Metric{Name: "hls_check_overloaded", Value: 1})
}

// timedOut reports whether a request failed for taking too long.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// lastMessage is what this cycle said about a playlist that could not be
// read; empty when it said nothing.
func (cy *cycle) lastMessage(variant string) string {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	for _, kind := range []string{ProbPlaylistUnreachable, ProbPlaylistHTTP, ProbPlaylistParse} {
		if p, ok := cy.probs[kind+"\x00"+variant]; ok {
			return p.Message
		}
	}
	return ""
}

// detail adds figures to a problem already raised in this cycle. kv are
// alternating keys and values; the first value recorded for a key stays.
func (cy *cycle) detail(kind, variant string, kv ...any) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	p, ok := cy.probs[kind+"\x00"+variant]
	if !ok {
		return
	}
	if p.Details == nil {
		p.Details = map[string]any{}
	}
	putDetails(p.Details, kv, false)
}

// about records circumstances of a variant's check, reported with every
// problem of that variant. A later value for a key replaces the earlier.
func (cy *cycle) about(variant string, kv ...any) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	m := cy.context[variant]
	if m == nil {
		m = map[string]any{}
		cy.context[variant] = m
	}
	putDetails(m, kv, true)
}

func putDetails(m map[string]any, kv []any, replace bool) {
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		if _, have := m[k]; have && !replace {
			continue
		}
		switch v := kv[i+1].(type) {
		case float64:
			// Figures are for reading: a millisecond is precise enough.
			m[k] = float64(int64(v*1000+0.5*sign(v))) / 1000
		case time.Duration:
			m[k] = float64(v.Milliseconds()) / 1000
		case string:
			if v != "" {
				m[k] = v
			}
		case []string:
			if len(v) > 0 {
				m[k] = v
			}
		default:
			m[k] = v
		}
	}
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

func (cy *cycle) snapshot(variant, url string, body []byte) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	if cy.snapped[variant] {
		return
	}
	cy.snapped[variant] = true
	if len(body) > snapshotMaxBytes {
		body = body[:snapshotMaxBytes]
	}
	sn := Snapshot{Variant: variant, URL: url, Body: string(body)}
	if p, ok := cy.before[variant]; ok {
		at := p.at
		sn.Previous, sn.PreviousTime = string(p.body), &at
	}
	cy.res.Snapshots = append(cy.res.Snapshots, sn)
}

// previous tells what the playlist looked like at the check before, so
// that a snapshot can show both: most playlist problems are a difference
// between two checks.
func (cy *cycle) previous(variant string, body []byte, at time.Time) {
	if len(body) > snapshotMaxBytes {
		body = body[:snapshotMaxBytes]
	}
	cy.mu.Lock()
	if cy.before == nil {
		cy.before = map[string]playlistText{}
	}
	cy.before[variant] = playlistText{body: body, at: at}
	cy.mu.Unlock()
}

// playlistText is a playlist as it was received.
type playlistText struct {
	body []byte
	at   time.Time
}

// source records which server answered for a playlist (segment=false) or
// for a segment of it.
func (cy *cycle) source(variant string, e *Endpoint, segment bool) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	src := cy.sources[variant]
	if src == nil {
		src = &Source{Variant: variant}
		cy.sources[variant] = src
	}
	if segment {
		src.Segment = e
	} else {
		src.Playlist = e
	}
}

func (cy *cycle) segment(s Segment) {
	cy.mu.Lock()
	cy.res.Segments = append(cy.res.Segments, s)
	cy.mu.Unlock()
}

// slack records how long a live playlist keeps its segments: the length of
// its window without the longest segment.
func (cy *cycle) slack(seconds float64) {
	cy.mu.Lock()
	if seconds > 0 && (cy.minSlack == 0 || seconds < cy.minSlack) {
		cy.minSlack = seconds
	}
	cy.mu.Unlock()
}

func (cy *cycle) live(targetDuration float64) {
	cy.mu.Lock()
	cy.anyLive = true
	if targetDuration > 0 && (cy.minTargetDuration == 0 || targetDuration < cy.minTargetDuration) {
		cy.minTargetDuration = targetDuration
	}
	cy.mu.Unlock()
}

func severityRank(s string) float64 {
	switch s {
	case SevCrit:
		return 2
	case SevWarn:
		return 1
	}
	return 0
}

// finish turns accumulated problems into the final result: a stable problem
// list, per-variant and overall status.
func (cy *cycle) finish(elapsed time.Duration) *Result {
	cy.mu.Lock()
	defer cy.mu.Unlock()

	perVariant := map[string]float64{}
	for _, v := range cy.res.Variants {
		perVariant[v.Key] = 0
	}
	overall := 0.0
	for _, p := range cy.probs {
		if about := cy.context[p.Variant]; len(about) > 0 {
			if p.Details == nil {
				p.Details = map[string]any{}
			}
			for k, v := range about {
				if _, have := p.Details[k]; !have {
					p.Details[k] = v
				}
			}
		}
		cy.res.Problems = append(cy.res.Problems, *p)
		r := severityRank(p.Severity)
		perVariant[p.Variant] = max(perVariant[p.Variant], r)
		overall = max(overall, r)
	}
	sort.Slice(cy.res.Problems, func(i, j int) bool {
		a, b := cy.res.Problems[i], cy.res.Problems[j]
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.Kind < b.Kind
	})
	for _, src := range cy.sources {
		cy.res.Sources = append(cy.res.Sources, *src)
	}
	sort.Slice(cy.res.Sources, func(i, j int) bool { return cy.res.Sources[i].Variant < cy.res.Sources[j].Variant })
	sort.Slice(cy.res.Segments, func(i, j int) bool {
		a, b := cy.res.Segments[i], cy.res.Segments[j]
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		return a.Seq < b.Seq
	})

	for key, r := range perVariant {
		cy.res.Metrics = append(cy.res.Metrics, Metric{Name: "hls_status", Variant: key, Value: r})
	}
	for key, t := range cy.tcp {
		cy.res.Metrics = append(cy.res.Metrics,
			Metric{Name: "hls_tcp_rtt_seconds", Variant: key, Value: t.rtt},
			Metric{Name: "hls_tcp_out_of_order", Variant: key, Value: t.outOfOrder},
			Metric{Name: "hls_tcp_retransmits", Variant: key, Value: t.retransmits})
	}
	cy.res.Metrics = append(cy.res.Metrics,
		Metric{Name: "hls_stream_status", Value: overall},
		Metric{Name: "hls_check_duration_seconds", Value: elapsed.Seconds()},
	)
	switch overall {
	case 2:
		cy.res.Status = StatusFail
	case 1:
		cy.res.Status = StatusWarn
	default:
		cy.res.Status = StatusOK
	}
	return cy.res
}
