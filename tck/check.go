package tck

// Result is the outcome of a single check: a name, a boolean verdict, and a
// free-form value (a number, a string, a snippet of the answer, …).
type Result struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Value string `json:"value"`
}

// Check evaluates a finished transcript and returns one or more results. A check
// may emit several rows (e.g. one per declared agent capability).
type Check func(t *Transcript) []Result

// checks is the ordered registry run against every agent's transcript.
var checks = []Check{
	checkAgentCapabilities,
	checkAvailableCommands,
	checkUsage,
	checkMeta,
	checkPromptFinished,
	checkToolUsage,
	checkCapital,
	checkListDir,
}

// RunChecks evaluates every registered check against the transcript and flattens
// the results in registry order.
func RunChecks(t *Transcript) []Result {
	var out []Result
	for _, c := range checks {
		out = append(out, c(t)...)
	}
	return out
}
