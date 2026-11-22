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
	t := r.Time
	var samples []metrics.Sample
	var journal []store.JournalEntry

	err := in.st.Tx(ctx, func(tx *store.Tx) error {
		last, err := tx.StreamCheckedAt(ctx, r.TargetID, agentID)
		if err != nil {
			return err
		}
		if last != 0 && t.UnixMilli() <= last {
			return ErrStale
		}
		var ids map[string]int64
		if r.Variants != nil {
			ids, err = tx.SyncVariants(ctx, r.TargetID, r.Variants, t)
		} else {
			ids, err = tx.VariantIDs(ctx, r.TargetID)
		}
		if err != nil {
			return err
		}
		active := map[string]bool{}
		for _, v := range r.Variants {
			active[v.Key] = true
		}
		variantID := func(key string) (int64, error) {
			if id, ok := ids[key]; ok {
				return id, nil
			}
			id, err := tx.EnsureVariant(ctx, r.TargetID, key, t)
			if err == nil {
				ids[key] = id
			}
			return id, err
		}

		// Metrics: time series plus the latest values per variant.
		perVariant := map[string]map[string]float64{}
		streamLevel := map[string]float64{}
		up := map[string]bool{}
		for _, m := range r.Metrics {
			if !metrics.ValidName(m.Name) || math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
				continue
			}
			var vid int64
			if m.Server != "" {
				// About a server, not a playlist: a series only.
			} else if m.Variant == "" {
				streamLevel[m.Name] = m.Value
			} else {
				if vid, err = variantID(m.Variant); err != nil {
					return err
				}
				if perVariant[m.Variant] == nil {
					perVariant[m.Variant] = map[string]float64{}
				}
				perVariant[m.Variant][m.Name] = m.Value
				if m.Name == "hls_up" {
					up[m.Variant] = m.Value == 1
				}
			}
			samples = append(samples, metrics.Sample{Name: m.Name, Stream: r.TargetID, Agent: agentID, Variant: vid, Server: m.Server, IP: m.IP, Value: m.Value, Time: t})
		}

		// Segment figures exist only in cycles that found a new segment, which
		// is roughly every other one. The latest state keeps the last known
		// value of everything, as long as the variant is still being reached.
		sources := map[string]probe.Source{}
		for _, src := range r.Sources {
			sources[src.Variant] = src
		}
		previous, err := tx.VariantMetrics(ctx, r.TargetID, agentID)
		if err != nil {
			return err
		}
		for key, vals := range perVariant {
			vid, err := variantID(key)
			if err != nil {
				return err
			}
			if up[key] {
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
			if err := tx.SaveVariantState(ctx, vid, agentID, statusName(vals["hls_status"]), t, vals, pl, seg); err != nil {
				return err
			}
			for what, e := range map[string]*probe.Endpoint{"playlist": pl, "segment": seg} {
				if e == nil || e.Host == "" {
					continue
				}
				if err := tx.RecordSource(ctx, vid, agentID, what, e, t); err != nil {
					return err
				}
			}
		}
		if err := tx.SaveStreamState(ctx, r.TargetID, agentID, r.Status, t, summarize(r, streamLevel, perVariant)); err != nil {
			return err
		}

		journal = journal[:0]
		for i := range r.Segments {
			vid, err := variantID(r.Segments[i].Variant)
			if err != nil {
				return err
			}
			journal = append(journal, store.JournalEntry{VariantID: vid, Segment: &r.Segments[i]})
		}
		// Incidents.
		open, err := tx.OpenIncidents(ctx, r.TargetID, agentID)
		if err != nil {
			return err
		}
		type key struct {
			variant int64
			kind    string
		}
		wasOpen := map[key]bool{}
		for _, inc := range open {
			wasOpen[key{inc.VariantID, inc.Kind}] = true
		}
		present := map[key]bool{}
		// A playlist is kept only for problems that begin in this cycle: one
		// that persists would otherwise store the same text again and again.
		began := map[string][]string{}
		for _, p := range r.Problems {
			vid, err := variantID(p.Variant)
			if err != nil {
				return err
			}
			present[key{vid, p.Kind}] = true
			if !wasOpen[key{vid, p.Kind}] {
				began[p.Variant] = append(began[p.Variant], p.Kind)
				if err := tx.ReopenIncident(ctx, r.TargetID, agentID, vid, p.Kind, t.Add(-in.ReopenWithin), t); err != nil {
					return err
				}
			}
			if err := tx.ReportIncident(ctx, r.TargetID, agentID, vid, p.Kind, p.Severity, p.Message, p.Details, p.Count, t); err != nil {
				return err
			}
		}
		// A look at the stream: the latest one is kept to be shown, and
		// the picture stays with the incidents it is the evidence of.
		if p := r.Picture; p != nil {
			if err := tx.SavePicture(ctx, store.StreamPicture{StreamID: r.TargetID, AgentID: agentID, Time: p.Time, Variant: p.Variant,
				HasPicture: p.HasPicture, Luma: p.Luma, Dark: p.Dark, HasAudio: p.HasAudio, AudioDB: p.AudioDB, JPEG: p.JPEG, Cells: p.Cells}); err != nil {
				return err
			}
			var seen []string
			for _, pr := range r.Problems {
				if probe.PictureProblem(pr.Kind) {
					seen = append(seen, pr.Kind)
				}
			}
			if err := tx.SaveIncidentPictures(ctx, r.TargetID, agentID, seen, p.Time, p.JPEG, p.Cells); err != nil {
				return err
			}
		}
		for _, sn := range r.Snapshots {
			if len(began[sn.Variant]) == 0 {
				continue
			}
			vid, err := variantID(sn.Variant)
			if err != nil {
				return err
			}
			if err := tx.InsertSnapshot(ctx, r.TargetID, agentID, vid, t, sn.URL, began[sn.Variant], sn.Body, sn.Previous, sn.PreviousTime); err != nil {
				return err
			}
		}
		for _, inc := range open {
			if present[key{inc.VariantID, inc.Kind}] {
				continue
			}
			switch {
			case r.Variants != nil && !active[inc.VariantKey]:
				// The variant is gone from the stream, and its problems with it.
				err = tx.CloseIncident(ctx, inc.ID, t)
			case !up[inc.VariantKey] || r.Overloaded != "":
				// The variant was not reached in this cycle, or the checker
				// could not keep up and left out what rests on timing: no
				// evidence either way.
			case inc.Severity == probe.SevInfo && t.Sub(inc.LastSeen) < in.CloseEventAfter:
				// Something a source does over and over is one event that
				// keeps recurring, not a new one every minute.
				err = tx.MarkIncidentClean(ctx, inc.ID)
			case inc.CleanCycles+1 >= in.CloseCycles && t.Sub(inc.LastSeen) >= in.CloseAfter:
				err = tx.CloseIncident(ctx, inc.ID, t)
			default:
				err = tx.MarkIncidentClean(ctx, inc.ID)
			}
			if err != nil {
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
	if err := in.st.AppendSegments(ctx, r.TargetID, agentID, journal); err != nil && ctx.Err() == nil {
		in.log.Warn("cannot write the segment journal", "stream", r.TargetID, "error", err)
	}
	if in.mx != nil {
		in.mx.Write(samples)
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
	video := 0
	for _, v := range r.Variants {
		if v.Kind == probe.KindVideo || v.Kind == probe.KindMedia {
			video++
		}
	}
	if r.Variants != nil {
		out["variants"] = float64(video)
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
