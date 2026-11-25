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
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/node"
	"github.com/hotid/hls-monitor/internal/types/overload"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/status"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
)

const snapshotMaxBytes = 64 << 10

// cycle accumulates the result of one check cycle. Variants are checked
// concurrently, so every method is safe for concurrent use.
type cycle struct {
	mu      sync.Mutex
	res     *Result
	probs   map[problemKey]*Problem
	snapped map[variantkey.Key]bool
	// before is the previous text of each media playlist.
	before  map[variantkey.Key]playlistText
	sources map[variantkey.Key]*Source
	// context holds, per variant, the circumstances of the check that
	// every problem of that variant is reported with.
	context map[variantkey.Key]*detail.Set
	// doubts counts, per problem, the occurrences that rest on timing alone:
	// a slow download, a request that timed out. See overloaded.
	doubts map[problemKey]int
	// tcp holds, per variant, what the kernel told of the connections the
	// cycle used.
	tcp map[variantkey.Key]*tcpSeen
	// servers holds the same by the server that answered.
	servers map[node.Node]*serverSeen

	minTargetDuration float64
	// minSlack is the shortest time, across live playlists, in which a
	// segment that is in the playlist now is still guaranteed to be there.
	minSlack float64
	anyLive  bool
}

// problemKey is what a problem of a cycle is one of a kind by: what is
// wrong and with which playlist.
type problemKey struct {
	kind    problemkind.Kind
	variant variantkey.Key
}

func newCycle(targetID id.Stream, now time.Time) *cycle {
	return &cycle{
		res:     &Result{TargetID: targetID, Time: now, Metrics: []Metric{}},
		probs:   map[problemKey]*Problem{},
		snapped: map[variantkey.Key]bool{},
		sources: map[variantkey.Key]*Source{},
		context: map[variantkey.Key]*detail.Set{},
	}
}

// metric records a figure of a playlist.
func (cy *cycle) metric(name metricname.Name, variant variantkey.Key, v float64) {
	cy.mu.Lock()
	cy.res.Metrics = append(cy.res.Metrics, Metric{Name: name, Variant: variant, Value: v})
	cy.mu.Unlock()
}

// streamMetric records a figure of the stream as a whole.
func (cy *cycle) streamMetric(name metricname.Name, v float64) {
	cy.mu.Lock()
	cy.res.Metrics = append(cy.res.Metrics, Metric{Name: name, Value: v})
	cy.mu.Unlock()
}

// problem raises a problem; the words that follow are its message, put
// together the way fmt does.
func (cy *cycle) problem(kind problemkind.Kind, sev severity.Severity, variant variantkey.Key, format string, args ...any) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	key := problemKey{kind, variant}
	if p, ok := cy.probs[key]; ok {
		p.Count++
		if sev.Worse(p.Severity) {
			p.Severity = sev
		}
		return
	}
	cy.probs[key] = &Problem{Kind: kind, Severity: sev, Variant: variant, Message: fmt.Sprintf(format, args...), Count: 1}
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
func (cy *cycle) network(variant variantkey.Key, resp *httpx.Response) {
	if resp == nil || resp.TCP == nil {
		return
	}
	cy.mu.Lock()
	defer cy.mu.Unlock()
	if cy.tcp == nil {
		cy.tcp = map[variantkey.Key]*tcpSeen{}
	}
	t := cy.tcp[variant]
	if t == nil {
		t = &tcpSeen{}
		cy.tcp[variant] = t
	}
	t.rtt = resp.TCP.RTT.Seconds()
	t.outOfOrder += float64(resp.TCP.NewOutOfOrder)
	t.retransmits += float64(resp.TCP.NewRetransmits)

	// The same by server: loss and delay belong to the way to a server far
	// more than to one stream among those it serves.
	at := serverOf(resp)
	if at.Host() == "" {
		return
	}
	if cy.servers == nil {
		cy.servers = map[node.Node]*serverSeen{}
	}
	s := cy.servers[at]
	if s == nil {
		s = &serverSeen{}
		cy.servers[at] = s
	}
	s.requests++
	s.rtt += resp.TCP.RTT.Seconds()
	s.outOfOrder += float64(resp.TCP.NewOutOfOrder)
	s.retransmits += float64(resp.TCP.NewRetransmits)
}

// serverSeen sums up the requests one server answered in a cycle.
type serverSeen struct {
	requests, rtt, outOfOrder, retransmits float64
}

func serverOf(resp *httpx.Response) node.Node { return endpoint(resp.URL, resp).Node() }

// doubt marks the latest occurrence of a problem as resting on timing
// alone: it would look the same if the checking process were the slow one.
func (cy *cycle) doubt(kind problemkind.Kind, variant variantkey.Key) {
	cy.mu.Lock()
	if cy.doubts == nil {
		cy.doubts = map[problemKey]int{}
	}
	cy.doubts[problemKey{kind, variant}]++
	cy.mu.Unlock()
}

// overloaded records that the process could not keep up during the cycle
// and drops the problems that every occurrence of rests on timing.
func (cy *cycle) overloaded(reason overload.Reason) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	cy.res.Overloaded = reason
	for key, n := range cy.doubts {
		if p, ok := cy.probs[key]; ok && n >= p.Count {
			delete(cy.probs, key)
		}
	}
	cy.res.Metrics = append(cy.res.Metrics, Metric{Name: metricname.CheckOverloaded(), Value: 1})
}

// timedOut reports whether a request failed for taking too long.
func timedOut(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// lastMessage is what this cycle said about a playlist that could not be
// read; empty when it said nothing.
func (cy *cycle) lastMessage(variant variantkey.Key) string {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	for _, kind := range []problemkind.Kind{problemkind.PlaylistUnreachable(), problemkind.PlaylistHTTP(), problemkind.PlaylistParse()} {
		if p, ok := cy.probs[problemKey{kind, variant}]; ok {
			return p.Message
		}
	}
	return ""
}

// detail adds figures to a problem already raised in this cycle; the
// first value recorded for a figure stays.
func (cy *cycle) detail(kind problemkind.Kind, variant variantkey.Key, fs ...detail.Field) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	if p, ok := cy.probs[problemKey{kind, variant}]; ok {
		p.Details.Add(fs...)
	}
}

// about records circumstances of a variant's check, reported with every
// problem of that variant. A later value of a figure replaces the earlier.
func (cy *cycle) about(variant variantkey.Key, fs ...detail.Field) {
	cy.mu.Lock()
	defer cy.mu.Unlock()
	m := cy.context[variant]
	if m == nil {
		m = &detail.Set{}
		cy.context[variant] = m
	}
	m.Replace(fs...)
}

func (cy *cycle) snapshot(variant variantkey.Key, url string, body []byte) {
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
func (cy *cycle) previous(variant variantkey.Key, body []byte, at time.Time) {
	if len(body) > snapshotMaxBytes {
		body = body[:snapshotMaxBytes]
	}
	cy.mu.Lock()
	if cy.before == nil {
		cy.before = map[variantkey.Key]playlistText{}
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
func (cy *cycle) source(variant variantkey.Key, e *Endpoint, segment bool) {
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

// picture records what a look at the stream found.
func (cy *cycle) picture(p *Picture) {
	cy.mu.Lock()
	cy.res.Picture = p
	cy.mu.Unlock()
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

// finish turns accumulated problems into the final result: a stable problem
// list, per-variant and overall status.
func (cy *cycle) finish(elapsed time.Duration) *Result {
	cy.mu.Lock()
	defer cy.mu.Unlock()

	perVariant := map[variantkey.Key]status.Status{}
	for _, v := range cy.res.Variants {
		perVariant[v.Key] = status.OK()
	}
	overall := status.OK()
	for _, p := range cy.probs {
		if about := cy.context[p.Variant]; about != nil {
			p.Details.Fill(*about)
		}
		cy.res.Problems = append(cy.res.Problems, *p)
		st := status.Of(p.Severity)
		if now, seen := perVariant[p.Variant]; !seen || st.Worse(now) {
			perVariant[p.Variant] = st
		}
		if st.Worse(overall) {
			overall = st
		}
	}
	sort.Slice(cy.res.Problems, func(i, j int) bool {
		a, b := cy.res.Problems[i], cy.res.Problems[j]
		if a.Variant != b.Variant {
			return a.Variant.Compare(b.Variant) < 0
		}
		return a.Kind.String() < b.Kind.String()
	})
	for _, src := range cy.sources {
		cy.res.Sources = append(cy.res.Sources, *src)
	}
	sort.Slice(cy.res.Sources, func(i, j int) bool { return cy.res.Sources[i].Variant.Compare(cy.res.Sources[j].Variant) < 0 })
	sort.Slice(cy.res.Segments, func(i, j int) bool {
		a, b := cy.res.Segments[i], cy.res.Segments[j]
		if a.Variant != b.Variant {
			return a.Variant.Compare(b.Variant) < 0
		}
		return a.Seq < b.Seq
	})

	for key, r := range perVariant {
		cy.res.Statuses = append(cy.res.Statuses, VariantStatus{Variant: key, Status: r})
		cy.res.Metrics = append(cy.res.Metrics, Metric{Name: metricname.Status(), Variant: key, Value: r.Rank()})
	}
	sort.Slice(cy.res.Statuses, func(i, j int) bool { return cy.res.Statuses[i].Variant.Compare(cy.res.Statuses[j].Variant) < 0 })
	for key, t := range cy.tcp {
		cy.res.Metrics = append(cy.res.Metrics,
			Metric{Name: metricname.TCPRTTSeconds(), Variant: key, Value: t.rtt},
			Metric{Name: metricname.TCPOutOfOrder(), Variant: key, Value: t.outOfOrder},
			Metric{Name: metricname.TCPRetransmits(), Variant: key, Value: t.retransmits})
	}
	for at, s := range cy.servers {
		cy.res.Metrics = append(cy.res.Metrics,
			Metric{Name: metricname.ServerRequests(), Server: at.Host(), IP: at.IP(), Value: s.requests},
			Metric{Name: metricname.ServerRTTSeconds(), Server: at.Host(), IP: at.IP(), Value: s.rtt / s.requests},
			Metric{Name: metricname.ServerOutOfOrder(), Server: at.Host(), IP: at.IP(), Value: s.outOfOrder},
			Metric{Name: metricname.ServerRetransmits(), Server: at.Host(), IP: at.IP(), Value: s.retransmits})
	}
	cy.res.Metrics = append(cy.res.Metrics,
		Metric{Name: metricname.StreamStatus(), Value: overall.Rank()},
		Metric{Name: metricname.CheckDurationSeconds(), Value: elapsed.Seconds()},
	)
	cy.res.Status = overall
	return cy.res
}
