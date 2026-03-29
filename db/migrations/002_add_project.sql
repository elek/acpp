-- +goose Up
ALTER TABLE session ADD COLUMN project TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session DROP COLUMN project;
