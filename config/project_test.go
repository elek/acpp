package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadProject_MissingFileReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()

	pc, err := LoadProject(dir)
	require.NoError(t, err)
	require.Equal(t, ProjectConfig{}, pc)
}

func TestLoadProject_FullFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, `
agent: claude-code-acp
sandbox:
  name: bbwrap
  profiles: ssh,systemd
`)

	pc, err := LoadProject(dir)
	require.NoError(t, err)
	require.Equal(t, "claude-code-acp", pc.Agent)
	require.Equal(t, "bbwrap", pc.Sandbox.Name)
	require.Equal(t, "ssh,systemd", pc.Sandbox.Profiles)
}

func TestLoadProject_PartialFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "agent: my-agent\n")

	pc, err := LoadProject(dir)
	require.NoError(t, err)
	require.Equal(t, "my-agent", pc.Agent)
	require.Empty(t, pc.Sandbox.Name)
	require.Empty(t, pc.Sandbox.Profiles)
}

func TestLoadProject_MalformedYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "agent: [unterminated\n")

	_, err := LoadProject(dir)
	require.Error(t, err)
}

func write(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".acpp.yaml"), []byte(content), 0o644))
}
