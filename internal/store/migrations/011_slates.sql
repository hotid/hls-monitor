-- +goose Up

-- What a look saw, shrunk: kept so that the picture can be compared with
-- others later, and marked as a slate.
ALTER TABLE stream_pictures ADD COLUMN cells BLOB;
ALTER TABLE incident_pictures ADD COLUMN cells BLOB;

-- Slates: pictures an operator puts on air in place of a channel, "the
-- broadcast is interrupted for technical reasons". Somebody marks one
-- once; any stream showing it afterwards is told at the first look.
CREATE TABLE slates (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    created_at INTEGER NOT NULL
);

-- A slate that moves has several frames; a look matches any of them.
CREATE TABLE slate_frames (
    id         INTEGER PRIMARY KEY,
    slate_id   INTEGER NOT NULL REFERENCES slates (id) ON DELETE CASCADE,
    cells      BLOB    NOT NULL,
    jpeg       BLOB    NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX slate_frames_slate ON slate_frames (slate_id);

-- +goose Down
DROP TABLE slate_frames;
DROP TABLE slates;
ALTER TABLE incident_pictures DROP COLUMN cells;
ALTER TABLE stream_pictures DROP COLUMN cells;
