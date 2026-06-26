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
