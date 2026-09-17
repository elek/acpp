package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNoneSandboxFilterEnvPassesEverything(t *testing.T) {
	sb := NewNoneSandbox()
	host := []string{"PATH=/bin", "AWS_SECRET_ACCESS_KEY=hunter2"}
	require.Equal(t, host, sb.FilterEnv(host))
}

func TestFilterEnvDefaultWhitelist(t *testing.T) {
	sb := testSandbox(t, "sandbox:\n  ro-bind:\n    - /bin\n")

	got := sb.FilterEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"LANG=en_US.UTF-8",
		"LC_TIME=hu_HU.UTF-8",
		"ANTHROPIC_API_KEY=sk-ant-xyz",
		"AWS_SECRET_ACCESS_KEY=hunter2",
		"GITHUB_TOKEN=ghp_xyz",
		"LD_PRELOAD=/tmp/evil.so",
		"TMPDIR=/home/test/scratch",
	})

	require.ElementsMatch(t, []string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"LANG=en_US.UTF-8",
		"LC_TIME=hu_HU.UTF-8",
		"ANTHROPIC_API_KEY=sk-ant-xyz",
	}, got)
}

func TestFilterEnvFragmentAddsNames(t *testing.T) {
	sb := testSandbox(t, `
sandbox:
  ro-bind:
    - /bin
`, "extra")

	got := sb.FilterEnv([]string{"MY_VAR=1", "OTHER=2"})
	require.Equal(t, []string{"MY_VAR=1"}, got)
}

func TestFilterEnvGlob(t *testing.T) {
	sb := testSandbox(t, `
sandbox:
  ro-bind:
    - /bin
  pass-env:
    - FOO_*
`)

	got := sb.FilterEnv([]string{"FOO_A=1", "FOO_=2", "FOO=3", "BAR_A=4"})
	require.ElementsMatch(t, []string{"FOO_A=1", "FOO_=2"}, got)
}

func TestFilterEnvDenyOverridesAllow(t *testing.T) {
	sb := testSandbox(t, `
sandbox:
  ro-bind:
    - /bin
  pass-env:
    - -ANTHROPIC_API_KEY
`)

	got := sb.FilterEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk", "ANTHROPIC_BASE_URL=http://x"})
	require.ElementsMatch(t, []string{"PATH=/bin", "ANTHROPIC_BASE_URL=http://x"}, got)
}

func TestFilterEnvDenyGlob(t *testing.T) {
	sb := testSandbox(t, `
sandbox:
  ro-bind:
    - /bin
  pass-env:
    - -ANTHROPIC_*
`)

	got := sb.FilterEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=sk", "ANTHROPIC_BASE_URL=http://x"})
	require.Equal(t, []string{"PATH=/bin"}, got)
}

// A user override that fully replaces the "sandbox" fragment must not strip
// PATH/HOME and leave the agent unable to exec: the Go baseline always applies.
func TestFilterEnvBaselineSurvivesFragmentReplacement(t *testing.T) {
	sb := testSandbox(t, `
sandbox:
  bind:
    - /tmp/custom
`)

	got := sb.FilterEnv([]string{"PATH=/bin", "HOME=/home/test", "GITHUB_TOKEN=x"})
	require.ElementsMatch(t, []string{"PATH=/bin", "HOME=/home/test"}, got)
}

func TestResolveSandboxPassEnvOverride(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("sandbox:\n  ro-bind:\n    - /bin\n"), 0o644))

	sb, err := ResolveSandbox("bbwrap", "", "/tmp", nil, nil,
		[]string{"GITHUB_TOKEN", "-ANTHROPIC_API_KEY"}, configPath)
	require.NoError(t, err)

	got := sb.FilterEnv([]string{"PATH=/bin", "GITHUB_TOKEN=ghp", "ANTHROPIC_API_KEY=sk"})
	require.ElementsMatch(t, []string{"PATH=/bin", "GITHUB_TOKEN=ghp"}, got)
}

// TMPDIR is deliberately not inherited (the sandbox mounts a fresh tmpfs at
// /tmp), so it is pinned with --setenv instead.
func TestBwrapArgsPinTmpdir(t *testing.T) {
	sb := testSandbox(t, "sandbox:\n  ro-bind:\n    - /bin\n")
	_, args := sb.Wrap("agent", nil)
	require.True(t, hasSetenv(args, "TMPDIR", "/tmp"), "expected --setenv TMPDIR /tmp in %v", args)
}

func TestEmbeddedProfilePassEnv(t *testing.T) {
	fragments, err := loadFragments()
	require.NoError(t, err)

	// The systemd profile binds /run/user/1000, so it carries the vars that
	// make that bind usable.
	require.Contains(t, fragments["systemd"].PassEnv, "XDG_RUNTIME_DIR")
	require.Contains(t, fragments["systemd"].PassEnv, "DBUS_SESSION_BUS_ADDRESS")

	// x11 exists as an opt-in profile pairing the socket binds with DISPLAY.
	require.Contains(t, fragments, "x11")
	require.Contains(t, fragments["x11"].PassEnv, "DISPLAY")
}

func hasSetenv(args []string, key, value string) bool {
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--setenv" && args[i+1] == key && args[i+2] == value {
			return true
		}
	}
	return false
}

// testSandbox builds a Sandbox from an inline config, optionally merging the
// named profiles. The config always gains an "extra" fragment used by the
// profile tests.
func testSandbox(t *testing.T, config string, profiles ...string) Sandbox {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	config += "\nextra:\n  pass-env:\n    - MY_VAR\n"
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o644))

	fragments, err := loadFragments(configPath)
	require.NoError(t, err)
	sb, err := NewBwrapSandbox("sandbox", profiles, "/tmp", fragments)
	require.NoError(t, err)
	return sb
}
