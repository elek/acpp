package sandbox

import "strings"

// defaultPassEnv is the baseline set of host environment variables an agent
// inherits inside the sandbox. Everything not matched here (and not added by a
// profile's "pass-env" or the per-project override) is dropped, so secrets that
// happen to be exported in the shell acpp was started from — AWS_*, GITHUB_TOKEN,
// NPM_TOKEN, DATABASE_URL — never reach the agent.
//
// A trailing "*" matches any suffix. This list is applied unconditionally rather
// than being read from config.yaml, so a user override that fully replaces the
// "sandbox" fragment cannot accidentally strip PATH and leave the agent unable
// to exec anything. To *remove* an entry, deny it with a leading "-"
// (e.g. "-ANTHROPIC_API_KEY") from a profile or the per-project override.
var defaultPassEnv = []string{
	// Without these nothing runs.
	"PATH",
	"HOME",
	"USER",
	"LOGNAME",
	"SHELL",
	"TERM", // /usr/share/terminfo is ro-bound by the base fragment

	// Locale and timezone. Without them Python/node mangle non-ASCII output.
	"LANG",
	"LC_*",
	"TZ", // /etc/localtime is ro-bound by the etc fragment

	// If set on the host these point under $HOME, which is bound anyway.
	"XDG_CONFIG_HOME",
	"XDG_CACHE_HOME",
	"XDG_DATA_HOME",

	// Corporate networks.
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",

	// Custom CA bundles, for TLS-intercepting proxies.
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"NODE_EXTRA_CA_CERTS",
	"CURL_CA_BUNDLE",

	// The agent's own credentials. These are exactly what a whitelist exists to
	// block, but the agent acpp deliberately launches is the one process it is
	// reasonable to hand them to — and blocking them would break every setup
	// that authenticates by env var rather than ~/.claude. Other providers'
	// keys (OPENAI_API_KEY, GEMINI_API_KEY, ...) stay blocked unless a profile
	// or the project opts in.
	"ANTHROPIC_*",
	"CLAUDE_CODE_*",
	"CLAUDE_CONFIG_DIR",
}

// Deliberately excluded, for the record:
//
//   - AWS_*, GOOGLE_APPLICATION_CREDENTIALS, GITHUB_TOKEN, GH_TOKEN, NPM_TOKEN,
//     DOCKER_*, DATABASE_URL, ACPP_*, SUDO_* — the point of the whitelist.
//   - LD_PRELOAD, LD_LIBRARY_PATH — code injection, and the sandbox's libraries
//     are not the host's.
//   - TMPDIR — the sandbox mounts a fresh tmpfs at /tmp, so a host TMPDIR
//     pointing elsewhere would silently break. buildBwrapArgs pins it to /tmp.
//   - DISPLAY, WAYLAND_DISPLAY, XAUTHORITY — see the x11 profile.
//   - XDG_RUNTIME_DIR, DBUS_SESSION_BUS_ADDRESS — see the systemd profile.
//   - SSH_AUTH_SOCK — the ssh profile sets it explicitly via --setenv.

// ParseEnvList splits a comma-separated pass-env override (the format stored in
// a project's sandbox_env column) into entries, dropping blanks.
func ParseEnvList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// envMatcher decides which host environment variables cross into the sandbox.
type envMatcher struct {
	allow []string
	deny  []string
}

// newEnvMatcher splits pass-env entries into allow and deny patterns. An entry
// with a leading "-" denies; deny always beats allow, regardless of the order
// the fragments were merged in, so a project can drop a variable the baseline
// grants without having to restate the whole list.
func newEnvMatcher(passEnv []string) envMatcher {
	m := envMatcher{allow: append([]string{}, defaultPassEnv...)}
	for _, entry := range passEnv {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if name, ok := strings.CutPrefix(entry, "-"); ok {
			m.deny = append(m.deny, name)
			continue
		}
		m.allow = append(m.allow, entry)
	}
	return m
}

func (m envMatcher) allows(name string) bool {
	for _, pattern := range m.deny {
		if matchEnvName(pattern, name) {
			return false
		}
	}
	for _, pattern := range m.allow {
		if matchEnvName(pattern, name) {
			return true
		}
	}
	return false
}

// matchEnvName reports whether name matches pattern, where a trailing "*" is a
// prefix wildcard ("LC_*" matches "LC_TIME" and "LC_"). No other glob syntax is
// supported — environment variable names do not need it.
func matchEnvName(pattern, name string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, prefix)
	}
	return pattern == name
}

// filterEnv keeps only the KEY=VALUE entries of hostEnv whose name is allowed.
func (m envMatcher) filterEnv(hostEnv []string) []string {
	filtered := make([]string, 0, len(hostEnv))
	for _, entry := range hostEnv {
		name, _, ok := strings.Cut(entry, "=")
		if ok && m.allows(name) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
