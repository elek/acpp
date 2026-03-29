-- +goose Up
ALTER TABLE session ADD COLUMN git_commit TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE session DROP COLUMN git_commit;
