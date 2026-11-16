-- +goose Up

-- Which server last answered for the playlist and for its segments, as
-- JSON (probe.Endpoint). NULL until known.
ALTER TABLE variant_state ADD COLUMN playlist_source TEXT;
ALTER TABLE variant_state ADD COLUMN segment_source TEXT;

-- +goose Down
ALTER TABLE variant_state DROP COLUMN segment_source;
ALTER TABLE variant_state DROP COLUMN playlist_source;
