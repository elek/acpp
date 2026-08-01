-- +goose Up
ALTER TABLE session ADD COLUMN context_used BIGINT NOT NULL DEFAULT 0;
ALTER TABLE session ADD COLUMN context_window BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE session DROP COLUMN IF EXISTS context_used;
ALTER TABLE session DROP COLUMN IF EXISTS context_window;
