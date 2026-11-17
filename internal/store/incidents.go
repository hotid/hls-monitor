package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Incident is a problem that lasted for some time on one variant of a
// stream as seen by one agent.
type Incident struct {
	ID          int64      `json:"id"`
	StreamID    int64      `json:"stream_id"`
	StreamName  string     `json:"stream_name"`
	AgentID     int64      `json:"agent_id"`
	AgentName   string     `json:"agent_name"`
	VariantID   int64      `json:"variant_id"`
	VariantKey  string     `json:"variant_key"`
	VariantKind string     `json:"variant_kind"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	Message     string     `json:"message"`
	StartedAt   time.Time  `json:"started_at"`
	LastSeen    time.Time  `json:"last_seen"`
	EndedAt     *time.Time `json:"ended_at"`
	Count       int64      `json:"count"`
	CleanCycles int        `json:"-"`
	// Episodes is how many times the problem began within this incident:
	// one that comes back soon after it went away is the same incident.
	// IdleSeconds is how long it was away between them.
	Episodes    int     `json:"episodes"`
	IdleSeconds float64 `json:"idle_seconds"`
	// Active tells that the problem was there at the latest check; an open
	// incident that is not active is waiting to see whether it is over.
	Active bool `json:"active"`
	// Details are the figures of the latest occurrence that had any.
	Details json.RawMessage `json:"details,omitempty" swaggertype:"object"`
}

const incidentSelect = `
	SELECT i.id, i.stream_id, s.name, i.agent_id, a.name, i.variant_id, v.key, v.kind,
	       i.kind, i.severity, i.message, i.started_at, i.last_seen, i.ended_at, i.count, i.clean_cycles, i.details, i.episodes, i.idle_ms
	FROM incidents i
	JOIN streams s ON s.id = i.stream_id
	JOIN agents a ON a.id = i.agent_id
	JOIN variants v ON v.id = i.variant_id`

func scanIncidents(rows *sql.Rows) ([]*Incident, error) {
	defer rows.Close()
	out := []*Incident{}
	for rows.Next() {
		var in Incident
		var started, last int64
		var ended sql.NullInt64
		var details string
		var idle int64
		if err := rows.Scan(&in.ID, &in.StreamID, &in.StreamName, &in.AgentID, &in.AgentName, &in.VariantID, &in.VariantKey, &in.VariantKind,
			&in.Kind, &in.Severity, &in.Message, &started, &last, &ended, &in.Count, &in.CleanCycles, &details, &in.Episodes, &idle); err != nil {
			return nil, err
		}
		if details != "" {
			in.Details = json.RawMessage(details)
		}
		in.StartedAt, in.LastSeen = fromMs(started), fromMs(last)
		in.EndedAt = fromMsPtr(ended)
		in.IdleSeconds = float64(idle) / 1000
		in.Active = !ended.Valid && in.CleanCycles == 0
		out = append(out, &in)
	}
	return out, rows.Err()
}

// OpenIncidents returns the open incidents of a stream reported by one agent.
func (tx *Tx) OpenIncidents(ctx context.Context, streamID, agentID int64) ([]*Incident, error) {
	rows, err := tx.q.QueryContext(ctx, incidentSelect+" WHERE i.stream_id = ? AND i.agent_id = ? AND i.ended_at IS NULL", streamID, agentID)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// ReportIncident opens an incident or, when one is already open for the
// same stream, agent, variant and kind, records another occurrence.
func (tx *Tx) ReportIncident(ctx context.Context, streamID, agentID, variantID int64, kind, severity, message string, details map[string]any, count int, now time.Time) error {
	detailsJSON := ""
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return err
		}
		detailsJSON = string(b)
	}
	_, err := tx.q.ExecContext(ctx, `
		INSERT INTO incidents (stream_id, agent_id, variant_id, kind, severity, message, details, started_at, last_seen, count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (stream_id, agent_id, variant_id, kind) WHERE ended_at IS NULL DO UPDATE SET
			severity = CASE
				WHEN excluded.severity = 'crit' OR incidents.severity = 'crit' THEN 'crit'
				WHEN excluded.severity = 'warn' OR incidents.severity = 'warn' THEN 'warn'
				ELSE 'info' END,
			message = excluded.message,
			details = CASE WHEN excluded.details <> '' THEN excluded.details ELSE incidents.details END,
			last_seen = excluded.last_seen,
			count = incidents.count + excluded.count,
			clean_cycles = 0`,
		streamID, agentID, variantID, kind, severity, message, detailsJSON, toMs(now), toMs(now), max(count, 1))
	return err
}

// ReopenIncident opens again the latest incident of the same stream, agent,
// variant and kind that ended at or after the moment given, if there is one
// and none is open: the problem is back, and it is the same incident.
func (tx *Tx) ReopenIncident(ctx context.Context, streamID, agentID, variantID int64, kind string, endedFrom, now time.Time) error {
	_, err := tx.q.ExecContext(ctx, `
		UPDATE incidents SET idle_ms = idle_ms + (? - ended_at), episodes = episodes + 1, ended_at = NULL
		WHERE id = (SELECT id FROM incidents WHERE stream_id = ? AND agent_id = ? AND variant_id = ? AND kind = ? AND ended_at >= ?
		            ORDER BY ended_at DESC LIMIT 1)
		  AND NOT EXISTS (SELECT 1 FROM incidents WHERE stream_id = ? AND agent_id = ? AND variant_id = ? AND kind = ? AND ended_at IS NULL)`,
		toMs(now), streamID, agentID, variantID, kind, toMs(endedFrom), streamID, agentID, variantID, kind)
	return err
}

// MarkIncidentClean counts a check cycle in which the problem did not occur.
func (tx *Tx) MarkIncidentClean(ctx context.Context, id int64) error {
	_, err := tx.q.ExecContext(ctx, "UPDATE incidents SET clean_cycles = clean_cycles + 1 WHERE id = ?", id)
	return err
}

// CloseIncident ends an incident.
func (tx *Tx) CloseIncident(ctx context.Context, id int64, now time.Time) error {
	_, err := tx.q.ExecContext(ctx, "UPDATE incidents SET ended_at = ? WHERE id = ? AND ended_at IS NULL", toMs(now), id)
	return err
}

// closeIncidents ends every open incident matching the condition, which
// refers to columns of the incidents table.
func (tx *Tx) closeIncidents(ctx context.Context, cond string, now time.Time, args ...any) error {
	_, err := tx.q.ExecContext(ctx, "UPDATE incidents SET ended_at = ? WHERE ended_at IS NULL AND "+cond, append([]any{toMs(now)}, args...)...)
	return err
}

// CloseAgentIncidents ends the open incidents an agent reported for streams
// it is no longer supposed to check.
func (s *Store) CloseAgentIncidents(ctx context.Context, agentID int64, now time.Time) error {
	return s.tx().closeIncidents(ctx, `agent_id = ? AND stream_id NOT IN (
		SELECT sa.stream_id FROM stream_agents sa
		JOIN streams st ON st.id = sa.stream_id
		JOIN agents ag ON ag.id = sa.agent_id
		WHERE sa.agent_id = ? AND st.enabled = 1 AND ag.enabled = 1)`, now, agentID, agentID)
}

// IncidentFilter selects incidents.
type IncidentFilter struct {
	ID       int64  // only this incident
	Kind     string // only this kind of problem
	StreamID int64
	AgentID  int64
	Open     *bool
	// Problems leaves out informational incidents.
	Problems bool
	// Severities, when set, leaves only incidents of these severities.
	Severities []string
	From       time.Time // incidents still open at or after this moment
	To         time.Time // incidents started before this moment
	Limit      int
}

// ListIncidents returns incidents, open ones first, then newest first.
func (s *Store) ListIncidents(ctx context.Context, f IncidentFilter) ([]*Incident, error) {
	var where []string
	var args []any
	if f.ID != 0 {
		where = append(where, "i.id = ?")
		args = append(args, f.ID)
	}
	if f.Kind != "" {
		where = append(where, "i.kind = ?")
		args = append(args, f.Kind)
	}
	if f.StreamID != 0 {
		where = append(where, "i.stream_id = ?")
		args = append(args, f.StreamID)
	}
	if f.AgentID != 0 {
		where = append(where, "i.agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.Open != nil {
		if *f.Open {
			where = append(where, "i.ended_at IS NULL")
		} else {
			where = append(where, "i.ended_at IS NOT NULL")
		}
	}
	if f.Problems {
		where = append(where, "i.severity <> 'info'")
	}
	if len(f.Severities) > 0 {
		where = append(where, "i.severity IN ("+placeholders(len(f.Severities))+")")
		for _, sev := range f.Severities {
			args = append(args, sev)
		}
	}
	if !f.From.IsZero() {
		where = append(where, "(i.ended_at IS NULL OR i.ended_at >= ?)")
		args = append(args, toMs(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, "i.started_at < ?")
		args = append(args, toMs(f.To))
	}
	q := incidentSelect
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	if f.Limit <= 0 || f.Limit > 2000 {
		f.Limit = 200
	}
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY i.ended_at IS NULL DESC, i.started_at DESC, i.id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// OpenIncidentCounts returns, per stream, the number of open incidents by severity.
func (s *Store) OpenIncidentCounts(ctx context.Context) (map[int64]map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT stream_id, severity, count(*) FROM incidents WHERE ended_at IS NULL GROUP BY stream_id, severity")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]int{}
	for rows.Next() {
		var id int64
		var sev string
		var n int
		if err := rows.Scan(&id, &sev, &n); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]int{}
		}
		out[id][sev] = n
	}
	return out, rows.Err()
}
