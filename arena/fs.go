package arena

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	getter "github.com/hashicorp/go-getter"
)

// fetchResources downloads every plan resource into resourcesDir using
// go-getter. Local relative sources (e.g. "./fixtures") resolve against planDir.
// It is a no-op when resourcesDir already exists and is non-empty (resume).
func fetchResources(ctx context.Context, plan *Plan, planDir, resourcesDir string) error {
	// resourcesDir only ever exists (and is non-empty) after a fully-successful
	// fetch, because we populate a temp dir and rename it into place atomically.
	// So a non-empty resourcesDir means "done"; anything else means "redo".
	if isNonEmptyDir(resourcesDir) {
		return nil
	}
	if len(plan.Resources) == 0 {
		return os.MkdirAll(resourcesDir, 0o755)
	}

	// Fetch into a sibling temp dir; a partial fetch (e.g. resource 2 fails after
	// resource 1 succeeds) leaves only the temp dir, which is discarded — so a
	// re-run refetches everything rather than treating the partial set as final.
	tmp := resourcesDir + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	for _, r := range plan.Resources {
		dst := filepath.Join(tmp, r.DestOrDefault())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			os.RemoveAll(tmp)
			return err
		}
		// go-getter symlinks local directory sources (its Copy option only
		// affects single files). That is fine: resources are a read-only
		// reference, and copyDir dereferences links when seeding run dirs, so
		// each contestant still gets an independent copy.
		c := &getter.Client{
			Ctx:  ctx,
			Src:  r.Src,
			Dst:  dst,
			Pwd:  planDir,
			Mode: getter.ClientModeAny,
		}
		if err := c.Get(); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("fetch resource %q: %w", r.Src, err)
		}
	}
	if err := os.RemoveAll(resourcesDir); err != nil { // empty/absent at this point
		os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, resourcesDir)
}

// collectArtifacts copies files under runDir matching any of the (stdlib
// filepath.Glob) patterns into outDir, preserving their path relative to
// runDir. Returns the relative paths copied. Missing matches are not an error.
func collectArtifacts(runDir, outDir string, patterns []string) ([]string, error) {
	var copied []string
	for _, pat := range patterns {
		matches, err := filepath.Glob(filepath.Join(runDir, pat))
		if err != nil {
			return nil, fmt.Errorf("artifact glob %q: %w", pat, err)
		}
		for _, m := range matches {
			rel, err := filepath.Rel(runDir, m)
			if err != nil {
				return nil, err
			}
			dst := filepath.Join(outDir, rel)
			info, err := os.Stat(m)
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				err = copyDir(m, dst)
			} else {
				err = copyFile(m, dst)
			}
			if err != nil {
				return nil, err
			}
			copied = append(copied, rel)
		}
	}
	sort.Strings(copied)
	return copied, nil
}

// copyDir recursively copies the src tree into dst, dereferencing symlinks
// throughout so the result is always an independent real tree. This matters
// because go-getter symlinks local directory sources (both at the resources
// root and at a non-"." dest), and each contestant's run dir must be isolated —
// a recreated symlink would make agents share (and corrupt) the original
// source. A cycle guard on resolved paths prevents infinite recursion.
func copyDir(src, dst string) error {
	return copyTree(src, dst, make(map[string]bool))
}

func copyTree(src, dst string, visited map[string]bool) error {
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	if visited[real] {
		return nil // symlink cycle or repeated target: copy once
	}
	visited[real] = true

	info, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(real, dst)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcChild := filepath.Join(real, e.Name())
		dstChild := filepath.Join(dst, e.Name())
		childInfo, err := os.Stat(srcChild) // follows symlinks
		if err != nil {
			continue // skip broken symlinks
		}
		if childInfo.IsDir() {
			if err := copyTree(srcChild, dstChild, visited); err != nil {
				return err
			}
		} else if err := copyFile(srcChild, dstChild); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies a single regular file, creating parent dirs and preserving mode.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// isNonEmptyDir reports whether path is a directory containing at least one entry.
func isNonEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}
