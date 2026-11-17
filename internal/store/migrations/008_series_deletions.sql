-- +goose Up

-- Time series carry the identifiers of the stream and the agent they
-- belong to, and live in another storage. An identifier given out again
-- would hand the history of what was deleted to something new, so the
-- highest one ever retired is remembered and never reused.
CREATE TABLE retired_ids (
    kind TEXT    PRIMARY KEY,
    last INTEGER NOT NULL
);

-- Series still to be deleted from the time series storage: it may be away
-- when a stream or an agent is deleted, and samples may still be on their
-- way to it.
CREATE TABLE series_deletions (
    label      TEXT    NOT NULL,
    id         INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (label, id)
);

-- +goose Down
DROP TABLE series_deletions;
DROP TABLE retired_ids;
