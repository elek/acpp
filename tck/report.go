package tck

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// AgentReport is the full result of testing one agent binary.
type AgentReport struct {
	Agent   string   `json:"agent"`
	Error   string   `json:"error,omitempty"`
	Results []Result `json:"results"`
}

// RenderJSON writes the reports as indented JSON.
func RenderJSON(w io.Writer, reports []AgentReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reports)
}

// RenderTable writes a compatibility matrix: one row per check (unioned across
// all agents, in first-seen order), one column per agent. Cells show a ✓/✗ mark
// and the check's value; "-" marks a check that did not run for that agent.
func RenderTable(w io.Writer, reports []AgentReport) {
	// Union of check names in first-seen order.
	var order []string
	seen := map[string]bool{}
	for _, rep := range reports {
		for _, res := range rep.Results {
			if !seen[res.Name] {
				seen[res.Name] = true
				order = append(order, res.Name)
			}
		}
	}

	// Index results by name per agent for quick lookup.
	byName := make([]map[string]Result, len(reports))
	for i, rep := range reports {
		m := make(map[string]Result, len(rep.Results))
		for _, res := range rep.Results {
			m[res.Name] = res
		}
		byName[i] = m
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	// Header.
	fmt.Fprint(tw, "CHECK")
	for _, rep := range reports {
		fmt.Fprintf(tw, "\t%s", truncate(rep.Agent, 32))
	}
	fmt.Fprintln(tw)

	// Rows.
	for _, name := range order {
		fmt.Fprint(tw, name)
		for _, m := range byName {
			res, ok := m[name]
			if !ok {
				fmt.Fprint(tw, "\t-")
				continue
			}
			mark := "✗"
			if res.OK {
				mark = "✓"
			}
			cell := mark
			if res.Value != "" {
				cell = mark + " " + res.Value
			}
			fmt.Fprintf(tw, "\t%s", cell)
		}
		fmt.Fprintln(tw)
	}

	tw.Flush()
}
