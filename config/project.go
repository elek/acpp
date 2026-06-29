package config

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// ProjectFile is the name of the per-project configuration file read from a
// project's working directory.
const ProjectFile = ".acpp.yaml"

// ProjectConfig holds per-project defaults loaded from a project directory's
// .acpp.yaml file. Fields left unset fall back to global config / CLI defaults.
type ProjectConfig struct {
	// Agent overrides the agent command used for the project's conversation.
	Agent   string         `yaml:"agent,omitempty"`
	Sandbox ProjectSandbox `yaml:"sandbox,omitempty"`
	// Hooks are the message-transform hooks attached to the project's
	// conversations, in order. See the hook package.
	Hooks []HookConfig `yaml:"hooks,omitempty"`
}

// HookConfig configures a single hook. Type selects the registered hook
// implementation; Params carries every other YAML key as a string, delivered
// verbatim to the hook factory.
type HookConfig struct {
	Type   string
	Params map[string]string
}

// UnmarshalYAML reads a hook entry as a flat map: the `type` key becomes Type,
// and every remaining key is folded into Params as a string. This lets hooks
// declare arbitrary params without a fixed schema, e.g.
//
//	- type: qdrant
//	  url: http://localhost:6333
//	  collection: acpp
func (h *HookConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw map[string]string
	if err := value.Decode(&raw); err != nil {
		return errors.Wrap(err, "decoding hook config")
	}
	h.Type = raw["type"]
	if h.Type == "" {
		return errors.New("hook config missing required 'type' field")
	}
	delete(raw, "type")
	h.Params = raw
	return nil
}

// ProjectSandbox configures the sandbox for a project's conversation.
type ProjectSandbox struct {
	// Name is the sandbox type ("bbwrap", "none", …).
	Name string `yaml:"name,omitempty"`
	// Profiles is a comma-separated list of sandbox profile fragments to merge.
	Profiles string `yaml:"profiles,omitempty"`
}

// LoadProject reads <dir>/.acpp.yaml. A missing file is not an error: it yields
// the zero ProjectConfig so callers fall back to their existing defaults.
// Malformed YAML returns an error.
func LoadProject(dir string) (ProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, ProjectFile))
	if err != nil {
		if os.IsNotExist(err) {
			return ProjectConfig{}, nil
		}
		return ProjectConfig{}, errors.Wrap(err, "reading project config file")
	}

	var pc ProjectConfig
	if err := yaml.Unmarshal(data, &pc); err != nil {
		return ProjectConfig{}, errors.Wrap(err, "parsing project config file")
	}
	return pc, nil
}
