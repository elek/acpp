package arena

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectArtifacts(t *testing.T) {
	run := t.TempDir()
	out := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(run, "review.json"), []byte(`{"ok":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(run, "ignore.txt"), []byte("x"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(run, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(run, "src", "main.go"), []byte("package main"), 0o644))

	copied, err := collectArtifacts(run, out, []string{"review.json", "src/*.go"})
	require.NoError(t, err)
	require.Equal(t, []string{"review.json", filepath.Join("src", "main.go")}, copied)

	got, err := os.ReadFile(filepath.Join(out, "review.json"))
	require.NoError(t, err)
	require.Equal(t, `{"ok":true}`, string(got))
	require.FileExists(t, filepath.Join(out, "src", "main.go"))
	require.NoFileExists(t, filepath.Join(out, "ignore.txt"))
}

func TestCopyDirIsolatesNestedSymlinkedResource(t *testing.T) {
	planDir := t.TempDir()
	src := filepath.Join(planDir, "fixtures")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "orig.txt"), []byte("original"), 0o644))

	// A mapping-form resource (dest != ".") makes go-getter symlink resources/repo.
	resourcesDir := filepath.Join(t.TempDir(), "resources")
	plan := &Plan{Resources: []Resource{{Src: "./fixtures", Dest: "repo"}}}
	require.NoError(t, fetchResources(context.Background(), plan, planDir, resourcesDir))

	// Seed two independent run dirs, then mutate one. The mutation must NOT reach
	// the other run dir, nor the original source (proving the copy is real, not a
	// shared symlink).
	runA := filepath.Join(t.TempDir(), "a")
	runB := filepath.Join(t.TempDir(), "b")
	require.NoError(t, copyDir(resourcesDir, runA))
	require.NoError(t, copyDir(resourcesDir, runB))

	require.NoError(t, os.WriteFile(filepath.Join(runA, "repo", "orig.txt"), []byte("mutated"), 0o644))

	b, err := os.ReadFile(filepath.Join(runB, "repo", "orig.txt"))
	require.NoError(t, err)
	require.Equal(t, "original", string(b), "run B must be isolated from run A")

	o, err := os.ReadFile(filepath.Join(src, "orig.txt"))
	require.NoError(t, err)
	require.Equal(t, "original", string(o), "original source must not be corrupted")
}

func TestFetchResourcesPartialFailureNotResumedAsComplete(t *testing.T) {
	planDir := t.TempDir()
	good := filepath.Join(planDir, "good")
	require.NoError(t, os.MkdirAll(good, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(good, "a.txt"), []byte("ok"), 0o644))

	resourcesDir := filepath.Join(t.TempDir(), "resources")
	// Second resource points at a nonexistent path → fetch fails after the first.
	plan := &Plan{Resources: []Resource{
		{Src: "./good", Dest: "good"},
		{Src: "./does-not-exist", Dest: "bad"},
	}}
	err := fetchResources(context.Background(), plan, planDir, resourcesDir)
	require.Error(t, err)

	// The partial fetch must not have created a "complete-looking" resources dir.
	require.False(t, isNonEmptyDir(resourcesDir),
		"partial fetch must not leave a non-empty resources dir that resume treats as done")
}

func TestFetchResourcesLocalAndResume(t *testing.T) {
	planDir := t.TempDir()
	// a local resource dir relative to the plan.
	src := filepath.Join(planDir, "fixtures")
	require.NoError(t, os.MkdirAll(src, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644))

	resourcesDir := filepath.Join(t.TempDir(), "resources")
	plan := &Plan{Resources: []Resource{{Src: "./fixtures"}}}

	require.NoError(t, fetchResources(context.Background(), plan, planDir, resourcesDir))
	require.FileExists(t, filepath.Join(resourcesDir, "a.txt"))

	// Resume: mutate the fetched copy, re-fetch, and confirm it was NOT re-copied.
	require.NoError(t, os.WriteFile(filepath.Join(resourcesDir, "a.txt"), []byte("changed"), 0o644))
	require.NoError(t, fetchResources(context.Background(), plan, planDir, resourcesDir))
	got, err := os.ReadFile(filepath.Join(resourcesDir, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "changed", string(got), "existing resources must not be re-fetched")
}
