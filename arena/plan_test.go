package arena

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePlanScalarAndMappingResources(t *testing.T) {
	p, err := ParsePlan([]byte(`
name: bench
prompt: do the thing
resources:
  - ./local/fixtures
  - src: github.com/org/repo//sub
    dest: repo
agents:
  - { name: claude, agent: claude-code-acp }
  - { name: rai, agent: rai acp }
artifacts:
  - review.json
`))
	require.NoError(t, err)
	require.Equal(t, "bench", p.Name)
	require.Len(t, p.Resources, 2)

	require.Equal(t, "./local/fixtures", p.Resources[0].Src)
	require.Equal(t, ".", p.Resources[0].DestOrDefault())

	require.Equal(t, "github.com/org/repo//sub", p.Resources[1].Src)
	require.Equal(t, "repo", p.Resources[1].DestOrDefault())

	require.Equal(t, []string{"review.json"}, p.Artifacts)
	require.Len(t, p.Agents, 2)
}

func TestParsePlanValidation(t *testing.T) {
	cases := map[string]string{
		"missing name":    "prompt: x\nagents:\n  - {name: a, agent: b}\n",
		"missing prompt":  "name: x\nagents:\n  - {name: a, agent: b}\n",
		"no agents":       "name: x\nprompt: y\n",
		"dup agent name":  "name: x\nprompt: y\nagents:\n  - {name: a, agent: b}\n  - {name: a, agent: c}\n",
		"missing command": "name: x\nprompt: y\nagents:\n  - {name: a}\n",
	}
	for label, doc := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := ParsePlan([]byte(doc))
			require.Error(t, err)
		})
	}
}
