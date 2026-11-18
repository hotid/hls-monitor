package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Slate is a picture put on air in place of a channel, as somebody marked it.
type Slate struct {
	ID        int64        `json:"id"`
	Name      string       `json:"name"`
	CreatedAt time.Time    `json:"created_at"`
	Frames    []SlateFrame `json:"frames"`
}

// SlateFrame is one picture of a slate.
type SlateFrame struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// Cells is the picture shrunk, what looks are compared with.
	Cells []byte `json:"-"`
}

// ErrNoPicture tells that there is no picture to mark where one was pointed at.
var ErrNoPicture = errors.New("there is no picture to mark")

// ListSlates returns the slates with their frames, by name.
func (s *Store) ListSlates(ctx context.Context) ([]*Slate, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, created_at FROM slates ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Slate{}
	byID := map[int64]*Slate{}
	for rows.Next() {
		var sl Slate
		var at int64
		if err := rows.Scan(&sl.ID, &sl.Name, &at); err != nil {
			return nil, err
		}
		sl.CreatedAt, sl.Frames = fromMs(at), []SlateFrame{}
		out = append(out, &sl)
		byID[sl.ID] = &sl
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	frames, err := s.db.QueryContext(ctx, "SELECT id, slate_id, cells, created_at FROM slate_frames ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer frames.Close()
	for frames.Next() {
		var f SlateFrame
		var slate, at int64
		if err := frames.Scan(&f.ID, &slate, &f.Cells, &at); err != nil {
			return nil, err
		}
		f.CreatedAt = fromMs(at)
		if sl := byID[slate]; sl != nil {
			sl.Frames = append(sl.Frames, f)
		}
	}
	return out, frames.Err()
}

// MarkSlate adds a picture to the slate of the given name, making the
// slate when there is none. The picture is the latest look at a stream
// from an agent, or, when incidentID is set, the one kept with an incident.
func (s *Store) MarkSlate(ctx context.Context, name string, streamID, agentID, incidentID int64, now time.Time) (*Slate, error) {
	var cells, jpeg []byte
	var err error
	if incidentID > 0 {
		err = s.db.QueryRowContext(ctx, "SELECT cells, jpeg FROM incident_pictures WHERE incident_id = ?", incidentID).Scan(&cells, &jpeg)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT cells, jpeg FROM stream_pictures WHERE stream_id = ? AND agent_id = ?", streamID, agentID).Scan(&cells, &jpeg)
	}
	if err == sql.ErrNoRows || (err == nil && (len(cells) == 0 || len(jpeg) == 0)) {
		return nil, ErrNoPicture
	}
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.Tx(ctx, func(tx *Tx) error {
		if _, err := tx.q.ExecContext(ctx, "INSERT INTO slates (name, created_at) VALUES (?, ?) ON CONFLICT (name) DO NOTHING", name, toMs(now)); err != nil {
			return err
		}
		if err := tx.q.QueryRowContext(ctx, "SELECT id FROM slates WHERE name = ?", name).Scan(&id); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, "INSERT INTO slate_frames (slate_id, cells, jpeg, created_at) VALUES (?, ?, ?, ?)", id, cells, jpeg, toMs(now))
		return err
	})
	if err != nil {
		return nil, err
	}
	all, err := s.ListSlates(ctx)
	if err != nil {
		return nil, err
	}
	for _, sl := range all {
		if sl.ID == id {
			return sl, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteSlate forgets a slate and its frames.
func (s *Store) DeleteSlate(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, "DELETE FROM slates WHERE id = ?", id))
}

// SlateFrameJPEG returns the picture of a slate's frame.
func (s *Store) SlateFrameJPEG(ctx context.Context, frameID int64) ([]byte, time.Time, error) {
	return s.jpeg(ctx, "SELECT jpeg, created_at FROM slate_frames WHERE id = ?", frameID)
}
