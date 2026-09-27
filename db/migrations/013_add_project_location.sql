-- +goose Up
ALTER TABLE project ADD COLUMN location TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE project DROP COLUMN IF EXISTS location;
