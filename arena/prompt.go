package arena

import "fmt"

// defaultEvalPrompt returns the built-in evaluation prompt for count candidates.
// It is used when the plan does not set evaluation.prompt.
func defaultEvalPrompt(count int) string {
	return fmt.Sprintf(`You are judging a blind comparison of AI-agent solutions to a single task.

The original task inputs are available (read-only) under /resources.
Each candidate solution is in its own directory under /outputs (there are %d of
them), each named by an opaque id. You do NOT know which agent produced which —
judge strictly on merit, not on any guess about authorship.

Steps:
1. Read the task context under /resources to understand what was asked.
2. Inspect every candidate directory under /outputs. Each contains the agent's
   written response (response.md) and any artifacts it produced.
3. Compare the candidates against each other on correctness, completeness, and
   overall quality of the work.
4. Assign each candidate an integer score from 1 (worst) to 10 (best). Use the
   full range to differentiate them; avoid giving everything the same score.

Write your judgement as JSON to a file named scores.json in the current working
directory. Write ONLY that file and nothing else. The schema is a JSON array:

[
  {"id": "<the directory id under /outputs>", "score": <integer 1-10>, "rationale": "<one sentence>"}
]

Include exactly one entry for every candidate directory under /outputs.`, count)
}
