-- +goose Up
CREATE TABLE project (
    name        TEXT PRIMARY KEY,
    agent       TEXT NOT NULL DEFAULT '',
    dir         TEXT NOT NULL DEFAULT '',
    sandbox     TEXT NOT NULL DEFAULT '',
    permission  TEXT NOT NULL DEFAULT '',
    env         JSONB NOT NULL DEFAULT '[]',
    repo        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS project;
