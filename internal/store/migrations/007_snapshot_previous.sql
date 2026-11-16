-- +goose Up

-- What the playlist said at the check before the snapshot and when that
-- was: most playlist problems are a difference between two checks.
ALTER TABLE playlist_snapshots ADD COLUMN previous_body TEXT NOT NULL DEFAULT '';
ALTER TABLE playlist_snapshots ADD COLUMN previous_time INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE playlist_snapshots DROP COLUMN previous_time;
ALTER TABLE playlist_snapshots DROP COLUMN previous_body;
