package config

import (
	"os"
	"path/filepath"
)

// FindProjectDir searches each base directory in searchPaths for a subdirectory
// named name and returns its absolute path. It is the shared directory-resolution
// used by every surface that maps a project/channel name to a working directory
// (Discord channels, the web /projects view). Returns ("", false) when name is
// empty or no matching directory exists.
func FindProjectDir(searchPaths []string, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	for _, base := range searchPaths {
		candidate := filepath.Join(base, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}
