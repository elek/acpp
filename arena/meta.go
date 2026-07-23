package arena

import (
	"os"

	"gopkg.in/yaml.v3"
)

// Meta is the durable name<->random_id mapping for one arena directory. It is
// the ONLY place the mapping lives, so the blind ids stay stable across
// re-runs. Evaluators never see it.
type Meta struct {
	Name        string     `yaml:"name"`
	Runs        []RunMeta  `yaml:"runs"`
	Evaluations []EvalMeta `yaml:"evaluations"`
}

// RunMeta records a contestant's blind run id.
type RunMeta struct {
	Name  string `yaml:"name"`
	Agent string `yaml:"agent"`
	ID    string `yaml:"id"`
}

// EvalMeta records an evaluator's working-dir id.
type EvalMeta struct {
	Name string `yaml:"name"`
	ID   string `yaml:"id"`
}

// LoadMeta reads meta.yaml. A missing file yields a zero Meta (not an error).
func LoadMeta(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Meta{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save writes meta.yaml.
func (m *Meta) Save(path string) error {
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Reconcile ensures every plan agent has a run and evaluation entry, minting
// ids via newID for any that are missing. Existing entries keep their id (so
// blind ids are stable) but refresh the agent command from the plan. The plan's
// name is copied onto the meta.
func (m *Meta) Reconcile(plan *Plan, newID func() string) {
	m.Name = plan.Name

	runByName := make(map[string]*RunMeta, len(m.Runs))
	for i := range m.Runs {
		runByName[m.Runs[i].Name] = &m.Runs[i]
	}
	evalByName := make(map[string]bool, len(m.Evaluations))
	for _, e := range m.Evaluations {
		evalByName[e.Name] = true
	}

	for _, a := range plan.Agents {
		if r, ok := runByName[a.Name]; ok {
			r.Agent = a.Agent
		} else {
			m.Runs = append(m.Runs, RunMeta{Name: a.Name, Agent: a.Agent, ID: newID()})
		}
		if !evalByName[a.Name] {
			m.Evaluations = append(m.Evaluations, EvalMeta{Name: a.Name, ID: newID()})
			evalByName[a.Name] = true
		}
	}
}

// RunByID returns the run id -> agent name map (used to un-blind scores).
func (m *Meta) RunByID() map[string]string {
	out := make(map[string]string, len(m.Runs))
	for _, r := range m.Runs {
		out[r.ID] = r.Name
	}
	return out
}

// RunIDs returns every contestant's run id.
func (m *Meta) RunIDs() []string {
	out := make([]string, 0, len(m.Runs))
	for _, r := range m.Runs {
		out = append(out, r.ID)
	}
	return out
}
