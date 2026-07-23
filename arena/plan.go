// Package arena runs several ACP agents on the same task in isolated
// directories, has each agent blindly score every result, and produces a
// comparative markdown report. See docs/plans/arena.md.
package arena

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Plan is the parsed arena plan file.
type Plan struct {
	Name       string     `yaml:"name"`
	Resources  []Resource `yaml:"resources"`
	Prompt     string     `yaml:"prompt"`
	Agents     []Agent    `yaml:"agents"`
	Artifacts  []string   `yaml:"artifacts"`
	Evaluation Evaluation `yaml:"evaluation"`
}

// Resource is one fetchable input, retrieved via go-getter. It accepts either a
// scalar string (the source, dest defaults to ".") or a {src, dest} mapping.
type Resource struct {
	Src  string `yaml:"src"`
	Dest string `yaml:"dest"`
}

// Agent is one contestant: Name is the report label (must be unique), Agent is
// the command string routed through the router (same resolution as `acpp run`).
type Agent struct {
	Name  string `yaml:"name"`
	Agent string `yaml:"agent"`
}

// Evaluation configures the scoring round.
type Evaluation struct {
	Prompt   string `yaml:"prompt"`
	Sandbox  string `yaml:"sandbox"`
	Profiles string `yaml:"profiles"`
}

// UnmarshalYAML accepts a scalar source string or a {src, dest} mapping.
func (r *Resource) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&r.Src)
	}
	type raw Resource
	var out raw
	if err := value.Decode(&out); err != nil {
		return err
	}
	*r = Resource(out)
	return nil
}

// DestOrDefault returns the destination subdir relative to the resources dir,
// defaulting to "." when unset.
func (r Resource) DestOrDefault() string {
	if r.Dest == "" {
		return "."
	}
	return r.Dest
}

// LoadPlan reads and validates a plan file.
func LoadPlan(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePlan(data)
}

// ParsePlan parses and validates plan YAML.
func ParsePlan(data []byte) (*Plan, error) {
	var p Plan
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the plan is internally consistent.
func (p *Plan) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("plan: name is required")
	}
	if p.Prompt == "" {
		return fmt.Errorf("plan: prompt is required")
	}
	if len(p.Agents) == 0 {
		return fmt.Errorf("plan: at least one agent is required")
	}
	seen := make(map[string]bool, len(p.Agents))
	for i, a := range p.Agents {
		if a.Name == "" {
			return fmt.Errorf("plan: agents[%d]: name is required", i)
		}
		if a.Agent == "" {
			return fmt.Errorf("plan: agent %q: agent command is required", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("plan: duplicate agent name %q", a.Name)
		}
		seen[a.Name] = true
	}
	for i, r := range p.Resources {
		if r.Src == "" {
			return fmt.Errorf("plan: resources[%d]: src is required", i)
		}
	}
	return nil
}
