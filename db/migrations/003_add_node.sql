-- +goose Up
ALTER TABLE session ADD COLUMN node TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session DROP COLUMN node;
