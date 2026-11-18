-- +goose Up

-- What the picture of a stream is checked for: everything by default,
-- 'static' when a picture that does not change is what the stream shows,
-- 'off' not to look at it at all.
ALTER TABLE streams ADD COLUMN picture TEXT NOT NULL DEFAULT '';

-- The latest look at a stream from every vantage point: a small picture
-- and what was measured on it. One row per stream and agent, replaced.
CREATE TABLE stream_pictures (
    stream_id   INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id    INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    time        INTEGER NOT NULL,
    variant     TEXT    NOT NULL DEFAULT '',
    has_picture INTEGER NOT NULL DEFAULT 0,
    luma        REAL    NOT NULL DEFAULT 0,
    dark        REAL    NOT NULL DEFAULT 0,
    has_audio   INTEGER NOT NULL DEFAULT 0,
    audio_db    REAL    NOT NULL DEFAULT 0,
    jpeg        BLOB,
    PRIMARY KEY (stream_id, agent_id)
) WITHOUT ROWID;

-- The picture an incident was last seen on: its evidence.
CREATE TABLE incident_pictures (
    incident_id INTEGER PRIMARY KEY REFERENCES incidents (id) ON DELETE CASCADE,
    time        INTEGER NOT NULL,
    jpeg        BLOB    NOT NULL
);

-- +goose Down
DROP TABLE incident_pictures;
DROP TABLE stream_pictures;
ALTER TABLE streams DROP COLUMN picture;
