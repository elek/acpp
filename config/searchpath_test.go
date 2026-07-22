package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectDir(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "myproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A regular file with a project-like name must not be mistaken for a dir.
	if err := os.WriteFile(filepath.Join(base, "afile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	t.Run("found in one of several bases", func(t *testing.T) {
		got, ok := FindProjectDir([]string{other, base}, "myproj")
		if !ok {
			t.Fatal("expected to find myproj")
		}
		if got != filepath.Join(base, "myproj") {
			t.Errorf("got %q, want %q", got, filepath.Join(base, "myproj"))
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, ok := FindProjectDir([]string{base, other}, "nope"); ok {
			t.Error("expected not found")
		}
	})

	t.Run("file is not a dir", func(t *testing.T) {
		if _, ok := FindProjectDir([]string{base}, "afile"); ok {
			t.Error("a regular file must not resolve as a project dir")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		if _, ok := FindProjectDir([]string{base}, ""); ok {
			t.Error("empty name must not resolve")
		}
	})

	t.Run("no search paths", func(t *testing.T) {
		if _, ok := FindProjectDir(nil, "myproj"); ok {
			t.Error("no search paths must not resolve")
		}
	})
}
