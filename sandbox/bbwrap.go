package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// bwrapSandbox wraps commands with bubblewrap (bwrap).
type bwrapSandbox struct {
	bwrapArgs []string
	cwd       string
}

// NewBwrapSandbox creates a Sandbox that wraps commands with bubblewrap.
// name is the fragment name to resolve from config (e.g. "sandbox").
// profiles are additional fragment names to merge (e.g. ["ssh", "systemd"]).
// cwd is the working directory to bind into the sandbox.
// allFragments is the merged fragment set (see loadFragments).
func NewBwrapSandbox(name string, profiles []string, cwd string, allFragments map[string]*BwrapConfig) (Sandbox, error) {
	if _, ok := allFragments[name]; !ok {
		return nil, fmt.Errorf("no config fragment %q found", name)
	}

	// Resolve fragment with inheritance
	resolved, err := resolveFragment(name, allFragments, nil)
	if err != nil {
		return nil, err
	}

	// Merge additional profiles
	for _, profile := range profiles {
		if profile == "" {
			continue
		}
		if _, ok := allFragments[profile]; !ok {
			return nil, fmt.Errorf("no config fragment %q found (profile)", profile)
		}
		profileResolved, err := resolveFragment(profile, allFragments, nil)
		if err != nil {
			return nil, err
		}
		resolved = mergeResolved(resolved, profileResolved)
	}

	// Build bwrap args
	args, err := buildBwrapArgs(name, resolved, cwd)
	if err != nil {
		return nil, err
	}

	return &bwrapSandbox{bwrapArgs: args, cwd: cwd}, nil
}

func (b *bwrapSandbox) Wrap(command string, args []string) (string, []string) {
	allArgs := make([]string, 0, len(b.bwrapArgs)+1+len(args))
	allArgs = append(allArgs, b.bwrapArgs...)
	allArgs = append(allArgs, command)
	allArgs = append(allArgs, args...)
	return "/usr/bin/bwrap", allArgs
}

// resolvedConfig holds the flattened result of resolving a fragment tree.
type resolvedConfig struct {
	roBind []string
	bind   []string
	env    map[string]string
}

func mergeResolved(base, overlay resolvedConfig) resolvedConfig {
	result := resolvedConfig{
		roBind: append(append([]string{}, base.roBind...), overlay.roBind...),
		bind:   append(append([]string{}, base.bind...), overlay.bind...),
		env:    make(map[string]string),
	}
	for k, v := range base.env {
		result.env[k] = v
	}
	for k, v := range overlay.env {
		result.env[k] = v
	}
	return result
}

// resolveFragment recursively resolves a fragment, following "extend" references.
// visited tracks already-resolved fragments to prevent circular dependencies.
func resolveFragment(name string, fragments map[string]*BwrapConfig, visited map[string]bool) (resolvedConfig, error) {
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[name] {
		return resolvedConfig{}, nil // circular dependency guard
	}
	visited[name] = true

	frag, ok := fragments[name]
	if !ok {
		return resolvedConfig{}, fmt.Errorf("extended fragment %q not found", name)
	}

	var result resolvedConfig
	result.env = make(map[string]string)

	// Process extends first (depth-first)
	for _, ext := range frag.Extend {
		child, err := resolveFragment(ext, fragments, visited)
		if err != nil {
			return resolvedConfig{}, err
		}
		result = mergeResolved(result, child)
	}

	// Collect this fragment's own entries
	result.roBind = append(result.roBind, frag.ROBind...)
	result.bind = append(result.bind, frag.Bind...)
	for k, v := range frag.Env {
		result.env[k] = v
	}

	return result, nil
}

// resolvePath resolves a path relative to $HOME if it's not absolute.
func resolvePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path)
}

// parseBindEntry parses a bind entry which may be "src:dest" or just "path".
func parseBindEntry(entry string) (src, dest string) {
	if idx := strings.Index(entry, ":"); idx >= 0 {
		src = entry[:idx]
		dest = entry[idx+1:]
	} else {
		src = entry
		dest = entry
	}
	src = resolvePath(src)
	dest = resolvePath(dest)
	return
}

// buildBwrapArgs constructs the bwrap command-line arguments from resolved config.
func buildBwrapArgs(name string, resolved resolvedConfig, cwd string) ([]string, error) {
	var args []string

	// Static setup
	args = append(args, "--tmpfs", "/tmp")
	args = append(args, "--dev", "/dev")
	args = append(args, "--proc", "/proc")
	args = append(args, "--hostname", name, "--unshare-uts")

	// Hostname and hosts files
	hostnameFile, err := writeTempFile(name)
	if err != nil {
		return nil, fmt.Errorf("write hostname file: %w", err)
	}
	hostsFile, err := writeTempFile("127.0.0.1 localhost " + name)
	if err != nil {
		return nil, fmt.Errorf("write hosts file: %w", err)
	}
	args = append(args, "--ro-bind", hostnameFile, "/etc/hostname")
	args = append(args, "--ro-bind", hostsFile, "/etc/hosts")

	// Config-driven ro-binds
	for _, entry := range resolved.roBind {
		src, dest := parseBindEntry(entry)
		args = append(args, "--ro-bind", src, dest)
	}

	// Config-driven binds
	for _, entry := range resolved.bind {
		src, dest := parseBindEntry(entry)
		args = append(args, "--bind", src, dest)
	}

	// Config-driven environment variables
	for key, value := range resolved.env {
		args = append(args, "--setenv", key, value)
	}

	// Current working directory
	args = append(args, "--bind", cwd, cwd)

	return args, nil
}

// writeTempFile creates a temp file with the given content and returns its path.
// The file is not cleaned up — it persists for the lifetime of the process
// (bwrap reads it at exec time).
func writeTempFile(content string) (string, error) {
	f, err := os.CreateTemp("", "acpp-sandbox-*")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// parseBwrapConfig parses YAML bytes into bwrap fragment definitions.
func parseBwrapConfig(data []byte) (map[string]*BwrapConfig, error) {
	var fragments map[string]*BwrapConfig
	if err := yaml.Unmarshal(data, &fragments); err != nil {
		return nil, err
	}
	return fragments, nil
}

// loadBwrapConfig loads a YAML file containing bwrap fragment definitions.
func loadBwrapConfig(path string) (map[string]*BwrapConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fragments, err := parseBwrapConfig(data)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return fragments, nil
}

// userConfigPath returns the path to the user's bwrap config override,
// ~/.config/acpp/bbwrap.yaml (honoring XDG_CONFIG_HOME). Returns "" if the
// config directory cannot be determined.
func userConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "acpp", "bbwrap.yaml")
}
