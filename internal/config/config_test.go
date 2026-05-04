package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFindProjectConfigWalkUp verifies that FindProjectConfig traverses parent
// directories and terminates correctly without spinning past the filesystem root.
func TestFindProjectConfigWalkUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "a", ".caddie.yaml")
	if err := os.WriteFile(marker, []byte("environment: demo"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := FindProjectConfig(deep)
	if got != marker {
		t.Errorf("got %q, want %q", got, marker)
	}
}

// TestFindProjectConfigNotFound verifies that FindProjectConfig returns ""
// when no .caddie.yaml exists and the walk-up terminates at the root.
// On Windows this also exercises the volume-root termination path.
func TestFindProjectConfigNotFound(t *testing.T) {
	dir := t.TempDir()
	got := FindProjectConfig(dir)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

// TestFindProjectConfigWindowsRoot is a Windows-specific regression test:
// before this fix, the loop condition `dir != "/"` never matched on Windows
// because the root is e.g. "C:\", causing an infinite loop.
func TestFindProjectConfigWindowsRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only regression test")
	}
	// Use the Windows temp dir, which is deep enough that the walk-up
	// will visit multiple parent levels before hitting the volume root.
	dir := os.TempDir()
	// Must not hang and must return "" (no .caddie.yaml from temp down to root).
	got := FindProjectConfig(dir)
	// We can't assert "" because someone may have placed a .caddie.yaml
	// somewhere on the path — we just verify it terminates.
	_ = got
}
