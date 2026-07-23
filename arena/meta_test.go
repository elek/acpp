package arena

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconcileAssignsStableIDs(t *testing.T) {
	plan := &Plan{
		Name: "bench",
		Agents: []Agent{
			{Name: "claude", Agent: "claude-code-acp"},
			{Name: "rai", Agent: "rai acp"},
		},
	}

	n := 0
	newID := func() string { n++; return fmt.Sprintf("id%d", n) }

	m := &Meta{}
	m.Reconcile(plan, newID)

	require.Equal(t, "bench", m.Name)
	require.Len(t, m.Runs, 2)
	require.Len(t, m.Evaluations, 2)
	firstClaudeRunID := m.Runs[0].ID

	// Re-running reconcile keeps existing ids and adds only the new agent.
	plan.Agents = append(plan.Agents, Agent{Name: "opencode", Agent: "opencode"})
	m.Reconcile(plan, newID)

	require.Len(t, m.Runs, 3)
	require.Equal(t, firstClaudeRunID, m.Runs[0].ID, "existing run id must be stable")
	require.NotEmpty(t, m.Runs[2].ID)
	require.Len(t, m.Evaluations, 3)
}

func TestReconcileRefreshesAgentCommand(t *testing.T) {
	plan := &Plan{Name: "b", Agents: []Agent{{Name: "a", Agent: "old"}}}
	newID := func() string { return "x" }
	m := &Meta{}
	m.Reconcile(plan, newID)

	plan.Agents[0].Agent = "new"
	m.Reconcile(plan, newID)
	require.Equal(t, "new", m.Runs[0].Agent)
	require.Len(t, m.Runs, 1)
}

func TestMetaSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.yaml")

	// Missing file loads as empty.
	m, err := LoadMeta(path)
	require.NoError(t, err)
	require.Empty(t, m.Runs)

	m = &Meta{
		Name:        "bench",
		Runs:        []RunMeta{{Name: "a", Agent: "cmd", ID: "r1"}},
		Evaluations: []EvalMeta{{Name: "a", ID: "e1"}},
	}
	require.NoError(t, m.Save(path))

	got, err := LoadMeta(path)
	require.NoError(t, err)
	require.Equal(t, m, got)
	require.Equal(t, map[string]string{"r1": "a"}, got.RunByID())
	require.Equal(t, []string{"r1"}, got.RunIDs())
}
