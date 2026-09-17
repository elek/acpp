package sandbox

import "sort"

// rootFragment is the fragment name ResolveSandbox resolves as the sandbox base.
// It is never offered as a selectable profile: every sandbox already includes it.
const rootFragment = "sandbox"

// syntheticBindFragments are the fragment names ResolveSandbox injects for
// caller-supplied binds and env whitelist entries. They exist only for the
// duration of one resolve and are never user-selectable.
var syntheticBindFragments = map[string]bool{
	roBindFragment:  true,
	rwBindFragment:  true,
	passEnvFragment: true,
}

// ListProfiles returns the sandbox profile names a project may select, sorted.
// The fragment set is the same one ResolveSandbox uses: the embedded config.yaml
// overlaid with the user's ~/.config/acpp/bbwrap.yaml, then any configPaths.
// This means anything ListProfiles offers is a name ResolveSandbox can resolve.
func ListProfiles(configPaths ...string) ([]string, error) {
	fragments, err := loadFragments(configOverlays(configPaths...)...)
	if err != nil {
		return nil, err
	}
	return selectableProfiles(fragments), nil
}

// selectableProfiles filters a fragment set down to the names worth offering as
// profiles: everything except the root fragment, the synthetic bind fragments,
// and any fragment reachable through another fragment's extend (those are
// building blocks like "base"/"etc", already pulled in by whoever extends them).
//
// A fragment that is both extended by another and meaningful to select directly
// is hidden by this rule. That is a deliberate trade: the alternative, listing
// building blocks, clutters the picker with entries that do nothing on their own.
func selectableProfiles(fragments map[string]*BwrapConfig) []string {
	extended := make(map[string]bool)
	for _, frag := range fragments {
		for _, name := range frag.Extend {
			extended[name] = true
		}
	}

	profiles := make([]string, 0, len(fragments))
	for name := range fragments {
		if name == rootFragment || extended[name] || syntheticBindFragments[name] {
			continue
		}
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	return profiles
}
