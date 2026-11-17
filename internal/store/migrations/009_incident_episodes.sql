-- +goose Up

-- A problem that comes back a few minutes after it went away is the same
-- incident going on, not a new one: the incident is opened again. episodes
-- counts how many times it began, idle_ms how long it was away in between.
ALTER TABLE incidents ADD COLUMN episodes INTEGER NOT NULL DEFAULT 1;
ALTER TABLE incidents ADD COLUMN idle_ms INTEGER NOT NULL DEFAULT 0;

-- What is wrong with a server rather than with one stream: the same
-- problem on several of its streams at once, or the way to it losing
-- packets. details holds the streams concerned.
CREATE TABLE server_incidents (
    id         INTEGER PRIMARY KEY,
    host       TEXT    NOT NULL,
    ip         TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    severity   TEXT    NOT NULL,
    message    TEXT    NOT NULL,
    details    TEXT    NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    ended_at   INTEGER,
    streams    INTEGER NOT NULL DEFAULT 0,
    episodes   INTEGER NOT NULL DEFAULT 1,
    idle_ms    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX server_incidents_open ON server_incidents (host, ip, kind) WHERE ended_at IS NULL;
CREATE INDEX server_incidents_started ON server_incidents (started_at);

-- +goose Down
DROP TABLE server_incidents;
ALTER TABLE incidents DROP COLUMN idle_ms;
ALTER TABLE incidents DROP COLUMN episodes;
