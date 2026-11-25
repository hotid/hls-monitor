package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
	"github.com/hotid/hls-monitor/internal/types/variantkey"
	"github.com/hotid/hls-monitor/internal/types/variantkind"
)

// Incident is a problem that lasted for some time on one variant of a
// stream as seen by one agent.
type Incident struct {
	ID          id.Incident       `json:"id"`
	StreamID    id.Stream         `json:"stream_id"`
	StreamName  string            `json:"stream_name"`
	AgentID     id.Agent          `json:"agent_id"`
	AgentName   string            `json:"agent_name"`
	VariantID   id.Variant        `json:"variant_id"`
	VariantKey  variantkey.Key    `json:"variant_key"`
	VariantKind variantkind.Kind  `json:"variant_kind"`
	Kind        problemkind.Kind  `json:"kind"`
	Severity    severity.Severity `json:"severity" enums:"info,warn,crit"`
	Message     string            `json:"message"`
	StartedAt   time.Time         `json:"started_at"`
	LastSeen    time.Time         `json:"last_seen"`
	EndedAt     *time.Time        `json:"ended_at"`
	Count       int64             `json:"count"`
	CleanCycles int               `json:"-"`
	// Episodes is how many times the problem began within this incident:
	// one that comes back soon after it went away is the same incident.
	// IdleSeconds is how long it was away between them.
	Episodes    int     `json:"episodes"`
	IdleSeconds float64 `json:"idle_seconds"`
	// Active tells that the problem was there at the latest check; an open
	// incident that is not active is waiting to see whether it is over.
	Active bool `json:"active"`
	// Details are the figures of the latest occurrence that had any.
	Details detail.Set `json:"details,omitzero" swaggertype:"object"`
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
		var kind, sev past
		if err := rows.Scan(&in.ID, &in.StreamID, &in.StreamName, &in.AgentID, &in.AgentName, &in.VariantID, &in.VariantKey, &in.VariantKind,
			&kind, &sev, &in.Message, &started, &last, &ended, &in.Count, &in.CleanCycles, &details, &in.Episodes, &idle); err != nil {
			return nil, err
		}
		var known bool
		if in.Kind, in.Severity, known = recorded(kind, sev); !known {
			continue
		}
		in.Details = detail.Stored(details)
		in.StartedAt, in.LastSeen = fromMs(started), fromMs(last)
		in.EndedAt = fromMsPtr(ended)
		in.IdleSeconds = float64(idle) / 1000
		in.Active = !ended.Valid && in.CleanCycles == 0
		out = append(out, &in)
	}
	return out, rows.Err()
}

// OpenIncidents returns the open incidents of a stream reported by one agent.
func (tx *Tx) OpenIncidents(ctx context.Context, streamID id.Stream, agentID id.Agent) ([]*Incident, error) {
	rows, err := tx.q.QueryContext(ctx, incidentSelect+" WHERE i.stream_id = ? AND i.agent_id = ? AND i.ended_at IS NULL", streamID, agentID)
	if err != nil {
		return nil, err
	}
	return scanIncidents(rows)
}

// ReportIncident opens an incident or, when one is already open for the
// same stream, agent, variant and kind, records another occurrence.
func (tx *Tx) ReportIncident(ctx context.Context, streamID id.Stream, agentID id.Agent, variantID id.Variant, kind problemkind.Kind, sev severity.Severity, message string, details detail.Set, count int, now time.Time) error {
	detailsJSON := ""
	if details.Len() > 0 {
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
		streamID, agentID, variantID, kind, sev, message, detailsJSON, toMs(now), toMs(now), max(count, 1))
	return err
}

// ReopenIncident opens again the latest incident of the same stream, agent,
// variant and kind that ended at or after the moment given, if there is one
// and none is open: the problem is back, and it is the same incident.
func (tx *Tx) ReopenIncident(ctx context.Context, streamID id.Stream, agentID id.Agent, variantID id.Variant, kind problemkind.Kind, endedFrom, now time.Time) error {
	_, err := tx.q.ExecContext(ctx, `
		UPDATE incidents SET idle_ms = idle_ms + (? - ended_at), episodes = episodes + 1, ended_at = NULL
		WHERE id = (SELECT id FROM incidents WHERE stream_id = ? AND agent_id = ? AND variant_id = ? AND kind = ? AND ended_at >= ?
		            ORDER BY ended_at DESC LIMIT 1)
		  AND NOT EXISTS (SELECT 1 FROM incidents WHERE stream_id = ? AND agent_id = ? AND variant_id = ? AND kind = ? AND ended_at IS NULL)`,
		toMs(now), streamID, agentID, variantID, kind, toMs(endedFrom), streamID, agentID, variantID, kind)
	return err
}

// MarkIncidentClean counts a check cycle in which the problem did not occur.
func (tx *Tx) MarkIncidentClean(ctx context.Context, incidentID id.Incident) error {
	_, err := tx.q.ExecContext(ctx, "UPDATE incidents SET clean_cycles = clean_cycles + 1 WHERE id = ?", incidentID)
	return err
}

// CloseIncident ends an incident.
func (tx *Tx) CloseIncident(ctx context.Context, incidentID id.Incident, now time.Time) error {
	_, err := tx.q.ExecContext(ctx, "UPDATE incidents SET ended_at = ? WHERE id = ? AND ended_at IS NULL", toMs(now), incidentID)
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
func (s *Store) CloseAgentIncidents(ctx context.Context, agentID id.Agent, now time.Time) error {
	return s.tx().closeIncidents(ctx, `agent_id = ? AND stream_id NOT IN (
		SELECT sa.stream_id FROM stream_agents sa
		JOIN streams st ON st.id = sa.stream_id
		JOIN agents ag ON ag.id = sa.agent_id
		WHERE sa.agent_id = ? AND st.enabled = 1 AND ag.enabled = 1)`, now, agentID, agentID)
}

// IncidentFilter selects incidents.
type IncidentFilter struct {
	ID       id.Incident      // only this incident
	Kind     problemkind.Kind // only this kind of problem; any when not set
	StreamID id.Stream
	AgentID  id.Agent
	Open     *bool
	// Problems leaves out informational incidents.
	Problems bool
	// Severities, when set, leaves only incidents of these severities.
	Severities []severity.Severity
	From       time.Time // incidents still open at or after this moment
	To         time.Time // incidents started before this moment
	Limit      int
}

// ListIncidents returns incidents, open ones first, then newest first.
func (s *Store) ListIncidents(ctx context.Context, f IncidentFilter) ([]*Incident, error) {
	var where []string
	var args []any
	if !f.ID.IsZero() {
		where = append(where, "i.id = ?")
		args = append(args, f.ID)
	}
	if !f.Kind.IsZero() {
		where = append(where, "i.kind = ?")
		args = append(args, f.Kind)
	}
	if !f.StreamID.IsZero() {
		where = append(where, "i.stream_id = ?")
		args = append(args, f.StreamID)
	}
	if !f.AgentID.IsZero() {
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
		where = append(where, "i.severity <> ?")
		args = append(args, severity.Info())
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
func (s *Store) OpenIncidentCounts(ctx context.Context) (map[id.Stream]map[severity.Severity]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT stream_id, severity, count(*) FROM incidents WHERE ended_at IS NULL GROUP BY stream_id, severity")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[id.Stream]map[severity.Severity]int{}
	for rows.Next() {
		var of id.Stream
		var sev severity.Severity
		var n int
		if err := rows.Scan(&of, &sev, &n); err != nil {
			return nil, err
		}
		if out[of] == nil {
			out[of] = map[severity.Severity]int{}
		}
		out[of][sev] = n
	}
	return out, rows.Err()
}

// OpenIncidentsByAgent counts the open incidents found from every vantage
// point: failures and warnings, in that order. Events are not counted.
func (s *Store) OpenIncidentsByAgent(ctx context.Context) (map[id.Agent][2]int, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT agent_id, severity, COUNT(*) FROM incidents WHERE ended_at IS NULL AND severity <> ? GROUP BY agent_id, severity", severity.Info())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[id.Agent][2]int{}
	for rows.Next() {
		var of id.Agent
		var sev severity.Severity
		var n int
		if err := rows.Scan(&of, &sev, &n); err != nil {
			return nil, err
		}
		c := out[of]
		if sev == severity.Crit() {
			c[0] += n
		} else {
			c[1] += n
		}
		out[of] = c
	}
	return out, rows.Err()
}

// past is the name of a kind or a severity as a record of an incident
// holds it. The record outlives the version that wrote it, so the name is
// read as it is and only then looked up: see recorded.
type past string

// recorded finds the kind and the severity an incident was recorded with.
// A record of something this version has no name for — a kind of problem
// that is told no more, or one a later version began to tell — is left
// out of what is listed, and said so in the log, rather than failing the
// whole list.
func recorded(kind, sev past) (problemkind.Kind, severity.Severity, bool) {
	k, err := problemkind.Parse(string(kind))
	if err != nil {
		slog.Warn("an incident of a kind this version does not know is left out", "kind", string(kind))
		return problemkind.Kind{}, severity.Severity{}, false
	}
	v, err := severity.Parse(string(sev))
	if err != nil {
		slog.Warn("an incident of a severity this version does not know is left out", "severity", string(sev))
		return problemkind.Kind{}, severity.Severity{}, false
	}
	return k, v, true
}
