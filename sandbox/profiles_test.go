package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectableProfiles_HidesRootAndBuildingBlocks(t *testing.T) {
	fragments := map[string]*BwrapConfig{
		"base":    {ROBind: []string{"/bin"}},
		"etc":     {ROBind: []string{"/etc/passwd"}},
		"sandbox": {Extend: []string{"base", "etc"}},
		"ssh":     {ROBind: []string{".ssh"}},
		"docker":  {Bind: []string{"/var/run/docker.sock"}},
	}

	// "sandbox" is the root fragment; "base" and "etc" are only reachable
	// through another fragment's extend, so none of the three is offered.
	require.Equal(t, []string{"docker", "ssh"}, selectableProfiles(fragments))
}

func TestSelectableProfiles_SortedAndUnique(t *testing.T) {
	fragments := map[string]*BwrapConfig{
		"sandbox": {},
		"ssh":     {},
		"android": {},
		"music":   {},
		"docker":  {},
	}

	require.Equal(t, []string{"android", "docker", "music", "ssh"}, selectableProfiles(fragments))
}

func TestSelectableProfiles_ExtendedFragmentHiddenEvenIfSelectable(t *testing.T) {
	fragments := map[string]*BwrapConfig{
		"sandbox":    {},
		"gpu-common": {ROBind: []string{"/sys"}},
		"nvidia":     {Extend: []string{"gpu-common"}},
	}

	// Documented limitation: a fragment referenced by another's extend is
	// treated as a building block and not offered, even though selecting it
	// directly would work.
	require.Equal(t, []string{"nvidia"}, selectableProfiles(fragments))
}

func TestSelectableProfiles_SyntheticBindFragmentsHidden(t *testing.T) {
	// ResolveSandbox injects caller binds as synthetic fragments. If a caller
	// ever lists profiles from a fragment set that has them, they must not show.
	fragments := map[string]*BwrapConfig{
		"sandbox":     {},
		"ssh":         {},
		"__robinds__": {ROBind: []string{"/tmp/x"}},
		"__rwbinds__": {Bind: []string{"/tmp/y"}},
	}

	require.Equal(t, []string{"ssh"}, selectableProfiles(fragments))
}

// isolateUserConfig points userConfigPath() at an empty directory so ListProfiles
// sees only the embedded config plus the test's own overlay, never whatever the
// developer happens to have in ~/.config/acpp/bbwrap.yaml.
func isolateUserConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestListProfiles_IncludesEmbeddedAndOverlayProfiles(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
android:
  ro-bind:
    - /home/test/prog/android
music:
  bind:
    - /home/test/music
`), 0o644))

	profiles, err := ListProfiles(configPath)
	require.NoError(t, err)
	// The embedded config supplies docker/nvidia/ssh/systemd; the overlay's own
	// fragments must appear alongside them.
	require.Subset(t, profiles, []string{"android", "music", "docker", "nvidia", "ssh", "systemd"})
	require.NotContains(t, profiles, "sandbox")
	require.NotContains(t, profiles, "base")
	require.NotContains(t, profiles, "etc")
}

func TestListProfiles_RedefinedFragmentNotDuplicated(t *testing.T) {
	isolateUserConfig(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(`
docker:
  bind:
    - /var/run/docker.sock
`), 0o644))

	profiles, err := ListProfiles(configPath)
	require.NoError(t, err)
	seen := 0
	for _, p := range profiles {
		if p == "docker" {
			seen++
		}
	}
	require.Equal(t, 1, seen)
}

func TestListProfiles_MalformedOverlayIsAnError(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("not: [valid"), 0o644))

	_, err := ListProfiles(configPath)
	require.Error(t, err)
}
