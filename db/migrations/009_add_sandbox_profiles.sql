-- +goose Up
ALTER TABLE project ADD COLUMN sandbox_profiles TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE project DROP COLUMN IF EXISTS sandbox_profiles;
