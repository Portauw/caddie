package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".caddie.yaml")
	if err := os.WriteFile(path, []byte("skills:\n  - \"*\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindProfile(t *testing.T) {
	t.Run("in_cwd", func(t *testing.T) {
		root := t.TempDir()
		want := writeProfile(t, root)
		if got := FindProfile(root); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("in_ancestor", func(t *testing.T) {
		root := t.TempDir()
		want := writeProfile(t, root)
		deep := filepath.Join(root, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindProfile(deep); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("nearest_wins", func(t *testing.T) {
		root := t.TempDir()
		writeProfile(t, root)
		child := filepath.Join(root, "child")
		want := writeProfile(t, child)
		if got := FindProfile(child); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	t.Run("none_found", func(t *testing.T) {
		root := t.TempDir()
		deep := filepath.Join(root, "x", "y")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := FindProfile(deep); got != "" {
			t.Errorf("got %q want empty string", got)
		}
	})
}
