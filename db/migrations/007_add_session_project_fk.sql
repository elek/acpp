-- +goose Up

-- Ensure every distinct base directory name has a project row.
-- This inserts projects for any session dirs that don't already have a matching project.
INSERT INTO project (name)
SELECT DISTINCT
    CASE
        WHEN dir = '' THEN 'default'
        ELSE regexp_replace(dir, '.*/([^/]+)/?$', '\1')
    END
FROM session
ON CONFLICT (name) DO NOTHING;

-- Add the column as nullable first so we can backfill.
ALTER TABLE session ADD COLUMN project_name TEXT;

-- Backfill from the base name of the dir column.
UPDATE session SET project_name = CASE
    WHEN dir = '' THEN 'default'
    ELSE regexp_replace(dir, '.*/([^/]+)/?$', '\1')
END;

-- Now make it NOT NULL and add the foreign key.
ALTER TABLE session ALTER COLUMN project_name SET NOT NULL;
ALTER TABLE session ADD CONSTRAINT fk_session_project
    FOREIGN KEY (project_name) REFERENCES project(name);

-- +goose Down
ALTER TABLE session DROP CONSTRAINT IF EXISTS fk_session_project;
ALTER TABLE session DROP COLUMN IF EXISTS project_name;
