package db

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acplib "github.com/elek/acpp/types"
)

// MemStore is an in-memory implementation of Store for testing and lightweight usage.
type MemStore struct {
	mu       sync.RWMutex
	sessions map[string]SessionRow
	logs     []LogRow
	projects map[string]ProjectRow
	logSeq   atomic.Int64
}

// NewMemStore creates a new empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		sessions: make(map[string]SessionRow),
		projects: make(map[string]ProjectRow),
	}
}

func (m *MemStore) Close() {}

func (m *MemStore) CompleteRunningSessions(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	now := time.Now()
	for id, s := range m.sessions {
		if s.Status == "running" || s.Status == "pending" {
			s.Status = "complete"
			s.FinishedAt = &now
			m.sessions[id] = s
			count++
		}
	}
	return count, nil
}

func (m *MemStore) InsertSession(ctx context.Context, id, sourceName, agent, dir, sandbox, node, gitCommit, projectName string, env []string, createdAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Mirror PostgresStore: a session implies its project exists, and the
	// project records the session's dir (backfilled when empty, never overwritten).
	if p, ok := m.projects[projectName]; !ok {
		m.projects[projectName] = ProjectRow{
			Name:      projectName,
			Dir:       dir,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		}
	} else if p.Dir == "" {
		p.Dir = dir
		m.projects[projectName] = p
	}
	envJSON, _ := json.Marshal(env)
	m.sessions[id] = SessionRow{
		ID:          id,
		SourceName:  sourceName,
		Agent:       filepath.Base(agent),
		Dir:         dir,
		Sandbox:     sandbox,
		Node:        node,
		GitCommit:   gitCommit,
		ProjectName: projectName,
		Env:         envJSON,
		Status:      string(acplib.StatusPending),
		CreatedAt:   createdAt,
	}
	return nil
}

func (m *MemStore) UpdateSession(ctx context.Context, id string, info acplib.StatusInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	s.Status = string(info.Status)
	s.Model = info.Model
	s.SDKVersion = info.SDKVersion
	s.PID = info.PID
	s.InputTokens = info.Usage.InputTokens
	s.OutputTokens = info.Usage.OutputTokens
	s.CacheCreationInputTokens = info.Usage.CacheCreationInputTokens
	s.CacheReadInputTokens = info.Usage.CacheReadInputTokens
	s.CostUSD = info.Usage.CostUSD
	s.PromptCount = info.Usage.PromptCount
	m.sessions[id] = s
	return nil
}

func (m *MemStore) FinishSession(ctx context.Context, id string, info acplib.StatusInfo, sessionError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	s.Status = string(info.Status)
	s.ErrorMsg = sessionError
	s.Model = info.Model
	s.SDKVersion = info.SDKVersion
	s.PID = info.PID
	s.InputTokens = info.Usage.InputTokens
	s.OutputTokens = info.Usage.OutputTokens
	s.CacheCreationInputTokens = info.Usage.CacheCreationInputTokens
	s.CacheReadInputTokens = info.Usage.CacheReadInputTokens
	s.CostUSD = info.Usage.CostUSD
	s.PromptCount = info.Usage.PromptCount
	now := time.Now()
	s.FinishedAt = &now
	m.sessions[id] = s
	return nil
}

func (m *MemStore) AddPromptDuration(ctx context.Context, id string, durationMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	s.PromptDurationMs += durationMs
	m.sessions[id] = s
	return nil
}

func (m *MemStore) InsertLog(ctx context.Context, sessionID, eventType string, payload json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, LogRow{
		ID:        m.logSeq.Add(1),
		SessionID: sessionID,
		EventType: eventType,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
	return nil
}

func (m *MemStore) ListSessions(ctx context.Context) ([]SessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]SessionRow, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *MemStore) GetSession(ctx context.Context, id string) (SessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return SessionRow{}, fmt.Errorf("session not found: %s", id)
	}
	return s, nil
}

func (m *MemStore) GetSessionLogs(ctx context.Context, sessionID string) ([]LogRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []LogRow
	for _, l := range m.logs {
		if l.SessionID == sessionID {
			result = append(result, l)
		}
	}
	return result, nil
}

func (m *MemStore) GetLog(ctx context.Context, id int64) (LogRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, l := range m.logs {
		if l.ID == id {
			return l, nil
		}
	}
	return LogRow{}, fmt.Errorf("log not found: %d", id)
}

func (m *MemStore) GetLogsByToolCallID(ctx context.Context, sessionID, toolCallID string) ([]LogRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []LogRow
	for _, l := range m.logs {
		if l.SessionID != sessionID {
			continue
		}
		if l.EventType != "tool_call" && l.EventType != "tool_call_update" {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(l.Payload, &p); err != nil {
			continue
		}
		if p["toolCallId"] == toolCallID {
			result = append(result, l)
		}
	}
	return result, nil
}

func (m *MemStore) GetSessionsByCommit(ctx context.Context, commit string) ([]SessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SessionRow
	for _, s := range m.sessions {
		if strings.HasPrefix(s.GitCommit, commit) {
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *MemStore) GetPromptTexts(ctx context.Context, sessionID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []string
	for _, l := range m.logs {
		if l.SessionID == sessionID && l.EventType == "prompt" {
			var p map[string]interface{}
			if err := json.Unmarshal(l.Payload, &p); err == nil {
				if text, ok := p["prompt"].(string); ok {
					result = append(result, text)
				}
			}
		}
	}
	return result, nil
}

func (m *MemStore) GetDailyStats(ctx context.Context, since time.Time, agent string) ([]DailyStatsRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	daily := map[string]*DailyStatsRow{}
	for _, s := range m.sessions {
		if s.CreatedAt.Before(since) {
			continue
		}
		if agent != "" && s.Agent != agent {
			continue
		}
		day := s.CreatedAt.Format("2006-01-02")
		d, ok := daily[day]
		if !ok {
			d = &DailyStatsRow{Day: day}
			daily[day] = d
		}
		d.Sessions++
		d.PromptCount += s.PromptCount
		d.InputTokens += s.InputTokens
		d.OutputTokens += s.OutputTokens
		d.CacheCreationInputTokens += s.CacheCreationInputTokens
		d.CacheReadInputTokens += s.CacheReadInputTokens
		d.CostUSD += s.CostUSD
	}
	result := make([]DailyStatsRow, 0, len(daily))
	for _, d := range daily {
		result = append(result, *d)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Day > result[j].Day
	})
	return result, nil
}

func (m *MemStore) GetMonthlyStatsByDir(ctx context.Context, months int, agent string) ([]MonthlyDirStatsRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type key struct{ month, dir string }
	monthly := map[key]*MonthlyDirStatsRow{}
	cutoff := time.Now().AddDate(0, -(months - 1), 0)
	cutoff = time.Date(cutoff.Year(), cutoff.Month(), 1, 0, 0, 0, 0, cutoff.Location())
	for _, s := range m.sessions {
		if s.CreatedAt.Before(cutoff) {
			continue
		}
		if agent != "" && s.Agent != agent {
			continue
		}
		k := key{s.CreatedAt.Format("2006-01"), s.Dir}
		d, ok := monthly[k]
		if !ok {
			d = &MonthlyDirStatsRow{Month: k.month, Dir: k.dir}
			monthly[k] = d
		}
		d.Sessions++
		d.PromptCount += s.PromptCount
		d.InputTokens += s.InputTokens
		d.OutputTokens += s.OutputTokens
		d.CacheCreationInputTokens += s.CacheCreationInputTokens
		d.CacheReadInputTokens += s.CacheReadInputTokens
		d.CostUSD += s.CostUSD
	}
	result := make([]MonthlyDirStatsRow, 0, len(monthly))
	for _, d := range monthly {
		result = append(result, *d)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Month != result[j].Month {
			return result[i].Month > result[j].Month
		}
		return result[i].CostUSD > result[j].CostUSD
	})
	return result, nil
}

func (m *MemStore) GetModelStats(ctx context.Context, since time.Time) ([]ModelStatsRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	type key struct{ agent, model string }
	stats := map[key]*ModelStatsRow{}
	for _, s := range m.sessions {
		if s.CreatedAt.Before(since) || s.Model == "" {
			continue
		}
		k := key{s.Agent, s.Model}
		d, ok := stats[k]
		if !ok {
			d = &ModelStatsRow{Agent: k.agent, Model: k.model}
			stats[k] = d
		}
		d.Sessions++
		d.PromptCount += s.PromptCount
		d.InputTokens += s.InputTokens
		d.OutputTokens += s.OutputTokens
		d.CacheCreationInputTokens += s.CacheCreationInputTokens
		d.CacheReadInputTokens += s.CacheReadInputTokens
		d.CostUSD += s.CostUSD
		d.PromptDurationMs += s.PromptDurationMs
	}
	result := make([]ModelStatsRow, 0, len(stats))
	for _, d := range stats {
		result = append(result, *d)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CostUSD > result[j].CostUSD
	})
	return result, nil
}

func (m *MemStore) GetInsights(ctx context.Context, since time.Time) (*InsightsData, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data := &InsightsData{}

	tools := map[string]int64{}
	skills := map[string]int64{}
	for _, l := range m.logs {
		if l.CreatedAt.Before(since) || l.EventType != "tool_call" {
			continue
		}
		var p map[string]interface{}
		if err := json.Unmarshal(l.Payload, &p); err != nil {
			continue
		}
		if title, ok := p["title"].(string); ok && title != "" {
			tools[title]++
			data.TotalTools++
		}
		if raw, ok := p["rawInput"].(map[string]interface{}); ok {
			if skill, ok := raw["skill"].(string); ok && skill != "" {
				skills[skill]++
				data.TotalSkills++
			}
		}
	}

	data.TopTools = topN(tools, 10)
	data.TopSkills = topN(skills, 10)

	var totalDuration float64
	for _, s := range m.sessions {
		if s.FinishedAt != nil && !s.CreatedAt.Before(since) {
			totalDuration += s.FinishedAt.Sub(s.CreatedAt).Minutes()
			data.TotalSessions++
		}
	}
	if data.TotalSessions > 0 {
		data.AvgDurationMin = totalDuration / float64(data.TotalSessions)
	}
	return data, nil
}

func topN(counts map[string]int64, n int) []ToolCountRow {
	result := make([]ToolCountRow, 0, len(counts))
	for name, count := range counts {
		result = append(result, ToolCountRow{Name: name, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})
	if len(result) > n {
		result = result[:n]
	}
	return result
}

func (m *MemStore) GetDistinctAgents(ctx context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	for _, s := range m.sessions {
		seen[s.Agent] = true
	}
	result := make([]string, 0, len(seen))
	for a := range seen {
		result = append(result, a)
	}
	sort.Strings(result)
	return result, nil
}

func (m *MemStore) ListProjectDirs(ctx context.Context) ([]ProjectDirRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dirs := map[string]bool{}
	running := map[string]bool{}
	for _, s := range m.sessions {
		if s.Dir == "" {
			continue
		}
		dirs[s.Dir] = true
		if s.Status == "running" || s.Status == "pending" {
			running[s.Dir] = true
		}
	}
	result := make([]ProjectDirRow, 0, len(dirs))
	for d := range dirs {
		result = append(result, ProjectDirRow{Dir: d, HasRunning: running[d]})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].HasRunning != result[j].HasRunning {
			return result[i].HasRunning
		}
		return result[i].Dir < result[j].Dir
	})
	return result, nil
}

func (m *MemStore) ListSessionsByDir(ctx context.Context, dir string) ([]SessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SessionRow
	for _, s := range m.sessions {
		if s.Dir == dir {
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *MemStore) ListSessionsByProject(ctx context.Context, projectName string) ([]SessionRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []SessionRow
	for _, s := range m.sessions {
		if s.ProjectName == projectName {
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	return result, nil
}

func (m *MemStore) GetProject(ctx context.Context, name string) (ProjectRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		now := time.Now()
		p = ProjectRow{
			Name:      name,
			CreatedAt: now,
			UpdatedAt: now,
		}
		m.projects[name] = p
	}
	return p, nil
}

func (m *MemStore) SetProjectField(ctx context.Context, name, field, value string) error {
	if !validProjectFields[field] {
		return fmt.Errorf("invalid project field: %s", field)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		now := time.Now()
		p = ProjectRow{Name: name, CreatedAt: now}
	}
	switch field {
	case "agent":
		p.Agent = value
	case "dir":
		p.Dir = value
	case "sandbox":
		p.Sandbox = value
	case "permission":
		p.Permission = value
	case "repo":
		p.Repo = value
	case "hooks":
		p.Hooks = value
	}
	p.UpdatedAt = time.Now()
	m.projects[name] = p
	return nil
}

func (m *MemStore) AppendProjectEnv(ctx context.Context, name, entry string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		now := time.Now()
		p = ProjectRow{Name: name, CreatedAt: now}
	}
	p.Env = append(p.Env, entry)
	p.UpdatedAt = time.Now()
	m.projects[name] = p
	return nil
}

func (m *MemStore) ClearProjectEnv(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[name]
	if !ok {
		return nil
	}
	p.Env = nil
	p.UpdatedAt = time.Now()
	m.projects[name] = p
	return nil
}

func (m *MemStore) ListProjects(ctx context.Context) ([]ProjectListRow, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	running := map[string]bool{}
	for _, s := range m.sessions {
		if s.Status == "running" || s.Status == "pending" {
			running[s.ProjectName] = true
		}
	}

	result := make([]ProjectListRow, 0, len(m.projects))
	for _, p := range m.projects {
		result = append(result, ProjectListRow{
			Name:       p.Name,
			Dir:        p.Dir,
			Agent:      p.Agent,
			HasRunning: running[p.Name],
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].HasRunning != result[j].HasRunning {
			return result[i].HasRunning
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
