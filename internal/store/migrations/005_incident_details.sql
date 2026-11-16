-- +goose Up

-- The figures an incident was concluded from, as a JSON object; empty when
-- the check gave none.
ALTER TABLE incidents ADD COLUMN details TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE incidents DROP COLUMN details;
