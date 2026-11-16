-- +goose Up

-- The segment journal moved to a file of its own (see journal.go), where it
-- is bounded by row count and by size. What it held here is recent history
-- only and is not carried over.
DROP TABLE IF EXISTS segments;

-- +goose Down
CREATE TABLE segments (
    id          INTEGER PRIMARY KEY,
    stream_id   INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id    INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    variant_id  INTEGER NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    seq         INTEGER NOT NULL,
    time        INTEGER NOT NULL,
    url         TEXT    NOT NULL,
    duration    REAL    NOT NULL,
    status      INTEGER NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    remote_addr TEXT    NOT NULL DEFAULT '',
    size        INTEGER NOT NULL DEFAULT 0,
    total_ms    REAL    NOT NULL DEFAULT 0,
    problems    TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX segments_stream_time ON segments (stream_id, time);
CREATE INDEX segments_time ON segments (time);
