-- +goose Up

-- session.id now holds the conversation_id (a stable id assigned before the ACP
-- handshake). The ACP session id — assigned only once the agent subprocess
-- completes session/new — moves to its own column, empty until then (and forever
-- for a stillborn conversation whose process never started).
--
-- Legacy rows keyed session.id by the ACP session id, so backfill acp_session_id
-- from it; those rows' id keeps serving as both the conversation id and the acp
-- id. No primary key or foreign key changes.
ALTER TABLE session ADD COLUMN acp_session_id TEXT NOT NULL DEFAULT '';
UPDATE session SET acp_session_id = id;

-- +goose Down
ALTER TABLE session DROP COLUMN IF EXISTS acp_session_id;
