package db

import (
	"context"

	"github.com/pkg/errors"
)

// ProjectDirRow holds a distinct project directory and its running status.
type ProjectDirRow struct {
	Dir        string
	HasRunning bool
}

// ListProjectDirs returns the distinct directory values from sessions, with active projects first, then alphabetically.
// Each entry also includes whether any session in that dir is currently running.
func (s *PostgresStore) ListProjectDirs(ctx context.Context) ([]ProjectDirRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			dir,
			bool_or(status IN ('running', 'pending')) AS has_running
		FROM session
		WHERE dir != ''
		GROUP BY dir
		ORDER BY MAX(GREATEST(created_at, finished_at)) DESC NULLS LAST, dir`)
	if err != nil {
		return nil, errors.Wrap(err, "querying project dirs")
	}
	defer rows.Close()

	var result []ProjectDirRow
	for rows.Next() {
		var r ProjectDirRow
		if err := rows.Scan(&r.Dir, &r.HasRunning); err != nil {
			return nil, errors.Wrap(err, "scanning project dir row")
		}
		result = append(result, r)
	}
	return result, nil
}

// ListSessionsByDir returns sessions for a given directory, ordered by creation time descending.
func (s *PostgresStore) ListSessionsByDir(ctx context.Context, dir string) ([]SessionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, error_msg, model, sdk_version, pid,
			input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			cost_usd, prompt_count, prompt_duration_ms, created_at, finished_at
		FROM session
		WHERE dir = $1
		ORDER BY created_at DESC`, dir)
	if err != nil {
		return nil, errors.Wrap(err, "querying sessions by dir")
	}
	defer rows.Close()

	var result []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(
			&r.ID, &r.SourceName, &r.Agent, &r.Dir, &r.Sandbox, &r.Node, &r.GitCommit, &r.ProjectName, &r.Env,
			&r.Status, &r.ErrorMsg, &r.Model, &r.SDKVersion, &r.PID,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.CostUSD, &r.PromptCount, &r.PromptDurationMs, &r.CreatedAt, &r.FinishedAt,
		); err != nil {
			return nil, errors.Wrap(err, "scanning session row")
		}
		result = append(result, r)
	}
	return result, nil
}

// ListSessionsByProject returns sessions for a given project name, ordered by creation time descending.
func (s *PostgresStore) ListSessionsByProject(ctx context.Context, projectName string) ([]SessionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, error_msg, model, sdk_version, pid,
			input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			cost_usd, prompt_count, prompt_duration_ms, created_at, finished_at
		FROM session
		WHERE project_name = $1
		ORDER BY created_at DESC`, projectName)
	if err != nil {
		return nil, errors.Wrap(err, "querying sessions by project")
	}
	defer rows.Close()

	var result []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(
			&r.ID, &r.SourceName, &r.Agent, &r.Dir, &r.Sandbox, &r.Node, &r.GitCommit, &r.ProjectName, &r.Env,
			&r.Status, &r.ErrorMsg, &r.Model, &r.SDKVersion, &r.PID,
			&r.InputTokens, &r.OutputTokens, &r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.CostUSD, &r.PromptCount, &r.PromptDurationMs, &r.CreatedAt, &r.FinishedAt,
		); err != nil {
			return nil, errors.Wrap(err, "scanning session row")
		}
		result = append(result, r)
	}
	return result, nil
}
