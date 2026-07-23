package arena

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Score is one evaluator's judgement of one contestant output.
type Score struct {
	ID        string `json:"id"`
	Score     int    `json:"score"`
	Rationale string `json:"rationale"`
}

// ParseScores parses a scores.json body and validates it against wantIDs: every
// wanted run id must be scored exactly once, with an integer score in 1..10.
// Unknown ids are rejected. It tolerates a JSON array embedded in surrounding
// prose or a ```json fence by extracting the outermost bracketed array.
func ParseScores(data []byte, wantIDs []string) ([]Score, error) {
	body := extractJSONArray(data)

	var scores []Score
	if err := json.Unmarshal(body, &scores); err != nil {
		return nil, fmt.Errorf("scores.json: invalid JSON: %w", err)
	}

	want := make(map[string]bool, len(wantIDs))
	for _, id := range wantIDs {
		want[id] = true
	}
	seen := make(map[string]bool, len(scores))
	for _, s := range scores {
		if !want[s.ID] {
			return nil, fmt.Errorf("scores.json: unknown id %q", s.ID)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("scores.json: duplicate id %q", s.ID)
		}
		if s.Score < 1 || s.Score > 10 {
			return nil, fmt.Errorf("scores.json: id %q score %d out of range 1..10", s.ID, s.Score)
		}
		seen[s.ID] = true
	}
	for _, id := range wantIDs {
		if !seen[id] {
			return nil, fmt.Errorf("scores.json: missing score for id %q", id)
		}
	}
	return scores, nil
}

// extractJSONArray returns the outermost [...] slice of data, or data unchanged
// if no bracket pair is found.
func extractJSONArray(data []byte) []byte {
	s := string(data)
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start >= 0 && end > start {
		return []byte(s[start : end+1])
	}
	return data
}
