package arena

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildReport(t *testing.T) {
	meta := &Meta{
		Name: "bench",
		Runs: []RunMeta{
			{Name: "claude", ID: "r1"},
			{Name: "rai", ID: "r2"},
		},
		Evaluations: []EvalMeta{
			{Name: "claude", ID: "e1"},
			{Name: "rai", ID: "e2"},
		},
	}
	results := map[string][]Score{
		"claude": {{ID: "r1", Score: 8}, {ID: "r2", Score: 5}},
		"rai":    {{ID: "r1", Score: 9}, {ID: "r2", Score: 4}},
	}

	report := BuildReport(meta, results, []string{"eval opencode: scores.json missing"})

	// claude total = 8+9 = 17, rai total = 5+4 = 9; claude ranked first.
	require.Contains(t, report, "# Arena report: bench")
	require.Contains(t, report, "| 1 | claude | 17 |")
	require.Contains(t, report, "| 2 | rai | 9 |")

	// pivot header lists both contestants.
	require.Contains(t, report, "| evaluator \\ contestant | claude | rai |")
	// incomplete note surfaced.
	require.Contains(t, report, "eval opencode: scores.json missing")

	// claude's row shows 8 for claude and 5 for rai.
	lines := strings.Split(report, "\n")
	var claudeRow string
	for _, l := range lines {
		if strings.HasPrefix(l, "| claude |") {
			claudeRow = l
		}
	}
	require.Equal(t, "| claude | 8 | 5 |", claudeRow)
}

func TestBuildReportMissingCellsBlank(t *testing.T) {
	meta := &Meta{
		Name:        "b",
		Runs:        []RunMeta{{Name: "a", ID: "r1"}, {Name: "b", ID: "r2"}},
		Evaluations: []EvalMeta{{Name: "a", ID: "e1"}, {Name: "b", ID: "e2"}},
	}
	// only evaluator "a" produced scores, and only for r1.
	results := map[string][]Score{"a": {{ID: "r1", Score: 6}}}

	report := BuildReport(meta, results, nil)
	// evaluator b row present but empty cells.
	require.Contains(t, report, "| b |  |  |")
}
