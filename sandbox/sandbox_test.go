package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoneSandbox(t *testing.T) {
	sb := NewNoneSandbox()
	cmd, args := sb.Wrap("echo", []string{"hello"})
	require.Equal(t, "echo", cmd)
	require.Equal(t, []string{"hello"}, args)
}

func TestResolveSandboxNone(t *testing.T) {
	sb, err := ResolveSandbox("none", "", "/tmp")
	require.NoError(t, err)
	cmd, args := sb.Wrap("echo", []string{"hello"})
	require.Equal(t, "echo", cmd)
	require.Equal(t, []string{"hello"}, args)
}

func TestResolveSandboxEmpty(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(`
sandbox:
  ro-bind:
    - /bin
`), 0o644)
	require.NoError(t, err)

	sb, err := ResolveSandbox("", "", "/tmp", configPath)
	require.NoError(t, err)
	cmd, _ := sb.Wrap("echo", []string{"hello"})
	// Empty sandbox type should default to bbwrap, not none
	require.Equal(t, "/usr/bin/bwrap", cmd)
}

func TestResolveSandboxUnknown(t *testing.T) {
	_, err := ResolveSandbox("unknown", "", "/tmp")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown sandbox type")
}

func TestLoadBwrapConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(`
base:
  ro-bind:
    - /bin
    - /usr/bin
sandbox:
  extend:
    - base
  bind:
    - /tmp/test
  env:
    FOO: bar
`), 0o644)
	require.NoError(t, err)

	fragments, err := loadBwrapConfig(configPath)
	require.NoError(t, err)
	require.Contains(t, fragments, "base")
	require.Contains(t, fragments, "sandbox")
	require.Equal(t, []string{"/bin", "/usr/bin"}, fragments["base"].ROBind)
	require.Equal(t, []string{"base"}, fragments["sandbox"].Extend)
	require.Equal(t, map[string]string{"FOO": "bar"}, fragments["sandbox"].Env)
}

func TestResolveFragment(t *testing.T) {
	fragments := map[string]*BwrapConfig{
		"base": {
			ROBind: []string{"/bin", "/usr/bin"},
		},
		"extra": {
			Bind: []string{"/tmp/extra"},
			Env:  map[string]string{"KEY": "value"},
		},
		"sandbox": {
			Extend: []string{"base", "extra"},
			ROBind: []string{"/lib"},
			Bind:   []string{"/tmp/sandbox"},
		},
	}

	resolved, err := resolveFragment("sandbox", fragments, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"/bin", "/usr/bin", "/lib"}, resolved.roBind)
	require.Equal(t, []string{"/tmp/extra", "/tmp/sandbox"}, resolved.bind)
	require.Equal(t, "value", resolved.env["KEY"])
}

func TestResolveFragmentCircular(t *testing.T) {
	fragments := map[string]*BwrapConfig{
		"a": {Extend: []string{"b"}},
		"b": {Extend: []string{"a"}},
	}
	// Should not infinite loop
	_, err := resolveFragment("a", fragments, nil)
	require.NoError(t, err)
}

func TestBwrapSandboxWrap(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(`
sandbox:
  ro-bind:
    - /bin
  env:
    FOO: bar
`), 0o644)
	require.NoError(t, err)

	fragments, err := loadFragments(configPath)
	require.NoError(t, err)
	sb, err := NewBwrapSandbox("sandbox", nil, "/tmp", fragments)
	require.NoError(t, err)

	cmd, args := sb.Wrap("echo", []string{"hello"})
	require.Equal(t, "/usr/bin/bwrap", cmd)
	// Should contain echo and hello at the end
	require.Equal(t, "echo", args[len(args)-2])
	require.Equal(t, "hello", args[len(args)-1])
}

func TestBwrapSandboxWithProfiles(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(configPath, []byte(`
sandbox:
  ro-bind:
    - /bin
ssh:
  ro-bind:
    - /home/test/.ssh
  env:
    SSH_AUTH_SOCK: /tmp/ssh.sock
`), 0o644)
	require.NoError(t, err)

	fragments, err := loadFragments(configPath)
	require.NoError(t, err)
	sb, err := NewBwrapSandbox("sandbox", []string{"ssh"}, "/tmp", fragments)
	require.NoError(t, err)

	cmd, args := sb.Wrap("agent", []string{})
	require.Equal(t, "/usr/bin/bwrap", cmd)

	// Should contain both /bin (from sandbox) and .ssh (from ssh profile)
	hasSSH := false
	for _, arg := range args {
		if arg == "/home/test/.ssh" {
			hasSSH = true
		}
	}
	require.True(t, hasSSH, "expected ssh profile ro-bind in bwrap args")
}

func TestEmbeddedFragmentsAlwaysPresent(t *testing.T) {
	// With no config files at all, the embedded fragments must be available.
	fragments, err := loadFragments()
	require.NoError(t, err)
	for _, name := range []string{"base", "etc", "sandbox", "ssh", "systemd", "docker"} {
		require.Contains(t, fragments, name, "embedded fragment %q must be present", name)
	}
}

func TestEmbeddedSandboxDefaultsToBaseEtc(t *testing.T) {
	fragments, err := loadFragments()
	require.NoError(t, err)
	require.Equal(t, []string{"base", "etc"}, fragments["sandbox"].Extend)

	// Resolving sandbox yields exactly the union of base and etc ro-binds.
	resolved, err := resolveFragment("sandbox", fragments, nil)
	require.NoError(t, err)
	expected := append(append([]string{}, fragments["base"].ROBind...), fragments["etc"].ROBind...)
	require.Equal(t, expected, resolved.roBind)
}

func TestUserConfigOverlayReplacesFragment(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "bbwrap.yaml")
	err := os.WriteFile(overlay, []byte(`
sandbox:
  extend:
    - base
  bind:
    - /tmp/custom
`), 0o644)
	require.NoError(t, err)

	fragments, err := loadFragments(overlay)
	require.NoError(t, err)

	// The embedded base fragment is still present (overlay does not remove it).
	require.Contains(t, fragments, "base")
	require.NotEmpty(t, fragments["base"].ROBind)

	// The sandbox fragment is fully replaced by the overlay (not merged).
	require.Equal(t, []string{"base"}, fragments["sandbox"].Extend)
	require.Equal(t, []string{"/tmp/custom"}, fragments["sandbox"].Bind)
	require.Empty(t, fragments["sandbox"].ROBind)
}

func TestParseBindEntry(t *testing.T) {
	src, dest := parseBindEntry("/foo/bar")
	require.Equal(t, "/foo/bar", src)
	require.Equal(t, "/foo/bar", dest)

	src, dest = parseBindEntry("/foo:/bar")
	require.Equal(t, "/foo", src)
	require.Equal(t, "/bar", dest)
}
