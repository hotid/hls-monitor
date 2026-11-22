// Package ingest takes check results, whether produced locally or reported
// by a remote agent, and turns them into stored state: time series,
// the segment journal, playlist snapshots and incidents.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hotid/hls-monitor/internal/metrics"
	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/store"
)

// ReopenWithin is how soon after it ended a problem that comes back is the
// same incident going on, by default.
const ReopenWithin = 10 * time.Minute

// Ingester applies results. It is safe for concurrent use.
type Ingester struct {
	st  *store.Store
	mx  *metrics.Client
	log *slog.Logger
	now func() time.Time

	// An incident is closed after this many consecutive cycles without the
	// problem, provided this much time has passed since it last occurred.
	CloseCycles int
	CloseAfter  time.Duration
	// CloseEventAfter is how long an informational incident stays open
	// after its last occurrence.
	CloseEventAfter time.Duration
	// ReopenWithin is how soon after it ended a problem that comes back
	// is the same incident going on rather than a new one.
	ReopenWithin time.Duration

	stored, repeated, failed atomic.Int64

	// own is the clip of its own each stream was last found to play, by
	// stream and vantage point. It is learnt again within a segment after
	// a restart, and so is not stored.
	ownMu sync.Mutex
	own   map[[2]int64]string

	// vantages is where every playlist ended as every place last saw it.
	vantageMu sync.Mutex
	vantages  map[vantageKey]*vantageState
}

// OwnClip names the clip of its own a stream plays as an agent sees it;
// empty when it plays none.
func (in *Ingester) OwnClip(streamID, agentID int64) string {
	in.ownMu.Lock()
	defer in.ownMu.Unlock()
	return in.own[[2]int64{streamID, agentID}]
}

func (in *Ingester) noteOwn(agentID int64, r *probe.Result) {
	in.ownMu.Lock()
	defer in.ownMu.Unlock()
	key := [2]int64{r.TargetID, agentID}
	if r.OwnClip == "" {
		delete(in.own, key)
		return
	}
	if in.own == nil {
		in.own = map[[2]int64]string{}
	}
	in.own[key] = r.OwnClip
}

// Stats counts what became of the results given to Apply since the start.
type Stats struct {
	Stored int64 `json:"stored"`
	// Repeated were refused as delivered before (ErrStale).
	Repeated int64 `json:"repeated"`
	// Failed could not be stored; an agent sends such a result again, a
	// result of the server's own checks is lost after a few attempts.
	Failed int64 `json:"failed"`
}

// Stats returns the counters.
func (in *Ingester) Stats() Stats {
	return Stats{Stored: in.stored.Load(), Repeated: in.repeated.Load(), Failed: in.failed.Load()}
}

// Apply stores one result; see apply. It counts the outcome.
func (in *Ingester) Apply(ctx context.Context, agentID int64, r *probe.Result) error {
	err := in.apply(ctx, agentID, r)
	switch {
	case err == nil:
		in.stored.Add(1)
		in.noteOwn(agentID, r)
	case errors.Is(err, ErrStale):
		in.repeated.Add(1)
	case !errors.Is(err, store.ErrNotFound):
		in.failed.Add(1)
	}
	return err
}

// New creates an ingester. mx may be nil.
func New(st *store.Store, mx *metrics.Client, log *slog.Logger) *Ingester {
	if log == nil {
		log = slog.Default()
	}
	return &Ingester{st: st, mx: mx, log: log, now: time.Now, CloseCycles: 3, CloseAfter: 30 * time.Second, CloseEventAfter: 30 * time.Minute, ReopenWithin: ReopenWithin}
}

// summaryAggregates are stream-wide figures kept with the latest state so that
// lists and dashboards do not need a time series query per stream.
var summaryAggregates = []struct {
	name string // key in the summary
	from string // source metric
	how  string // max, min, sum, avg
}{
	{"bitrate_bps", "hls_segment_bitrate_bps", "max"},
	{"declared_bandwidth_bps", "hls_variant_declared_bandwidth_bps", "max"},
	{"segment_total_seconds", "hls_segment_total_seconds", "avg"},
	{"segment_download_ratio", "hls_segment_download_ratio", "max"},
	{"playlist_total_seconds", "hls_playlist_total_seconds", "max"},
	{"live_latency_seconds", "hls_live_latency_seconds", "max"},
	{"buffer_seconds", "hls_buffer_seconds", "min"},
	{"segments_checked", "hls_segments_checked", "sum"},
	{"segment_errors", "hls_segment_errors", "sum"},
	{"stalls", "hls_stalls", "sum"},
	{"height", "hls_media_height", "max"},
	{"width", "hls_media_width", "max"},
	{"fps", "hls_media_fps", "max"},
	// 1 for a picture in PQ (HDR10, Dolby Vision), 2 for HLG, 0 for standard.
	{"hdr", "hls_media_hdr", "max"},
	// Loudness by EBU R 128 of the loudest track, in LUFS: measured now
	// and then, and kept until measured again.
	{"loudness_lufs", "hls_audio_loudness_lufs", "max"},
	// Cues in the subtitles in the last hour; 0 for subtitles that are empty.
	{"subtitle_cues_hour", "hls_subtitle_cues_hour", "max"},
}

// ErrStale is returned for a result that is not newer than the newest one
// already stored for the same stream from the same agent.
var ErrStale = errors.New("the result is not newer than what is already stored")

// apply stores one result reported by one agent. It returns store.ErrNotFound
// when the stream no longer exists.
//
// An agent delivers the results of a stream in the order it made them, so a
// result that is not newer than the stored one has been delivered before:
// the answer to its first delivery was lost and the agent sent it again.
// It is refused with ErrStale and changes nothing; that is what makes
// delivery safe to repeat, and keeps an old result from replacing the
// state a newer one has set.
func (in *Ingester) apply(ctx context.Context, agentID int64, r *probe.Result) error {
	now := in.now()
	// Agents may deliver results late, but a time from the future is a clock problem.
	if r.Time.IsZero() || r.Time.After(now.Add(5*time.Minute)) {
		r.Time = now
	}
	in.compareVantages(agentID, r, now)
	a := &applying{in: in, ctx: ctx, agentID: agentID, r: r, t: r.Time}

	err := in.st.Tx(ctx, func(tx *store.Tx) error {
		a.tx = tx
		last, err := tx.StreamCheckedAt(ctx, r.TargetID, agentID)
		if err != nil {
			return err
		}
		if last != 0 && a.t.UnixMilli() <= last {
			return ErrStale
		}
		if r.Variants != nil {
			a.ids, err = tx.SyncVariants(ctx, r.TargetID, r.Variants, a.t)
		} else {
			a.ids, err = tx.VariantIDs(ctx, r.TargetID)
		}
		if err != nil {
			return err
		}
		a.active = map[string]bool{}
		for _, v := range r.Variants {
			a.active[v.Key] = true
		}
		for _, step := range []func() error{a.metrics, a.states, a.journal, a.incidents, a.picture, a.snapshots, a.quiet} {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			return store.ErrNotFound // the stream or the agent was deleted meanwhile
		}
		return err
	}
	// The journal is a convenience kept apart from the state: failing to
	// write it must not lose the result that has just been stored.
	if err := in.st.AppendSegments(ctx, r.TargetID, agentID, a.entries); err != nil && ctx.Err() == nil {
		in.log.Warn("cannot write the segment journal", "stream", r.TargetID, "error", err)
	}
	if in.mx != nil {
		in.mx.Write(a.samples)
	}
	return nil
}

// applying is the storing of one result, within one transaction.
type applying struct {
	in      *Ingester
	ctx     context.Context
	tx      *store.Tx
	agentID int64
	r       *probe.Result
	t       time.Time
	// ids are the variants of the stream by key, active those the result
	// lists.
	ids    map[string]int64
	active map[string]bool
	// The figures of the result: by variant, of the stream as a whole,
	// and which variants were reached.
	perVariant  map[string]map[string]float64
	streamLevel map[string]float64
	up          map[string]bool
	samples     []metrics.Sample
	entries     []store.JournalEntry
	// open are the incidents open before the result, present the problems
	// it reports, began those of them that begin with it, by variant.
	open    []*store.Incident
	present map[incidentKey]bool
	began   map[string][]string
}

type incidentKey struct {
	variant int64
	kind    string
}

// variantID is the ID of a variant, made when it is not known yet.
func (a *applying) variantID(key string) (int64, error) {
	if id, ok := a.ids[key]; ok {
		return id, nil
	}
	id, err := a.tx.EnsureVariant(a.ctx, a.r.TargetID, key, a.t)
	if err == nil {
		a.ids[key] = id
	}
	return id, err
}

// metrics sorts the figures: time series, plus the latest values per variant.
func (a *applying) metrics() error {
	a.perVariant, a.streamLevel, a.up = map[string]map[string]float64{}, map[string]float64{}, map[string]bool{}
	for _, m := range a.r.Metrics {
		if !metrics.ValidName(m.Name) || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			continue
		}
		var vid int64
		switch {
		case m.Server != "":
			// About a server, not a playlist: a series only.
		case m.Variant == "":
			a.streamLevel[m.Name] = m.Value
		default:
			var err error
			if vid, err = a.variantID(m.Variant); err != nil {
				return err
			}
			if a.perVariant[m.Variant] == nil {
				a.perVariant[m.Variant] = map[string]float64{}
			}
			a.perVariant[m.Variant][m.Name] = m.Value
			if m.Name == "hls_up" {
				a.up[m.Variant] = m.Value == 1
			}
		}
		a.samples = append(a.samples, metrics.Sample{Name: m.Name, Stream: a.r.TargetID, Agent: a.agentID, Variant: vid, Server: m.Server, IP: m.IP, Value: m.Value, Time: a.t})
	}
	return nil
}

// states saves the latest state of every variant and of the stream.
// Segment figures exist only in cycles that found a new segment, which is
// roughly every other one. The latest state keeps the last known value of
// everything, as long as the variant is still being reached.
func (a *applying) states() error {
	ctx, tx, r := a.ctx, a.tx, a.r
	sources := map[string]probe.Source{}
	for _, src := range r.Sources {
		sources[src.Variant] = src
	}
	previous, err := tx.VariantMetrics(ctx, r.TargetID, a.agentID)
	if err != nil {
		return err
	}
	for key, vals := range a.perVariant {
		vid, err := a.variantID(key)
		if err != nil {
			return err
		}
		if a.up[key] {
			for name, v := range previous[vid] {
				if _, ok := vals[name]; !ok {
					vals[name] = v
				}
			}
		}
		var pl, seg *probe.Endpoint
		if src, ok := sources[key]; ok {
			pl, seg = src.Playlist, src.Segment
		}
		if err := tx.SaveVariantState(ctx, vid, a.agentID, statusName(vals["hls_status"]), a.t, vals, pl, seg); err != nil {
			return err
		}
		for what, e := range map[string]*probe.Endpoint{"playlist": pl, "segment": seg} {
			if e == nil || e.Host == "" {
				continue
			}
			if err := tx.RecordSource(ctx, vid, a.agentID, what, e, a.t); err != nil {
				return err
			}
		}
	}
	return tx.SaveStreamState(ctx, r.TargetID, a.agentID, r.Status, a.t, summarize(r, a.streamLevel, a.perVariant))
}

// journal makes the entries of the segment journal, written after the
// transaction.
func (a *applying) journal() error {
	a.entries = a.entries[:0]
	for i := range a.r.Segments {
		vid, err := a.variantID(a.r.Segments[i].Variant)
		if err != nil {
			return err
		}
		a.entries = append(a.entries, store.JournalEntry{VariantID: vid, Segment: &a.r.Segments[i]})
	}
	return nil
}

// incidents opens or goes on with an incident for every problem reported.
func (a *applying) incidents() error {
	ctx, tx, r := a.ctx, a.tx, a.r
	var err error
	if a.open, err = tx.OpenIncidents(ctx, r.TargetID, a.agentID); err != nil {
		return err
	}
	wasOpen := map[incidentKey]bool{}
	for _, inc := range a.open {
		wasOpen[incidentKey{inc.VariantID, inc.Kind}] = true
	}
	a.present = map[incidentKey]bool{}
	// A playlist is kept only for problems that begin in this cycle: one
	// that persists would otherwise store the same text again and again.
	a.began = map[string][]string{}
	for _, p := range r.Problems {
		vid, err := a.variantID(p.Variant)
		if err != nil {
			return err
		}
		k := incidentKey{vid, p.Kind}
		a.present[k] = true
		if !wasOpen[k] {
			a.began[p.Variant] = append(a.began[p.Variant], p.Kind)
			if err := tx.ReopenIncident(ctx, r.TargetID, a.agentID, vid, p.Kind, a.t.Add(-a.in.ReopenWithin), a.t); err != nil {
				return err
			}
		}
		if err := tx.ReportIncident(ctx, r.TargetID, a.agentID, vid, p.Kind, p.Severity, p.Message, p.Details, p.Count, a.t); err != nil {
			return err
		}
	}
	return nil
}

// picture keeps the latest look at the stream to be shown, and the picture
// with the incidents it is the evidence of.
func (a *applying) picture() error {
	p := a.r.Picture
	if p == nil {
		return nil
	}
	if err := a.tx.SavePicture(a.ctx, store.StreamPicture{StreamID: a.r.TargetID, AgentID: a.agentID, Time: p.Time, Variant: p.Variant,
		HasPicture: p.HasPicture, Luma: p.Luma, Dark: p.Dark, HasAudio: p.HasAudio, AudioDB: p.AudioDB, JPEG: p.JPEG, Cells: p.Cells}); err != nil {
		return err
	}
	var seen []string
	for _, pr := range a.r.Problems {
		if probe.PictureProblem(pr.Kind) {
			seen = append(seen, pr.Kind)
		}
	}
	return a.tx.SaveIncidentPictures(a.ctx, a.r.TargetID, a.agentID, seen, p.Time, p.JPEG, p.Cells)
}

// snapshots keeps the playlists of the problems that began.
func (a *applying) snapshots() error {
	for _, sn := range a.r.Snapshots {
		if len(a.began[sn.Variant]) == 0 {
			continue
		}
		vid, err := a.variantID(sn.Variant)
		if err != nil {
			return err
		}
		if err := a.tx.InsertSnapshot(a.ctx, a.r.TargetID, a.agentID, vid, a.t, sn.URL, a.began[sn.Variant], sn.Body, sn.Previous, sn.PreviousTime); err != nil {
			return err
		}
	}
	return nil
}

// quiet goes through the open incidents the result does not report: it
// counts a clean cycle for them, or closes them.
func (a *applying) quiet() error {
	tx, r, t := a.tx, a.r, a.t
	for _, inc := range a.open {
		if a.present[incidentKey{inc.VariantID, inc.Kind}] {
			continue
		}
		var err error
		switch {
		case r.Variants != nil && !a.active[inc.VariantKey]:
			// The variant is gone from the stream, and its problems with it.
			err = tx.CloseIncident(a.ctx, inc.ID, t)
		case !a.up[inc.VariantKey] || r.Overloaded != "":
			// The variant was not reached in this cycle, or the checker
			// could not keep up and left out what rests on timing: no
			// evidence either way.
		case inc.Severity == probe.SevInfo && t.Sub(inc.LastSeen) < a.in.CloseEventAfter:
			// Something a source does over and over is one event that
			// keeps recurring, not a new one every minute.
			err = tx.MarkIncidentClean(a.ctx, inc.ID)
		case inc.CleanCycles+1 >= a.in.CloseCycles && t.Sub(inc.LastSeen) >= a.in.CloseAfter:
			err = tx.CloseIncident(a.ctx, inc.ID, t)
		default:
			err = tx.MarkIncidentClean(a.ctx, inc.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func statusName(v float64) string {
	switch {
	case v >= 2:
		return probe.StatusFail
	case v >= 1:
		return probe.StatusWarn
	}
	return probe.StatusOK
}

func summarize(r *probe.Result, streamLevel map[string]float64, perVariant map[string]map[string]float64) map[string]float64 {
	out := map[string]float64{}
	if v, ok := streamLevel["hls_check_duration_seconds"]; ok {
		out["check_duration_seconds"] = v
	}
	for _, a := range summaryAggregates {
		var acc float64
		n := 0
		for _, vals := range perVariant {
			v, ok := vals[a.from]
			if !ok {
				continue
			}
			switch {
			case n == 0:
				acc = v
			case a.how == "max":
				acc = max(acc, v)
			case a.how == "min":
				acc = min(acc, v)
			default:
				acc += v
			}
			n++
		}
		if n == 0 {
			continue
		}
		if a.how == "avg" {
			acc /= float64(n)
		}
		out[a.name] = acc
	}
	video, subtitles := 0, 0
	for _, v := range r.Variants {
		switch v.Kind {
		case probe.KindVideo, probe.KindMedia:
			video++
		case probe.KindSubtitles:
			subtitles++
		}
	}
	if r.Variants != nil {
		out["variants"] = float64(video)
		out["subtitles"] = float64(subtitles)
	}
	problems := 0
	for _, p := range r.Problems {
		if p.Severity != probe.SevInfo {
			problems++
		}
	}
	out["problems"] = float64(problems)
	if r.Overloaded != "" {
		out["overloaded"] = 1
	}
	return out
}
