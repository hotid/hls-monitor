package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/probe"
)

// Stream is a monitored HLS stream.
type Stream struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Enabled     bool              `json:"enabled"`
	Level       string            `json:"level"`
	IntervalSec float64           `json:"interval_sec"`
	TimeoutSec  float64           `json:"timeout_sec"`
	MaxSegments int               `json:"max_segments"`
	Headers     map[string]string `json:"headers"`
	Insecure    bool              `json:"insecure"`
	Notes       string            `json:"notes"`
	Revision    int64             `json:"revision"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	AgentIDs    []int64           `json:"agent_ids"`
}

// StreamInput is what can be set when creating or editing a stream.
type StreamInput struct {
	Name        string
	URL         string
	Enabled     bool
	Level       string
	IntervalSec float64
	TimeoutSec  float64
	MaxSegments int
	Headers     map[string]string
	Insecure    bool
	Notes       string
	AgentIDs    []int64
}

const streamCols = "id, name, url, enabled, level, interval_sec, timeout_sec, max_segments, headers, insecure, notes, revision, created_at, updated_at"

func scanStream(row interface{ Scan(...any) error }) (*Stream, error) {
	var st Stream
	var headers string
	var created, updated int64
	if err := row.Scan(&st.ID, &st.Name, &st.URL, &st.Enabled, &st.Level, &st.IntervalSec, &st.TimeoutSec, &st.MaxSegments,
		&headers, &st.Insecure, &st.Notes, &st.Revision, &created, &updated); err != nil {
		return nil, notFound(err)
	}
	st.Headers = map[string]string{}
	_ = json.Unmarshal([]byte(headers), &st.Headers)
	st.CreatedAt, st.UpdatedAt = fromMs(created), fromMs(updated)
	st.AgentIDs = []int64{}
	return &st, nil
}

func headersJSON(h map[string]string) string {
	if h == nil {
		h = map[string]string{}
	}
	b, _ := json.Marshal(h)
	return string(b)
}

func (tx *Tx) setStreamAgents(ctx context.Context, streamID int64, agentIDs []int64) error {
	if _, err := tx.q.ExecContext(ctx, "DELETE FROM stream_agents WHERE stream_id = ?", streamID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range agentIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.q.ExecContext(ctx, "INSERT INTO stream_agents (stream_id, agent_id) VALUES (?, ?)", streamID, id); err != nil {
			return err
		}
	}
	// What unassigned agents reported is no longer current.
	args := []any{streamID}
	cond := ""
	if len(agentIDs) > 0 {
		cond = " AND agent_id NOT IN (" + placeholders(len(agentIDs)) + ")"
		for _, id := range agentIDs {
			args = append(args, id)
		}
	}
	if _, err := tx.q.ExecContext(ctx, "DELETE FROM stream_state WHERE stream_id = ?"+cond, args...); err != nil {
		return err
	}
	_, err := tx.q.ExecContext(ctx,
		"DELETE FROM variant_state WHERE variant_id IN (SELECT id FROM variants WHERE stream_id = ?)"+cond, args...)
	return err
}

// CreateStream adds a stream. It returns ErrConflict when the name is taken
// and ErrNotFound when an agent does not exist.
func (s *Store) CreateStream(ctx context.Context, in StreamInput, now time.Time) (*Stream, error) {
	var id int64
	err := s.Tx(ctx, func(tx *Tx) error {
		res, err := tx.q.ExecContext(ctx, `
			INSERT INTO streams (id, name, url, enabled, level, interval_sec, timeout_sec, max_segments, headers, insecure, notes, created_at, updated_at)
			VALUES (`+nextID("streams", "stream")+`, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.Name, in.URL, in.Enabled, in.Level, in.IntervalSec, in.TimeoutSec, in.MaxSegments,
			headersJSON(in.Headers), in.Insecure, in.Notes, toMs(now), toMs(now))
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		return tx.setStreamAgents(ctx, id, in.AgentIDs)
	})
	if err != nil {
		return nil, mapStreamErr(err)
	}
	return s.GetStream(ctx, id)
}

// UpdateStream replaces the settings of a stream and bumps its revision.
func (s *Store) UpdateStream(ctx context.Context, id int64, in StreamInput, now time.Time) (*Stream, error) {
	err := s.Tx(ctx, func(tx *Tx) error {
		err := affected(tx.q.ExecContext(ctx, `
			UPDATE streams SET name = ?, url = ?, enabled = ?, level = ?, interval_sec = ?, timeout_sec = ?, max_segments = ?,
				headers = ?, insecure = ?, notes = ?, revision = revision + 1, updated_at = ?
			WHERE id = ?`,
			in.Name, in.URL, in.Enabled, in.Level, in.IntervalSec, in.TimeoutSec, in.MaxSegments,
			headersJSON(in.Headers), in.Insecure, in.Notes, toMs(now), id))
		if err != nil {
			return err
		}
		if err := tx.setStreamAgents(ctx, id, in.AgentIDs); err != nil {
			return err
		}
		// Incidents reported by agents that no longer check the stream are over.
		return tx.closeIncidents(ctx, "stream_id = ? AND agent_id NOT IN (SELECT agent_id FROM stream_agents WHERE stream_id = ?)", now, id, id)
	})
	if err != nil {
		return nil, mapStreamErr(err)
	}
	return s.GetStream(ctx, id)
}

func mapStreamErr(err error) error {
	switch {
	case isUnique(err):
		return ErrConflict
	case err != nil && containsFK(err):
		return ErrNotFound
	}
	return err
}

func containsFK(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// SetStreamEnabled switches monitoring of a stream on or off.
func (s *Store) SetStreamEnabled(ctx context.Context, id int64, enabled bool, now time.Time) error {
	return s.Tx(ctx, func(tx *Tx) error {
		err := affected(tx.q.ExecContext(ctx,
			"UPDATE streams SET enabled = ?, revision = revision + 1, updated_at = ? WHERE id = ?", enabled, toMs(now), id))
		if err != nil || enabled {
			return err
		}
		return tx.closeIncidents(ctx, "stream_id = ?", now, id)
	})
}

// DeleteStream removes a stream with all its history.
func (s *Store) DeleteStream(ctx context.Context, id int64) error {
	err := s.Tx(ctx, func(tx *Tx) error {
		if err := affected(tx.q.ExecContext(ctx, "DELETE FROM streams WHERE id = ?", id)); err != nil {
			return err
		}
		return tx.retire(ctx, "stream", id)
	})
	if err != nil {
		return err
	}
	return s.dropJournal(ctx, "stream_id", id)
}

// nextID is the SQL of the identifier for a new row: above every one in
// use and every one retired.
func nextID(table, kind string) string {
	return `(SELECT MAX(COALESCE((SELECT MAX(id) FROM ` + table + `), 0), COALESCE((SELECT last FROM retired_ids WHERE kind = '` + kind + `'), 0)) + 1)`
}

// retire records that an identifier is never to be given out again and
// that its time series are to be deleted.
func (tx *Tx) retire(ctx context.Context, kind string, id int64) error {
	if _, err := tx.q.ExecContext(ctx, `
		INSERT INTO retired_ids (kind, last) VALUES (?, ?)
		ON CONFLICT (kind) DO UPDATE SET last = MAX(last, excluded.last)`, kind, id); err != nil {
		return err
	}
	_, err := tx.q.ExecContext(ctx, `
		INSERT INTO series_deletions (label, id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (label, id) DO NOTHING`, kind, id, toMs(time.Now()))
	return err
}

// SeriesDeletion is a set of time series waiting to be deleted: every
// series whose label has this value.
type SeriesDeletion struct {
	Label     string
	ID        int64
	CreatedAt time.Time
}

// SeriesDeletions lists what is still to be deleted from the time series
// storage.
func (s *Store) SeriesDeletions(ctx context.Context) ([]SeriesDeletion, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT label, id, created_at FROM series_deletions ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesDeletion
	for rows.Next() {
		var d SeriesDeletion
		var at int64
		if err := rows.Scan(&d.Label, &d.ID, &at); err != nil {
			return nil, err
		}
		d.CreatedAt = fromMs(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// SeriesDeleted forgets a deletion that has been carried out.
func (s *Store) SeriesDeleted(ctx context.Context, label string, id int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM series_deletions WHERE label = ? AND id = ?", label, id)
	return err
}

func (s *Store) GetStream(ctx context.Context, id int64) (*Stream, error) {
	st, err := scanStream(s.db.QueryRowContext(ctx, "SELECT "+streamCols+" FROM streams WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT agent_id FROM stream_agents WHERE stream_id = ? ORDER BY agent_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a int64
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		st.AgentIDs = append(st.AgentIDs, a)
	}
	return st, rows.Err()
}

func (s *Store) ListStreams(ctx context.Context) ([]*Stream, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+streamCols+" FROM streams ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Stream{}
	byID := map[int64]*Stream{}
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
		byID[st.ID] = st
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	links, err := s.db.QueryContext(ctx, "SELECT stream_id, agent_id FROM stream_agents ORDER BY agent_id")
	if err != nil {
		return nil, err
	}
	defer links.Close()
	for links.Next() {
		var sid, aid int64
		if err := links.Scan(&sid, &aid); err != nil {
			return nil, err
		}
		if st := byID[sid]; st != nil {
			st.AgentIDs = append(st.AgentIDs, aid)
		}
	}
	return out, links.Err()
}

// TargetsForAgent lists what an agent has to check: enabled streams
// assigned to it, provided the agent itself is enabled.
func (s *Store) TargetsForAgent(ctx context.Context, agentID int64) ([]probe.Target, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+streamCols+` FROM streams
		WHERE enabled = 1
		  AND id IN (SELECT stream_id FROM stream_agents WHERE agent_id = ?)
		  AND EXISTS (SELECT 1 FROM agents WHERE id = ? AND enabled = 1)`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []probe.Target{}
	for rows.Next() {
		st, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, probe.Target{
			ID:          st.ID,
			URL:         st.URL,
			Level:       probe.Level(st.Level),
			IntervalSec: st.IntervalSec,
			TimeoutSec:  st.TimeoutSec,
			MaxSegments: st.MaxSegments,
			Headers:     st.Headers,
			Insecure:    st.Insecure,
			Revision:    st.Revision,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, rows.Err()
}

// StreamAssigned reports whether an enabled stream is assigned to the agent.
func (s *Store) StreamAssigned(ctx context.Context, streamID, agentID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FROM stream_agents sa JOIN streams st ON st.id = sa.stream_id
		WHERE sa.stream_id = ? AND sa.agent_id = ? AND st.enabled = 1`, streamID, agentID).Scan(&n)
	return n > 0, err
}
