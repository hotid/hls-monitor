-- +goose Up

-- Every server that answered for a playlist or its segments, with how
-- often and when. One row per variant, agent, kind of request and server.
CREATE TABLE variant_sources (
    variant_id INTEGER NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    agent_id   INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    what       TEXT    NOT NULL CHECK (what IN ('playlist', 'segment')),
    host       TEXT    NOT NULL,
    ip         TEXT    NOT NULL,
    software   TEXT    NOT NULL DEFAULT '',
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    hits       INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (variant_id, agent_id, what, host, ip)
) WITHOUT ROWID;
CREATE INDEX variant_sources_last_seen ON variant_sources (last_seen);

-- +goose Down
DROP TABLE variant_sources;
