package arena

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// BuildReport renders report.md. results maps an evaluator name to its
// validated scores (each Score.ID is a contestant run id). incomplete holds
// human-readable notes about runs/evaluators that produced no usable output.
func BuildReport(meta *Meta, results map[string][]Score, incomplete []string) string {
	idToName := meta.RunByID()

	// contestant column order follows meta.Runs; evaluator row order meta.Evaluations.
	contestants := make([]string, 0, len(meta.Runs))
	for _, r := range meta.Runs {
		contestants = append(contestants, r.Name)
	}
	evaluators := make([]string, 0, len(meta.Evaluations))
	for _, e := range meta.Evaluations {
		evaluators = append(evaluators, e.Name)
	}

	// cell[evaluator][contestant] = score, when present.
	cell := make(map[string]map[string]int)
	totals := make(map[string]int)
	for evaluator, scores := range results {
		row := make(map[string]int)
		for _, s := range scores {
			name, ok := idToName[s.ID]
			if !ok {
				continue
			}
			row[name] = s.Score
			totals[name] += s.Score
		}
		cell[evaluator] = row
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Arena report: %s\n\n", meta.Name)

	// Final scores, ranked by total (desc), ties broken by name for stability.
	b.WriteString("## Final scores\n\n")
	b.WriteString("| Rank | Agent | Total |\n|---|---|---|\n")
	ranked := append([]string(nil), contestants...)
	sort.SliceStable(ranked, func(i, j int) bool {
		if totals[ranked[i]] != totals[ranked[j]] {
			return totals[ranked[i]] > totals[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	for i, name := range ranked {
		fmt.Fprintf(&b, "| %d | %s | %d |\n", i+1, name, totals[name])
	}

	// Pivot: rows = evaluator, cols = contestant.
	b.WriteString("\n## Scores given (rows = evaluator, columns = contestant)\n\n")
	b.WriteString("| evaluator \\ contestant |")
	for _, c := range contestants {
		fmt.Fprintf(&b, " %s |", c)
	}
	b.WriteString("\n|---|")
	for range contestants {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, ev := range evaluators {
		fmt.Fprintf(&b, "| %s |", ev)
		row := cell[ev]
		for _, c := range contestants {
			if v, ok := row[c]; ok {
				fmt.Fprintf(&b, " %s |", strconv.Itoa(v))
			} else {
				b.WriteString("  |")
			}
		}
		b.WriteString("\n")
	}

	if len(incomplete) > 0 {
		b.WriteString("\n## Incomplete\n\n")
		for _, note := range incomplete {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}

	return b.String()
}
