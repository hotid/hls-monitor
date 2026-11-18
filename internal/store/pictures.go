package store

import (
	"context"
	"database/sql"
	"time"
)

// StreamPicture is the latest look at a stream from one vantage point.
type StreamPicture struct {
	StreamID  int64     `json:"stream_id"`
	AgentID   int64     `json:"agent_id"`
	AgentName string    `json:"agent_name"`
	Time      time.Time `json:"time"`
	Variant   string    `json:"variant"`
	// HasPicture tells that there was a picture to look at; Luma is its
	// mean brightness, 0 to 255, Dark the share of points all but black.
	HasPicture bool    `json:"has_picture"`
	Luma       float64 `json:"luma"`
	Dark       float64 `json:"dark"`
	// AudioDB is the level of the sound in decibels below full scale.
	HasAudio bool    `json:"has_audio"`
	AudioDB  float64 `json:"audio_db"`
	JPEG     []byte  `json:"-"`
}

// SavePicture keeps the latest look at a stream from an agent.
func (tx *Tx) SavePicture(ctx context.Context, p StreamPicture) error {
	_, err := tx.q.ExecContext(ctx, `
		INSERT INTO stream_pictures (stream_id, agent_id, time, variant, has_picture, luma, dark, has_audio, audio_db, jpeg)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (stream_id, agent_id) DO UPDATE SET time = excluded.time, variant = excluded.variant,
			has_picture = excluded.has_picture, luma = excluded.luma, dark = excluded.dark,
			has_audio = excluded.has_audio, audio_db = excluded.audio_db, jpeg = excluded.jpeg`,
		p.StreamID, p.AgentID, toMs(p.Time), p.Variant, p.HasPicture, p.Luma, p.Dark, p.HasAudio, p.AudioDB, p.JPEG)
	return err
}

// SaveIncidentPictures keeps a picture as the evidence of the open
// incidents of the given kinds of a stream seen from an agent.
func (tx *Tx) SaveIncidentPictures(ctx context.Context, streamID, agentID int64, kinds []string, at time.Time, jpeg []byte) error {
	if len(kinds) == 0 || len(jpeg) == 0 {
		return nil
	}
	args := []any{toMs(at), jpeg, streamID, agentID}
	for _, k := range kinds {
		args = append(args, k)
	}
	_, err := tx.q.ExecContext(ctx, `
		INSERT INTO incident_pictures (incident_id, time, jpeg)
		SELECT id, ?, ? FROM incidents WHERE stream_id = ? AND agent_id = ? AND ended_at IS NULL AND kind IN (`+placeholders(len(kinds))+`)
		ON CONFLICT (incident_id) DO UPDATE SET time = excluded.time, jpeg = excluded.jpeg`, args...)
	return err
}

// StreamPictures returns the latest looks at a stream, without the
// pictures themselves; the local vantage point first.
func (s *Store) StreamPictures(ctx context.Context, streamID int64) ([]StreamPicture, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.stream_id, p.agent_id, a.name, p.time, p.variant, p.has_picture, p.luma, p.dark, p.has_audio, p.audio_db
		FROM stream_pictures p JOIN agents a ON a.id = p.agent_id WHERE p.stream_id = ? ORDER BY p.agent_id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StreamPicture{}
	for rows.Next() {
		var p StreamPicture
		var at int64
		if err := rows.Scan(&p.StreamID, &p.AgentID, &p.AgentName, &at, &p.Variant, &p.HasPicture, &p.Luma, &p.Dark, &p.HasAudio, &p.AudioDB); err != nil {
			return nil, err
		}
		p.Time = fromMs(at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PictureJPEG returns the picture of the latest look at a stream from an
// agent and when it was taken; ErrNotFound when there is none.
func (s *Store) PictureJPEG(ctx context.Context, streamID, agentID int64) ([]byte, time.Time, error) {
	return s.jpeg(ctx, "SELECT jpeg, time FROM stream_pictures WHERE stream_id = ? AND agent_id = ? AND jpeg IS NOT NULL", streamID, agentID)
}

// IncidentPictureJPEG returns the picture kept with an incident.
func (s *Store) IncidentPictureJPEG(ctx context.Context, incidentID int64) ([]byte, time.Time, error) {
	return s.jpeg(ctx, "SELECT jpeg, time FROM incident_pictures WHERE incident_id = ?", incidentID)
}

func (s *Store) jpeg(ctx context.Context, q string, args ...any) ([]byte, time.Time, error) {
	var b []byte
	var at int64
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&b, &at)
	if err == sql.ErrNoRows || (err == nil && len(b) == 0) {
		return nil, time.Time{}, ErrNotFound
	}
	return b, fromMs(at), err
}
