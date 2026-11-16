-- +goose Up

-- All timestamps are Unix milliseconds.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('admin', 'viewer')),
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash BLOB    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX sessions_expires ON sessions (expires_at);

-- Read-only tokens for external monitoring systems.
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    token_hash   BLOB    NOT NULL UNIQUE,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER
);

-- Vantage points. Agent 1 is the server itself.
CREATE TABLE agents (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE,
    kind            TEXT    NOT NULL CHECK (kind IN ('local', 'remote')),
    enabled         INTEGER NOT NULL DEFAULT 1,
    key_hash        BLOB,
    enroll_hash     BLOB,
    enroll_expires  INTEGER,
    created_at      INTEGER NOT NULL,
    last_seen       INTEGER,
    version         TEXT    NOT NULL DEFAULT '',
    remote_addr     TEXT    NOT NULL DEFAULT '',
    clock_offset_ms REAL
);
CREATE UNIQUE INDEX agents_key ON agents (key_hash) WHERE key_hash IS NOT NULL;
CREATE UNIQUE INDEX agents_enroll ON agents (enroll_hash) WHERE enroll_hash IS NOT NULL;

INSERT INTO agents (id, name, kind, created_at) VALUES (1, 'local', 'local', unixepoch() * 1000);

CREATE TABLE streams (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    url          TEXT    NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    level        TEXT    NOT NULL DEFAULT 'segments' CHECK (level IN ('playlist', 'segments', 'container')),
    interval_sec REAL    NOT NULL DEFAULT 0,
    timeout_sec  REAL    NOT NULL DEFAULT 0,
    max_segments INTEGER NOT NULL DEFAULT 0,
    headers      TEXT    NOT NULL DEFAULT '{}',
    insecure     INTEGER NOT NULL DEFAULT 0,
    notes        TEXT    NOT NULL DEFAULT '',
    revision     INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE stream_agents (
    stream_id INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id  INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    PRIMARY KEY (stream_id, agent_id)
) WITHOUT ROWID;
CREATE INDEX stream_agents_agent ON stream_agents (agent_id);

-- Playlists of a stream: the master, its variants and renditions.
CREATE TABLE variants (
    id            INTEGER PRIMARY KEY,
    stream_id     INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    key           TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    uri           TEXT    NOT NULL DEFAULT '',
    name          TEXT    NOT NULL DEFAULT '',
    bandwidth     INTEGER NOT NULL DEFAULT 0,
    avg_bandwidth INTEGER NOT NULL DEFAULT 0,
    resolution    TEXT    NOT NULL DEFAULT '',
    codecs        TEXT    NOT NULL DEFAULT '',
    frame_rate    REAL    NOT NULL DEFAULT 0,
    grp           TEXT    NOT NULL DEFAULT '',
    language      TEXT    NOT NULL DEFAULT '',
    active        INTEGER NOT NULL DEFAULT 1,
    first_seen    INTEGER NOT NULL,
    last_seen     INTEGER NOT NULL,
    UNIQUE (stream_id, key)
);

-- Latest state of a stream as seen by one agent.
CREATE TABLE stream_state (
    stream_id  INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id   INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    status     TEXT    NOT NULL,
    checked_at INTEGER NOT NULL,
    ok_at      INTEGER,
    summary    TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (stream_id, agent_id)
) WITHOUT ROWID;

-- Latest state of one variant as seen by one agent.
CREATE TABLE variant_state (
    variant_id INTEGER NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    agent_id   INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    status     TEXT    NOT NULL,
    checked_at INTEGER NOT NULL,
    metrics    TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (variant_id, agent_id)
) WITHOUT ROWID;

-- Journal of checked segments, kept for a short time.
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

-- Playlist text captured when something was wrong with it.
CREATE TABLE playlist_snapshots (
    id         INTEGER PRIMARY KEY,
    stream_id  INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id   INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    variant_id INTEGER NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    time       INTEGER NOT NULL,
    url        TEXT    NOT NULL,
    problems   TEXT    NOT NULL DEFAULT '',
    body       TEXT    NOT NULL
);
CREATE INDEX playlist_snapshots_stream_time ON playlist_snapshots (stream_id, time);
CREATE INDEX playlist_snapshots_time ON playlist_snapshots (time);

CREATE TABLE incidents (
    id           INTEGER PRIMARY KEY,
    stream_id    INTEGER NOT NULL REFERENCES streams (id) ON DELETE CASCADE,
    agent_id     INTEGER NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    variant_id   INTEGER NOT NULL REFERENCES variants (id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL,
    severity     TEXT    NOT NULL,
    message      TEXT    NOT NULL,
    started_at   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,
    ended_at     INTEGER,
    count        INTEGER NOT NULL DEFAULT 1,
    clean_cycles INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX incidents_open ON incidents (stream_id, agent_id, variant_id, kind) WHERE ended_at IS NULL;
CREATE INDEX incidents_stream_started ON incidents (stream_id, started_at);
CREATE INDEX incidents_started ON incidents (started_at);

-- +goose Down
DROP TABLE incidents;
DROP TABLE playlist_snapshots;
DROP TABLE segments;
DROP TABLE variant_state;
DROP TABLE stream_state;
DROP TABLE variants;
DROP TABLE stream_agents;
DROP TABLE streams;
DROP TABLE agents;
DROP TABLE api_tokens;
DROP TABLE sessions;
DROP TABLE users;
