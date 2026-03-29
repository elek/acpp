-- +goose Up
ALTER TABLE project ADD COLUMN hooks TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE project DROP COLUMN IF EXISTS hooks;
