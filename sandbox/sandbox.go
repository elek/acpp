package sandbox

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

//go:embed config.yaml
var embeddedBwrapConfig []byte

// Sandbox wraps command execution, optionally inside a bubblewrap sandbox.
type Sandbox interface {
	// Wrap returns the command and args to execute, wrapping the given
	// command+args with sandbox if configured. env is passed through.
	Wrap(command string, args []string) (string, []string)
}

// BwrapConfig represents a single fragment in the bwrap config YAML.
type BwrapConfig struct {
	Extend []string          `yaml:"extend"`
	ROBind []string          `yaml:"ro-bind"`
	Bind   []string          `yaml:"bind"`
	Env    map[string]string `yaml:"env"`
}

// noneSandbox passes commands through without wrapping.
type noneSandbox struct{}

func (n *noneSandbox) Wrap(command string, args []string) (string, []string) {
	return command, args
}

// NewNoneSandbox returns a Sandbox that does not wrap commands.
func NewNoneSandbox() Sandbox {
	return &noneSandbox{}
}

// ResolveSandbox creates a Sandbox based on the sandbox setting string.
// sandboxType is the type ("bbwrap", "bwrap", "sandbox", or "none"; empty defaults to bbwrap).
// profiles is a comma-separated list of additional profiles.
// cwd is the working directory.
// configPaths are optional additional config files; if empty, DefaultBwrapConfigPaths is used.
func ResolveSandbox(sandboxType string, profiles string, cwd string, configPaths ...string) (Sandbox, error) {
	if sandboxType == "none" {
		return NewNoneSandbox(), nil
	}

	if sandboxType == "" {
		sandboxType = "bbwrap"
	}

	if sandboxType != "bbwrap" && sandboxType != "bwrap" && sandboxType != "sandbox" {
		return nil, fmt.Errorf("unknown sandbox type: %s", sandboxType)
	}

	// The user override (~/.config/acpp/bbwrap.yaml) is applied first, then any
	// explicitly-passed configPaths take precedence over it.
	overlays := configPaths
	if userPath := userConfigPath(); userPath != "" {
		if _, err := os.Stat(userPath); err == nil {
			overlays = append([]string{userPath}, configPaths...)
		}
	}

	fragments, err := loadFragments(overlays...)
	if err != nil {
		return nil, err
	}

	var profileList []string
	if profiles != "" {
		for _, p := range strings.Split(profiles, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				profileList = append(profileList, p)
			}
		}
	}

	return NewBwrapSandbox("sandbox", profileList, cwd, fragments)
}

// loadFragments builds the bwrap fragment set. The embedded config is always
// the base layer; then each configPath is overlaid in order. Fragments from
// later sources fully replace earlier fragments with the same name.
func loadFragments(configPaths ...string) (map[string]*BwrapConfig, error) {
	allFragments := make(map[string]*BwrapConfig)

	embedded, err := parseBwrapConfig(embeddedBwrapConfig)
	if err != nil {
		return nil, fmt.Errorf("parsing embedded config: %w", err)
	}
	for k, v := range embedded {
		allFragments[k] = v
	}

	for _, path := range configPaths {
		fragments, err := loadBwrapConfig(path)
		if err != nil {
			return nil, fmt.Errorf("loading bwrap config %s: %w", path, err)
		}
		for k, v := range fragments {
			allFragments[k] = v
		}
	}

	return allFragments, nil
}

// LookupBwrap checks if bwrap is available on the system.
func LookupBwrap() error {
	_, err := exec.LookPath("bwrap")
	return err
}
