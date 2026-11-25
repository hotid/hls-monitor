// Package probe checks one HLS stream: playlists, segments and, optionally,
// the media container. It keeps the state needed between check cycles but
// stores nothing itself: every cycle produces a Result for the caller.
package probe

import (
	"encoding/json"
	"time"

	"github.com/hotid/hls-monitor/internal/httpx"
	"github.com/hotid/hls-monitor/internal/media"
	"github.com/hotid/hls-monitor/internal/types/cachestatus"
	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/level"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/node"
	"github.com/hotid/hls-monitor/internal/types/overload"
	"github.com/hotid/hls-monitor/internal/types/picturemode"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/status"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

// Target is the configuration of one monitored stream.
type Target struct {
	ID    id.Stream   `json:"id"`
	URL   string      `json:"url"`
	Level level.Level `json:"level" enums:"playlist,header,segments,container"`
	// IntervalSec is the pause between cycles; zero derives it from the
	// playlist target duration.
	IntervalSec float64 `json:"interval_sec"`
	// TimeoutSec bounds a single HTTP request; zero means 10 seconds.
	TimeoutSec float64 `json:"timeout_sec"`
	// MaxSegments limits segment downloads per variant per cycle; zero means 3.
	MaxSegments int               `json:"max_segments"`
	Headers     map[string]string `json:"headers,omitempty"`
	Insecure    bool              `json:"insecure,omitempty"`
	// Picture says what the picture and the sound are checked for.
	Picture picturemode.Mode `json:"picture,omitzero"`
	// Revision changes whenever the configuration changes.
	Revision int64 `json:"revision"`
}

// UnmarshalJSON reads a target as it travels between the server and an
// agent. One that does not say what the picture is checked for means
// everything: that is how it was written before there was a choice.
func (t *Target) UnmarshalJSON(b []byte) error {
	type wire Target
	w := wire{Picture: picturemode.Check()}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*t = Target(w)
	return nil
}

// Variant describes one playlist of the stream.
type Variant struct {
	Key          variantkey.Key   `json:"key"`
	Kind         variantkind.Kind `json:"kind" enums:"master,media,video,audio,subtitles"`
	URI          string           `json:"uri,omitempty"`
	Name         string           `json:"name,omitempty"`
	Bandwidth    int64            `json:"bandwidth,omitempty"`
	AvgBandwidth int64            `json:"avg_bandwidth,omitempty"`
	Resolution   string           `json:"resolution,omitempty"`
	Codecs       string           `json:"codecs,omitempty"`
	FrameRate    float64          `json:"frame_rate,omitempty"`
	Group        string           `json:"group,omitempty"`
	Language     string           `json:"language,omitempty"`
}

// Problem is one kind of trouble found for one variant in a cycle.
type Problem struct {
	Kind     problemkind.Kind  `json:"kind"`
	Severity severity.Severity `json:"severity" enums:"info,warn,crit"`
	Variant  variantkey.Key    `json:"variant"`
	Message  string            `json:"message"`
	// Count is how many times it occurred within the cycle.
	Count int `json:"count"`
	// Details are the figures the conclusion was drawn from: what was
	// requested, what came back and what it was compared with.
	Details detail.Set `json:"details,omitzero" swaggertype:"object"`
}

// Metric is one numeric observation of a cycle.
type Metric struct {
	Name    metricname.Name `json:"n"`
	Variant variantkey.Key  `json:"v,omitzero"`
	Value   float64         `json:"x"`
	// Server and IP name the server a figure is about, for the few that
	// describe the way to a server rather than a playlist: the host that
	// answered and the address the connection went to.
	Server string `json:"s,omitempty"`
	IP     string `json:"ip,omitempty"`
}

// Segment is the record of one checked media segment.
type Segment struct {
	Variant       variantkey.Key `json:"variant"`
	Seq           uint64         `json:"seq"`
	URL           string         `json:"url"`
	Time          time.Time      `json:"time"`
	Duration      float64        `json:"duration"`
	ProgramTime   *time.Time     `json:"program_time,omitempty"`
	Discontinuity bool           `json:"discontinuity,omitempty"`

	Status     int                `json:"status"`
	Error      string             `json:"error,omitempty"`
	RemoteAddr string             `json:"remote_addr,omitempty"`
	Proto      string             `json:"proto,omitempty"`
	Cache      cachestatus.Status `json:"cache,omitzero" enums:"hit,miss"`
	Size       int64              `json:"size"`
	// Fetched is how many bytes were actually downloaded; less than Size
	// when only the beginning of the segment was requested.
	Fetched   int64   `json:"fetched"`
	Partial   bool    `json:"partial,omitempty"`
	DNSMs     float64 `json:"dns_ms"`
	ConnectMs float64 `json:"connect_ms"`
	TLSMs     float64 `json:"tls_ms"`
	TTFBMs    float64 `json:"ttfb_ms"`
	TotalMs   float64 `json:"total_ms"`
	Bps       float64 `json:"bps"`

	// Source is the server that answered, when a response was received.
	Source *Endpoint `json:"source,omitempty"`

	Media    *media.Info        `json:"media,omitempty"`
	Problems []problemkind.Kind `json:"problems,omitempty"`
}

// Endpoint is the server that actually answered a request: after
// redirects, behind a balancer or an anycast address.
type Endpoint struct {
	// URL is the address the response finally came from.
	URL string `json:"url"`
	// Host is the host part of URL, with the port when it is not the default.
	Host string `json:"host"`
	// IP is the address of the server the connection was made to.
	IP    string `json:"ip,omitempty"`
	Proto string `json:"proto,omitempty"`
	// Headers are the response headers that identify the server or cache
	// node and its software (Server, Via, X-Cache and the like).
	Headers map[string]string `json:"headers,omitempty"`
	// Redirects are the hops followed from the requested URL, in order.
	Redirects []httpx.Hop `json:"redirects,omitempty"`
}

// Node is what identifies the answering server for change detection.
func (e *Endpoint) Node() node.Node { return node.Of(e.Host, e.IP) }

// Source tells where one playlist and its segments were served from in a cycle.
type Source struct {
	Variant  variantkey.Key `json:"variant"`
	Playlist *Endpoint      `json:"playlist,omitempty"`
	// Segment is the endpoint of the last segment downloaded in the cycle.
	Segment *Endpoint `json:"segment,omitempty"`
}

// Snapshot is the text of a playlist captured when something was wrong with it.
type Snapshot struct {
	Variant variantkey.Key `json:"variant"`
	URL     string         `json:"url"`
	Body    string         `json:"body"`
	// Previous is the text the same playlist had at the check before, and
	// PreviousTime when it was received; absent when there was none.
	Previous     string     `json:"previous,omitempty"`
	PreviousTime *time.Time `json:"previous_time,omitempty"`
}

// Result is the outcome of one check cycle of one stream.
type Result struct {
	TargetID id.Stream     `json:"target_id"`
	Time     time.Time     `json:"time"`
	Status   status.Status `json:"status" enums:"ok,warn,fail"`
	// Variants is the current set of playlists; nil when it could not be
	// determined in this cycle (the entry playlist failed).
	Variants []Variant `json:"variants,omitempty"`
	// Sources tells, per playlist, which servers answered in this cycle.
	Sources   []Source   `json:"sources,omitempty"`
	Metrics   []Metric   `json:"metrics"`
	Segments  []Segment  `json:"segments,omitempty"`
	Problems  []Problem  `json:"problems,omitempty"`
	Snapshots []Snapshot `json:"snapshots,omitempty"`
	// Picture is what a look at the stream found, in the cycles that looked.
	Picture *Picture `json:"picture,omitempty"`
	// OwnClip names the clip the stream is found to play that is known to
	// be the channel's own, and so no fault.
	OwnClip string `json:"own_clip,omitempty"`
	// Overloaded names what the checking process itself was short of during
	// the cycle; empty when it kept up. Findings that rest on timing alone
	// are left out of such a result: a monitor that cannot keep up measures
	// its own delay, not the stream's.
	Overloaded overload.Reason `json:"overloaded,omitzero" enums:"processor"`
}

// Thresholds tune when observations become problems.
type Thresholds struct {
	StaleFactor         float64       // playlist unchanged for this many target durations
	StaleMin            time.Duration // but never sooner than this
	TargetDurationSlack float64       // seconds a segment may exceed the target duration
	PDTDrift            float64       // seconds of program-date-time vs duration mismatch
	DurationMismatch    float64       // seconds between EXTINF and media duration
	PTSGap              float64       // seconds between consecutive segments
	PTSGapEstimated     float64       // same, when the previous end is estimated from EXTINF
	AVOffset            float64       // seconds between audio and video start
	BitrateFactor       float64       // actual / declared BANDWIDTH
	CertWarn            time.Duration
	CertCrit            time.Duration
	VariantEdgeSpread   float64 // target durations between the live edges of variants
}

// DefaultThresholds are used when a Checker is created without overrides.
func DefaultThresholds() Thresholds {
	return Thresholds{
		StaleFactor:         3,
		StaleMin:            10 * time.Second,
		TargetDurationSlack: 0.5,
		PDTDrift:            0.5,
		DurationMismatch:    0.5,
		PTSGap:              0.2,
		PTSGapEstimated:     0.5,
		AVOffset:            1.0,
		BitrateFactor:       1.5,
		CertWarn:            14 * 24 * time.Hour,
		CertCrit:            3 * 24 * time.Hour,
		VariantEdgeSpread:   2,
	}
}
