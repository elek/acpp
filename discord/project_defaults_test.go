package discord

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/elek/acpp/config"
	"github.com/stretchr/testify/require"
)

// writeExecutable creates an executable file named name in dir and returns its path.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755))
	return path
}

func TestResolveProjectDefaults_EmptyFileUsesDefaults(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Sandbox: "bbwrap"}}

	agent, sandboxType, profiles := resolveProjectDefaults(config.ProjectConfig{}, "default-agent", cfg)
	require.Equal(t, "default-agent", agent)
	require.Equal(t, "bbwrap", sandboxType)
	require.Empty(t, profiles)
}

func TestResolveProjectDefaults_FileAgentOverridesDefault(t *testing.T) {
	cfg := &config.Config{}

	agent, _, _ := resolveProjectDefaults(config.ProjectConfig{Agent: "project-agent"}, "default-agent", cfg)
	require.Equal(t, "project-agent", agent)
}

func TestResolveProjectDefaults_FileSandboxOverridesGlobalDefault(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Sandbox: "bbwrap"}}

	pc := config.ProjectConfig{Sandbox: config.ProjectSandbox{Name: "none", Profiles: "ssh,systemd"}}
	_, sandboxType, profiles := resolveProjectDefaults(pc, "default-agent", cfg)
	require.Equal(t, "none", sandboxType)
	require.Equal(t, "ssh,systemd", profiles)
}

func TestResolveProjectDefaults_AgentRunsThroughResolveAgent(t *testing.T) {
	dir := t.TempDir()
	bin := writeExecutable(t, dir, "my-agent")
	cfg := &config.Config{AgentPath: []string{dir}}

	agent, _, _ := resolveProjectDefaults(config.ProjectConfig{Agent: "my-agent --flag"}, "default-agent", cfg)
	require.Equal(t, bin+" --flag", agent)
}
