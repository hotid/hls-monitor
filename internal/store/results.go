package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/metricname"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/sourcekind"
	"github.com/hotid/hls-monitor/internal/types/status"
	"github.com/hotid/hls-monitor/internal/types/summarykey"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

// Variant is a playlist of a stream as stored.
type Variant struct {
	ID       id.Variant `json:"id"`
	StreamID id.Stream  `json:"stream_id"`
	probe.Variant
	Active   bool      `json:"active"`
	LastSeen time.Time `json:"last_seen"`
}

const variantCols = "id, stream_id, key, kind, uri, name, bandwidth, avg_bandwidth, resolution, codecs, frame_rate, grp, language, active, last_seen"

func scanVariant(row interface{ Scan(...any) error }) (*Variant, error) {
	var v Variant
	var seen int64
	if err := row.Scan(&v.ID, &v.StreamID, &v.Key, &v.Kind, &v.URI, &v.Name, &v.Bandwidth, &v.AvgBandwidth, &v.Resolution,
		&v.Codecs, &v.FrameRate, &v.Group, &v.Language, &v.Active, &seen); err != nil {
		return nil, notFound(err)
	}
	v.LastSeen = fromMs(seen)
	return &v, nil
}

// SyncVariants stores the current set of playlists of a stream: listed ones
// are created or refreshed, all others are marked inactive. It returns the
// IDs of every variant the stream ever had, by key.
func (tx *Tx) SyncVariants(ctx context.Context, streamID id.Stream, vs []probe.Variant, now time.Time) (map[variantkey.Key]id.Variant, error) {
	if _, err := tx.q.ExecContext(ctx, "UPDATE variants SET active = 0 WHERE stream_id = ?", streamID); err != nil {
		return nil, err
	}
	for _, v := range vs {
		_, err := tx.q.ExecContext(ctx, `
			INSERT INTO variants (stream_id, key, kind, uri, name, bandwidth, avg_bandwidth, resolution, codecs, frame_rate, grp, language, active, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
			ON CONFLICT (stream_id, key) DO UPDATE SET
				kind = excluded.kind, uri = excluded.uri, name = excluded.name, bandwidth = excluded.bandwidth,
				avg_bandwidth = excluded.avg_bandwidth, resolution = excluded.resolution, codecs = excluded.codecs,
				frame_rate = excluded.frame_rate, grp = excluded.grp, language = excluded.language,
				active = 1, last_seen = excluded.last_seen`,
			streamID, v.Key, v.Kind, v.URI, v.Name, v.Bandwidth, v.AvgBandwidth, v.Resolution, v.Codecs, v.FrameRate, v.Group, v.Language,
			toMs(now), toMs(now))
		if err != nil {
			return nil, err
		}
	}
	return tx.VariantIDs(ctx, streamID)
}

// VariantIDs returns the IDs of all variants of a stream by key.
func (tx *Tx) VariantIDs(ctx context.Context, streamID id.Stream) (map[variantkey.Key]id.Variant, error) {
	rows, err := tx.q.QueryContext(ctx, "SELECT key, id FROM variants WHERE stream_id = ?", streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[variantkey.Key]id.Variant{}
	for rows.Next() {
		var k variantkey.Key
		var v id.Variant
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// EnsureVariant returns the ID of a variant, creating a placeholder when a
// result refers to a key that was never listed (the entry playlist failing
// before its type is known).
func (tx *Tx) EnsureVariant(ctx context.Context, streamID id.Stream, key variantkey.Key, now time.Time) (id.Variant, error) {
	kind := variantkind.Video()
	switch key {
	case variantkey.Master():
		kind = variantkind.Master()
	case variantkey.Media():
		kind = variantkind.Media()
	}
	var made id.Variant
	err := tx.q.QueryRowContext(ctx, `
		INSERT INTO variants (stream_id, key, kind, active, first_seen, last_seen) VALUES (?, ?, ?, 1, ?, ?)
		ON CONFLICT (stream_id, key) DO UPDATE SET last_seen = excluded.last_seen
		RETURNING id`, streamID, key, kind, toMs(now), toMs(now)).Scan(&made)
	return made, err
}

func (s *Store) ListVariants(ctx context.Context, streamID id.Stream) ([]*Variant, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+variantCols+" FROM variants WHERE stream_id = ? ORDER BY active DESC, kind = 'master' DESC, kind, bandwidth DESC, key", streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Variant{}
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListAllVariants returns the active variants of every stream.
func (s *Store) ListAllVariants(ctx context.Context) ([]*Variant, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+variantCols+" FROM variants WHERE active = 1 ORDER BY stream_id, kind = 'master' DESC, kind, bandwidth DESC, key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Variant{}
	for rows.Next() {
		v, err := scanVariant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// StreamState is the latest state of a stream as seen by one agent.
type StreamState struct {
	StreamID  id.Stream                  `json:"stream_id"`
	AgentID   id.Agent                   `json:"agent_id"`
	Status    status.Status              `json:"status"`
	CheckedAt time.Time                  `json:"checked_at"`
	OKAt      *time.Time                 `json:"ok_at"`
	Summary   map[summarykey.Key]float64 `json:"summary"`
}

// VariantState is the latest state of a variant as seen by one agent.
type VariantState struct {
	VariantID id.Variant                  `json:"variant_id"`
	AgentID   id.Agent                    `json:"agent_id"`
	Status    status.Status               `json:"status"`
	CheckedAt time.Time                   `json:"checked_at"`
	Metrics   map[metricname.Name]float64 `json:"metrics"`
	// PlaylistSource and SegmentSource are the servers that last answered.
	PlaylistSource *probe.Endpoint `json:"playlist_source,omitempty"`
	SegmentSource  *probe.Endpoint `json:"segment_source,omitempty"`
}

// name is a value of a closed set that is written by its name.
type name interface {
	comparable
	MarshalText() ([]byte, error)
}

// figuresJSON writes figures by name the way they are stored.
func figuresJSON[K name](m map[K]float64) (string, error) {
	if m == nil {
		m = map[K]float64{}
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// readFigures reads figures stored by name. What was stored by a name
// this version does not have — an older or a newer one wrote it — is left
// out: the figures of a state are replaced by the next check.
func readFigures[K name](text string, parse func(string) (K, error)) map[K]float64 {
	out := map[K]float64{}
	var raw map[string]float64
	if json.Unmarshal([]byte(text), &raw) != nil {
		return out
	}
	for n, v := range raw {
		if k, err := parse(n); err == nil {
			out[k] = v
		}
	}
	return out
}

// kindsText writes kinds of problems the way a column holds them: their
// names with commas between. readKinds reads them back, leaving out a
// name this version does not have.
func kindsText(kinds []problemkind.Kind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.String()
	}
	return strings.Join(names, ",")
}

func readKinds(text string) []problemkind.Kind {
	out := []problemkind.Kind{}
	if text == "" {
		return out
	}
	for _, n := range strings.Split(text, ",") {
		if k, err := problemkind.Parse(n); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// StreamCheckedAt returns the time, in Unix milliseconds, of the newest
// result stored for the stream from the agent; zero when there is none.
func (tx *Tx) StreamCheckedAt(ctx context.Context, streamID id.Stream, agentID id.Agent) (int64, error) {
	var ms int64
	err := tx.q.QueryRowContext(ctx, "SELECT checked_at FROM stream_state WHERE stream_id = ? AND agent_id = ?", streamID, agentID).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return ms, err
}

func (tx *Tx) SaveStreamState(ctx context.Context, streamID id.Stream, agentID id.Agent, st status.Status, now time.Time, summary map[summarykey.Key]float64) error {
	var okAt any
	if st != status.Fail() {
		okAt = toMs(now)
	}
	figures, err := figuresJSON(summary)
	if err != nil {
		return err
	}
	_, err = tx.q.ExecContext(ctx, `
		INSERT INTO stream_state (stream_id, agent_id, status, checked_at, ok_at, summary) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (stream_id, agent_id) DO UPDATE SET
			status = excluded.status, checked_at = excluded.checked_at,
			ok_at = COALESCE(excluded.ok_at, stream_state.ok_at), summary = excluded.summary`,
		streamID, agentID, st, toMs(now), okAt, figures)
	return err
}

// SaveVariantState stores the latest state of a variant. A nil source
// leaves the stored one as it is: not every cycle downloads a segment.
func (tx *Tx) SaveVariantState(ctx context.Context, variantID id.Variant, agentID id.Agent, st status.Status, now time.Time, metrics map[metricname.Name]float64, playlist, segment *probe.Endpoint) error {
	enc := func(e *probe.Endpoint) any {
		if e == nil {
			return nil
		}
		b, _ := json.Marshal(e)
		return string(b)
	}
	figures, err := figuresJSON(metrics)
	if err != nil {
		return err
	}
	_, err = tx.q.ExecContext(ctx, `
		INSERT INTO variant_state (variant_id, agent_id, status, checked_at, metrics, playlist_source, segment_source) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (variant_id, agent_id) DO UPDATE SET
			status = excluded.status, checked_at = excluded.checked_at, metrics = excluded.metrics,
			playlist_source = COALESCE(excluded.playlist_source, variant_state.playlist_source),
			segment_source = COALESCE(excluded.segment_source, variant_state.segment_source)`,
		variantID, agentID, st, toMs(now), figures, enc(playlist), enc(segment))
	return err
}

// StreamStates returns the latest states; streamID 0 means all streams.
func (s *Store) StreamStates(ctx context.Context, streamID id.Stream) ([]*StreamState, error) {
	q := "SELECT stream_id, agent_id, status, checked_at, ok_at, summary FROM stream_state"
	var args []any
	if !streamID.IsZero() {
		q += " WHERE stream_id = ?"
		args = append(args, streamID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY stream_id, agent_id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StreamState{}
	for rows.Next() {
		var st StreamState
		var checked int64
		var okAt sql.NullInt64
		var summary string
		if err := rows.Scan(&st.StreamID, &st.AgentID, &st.Status, &checked, &okAt, &summary); err != nil {
			return nil, err
		}
		st.CheckedAt = fromMs(checked)
		st.OKAt = fromMsPtr(okAt)
		st.Summary = readFigures(summary, summarykey.Parse)
		out = append(out, &st)
	}
	return out, rows.Err()
}

// VariantMetrics returns the latest metrics one agent reported for each
// variant of a stream, by variant ID.
func (tx *Tx) VariantMetrics(ctx context.Context, streamID id.Stream, agentID id.Agent) (map[id.Variant]map[metricname.Name]float64, error) {
	rows, err := tx.q.QueryContext(ctx, `
		SELECT vs.variant_id, vs.metrics FROM variant_state vs JOIN variants v ON v.id = vs.variant_id
		WHERE v.stream_id = ? AND vs.agent_id = ?`, streamID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[id.Variant]map[metricname.Name]float64{}
	for rows.Next() {
		var v id.Variant
		var raw string
		if err := rows.Scan(&v, &raw); err != nil {
			return nil, err
		}
		out[v] = readFigures(raw, metricname.Parse)
	}
	return out, rows.Err()
}

func (s *Store) VariantStates(ctx context.Context, streamID id.Stream) ([]*VariantState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT vs.variant_id, vs.agent_id, vs.status, vs.checked_at, vs.metrics, vs.playlist_source, vs.segment_source
		FROM variant_state vs JOIN variants v ON v.id = vs.variant_id
		WHERE v.stream_id = ? ORDER BY vs.variant_id, vs.agent_id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*VariantState{}
	for rows.Next() {
		var st VariantState
		var checked int64
		var metrics string
		var plSrc, segSrc sql.NullString
		if err := rows.Scan(&st.VariantID, &st.AgentID, &st.Status, &checked, &metrics, &plSrc, &segSrc); err != nil {
			return nil, err
		}
		st.CheckedAt = fromMs(checked)
		st.Metrics = readFigures(metrics, metricname.Parse)
		for _, src := range []struct {
			raw sql.NullString
			dst **probe.Endpoint
		}{{plSrc, &st.PlaylistSource}, {segSrc, &st.SegmentSource}} {
			if src.raw.Valid {
				var e probe.Endpoint
				if json.Unmarshal([]byte(src.raw.String), &e) == nil {
					*src.dst = &e
				}
			}
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}

// SegmentRecord is one row of the segment journal.
type SegmentRecord struct {
	ID         id.Record          `json:"id"`
	StreamID   id.Stream          `json:"stream_id"`
	AgentID    id.Agent           `json:"agent_id"`
	VariantID  id.Variant         `json:"variant_id"`
	Seq        uint64             `json:"seq"`
	Time       time.Time          `json:"time"`
	URL        string             `json:"url"`
	Duration   float64            `json:"duration"`
	Status     int                `json:"status"`
	Error      string             `json:"error"`
	RemoteAddr string             `json:"remote_addr"`
	Size       int64              `json:"size"`
	TotalMs    float64            `json:"total_ms"`
	Problems   []problemkind.Kind `json:"problems"`
	Detail     json.RawMessage    `json:"detail"`
}

// Snapshot is a stored playlist text.
type Snapshot struct {
	ID        id.Snapshot        `json:"id"`
	StreamID  id.Stream          `json:"stream_id"`
	AgentID   id.Agent           `json:"agent_id"`
	VariantID id.Variant         `json:"variant_id"`
	Time      time.Time          `json:"time"`
	URL       string             `json:"url"`
	Problems  []problemkind.Kind `json:"problems"`
	Body      string             `json:"body,omitempty"`
	// PreviousBody is the playlist at the check before and PreviousTime
	// when it was received; absent when the snapshot has none.
	PreviousBody string     `json:"previous_body,omitempty"`
	PreviousTime *time.Time `json:"previous_time,omitempty"`
}

func (tx *Tx) InsertSnapshot(ctx context.Context, streamID id.Stream, agentID id.Agent, variantID id.Variant, now time.Time, url string, problems []problemkind.Kind, body, previous string, previousTime *time.Time) error {
	var prevMs int64
	if previousTime != nil && previous != "" {
		prevMs = toMs(*previousTime)
	}
	_, err := tx.q.ExecContext(ctx,
		"INSERT INTO playlist_snapshots (stream_id, agent_id, variant_id, time, url, problems, body, previous_body, previous_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		streamID, agentID, variantID, toMs(now), url, kindsText(problems), body, previous, prevMs)
	return err
}

// ListSnapshots returns snapshots of a stream without bodies, newest first.
func (s *Store) ListSnapshots(ctx context.Context, streamID id.Stream, limit int) ([]*Snapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, stream_id, agent_id, variant_id, time, url, problems, '', '', previous_time FROM playlist_snapshots WHERE stream_id = ? ORDER BY time DESC, id DESC LIMIT ?",
		streamID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Snapshot{}
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

func (s *Store) GetSnapshot(ctx context.Context, snapshotID id.Snapshot) (*Snapshot, error) {
	return scanSnapshot(s.db.QueryRowContext(ctx,
		"SELECT id, stream_id, agent_id, variant_id, time, url, problems, body, previous_body, previous_time FROM playlist_snapshots WHERE id = ?", snapshotID))
}

func scanSnapshot(row interface{ Scan(...any) error }) (*Snapshot, error) {
	var sn Snapshot
	var t, prev int64
	var problems string
	if err := row.Scan(&sn.ID, &sn.StreamID, &sn.AgentID, &sn.VariantID, &t, &sn.URL, &problems, &sn.Body, &sn.PreviousBody, &prev); err != nil {
		return nil, notFound(err)
	}
	sn.Time = fromMs(t)
	if prev > 0 {
		at := fromMs(prev)
		sn.PreviousTime = &at
	}
	sn.Problems = readKinds(problems)
	return &sn, nil
}

// sourceHistory is how long a server that stopped answering is remembered.
const sourceHistory = 30 * 24 * time.Hour

// SourceSeen is one server that answered for a playlist or its segments.
type SourceSeen struct {
	StreamID  id.Stream       `json:"stream_id"`
	VariantID id.Variant      `json:"variant_id"`
	AgentID   id.Agent        `json:"agent_id"`
	What      sourcekind.Kind `json:"what" enums:"playlist,segment"`
	Host      string          `json:"host"`
	IP        string          `json:"ip"`
	Software  string          `json:"software"`
	FirstSeen time.Time       `json:"first_seen"`
	LastSeen  time.Time       `json:"last_seen"`
	Hits      int64           `json:"hits"`
}

// RecordSource counts one more answer from a server.
func (tx *Tx) RecordSource(ctx context.Context, variantID id.Variant, agentID id.Agent, what sourcekind.Kind, e *probe.Endpoint, now time.Time) error {
	_, err := tx.q.ExecContext(ctx, `
		INSERT INTO variant_sources (variant_id, agent_id, what, host, ip, software, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (variant_id, agent_id, what, host, ip) DO UPDATE SET
			software = excluded.software, last_seen = excluded.last_seen, hits = variant_sources.hits + 1`,
		variantID, agentID, what, e.Host, e.IP, e.Headers["Server"], toMs(now), toMs(now))
	return err
}

// ListSources returns the servers seen since a moment, most recent first:
// for one stream, or for all streams when streamID is zero.
func (s *Store) ListSources(ctx context.Context, streamID id.Stream, since time.Time) ([]*SourceSeen, error) {
	q := `SELECT v.stream_id, vs.variant_id, vs.agent_id, vs.what, vs.host, vs.ip, vs.software, vs.first_seen, vs.last_seen, vs.hits
		FROM variant_sources vs JOIN variants v ON v.id = vs.variant_id
		WHERE vs.last_seen >= ?`
	args := []any{toMs(since)}
	if !streamID.IsZero() {
		q += " AND v.stream_id = ?"
		args = append(args, streamID)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY vs.last_seen DESC, vs.host", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SourceSeen{}
	for rows.Next() {
		var r SourceSeen
		var first, last int64
		if err := rows.Scan(&r.StreamID, &r.VariantID, &r.AgentID, &r.What, &r.Host, &r.IP, &r.Software, &first, &last, &r.Hits); err != nil {
			return nil, err
		}
		r.FirstSeen, r.LastSeen = fromMs(first), fromMs(last)
		out = append(out, &r)
	}
	return out, rows.Err()
}

// Retention is how long each kind of history is kept.
type Retention struct {
	Segments  time.Duration
	Snapshots time.Duration
	// Incidents is for closed warnings and failures, Events for closed
	// informational incidents, which are far more numerous.
	Incidents time.Duration
	Events    time.Duration
}

// Purge deletes journal rows, snapshots, closed incidents and sessions that
// are past their retention. Rows go a batch at a time, so however much has
// piled up, writers are never kept waiting for long.
func (s *Store) Purge(ctx context.Context, keep Retention, now time.Time) (deleted int64, err error) {
	for _, st := range []struct {
		db     *sql.DB
		table  string
		cond   string
		before time.Time
	}{
		{s.journal, "segments", "time < ?", now.Add(-keep.Segments)},
		{s.db, "playlist_snapshots", "time < ?", now.Add(-keep.Snapshots)},
		{s.db, "incidents", "ended_at IS NOT NULL AND severity = 'info' AND ended_at < ?", now.Add(-keep.Events)},
		{s.db, "incidents", "ended_at IS NOT NULL AND ended_at < ?", now.Add(-keep.Incidents)},
		{s.db, "server_incidents", "ended_at IS NOT NULL AND ended_at < ?", now.Add(-keep.Incidents)},
	} {
		n, err := deleteInBatches(ctx, st.db, st.table, st.cond, toMs(st.before))
		deleted += n
		if err != nil {
			return deleted, fmt.Errorf("purge %s: %w", st.table, err)
		}
	}
	// Small tables without a rowid: a plain delete.
	for q, before := range map[string]time.Time{
		"DELETE FROM sessions WHERE expires_at < ?":       now,
		"DELETE FROM variant_sources WHERE last_seen < ?": now.Add(-sourceHistory),
	} {
		res, err := s.db.ExecContext(ctx, q, toMs(before))
		if err != nil {
			return deleted, err
		}
		if n, err := res.RowsAffected(); err == nil {
			deleted += n
		}
	}
	return deleted, nil
}
