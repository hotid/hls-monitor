-- +goose Up

-- A clip going round is known by the coded frames it is made of: no
-- decoding is needed to tell it when it is played again. Somebody who saw
-- it says what it is — a slate, or (own) something the channel itself
-- plays over and over, which is known in order to be left alone.
ALTER TABLE slates ADD COLUMN own INTEGER NOT NULL DEFAULT 0;

CREATE TABLE slate_keys (
    slate_id INTEGER NOT NULL REFERENCES slates (id) ON DELETE CASCADE,
    hash     INTEGER NOT NULL,
    PRIMARY KEY (slate_id, hash)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE slate_keys;
ALTER TABLE slates DROP COLUMN own;
