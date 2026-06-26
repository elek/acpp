package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// ToolPermissionAction defines what to do when a rule matches.
type ToolPermissionAction string

const (
	// ToolPermissionDeny blocks the tool call entirely.
	ToolPermissionDeny ToolPermissionAction = "deny"
	// ToolPermissionAsk prompts the user for approval before allowing.
	ToolPermissionAsk ToolPermissionAction = "ask"

	ToolPermissionApprove ToolPermissionAction = "approve"
)

// ToolPermissionRule defines a single permission rule for tool calls.
// If Title is set, it must glob-match the tool title (case-insensitive).
// If Input is set, ALL entries must glob-match the corresponding named
// input parameter (case-insensitive). Both Title and Input must match
// for the rule to fire. First matching rule wins.
type ToolPermissionRule struct {
	Title  string               `yaml:"title,omitempty"`
	Action ToolPermissionAction `yaml:"action"`
	Input  map[string]string    `yaml:"input,omitempty"`
}

// OTLPTLSConfig holds TLS settings for the OTLP exporter.
type OTLPTLSConfig struct {
	Insecure bool `yaml:"insecure"`
}

// OTLPConfig holds OpenTelemetry OTLP exporter configuration.
type OTLPConfig struct {
	Endpoint string            `yaml:"endpoint"`
	Headers  map[string]string `yaml:"headers"`
	TLS      OTLPTLSConfig     `yaml:"tls"`
}

// DatabaseConfig holds PostgreSQL connection configuration.
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// DesktopConfig holds desktop application settings.
type DesktopConfig struct {
	Zoom float64 `yaml:"zoom,omitempty"`
}

// Config holds application configuration
type Config struct {
	Database        DatabaseConfig  `yaml:"database"`
	DiscordToken    string          `yaml:"discord_token"`
	WebAddr         string          `yaml:"web_addr"`
	SandboxDir      string          `yaml:"sandbox_dir"`
	Defaults        Defaults        `yaml:"defaults"`
	AgentPath       []string        `yaml:"agent_path"`
	SearchPath      []string        `yaml:"search_path"`
	ToolPermissions ToolPermissions `yaml:"tool_permissions"`
	ScheduledJobs   []ScheduledJob  `yaml:"scheduled_jobs"`
	OTLP            OTLPConfig      `yaml:"otlp"`
	Desktop         DesktopConfig   `yaml:"desktop,omitempty"`
}

type ToolPermissions struct {
	Rules   []ToolPermissionRule `yaml:"rules"`
	Default ToolPermissionAction `yaml:"default"`
}

// Defaults holds default values for session creation
type Defaults struct {
	Agent        string   `yaml:"agent"`
	Sandbox      string   `yaml:"sandbox"`
	EnvWhitelist []string `yaml:"env_whitelist"`
}

// ScheduledJob defines a cron-triggered prompt execution.
type ScheduledJob struct {
	Name            string   `yaml:"name"`
	Prompt          string   `yaml:"prompt"`
	Schedule        string   `yaml:"schedule"`
	Dir             string   `yaml:"dir"`
	Agent           string   `yaml:"agent,omitempty"`
	Sandbox         string   `yaml:"sandbox,omitempty"`
	SandboxProfiles string   `yaml:"sandbox_profiles,omitempty"`
	Permission      string   `yaml:"permission,omitempty"`
	Hooks           string   `yaml:"hooks,omitempty"`
	Env             []string `yaml:"env,omitempty"`
	ReuseSession    bool     `yaml:"reuse_session,omitempty"`
}

// MatchToolPermission finds the first matching permission rule for the given tool title and input.
// Returns the matching rule's action, or empty string if no rule matches (auto-approve).
// Title and input values are matched case-insensitively using filepath.Match glob patterns.
func (c *Config) MatchToolPermission(title string, input map[string]string) ToolPermissionAction {
	titleLower := strings.ToLower(title)
	for _, rule := range c.ToolPermissions.Rules {
		if !matchRule(rule, titleLower, input) {
			continue
		}
		return rule.Action
	}
	return ""
}

// matchRule checks if a single rule matches the given title and input.
func matchRule(rule ToolPermissionRule, titleLower string, input map[string]string) bool {
	// At least one of Title or Input must be specified
	if rule.Title == "" && len(rule.Input) == 0 {
		return false
	}

	// If Title is set, it must glob-match
	if rule.Title != "" {
		if !wildcardMatch(strings.ToLower(rule.Title), titleLower) {
			return false
		}
	}

	// If Input is set, ALL entries must glob-match
	for paramName, pattern := range rule.Input {
		value, ok := input[paramName]
		if !ok {
			return false
		}
		if !wildcardMatch(strings.ToLower(pattern), strings.ToLower(value)) {
			return false
		}
	}

	return true
}

// wildcardMatch matches a pattern against a string where * matches any
// sequence of characters (including path separators, unlike filepath.Match).
func wildcardMatch(pattern, s string) bool {
	// Simple case: no wildcards
	if !strings.Contains(pattern, "*") {
		return pattern == s
	}

	parts := strings.Split(pattern, "*")

	// Check prefix (before first *)
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]

	// Check each middle segment
	for i := 1; i < len(parts)-1; i++ {
		idx := strings.Index(s, parts[i])
		if idx < 0 {
			return false
		}
		s = s[idx+len(parts[i]):]
	}

	// Check suffix (after last *)
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// DefaultConfig returns a Config with hardcoded defaults
func DefaultConfig() *Config {
	return &Config{
		Defaults: Defaults{
			Agent: "claude-code-acp",
		},
	}
}

// ResolveAgent resolves a relative agent command using AgentPath.
// If the agent command (first word) is already absolute or AgentPath is empty,
// it is returned unchanged. Otherwise, each directory in AgentPath is checked
// for an executable file matching the command name.
func (c *Config) ResolveAgent(agent string) string {
	if len(c.AgentPath) == 0 {
		return agent
	}
	fields := strings.Fields(agent)
	if len(fields) == 0 {
		return agent
	}
	cmd := fields[0]
	if filepath.IsAbs(cmd) || strings.Contains(cmd, string(filepath.Separator)) {
		return agent
	}
	for _, dir := range c.AgentPath {
		candidate := filepath.Join(dir, cmd)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			fields[0] = candidate
			return strings.Join(fields, " ")
		}
	}
	return agent
}

// configPath returns the path to the config file.
func configPath() (string, error) {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "acpp", "config.yaml"), nil
}

// Save writes configuration to ~/.config/acpp/config.yaml.
func (c *Config) Save() error {
	path, err := configPath()
	if err != nil {
		return errors.Wrap(err, "determining config path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return errors.Wrap(err, "creating config directory")
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return errors.Wrap(err, "marshalling config")
	}
	return errors.Wrap(os.WriteFile(path, data, 0o644), "writing config file")
}

// Load reads configuration from ~/.config/acpp/config.yaml
// If the file doesn't exist, returns default config.
// If XDG_CONFIG_HOME is set, uses that instead of ~/.config
func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return DefaultConfig(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, errors.Wrap(err, "reading config file")
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, errors.Wrap(err, "parsing config file")
	}

	return cfg, nil
}
