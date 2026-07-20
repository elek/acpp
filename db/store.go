package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/elek/acpp/acp"
	acplib "github.com/elek/acpp/types"
	"github.com/pkg/errors"
)

// ProjectRow holds a project record from the database.
type ProjectRow struct {
	Name            string
	Agent           string
	Dir             string
	Sandbox         string
	SandboxProfiles string
	Permission      string
	Env             []string
	Repo            string
	Hooks           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ProjectListRow is a summary row returned by ListProjects.
type ProjectListRow struct {
	Name       string
	Dir        string
	Agent      string
	HasRunning bool
	// LastUsed is the most recent activity across the project's sessions
	// (the latest of any session's created_at/finished_at). Zero when the
	// project has no sessions.
	LastUsed time.Time
}

// ProjectStore is the interface for reading and writing project data.
type ProjectStore interface {
	GetProject(ctx context.Context, name string) (ProjectRow, error)
	SetProjectField(ctx context.Context, name, field, value string) error
	AppendProjectEnv(ctx context.Context, name, entry string) error
	ClearProjectEnv(ctx context.Context, name string) error
	ListProjects(ctx context.Context) ([]ProjectListRow, error)
}

// SessionWriter is the interface for writing session and log data.
type SessionWriter interface {
	CompleteRunningSessions(ctx context.Context) (int64, error)
	InsertSession(ctx context.Context, id, sourceName, agent, dir, sandbox, node, gitCommit, projectName string, env []string, createdAt time.Time) error
	UpdateSession(ctx context.Context, id string, info acplib.StatusInfo) error
	FinishSession(ctx context.Context, id string, info acplib.StatusInfo, sessionError string) error
	AddPromptDuration(ctx context.Context, id string, durationMs int64) error
	InsertLog(ctx context.Context, sessionID, eventType string, payload json.RawMessage) error
}

// SessionReader is the interface for reading session and log data.
type SessionReader interface {
	ListSessions(ctx context.Context) ([]SessionRow, error)
	GetSession(ctx context.Context, id string) (SessionRow, error)
	GetSessionLogs(ctx context.Context, sessionID string) ([]LogRow, error)
	GetLog(ctx context.Context, id int64) (LogRow, error)
	GetLogsByToolCallID(ctx context.Context, sessionID, toolCallID string) ([]LogRow, error)
	GetSessionsByCommit(ctx context.Context, commit string) ([]SessionRow, error)
	GetPromptTexts(ctx context.Context, sessionID string) ([]string, error)
	GetDailyStats(ctx context.Context, since time.Time, agent string) ([]DailyStatsRow, error)
	GetMonthlyStatsByDir(ctx context.Context, months int, agent string) ([]MonthlyDirStatsRow, error)
	GetModelStats(ctx context.Context, since time.Time) ([]ModelStatsRow, error)
	GetInsights(ctx context.Context, since time.Time) (*InsightsData, error)
	GetDistinctAgents(ctx context.Context) ([]string, error)
	ListProjectDirs(ctx context.Context) ([]ProjectDirRow, error)
	ListSessionsByDir(ctx context.Context, dir string) ([]SessionRow, error)
	ListSessionsByProject(ctx context.Context, projectName string) ([]SessionRow, error)
}

// CompleteRunningSessions marks all sessions with status 'running' or 'pending' as 'complete'.
// This should be called at startup to clean up sessions left in a running state
// from a previous process that was killed.
func (s *PostgresStore) CompleteRunningSessions(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE session SET
			status = 'complete',
			finished_at = now()
		WHERE status IN ('running', 'pending')`)
	if err != nil {
		return 0, errors.Wrap(err, "completing running sessions")
	}
	return tag.RowsAffected(), nil
}

// InsertSession creates a new session row with initial parameters.
// The agent value is stored as its base name (without directory path).
func (s *PostgresStore) InsertSession(ctx context.Context, id, sourceName, agent, dir, sandbox, node, gitCommit, projectName string, env []string, createdAt time.Time) error {
	envJSON, err := json.Marshal(env)
	if err != nil {
		return errors.Wrap(err, "marshaling env")
	}

	// A session can't exist without its project: ensure the project row exists so
	// the project_name foreign key is satisfied. Ad-hoc sessions (run/cat/web with
	// ProjectID set to a cwd) otherwise have no project row created for them.
	//
	// Record the session's dir on the project too: the web UI resolves a project's
	// working directory from project.dir (to start further sessions from the
	// /projects view), so a project first created here must not be left dir-less.
	// Backfill an empty dir but never overwrite one already set.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO project (name, dir) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE
		SET dir = COALESCE(NULLIF(project.dir, ''), EXCLUDED.dir)`, projectName, dir); err != nil {
		return errors.Wrap(err, "ensuring project")
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO session (id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		id, sourceName, filepath.Base(agent), dir, sandbox, node, gitCommit, projectName, envJSON, string(acplib.StatusPending), createdAt,
	)
	return errors.Wrap(err, "inserting session")
}

// UpdateSession updates the session's status, model, and usage statistics.
func (s *PostgresStore) UpdateSession(ctx context.Context, id string, info acplib.StatusInfo) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE session SET
			status = $2,
			model = $3,
			sdk_version = $4,
			pid = $5,
			input_tokens = $6,
			output_tokens = $7,
			cache_creation_input_tokens = $8,
			cache_read_input_tokens = $9,
			cost_usd = $10,
			prompt_count = $11
		WHERE id = $1`,
		id,
		string(info.Status),
		info.Model,
		info.SDKVersion,
		info.PID,
		info.Usage.InputTokens,
		info.Usage.OutputTokens,
		info.Usage.CacheCreationInputTokens,
		info.Usage.CacheReadInputTokens,
		info.Usage.CostUSD,
		info.Usage.PromptCount,
	)
	return errors.Wrap(err, "updating session")
}

// FinishSession updates the session and sets the finished_at timestamp.
func (s *PostgresStore) FinishSession(ctx context.Context, id string, info acplib.StatusInfo, sessionError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE session SET
			status = $2,
			error_msg = $3,
			model = $4,
			sdk_version = $5,
			pid = $6,
			input_tokens = $7,
			output_tokens = $8,
			cache_creation_input_tokens = $9,
			cache_read_input_tokens = $10,
			cost_usd = $11,
			prompt_count = $12,
			finished_at = now()
		WHERE id = $1`,
		id,
		string(info.Status),
		sessionError,
		info.Model,
		info.SDKVersion,
		info.PID,
		info.Usage.InputTokens,
		info.Usage.OutputTokens,
		info.Usage.CacheCreationInputTokens,
		info.Usage.CacheReadInputTokens,
		info.Usage.CostUSD,
		info.Usage.PromptCount,
	)
	return errors.Wrap(err, "finishing session")
}

// AddPromptDuration atomically adds the given duration (in milliseconds) to the session's cumulative prompt_duration_ms.
func (s *PostgresStore) AddPromptDuration(ctx context.Context, id string, durationMs int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE session SET prompt_duration_ms = prompt_duration_ms + $2
		WHERE id = $1`, id, durationMs)
	return errors.Wrap(err, "adding prompt duration")
}

// InsertLog inserts an event log entry for a session.
func (s *PostgresStore) InsertLog(ctx context.Context, sessionID, eventType string, payload json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO log (session_id, event_type, payload)
		VALUES ($1, $2, $3)`,
		sessionID, eventType, payload,
	)
	return errors.Wrap(err, "inserting log")
}

// SessionRow holds a session record from the database.
type SessionRow struct {
	ID                       string
	SourceName               string
	Agent                    string
	Dir                      string
	Sandbox                  string
	Node                     string
	GitCommit                string
	ProjectName              string
	Env                      json.RawMessage
	Status                   string
	ErrorMsg                 string
	Model                    string
	SDKVersion               string
	PID                      int
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	CostUSD                  float64
	PromptCount              int64
	PromptDurationMs         int64
	CreatedAt                time.Time
	FinishedAt               *time.Time
}

// LogRow holds an event log record from the database.
type LogRow struct {
	ID        int64
	SessionID string
	EventType string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// ListSessions returns all sessions ordered by creation time descending.
func (s *PostgresStore) ListSessions(ctx context.Context) ([]SessionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, error_msg, model, sdk_version, pid,
			input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			cost_usd, prompt_count, prompt_duration_ms, created_at, finished_at
		FROM session
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, errors.Wrap(err, "querying sessions")
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

// GetSession returns a single session by ID.
func (s *PostgresStore) GetSession(ctx context.Context, id string) (SessionRow, error) {
	var r SessionRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, error_msg, model, sdk_version, pid,
			input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			cost_usd, prompt_count, prompt_duration_ms, created_at, finished_at
		FROM session
		WHERE id = $1`, id).Scan(
		&r.ID, &r.SourceName, &r.Agent, &r.Dir, &r.Sandbox, &r.Node, &r.GitCommit, &r.ProjectName, &r.Env,
		&r.Status, &r.ErrorMsg, &r.Model, &r.SDKVersion, &r.PID,
		&r.InputTokens, &r.OutputTokens, &r.CacheCreationInputTokens, &r.CacheReadInputTokens,
		&r.CostUSD, &r.PromptCount, &r.PromptDurationMs, &r.CreatedAt, &r.FinishedAt,
	)
	if err != nil {
		return r, errors.Wrap(err, "querying session")
	}
	return r, nil
}

// GetSessionLogs returns all event logs for a session ordered by creation time.
func (s *PostgresStore) GetSessionLogs(ctx context.Context, sessionID string) ([]LogRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, event_type, payload, created_at
		FROM log
		WHERE session_id = $1
		ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "querying logs")
	}
	defer rows.Close()

	var result []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.EventType, &r.Payload, &r.CreatedAt); err != nil {
			return nil, errors.Wrap(err, "scanning log row")
		}
		result = append(result, r)
	}
	return result, nil
}

// GetLog returns a single event log entry by its ID.
func (s *PostgresStore) GetLog(ctx context.Context, id int64) (LogRow, error) {
	var r LogRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, session_id, event_type, payload, created_at
		FROM log
		WHERE id = $1`, id).Scan(&r.ID, &r.SessionID, &r.EventType, &r.Payload, &r.CreatedAt)
	if err != nil {
		return r, errors.Wrap(err, "querying log")
	}
	return r, nil
}

// GetLogsByToolCallID returns all log entries for a given session that share the same toolCallId, ordered by time.
func (s *PostgresStore) GetLogsByToolCallID(ctx context.Context, sessionID, toolCallID string) ([]LogRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, event_type, payload, created_at
		FROM log
		WHERE session_id = $1
		  AND event_type IN ('tool_call', 'tool_call_update')
		  AND payload->>'toolCallId' = $2
		ORDER BY created_at`, sessionID, toolCallID)
	if err != nil {
		return nil, errors.Wrap(err, "querying logs by toolCallId")
	}
	defer rows.Close()

	var result []LogRow
	for rows.Next() {
		var r LogRow
		if err := rows.Scan(&r.ID, &r.SessionID, &r.EventType, &r.Payload, &r.CreatedAt); err != nil {
			return nil, errors.Wrap(err, "scanning log row")
		}
		result = append(result, r)
	}
	return result, nil
}

// GetSessionsByCommit returns all sessions matching the given git commit prefix, ordered by creation time descending.
func (s *PostgresStore) GetSessionsByCommit(ctx context.Context, commit string) ([]SessionRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source_name, agent, dir, sandbox, node, git_commit, project_name, env, status, error_msg, model, sdk_version, pid,
			input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens,
			cost_usd, prompt_count, prompt_duration_ms, created_at, finished_at
		FROM session
		WHERE git_commit LIKE $1 || '%'
		ORDER BY created_at DESC`, commit)
	if err != nil {
		return nil, errors.Wrap(err, "querying sessions by commit")
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

// GetPromptTexts returns the prompt texts for a session extracted from the log table.
func (s *PostgresStore) GetPromptTexts(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT payload->>'prompt' AS prompt_text
		FROM log
		WHERE session_id = $1 AND event_type = 'prompt'
		ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, errors.Wrap(err, "querying prompt texts")
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, errors.Wrap(err, "scanning prompt text")
		}
		result = append(result, text)
	}
	return result, nil
}

// DailyStatsRow holds aggregated token usage for a single day.
type DailyStatsRow struct {
	Day                      string
	Sessions                 int64
	PromptCount              int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	CostUSD                  float64
}

// MonthlyDirStatsRow holds aggregated token usage for a directory in a given month.
type MonthlyDirStatsRow struct {
	Month                    string
	Dir                      string
	Sessions                 int64
	PromptCount              int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	CostUSD                  float64
}

// GetMonthlyStatsByDir returns monthly aggregated token usage grouped by directory.
// When agent is non-empty, only sessions with that agent value are included.
func (s *PostgresStore) GetMonthlyStatsByDir(ctx context.Context, months int, agent string) ([]MonthlyDirStatsRow, error) {
	query := `
		SELECT
			TO_CHAR(s.created_at, 'YYYY-MM') AS month,
			s.dir,
			COUNT(s.id) AS sessions,
			COALESCE(SUM(s.prompt_count), 0) AS prompt_count,
			COALESCE(SUM(s.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(s.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(s.cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			COALESCE(SUM(s.cache_read_input_tokens), 0) AS cache_read_input_tokens,
			COALESCE(SUM(s.cost_usd), 0) AS cost_usd
		FROM session s
		WHERE s.created_at >= date_trunc('month', CURRENT_DATE) - ($1 - 1) * INTERVAL '1 month'
		  AND s.prompt_count > 0`
	args := []interface{}{months}
	if agent != "" {
		query += ` AND s.agent = $2`
		args = append(args, agent)
	}
	query += `
		GROUP BY month, s.dir
		ORDER BY month DESC, cost_usd DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "querying monthly stats by dir")
	}
	defer rows.Close()

	var result []MonthlyDirStatsRow
	for rows.Next() {
		var r MonthlyDirStatsRow
		if err := rows.Scan(&r.Month, &r.Dir, &r.Sessions, &r.PromptCount,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.CostUSD); err != nil {
			return nil, errors.Wrap(err, "scanning monthly dir stats row")
		}
		result = append(result, r)
	}
	return result, nil
}

// GetDailyStats returns daily aggregated token usage from the given date until today.
// When agent is non-empty, only sessions with that agent value are included.
func (s *PostgresStore) GetDailyStats(ctx context.Context, since time.Time, agent string) ([]DailyStatsRow, error) {
	joinCond := `s.created_at::date = d.day`
	args := []interface{}{since}
	if agent != "" {
		joinCond += ` AND s.agent = $2`
		args = append(args, agent)
	}
	query := `
		SELECT
			TO_CHAR(d.day, 'YYYY-MM-DD') AS day,
			COALESCE(SUM(s.input_tokens), 0) AS input_tokens,
			COALESCE(SUM(s.output_tokens), 0) AS output_tokens,
			COALESCE(SUM(s.cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			COALESCE(SUM(s.cache_read_input_tokens), 0) AS cache_read_input_tokens,
			COALESCE(SUM(s.cost_usd), 0) AS cost_usd,
			COUNT(s.id) AS sessions,
			COALESCE(SUM(s.prompt_count), 0) AS prompt_count
		FROM generate_series(
			$1::date,
			CURRENT_DATE::date,
			'1 day'
		) AS d(day)
		LEFT JOIN session s ON ` + joinCond + ` AND s.prompt_count > 0
		GROUP BY d.day
		ORDER BY d.day DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, errors.Wrap(err, "querying daily stats")
	}
	defer rows.Close()

	var result []DailyStatsRow
	for rows.Next() {
		var r DailyStatsRow
		if err := rows.Scan(&r.Day, &r.InputTokens, &r.OutputTokens,
			&r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.CostUSD, &r.Sessions, &r.PromptCount); err != nil {
			return nil, errors.Wrap(err, "scanning daily stats row")
		}
		result = append(result, r)
	}
	return result, nil
}

// ToolCountRow holds a tool/skill name and its usage count.
type ToolCountRow struct {
	Name  string
	Count int64
}

// InsightsData holds all the data for the insights page.
type InsightsData struct {
	TopTools       []ToolCountRow
	TopSkills      []ToolCountRow
	AvgDurationMin float64
	TotalSessions  int64
	TotalTools     int64
	TotalSkills    int64
}

// GetInsights returns aggregated insights: top tools, top skills, and average session duration.
func (s *PostgresStore) GetInsights(ctx context.Context, since time.Time) (*InsightsData, error) {
	data := &InsightsData{}

	// Top 10 tools by usage count (title from tool_call payload).
	toolRows, err := s.pool.Query(ctx, `
		SELECT payload->>'title' AS tool_name, COUNT(*) AS cnt
		FROM log
		WHERE event_type = 'tool_call'
		  AND payload->>'title' IS NOT NULL
		  AND payload->>'title' != ''
		  AND created_at >= $1
		GROUP BY tool_name
		ORDER BY cnt DESC
		LIMIT 10`, since)
	if err != nil {
		return nil, errors.Wrap(err, "querying top tools")
	}
	defer toolRows.Close()
	for toolRows.Next() {
		var r ToolCountRow
		if err := toolRows.Scan(&r.Name, &r.Count); err != nil {
			return nil, errors.Wrap(err, "scanning tool row")
		}
		data.TopTools = append(data.TopTools, r)
	}

	// Top 10 skills (tool_call where rawInput contains "skill" key).
	skillRows, err := s.pool.Query(ctx, `
		SELECT payload->'rawInput'->>'skill' AS skill_name, COUNT(*) AS cnt
		FROM log
		WHERE event_type = 'tool_call'
		  AND payload->'rawInput'->>'skill' IS NOT NULL
		  AND payload->'rawInput'->>'skill' != ''
		  AND created_at >= $1
		GROUP BY skill_name
		ORDER BY cnt DESC
		LIMIT 10`, since)
	if err != nil {
		return nil, errors.Wrap(err, "querying top skills")
	}
	defer skillRows.Close()
	for skillRows.Next() {
		var r ToolCountRow
		if err := skillRows.Scan(&r.Name, &r.Count); err != nil {
			return nil, errors.Wrap(err, "scanning skill row")
		}
		data.TopSkills = append(data.TopSkills, r)
	}

	// Average session duration and totals.
	err = s.pool.QueryRow(ctx, `
		SELECT
			COALESCE(AVG(EXTRACT(EPOCH FROM (finished_at - created_at)) / 60), 0) AS avg_duration_min,
			COUNT(*) AS total_sessions
		FROM session
		WHERE finished_at IS NOT NULL
		  AND prompt_count > 0
		  AND created_at >= $1`, since).Scan(&data.AvgDurationMin, &data.TotalSessions)
	if err != nil {
		return nil, errors.Wrap(err, "querying session duration")
	}

	// Total tool calls.
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM log
		WHERE event_type = 'tool_call' AND created_at >= $1`, since).Scan(&data.TotalTools)
	if err != nil {
		return nil, errors.Wrap(err, "querying total tools")
	}

	// Total skill invocations.
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM log
		WHERE event_type = 'tool_call'
		  AND payload->'rawInput'->>'skill' IS NOT NULL
		  AND payload->'rawInput'->>'skill' != ''
		  AND created_at >= $1`, since).Scan(&data.TotalSkills)
	if err != nil {
		return nil, errors.Wrap(err, "querying total skills")
	}

	return data, nil
}

func (s *PostgresStore) GetDistinctAgents(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT agent FROM session ORDER BY agent`)
	if err != nil {
		return nil, errors.Wrap(err, "querying distinct agents")
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return nil, errors.Wrap(err, "scanning agent row")
		}
		result = append(result, agent)
	}
	return result, nil
}

// ModelStatsRow holds aggregated stats for an agent+model combination.
type ModelStatsRow struct {
	Agent                    string
	Model                    string
	Sessions                 int64
	PromptCount              int64
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	CostUSD                  float64
	PromptDurationMs         int64
}

// GetModelStats returns aggregated stats grouped by agent and model.
func (s *PostgresStore) GetModelStats(ctx context.Context, since time.Time) ([]ModelStatsRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			agent,
			model,
			COUNT(id) AS sessions,
			COALESCE(SUM(prompt_count), 0) AS prompt_count,
			COALESCE(SUM(input_tokens), 0) AS input_tokens,
			COALESCE(SUM(output_tokens), 0) AS output_tokens,
			COALESCE(SUM(cache_creation_input_tokens), 0) AS cache_creation_input_tokens,
			COALESCE(SUM(cache_read_input_tokens), 0) AS cache_read_input_tokens,
			COALESCE(SUM(cost_usd), 0) AS cost_usd,
			COALESCE(SUM(prompt_duration_ms), 0) AS prompt_duration_ms
		FROM session
		WHERE created_at >= $1
		  AND model != ''
		  AND prompt_count > 0
		GROUP BY agent, model
		ORDER BY cost_usd DESC`, since)
	if err != nil {
		return nil, errors.Wrap(err, "querying model stats")
	}
	defer rows.Close()

	var result []ModelStatsRow
	for rows.Next() {
		var r ModelStatsRow
		if err := rows.Scan(&r.Agent, &r.Model, &r.Sessions, &r.PromptCount,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheCreationInputTokens, &r.CacheReadInputTokens,
			&r.CostUSD, &r.PromptDurationMs); err != nil {
			return nil, errors.Wrap(err, "scanning model stats row")
		}
		result = append(result, r)
	}
	return result, nil
}

// validProjectFields lists the columns that can be set via SetProjectField.
var validProjectFields = map[string]bool{
	"agent":            true,
	"dir":              true,
	"sandbox":          true,
	"sandbox_profiles": true,
	"permission":       true,
	"repo":             true,
	"hooks":            true,
}

// GetProject returns the project with the given name.
// If the project does not exist, it is created with defaults and returned.
func (s *PostgresStore) GetProject(ctx context.Context, name string) (ProjectRow, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project (name) VALUES ($1)
		ON CONFLICT (name) DO NOTHING`, name)
	if err != nil {
		return ProjectRow{}, errors.Wrap(err, "upserting project")
	}

	var r ProjectRow
	var envJSON json.RawMessage
	err = s.pool.QueryRow(ctx, `
		SELECT name, agent, dir, sandbox, sandbox_profiles, permission, env, repo, hooks, created_at, updated_at
		FROM project WHERE name = $1`, name).Scan(
		&r.Name, &r.Agent, &r.Dir, &r.Sandbox, &r.SandboxProfiles, &r.Permission, &envJSON, &r.Repo, &r.Hooks, &r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return ProjectRow{}, errors.Wrap(err, "querying project")
	}
	if err := json.Unmarshal(envJSON, &r.Env); err != nil {
		return ProjectRow{}, errors.Wrap(err, "unmarshaling project env")
	}
	return r, nil
}

// SetProjectField updates a single text field on a project row.
// The project is created if it does not exist.
func (s *PostgresStore) SetProjectField(ctx context.Context, name, field, value string) error {
	if !validProjectFields[field] {
		return fmt.Errorf("invalid project field: %s", field)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project (name, `+field+`) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET `+field+` = $2, updated_at = now()`, name, value)
	return errors.Wrap(err, "setting project field")
}

// AppendProjectEnv appends an env entry to the project's env array.
// The project is created if it does not exist.
func (s *PostgresStore) AppendProjectEnv(ctx context.Context, name, entry string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO project (name, env) VALUES ($1, jsonb_build_array($2::text))
		ON CONFLICT (name) DO UPDATE SET env = project.env || jsonb_build_array($2::text), updated_at = now()`, name, entry)
	return errors.Wrap(err, "appending project env")
}

// ClearProjectEnv clears all env entries for a project.
func (s *PostgresStore) ClearProjectEnv(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE project SET env = '[]', updated_at = now() WHERE name = $1`, name)
	return errors.Wrap(err, "clearing project env")
}

// ListProjects returns all projects from the project table with running status.
func (s *PostgresStore) ListProjects(ctx context.Context) ([]ProjectListRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.name, p.dir, p.agent,
		       COALESCE(bool_or(s.status IN ('running', 'pending')), false) AS has_running,
		       MAX(GREATEST(s.created_at, s.finished_at)) AS last_used
		FROM project p
		LEFT JOIN session s ON s.project_name = p.name
		GROUP BY p.name, p.dir, p.agent
		ORDER BY last_used DESC NULLS LAST, p.name`)
	if err != nil {
		return nil, errors.Wrap(err, "listing projects")
	}
	defer rows.Close()
	var result []ProjectListRow
	for rows.Next() {
		var r ProjectListRow
		var lastUsed *time.Time
		if err := rows.Scan(&r.Name, &r.Dir, &r.Agent, &r.HasRunning, &lastUsed); err != nil {
			return nil, errors.Wrap(err, "scanning project row")
		}
		if lastUsed != nil {
			r.LastUsed = *lastUsed
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// MarshalEvent renders a raw ACP update into the JSON payload and event-type
// string consumers (the browser and the log table) use. The whole SessionUpdate
// is marshalled — its union flattens the variant's fields next to a
// "sessionUpdate" discriminator — which is exactly the shape persisted history
// replays with.
func MarshalEvent(update acp.SessionUpdate) (json.RawMessage, string) {
	eventType := ClassifyEvent(update)
	raw, err := json.Marshal(update)
	if err != nil {
		slog.Debug("db: marshal event fallback", "type", eventType, "error", err)
		raw = []byte(`{"_marshalError":` + strconv.Quote(err.Error()) + `,"_eventType":"` + eventType + `"}`)
	}
	return raw, eventType
}

// ClassifyEvent returns the event type string for a SessionUpdate.
func ClassifyEvent(event acp.SessionUpdate) string {
	switch {
	case event.AgentMessageChunk != nil:
		return "agent_message_chunk"
	case event.AgentThoughtChunk != nil:
		return "agent_thought_chunk"
	case event.ToolCall != nil:
		return "tool_call"
	case event.ToolCallUpdate != nil:
		return "tool_call_update"
	case event.Plan != nil:
		return "plan"
	default:
		return "unknown"
	}
}
