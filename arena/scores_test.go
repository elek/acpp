package arena

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseScoresValid(t *testing.T) {
	scores, err := ParseScores([]byte(`
	[
	  {"id": "r1", "score": 7, "rationale": "good"},
	  {"id": "r2", "score": 3, "rationale": "meh"}
	]`), []string{"r1", "r2"})
	require.NoError(t, err)
	require.Len(t, scores, 2)
}

func TestParseScoresTolerantOfSurroundingProse(t *testing.T) {
	body := "Here are my scores:\n```json\n[{\"id\":\"r1\",\"score\":5}]\n```\nThanks!"
	scores, err := ParseScores([]byte(body), []string{"r1"})
	require.NoError(t, err)
	require.Equal(t, 5, scores[0].Score)
}

func TestParseScoresErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want []string
	}{
		"missing id":   {`[{"id":"r1","score":5}]`, []string{"r1", "r2"}},
		"unknown id":   {`[{"id":"r1","score":5},{"id":"zz","score":5}]`, []string{"r1"}},
		"out of range": {`[{"id":"r1","score":11}]`, []string{"r1"}},
		"zero score":   {`[{"id":"r1","score":0}]`, []string{"r1"}},
		"duplicate id": {`[{"id":"r1","score":5},{"id":"r1","score":6}]`, []string{"r1"}},
		"not json":     {`not json at all`, []string{"r1"}},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := ParseScores([]byte(tc.body), tc.want)
			require.Error(t, err)
		})
	}
}
