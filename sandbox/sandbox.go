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
	// command+args with sandbox if configured.
	Wrap(command string, args []string) (string, []string)

	// FilterEnv returns the subset of hostEnv (KEY=VALUE entries) the sandboxed
	// process may inherit. bwrap passes its own environment to the child, so
	// restricting the environment of the bwrap process itself is what enforces
	// the whitelist — deliberately not --clearenv plus --setenv, which would put
	// secrets in the bwrap argv for any process to read from /proc.
	FilterEnv(hostEnv []string) []string
}

// BwrapConfig represents a single fragment in the bwrap config YAML.
type BwrapConfig struct {
	Extend  []string          `yaml:"extend"`
	ROBind  []string          `yaml:"ro-bind"`
	Bind    []string          `yaml:"bind"`
	DevBind []string          `yaml:"dev-bind"`
	Env     map[string]string `yaml:"env"`
	// PassEnv names host environment variables this fragment adds to the
	// inherited set, on top of defaultPassEnv. A trailing "*" is a prefix
	// wildcard; a leading "-" denies instead of allows.
	PassEnv []string `yaml:"pass-env"`
}

// noneSandbox passes commands through without wrapping.
type noneSandbox struct{}

func (n *noneSandbox) Wrap(command string, args []string) (string, []string) {
	return command, args
}

// FilterEnv passes the host environment through untouched: "none" means no
// isolation at all, so there is nothing to filter for.
func (n *noneSandbox) FilterEnv(hostEnv []string) []string {
	return hostEnv
}

// NewNoneSandbox returns a Sandbox that does not wrap commands.
func NewNoneSandbox() Sandbox {
	return &noneSandbox{}
}

// ResolveSandbox creates a Sandbox based on the sandbox setting string.
// sandboxType is the type ("bbwrap", "bwrap", "sandbox", or "none"; empty defaults to bbwrap).
// profiles is a comma-separated list of additional profiles.
// cwd is the working directory.
// roBinds are caller-supplied read-only bind entries ("src" or "src:dest",
// following parseBindEntry semantics) injected on top of the resolved
// fragments. They are ignored when sandboxType is "none".
// rwBinds are the read-write equivalent of roBinds (emitted as --bind), layered
// on top of roBinds; also ignored when sandboxType is "none".
// passEnv are caller-supplied environment whitelist entries (the per-project
// override) layered on top of the resolved fragments' pass-env; see envMatcher.
// configPaths are optional additional config files; if empty, DefaultBwrapConfigPaths is used.
func ResolveSandbox(sandboxType string, profiles string, cwd string, roBinds, rwBinds, passEnv []string, configPaths ...string) (Sandbox, error) {
	if sandboxType == "none" {
		return NewNoneSandbox(), nil
	}

	if sandboxType == "" {
		sandboxType = "bbwrap"
	}

	if sandboxType != "bbwrap" && sandboxType != "bwrap" && sandboxType != "sandbox" {
		return nil, fmt.Errorf("unknown sandbox type: %s", sandboxType)
	}

	fragments, err := loadFragments(configOverlays(configPaths...)...)
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

	// Inject caller-supplied read-only binds as a synthetic profile so they are
	// merged on top of the resolved base fragment (appended after its binds).
	if len(roBinds) > 0 {
		fragments[roBindFragment] = &BwrapConfig{ROBind: roBinds}
		profileList = append(profileList, roBindFragment)
	}

	// Read-write binds are the same, layered after the ro-binds.
	if len(rwBinds) > 0 {
		fragments[rwBindFragment] = &BwrapConfig{Bind: rwBinds}
		profileList = append(profileList, rwBindFragment)
	}

	// The per-project environment whitelist override, layered last. Order only
	// matters for readability: deny entries beat allow entries regardless.
	if len(passEnv) > 0 {
		fragments[passEnvFragment] = &BwrapConfig{PassEnv: passEnv}
		profileList = append(profileList, passEnvFragment)
	}

	return NewBwrapSandbox(rootFragment, profileList, cwd, fragments)
}

// roBindFragment, rwBindFragment and passEnvFragment are the synthetic fragment
// names used to layer caller-supplied settings on top of the resolved profiles.
const (
	roBindFragment  = "__robinds__"
	rwBindFragment  = "__rwbinds__"
	passEnvFragment = "__passenv__"
)

// configOverlays returns the overlay paths to layer over the embedded config:
// the user override (~/.config/acpp/bbwrap.yaml) first, then any explicitly
// passed configPaths, which take precedence over it.
func configOverlays(configPaths ...string) []string {
	if userPath := userConfigPath(); userPath != "" {
		if _, err := os.Stat(userPath); err == nil {
			return append([]string{userPath}, configPaths...)
		}
	}
	return configPaths
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
