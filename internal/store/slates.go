package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/hotid/hls-monitor/internal/types/detail"
	"github.com/hotid/hls-monitor/internal/types/id"
)

// Slate is a picture put on air in place of a channel, as somebody marked it.
type Slate struct {
	ID        id.Slate     `json:"id"`
	Name      string       `json:"name"`
	CreatedAt time.Time    `json:"created_at"`
	Frames    []SlateFrame `json:"frames"`
	// Own tells that it is no slate but a clip a channel itself plays over
	// and over: a stream playing it is left alone.
	Own bool `json:"own"`
	// CodedFrames is how many coded frames of a clip it is known by; Keys
	// are those frames, sorted.
	CodedFrames int      `json:"coded_frames"`
	Keys        []uint64 `json:"-"`
}

// SlateFrame is one picture of a slate.
type SlateFrame struct {
	ID        id.Frame  `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	// Cells is the picture shrunk, what looks are compared with.
	Cells []byte `json:"-"`
}

// ErrNoPicture tells that there is no picture to mark where one was pointed
// at, nor a clip.
var ErrNoPicture = errors.New("there is no picture to mark")

// ListSlates returns the slates with their frames, by name.
func (s *Store) ListSlates(ctx context.Context) ([]*Slate, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, created_at, own FROM slates ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Slate{}
	byID := map[id.Slate]*Slate{}
	for rows.Next() {
		var sl Slate
		var at int64
		if err := rows.Scan(&sl.ID, &sl.Name, &at, &sl.Own); err != nil {
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
		var slate id.Slate
		var at int64
		if err := frames.Scan(&f.ID, &slate, &f.Cells, &at); err != nil {
			return nil, err
		}
		f.CreatedAt = fromMs(at)
		if sl := byID[slate]; sl != nil {
			sl.Frames = append(sl.Frames, f)
		}
	}
	if err := frames.Err(); err != nil {
		return nil, err
	}
	keys, err := s.db.QueryContext(ctx, "SELECT slate_id, hash FROM slate_keys")
	if err != nil {
		return nil, err
	}
	defer keys.Close()
	for keys.Next() {
		var slate id.Slate
		var hash int64
		if err := keys.Scan(&slate, &hash); err != nil {
			return nil, err
		}
		if sl := byID[slate]; sl != nil {
			sl.Keys = append(sl.Keys, uint64(hash))
		}
	}
	for _, sl := range out {
		slices.Sort(sl.Keys)
		sl.CodedFrames = len(sl.Keys)
	}
	return out, keys.Err()
}

// MarkSlate adds what a stream shows to the slate of the given name,
// making the slate when there is none. What is added is the picture of the
// latest look at a stream from an agent or, when incidentID is set, the
// one kept with an incident — and, when the incident is a clip going
// round, the coded frames the clip is known by. With own set it is not a
// slate that is marked but a clip the channel itself plays: that one is
// known by its coded frames alone, for a picture that merely looks like it
// may well be a fault. ErrConflict tells that the name is taken by the
// other kind.
func (s *Store) MarkSlate(ctx context.Context, name string, streamID id.Stream, agentID id.Agent, incidentID id.Incident, own bool, now time.Time) (*Slate, error) {
	var cells, jpeg []byte
	var keys []uint64
	var err error
	if !incidentID.IsZero() {
		var details sql.NullString
		if err = s.db.QueryRowContext(ctx, "SELECT details FROM incidents WHERE id = ?", incidentID).Scan(&details); err != nil {
			return nil, notFound(err)
		}
		keys = detail.Stored(details.String).Hashes(detail.KeyClipSignature())
		err = s.db.QueryRowContext(ctx, "SELECT cells, jpeg FROM incident_pictures WHERE incident_id = ?", incidentID).Scan(&cells, &jpeg)
	} else {
		err = s.db.QueryRowContext(ctx, "SELECT cells, jpeg FROM stream_pictures WHERE stream_id = ? AND agent_id = ?", streamID, agentID).Scan(&cells, &jpeg)
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	picture := err == nil && len(cells) > 0 && len(jpeg) > 0 && !own
	if !picture && len(keys) == 0 {
		return nil, ErrNoPicture
	}
	var marked id.Slate
	err = s.Tx(ctx, func(tx *Tx) error {
		if _, err := tx.q.ExecContext(ctx, "INSERT INTO slates (name, created_at, own) VALUES (?, ?, ?) ON CONFLICT (name) DO NOTHING", name, toMs(now), own); err != nil {
			return err
		}
		var was bool
		if err := tx.q.QueryRowContext(ctx, "SELECT id, own FROM slates WHERE name = ?", name).Scan(&marked, &was); err != nil {
			return err
		}
		if was != own {
			return ErrConflict
		}
		for _, h := range keys {
			if _, err := tx.q.ExecContext(ctx, "INSERT INTO slate_keys (slate_id, hash) VALUES (?, ?) ON CONFLICT DO NOTHING", marked, int64(h)); err != nil {
				return err
			}
		}
		if !picture {
			return nil
		}
		_, err := tx.q.ExecContext(ctx, "INSERT INTO slate_frames (slate_id, cells, jpeg, created_at) VALUES (?, ?, ?, ?)", marked, cells, jpeg, toMs(now))
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
		if sl.ID == marked {
			return sl, nil
		}
	}
	return nil, ErrNotFound
}

// DeleteSlate forgets a slate and its frames.
func (s *Store) DeleteSlate(ctx context.Context, slateID id.Slate) error {
	return affected(s.db.ExecContext(ctx, "DELETE FROM slates WHERE id = ?", slateID))
}

// DeleteSlateFrame forgets one picture of a slate, and the slate with it
// when that was its last and it is not known by a clip either.
func (s *Store) DeleteSlateFrame(ctx context.Context, frameID id.Frame) error {
	return s.Tx(ctx, func(tx *Tx) error {
		var slate id.Slate
		if err := tx.q.QueryRowContext(ctx, "SELECT slate_id FROM slate_frames WHERE id = ?", frameID).Scan(&slate); err != nil {
			return notFound(err)
		}
		if _, err := tx.q.ExecContext(ctx, "DELETE FROM slate_frames WHERE id = ?", frameID); err != nil {
			return err
		}
		_, err := tx.q.ExecContext(ctx, "DELETE FROM slates WHERE id = ? AND NOT EXISTS (SELECT 1 FROM slate_frames WHERE slate_id = ?) AND NOT EXISTS (SELECT 1 FROM slate_keys WHERE slate_id = ?)", slate, slate, slate)
		return err
	})
}

// SlateFrameJPEG returns the picture of a slate's frame.
func (s *Store) SlateFrameJPEG(ctx context.Context, frameID id.Frame) ([]byte, time.Time, error) {
	return s.jpeg(ctx, "SELECT jpeg, created_at FROM slate_frames WHERE id = ?", frameID)
}
