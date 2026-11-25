package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"time"

	"github.com/hotid/hls-monitor/internal/types/id"
	"github.com/hotid/hls-monitor/internal/types/problemkind"
	"github.com/hotid/hls-monitor/internal/types/severity"
)

// ServerIncident is something wrong with a server rather than with one of
// its streams: the same problem on several streams at once, or the way to
// the server losing packets.
type ServerIncident struct {
	ID       id.ServerIncident `json:"id"`
	Host     string            `json:"host"`
	IP       netip.Addr        `json:"ip" swaggertype:"string"`
	Kind     problemkind.Kind  `json:"kind"`
	Severity severity.Severity `json:"severity" enums:"info,warn,crit"`
	Message  string            `json:"message"`
	// Streams is the largest number of streams that had the problem at once.
	Streams   int        `json:"streams"`
	StartedAt time.Time  `json:"started_at"`
	LastSeen  time.Time  `json:"last_seen"`
	EndedAt   *time.Time `json:"ended_at"`
	// Episodes is how many times it began; IdleSeconds how long it was
	// away between them.
	Episodes    int     `json:"episodes"`
	IdleSeconds float64 `json:"idle_seconds"`
	// Details name the streams concerned at the latest look.
	Details ServerIncidentDetails `json:"details,omitzero"`
}

// StreamRef is enough to name a stream and link to it.
type StreamRef struct {
	ID   id.Stream `json:"id"`
	Name string    `json:"name"`
}

// ServerIncidentDetails is what a server's incident tells besides its message.
type ServerIncidentDetails struct {
	// Streams had the problem at the latest look; Example is what one of
	// them said.
	Streams []StreamRef `json:"streams,omitempty"`
	Example string      `json:"example,omitempty"`
	// For loss: the figures of the last five minutes.
	Requests    float64 `json:"requests,omitempty"`
	Retransmits float64 `json:"retransmits,omitempty"`
}

// IsZero tells that there is nothing to tell.
func (d ServerIncidentDetails) IsZero() bool {
	return len(d.Streams) == 0 && d.Example == "" && d.Requests == 0 && d.Retransmits == 0
}

// ReportServerIncident opens an incident of a server, records that an open
// one goes on, or opens again one that ended after reopenFrom.
func (s *Store) ReportServerIncident(ctx context.Context, in ServerIncident, reopenFrom, now time.Time) error {
	details := ""
	if !in.Details.IsZero() {
		b, err := json.Marshal(in.Details)
		if err != nil {
			return err
		}
		details = string(b)
	}
	return s.Tx(ctx, func(tx *Tx) error {
		if _, err := tx.q.ExecContext(ctx, `
			UPDATE server_incidents SET idle_ms = idle_ms + (? - ended_at), episodes = episodes + 1, ended_at = NULL
			WHERE id = (SELECT id FROM server_incidents WHERE host = ? AND ip = ? AND kind = ? AND ended_at >= ? ORDER BY ended_at DESC LIMIT 1)
			  AND NOT EXISTS (SELECT 1 FROM server_incidents WHERE host = ? AND ip = ? AND kind = ? AND ended_at IS NULL)`,
			toMs(now), in.Host, addrText(in.IP), in.Kind, toMs(reopenFrom), in.Host, addrText(in.IP), in.Kind); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, `
			INSERT INTO server_incidents (host, ip, kind, severity, message, details, started_at, last_seen, streams)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (host, ip, kind) WHERE ended_at IS NULL DO UPDATE SET
				severity = CASE WHEN excluded.severity = `+lit(severity.Crit())+` OR server_incidents.severity = `+lit(severity.Crit())+` THEN `+lit(severity.Crit())+` ELSE `+lit(severity.Warn())+` END,
				message = excluded.message,
				details = excluded.details,
				last_seen = excluded.last_seen,
				streams = max(server_incidents.streams, excluded.streams)`,
			in.Host, addrText(in.IP), in.Kind, in.Severity, in.Message, details, toMs(now), toMs(now), in.Streams)
		return err
	})
}

// CloseServerIncidents ends the open incidents not seen since the moment given.
func (s *Store) CloseServerIncidents(ctx context.Context, notSeenSince, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE server_incidents SET ended_at = ? WHERE ended_at IS NULL AND last_seen < ?", toMs(now), toMs(notSeenSince))
	return err
}

// ListServerIncidents returns incidents of servers, open ones first, then
// newest first; only the open ones when asked.
func (s *Store) ListServerIncidents(ctx context.Context, onlyOpen bool, limit int) ([]*ServerIncident, error) {
	q := `SELECT id, host, ip, kind, severity, message, details, started_at, last_seen, ended_at, streams, episodes, idle_ms FROM server_incidents`
	if onlyOpen {
		q += " WHERE ended_at IS NULL"
	}
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY ended_at IS NULL DESC, started_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ServerIncident{}
	for rows.Next() {
		var in ServerIncident
		var started, last, idle int64
		var ended sql.NullInt64
		var details string
		var kind, sev past
		if err := rows.Scan(&in.ID, &in.Host, (*storedAddr)(&in.IP), &kind, &sev, &in.Message, &details, &started, &last, &ended, &in.Streams, &in.Episodes, &idle); err != nil {
			return nil, err
		}
		var known bool
		if in.Kind, in.Severity, known = recorded(kind, sev); !known {
			continue
		}
		if details != "" {
			// What cannot be read is what another version wrote: the
			// incident is told without it.
			_ = json.Unmarshal([]byte(details), &in.Details)
		}
		in.StartedAt, in.LastSeen, in.EndedAt = fromMs(started), fromMs(last), fromMsPtr(ended)
		in.IdleSeconds = float64(idle) / 1000
		out = append(out, &in)
	}
	return out, rows.Err()
}
