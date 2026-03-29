-- +goose Up
CREATE TABLE session (
    id          TEXT PRIMARY KEY,
    source_name TEXT NOT NULL,
    agent       TEXT NOT NULL,
    dir         TEXT NOT NULL,
    sandbox     TEXT NOT NULL DEFAULT '',
    env         JSONB NOT NULL DEFAULT '[]',
    status      TEXT NOT NULL DEFAULT 'pending',
    error_msg   TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    sdk_version TEXT NOT NULL DEFAULT '',
    pid         INTEGER NOT NULL DEFAULT 0,
    input_tokens               BIGINT NOT NULL DEFAULT 0,
    output_tokens              BIGINT NOT NULL DEFAULT 0,
    cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_input_tokens    BIGINT NOT NULL DEFAULT 0,
    cost_usd                   DOUBLE PRECISION NOT NULL DEFAULT 0,
    prompt_count               BIGINT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE log (
    id         BIGSERIAL PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES session(id),
    event_type TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_log_session_id ON log(session_id);
CREATE INDEX idx_log_event_type ON log(event_type);

-- +goose Down
DROP TABLE IF EXISTS log;
DROP TABLE IF EXISTS session;
