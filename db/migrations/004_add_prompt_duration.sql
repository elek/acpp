-- +goose Up
ALTER TABLE session ADD COLUMN prompt_duration_ms BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE session DROP COLUMN prompt_duration_ms;
